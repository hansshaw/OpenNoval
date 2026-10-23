package desktopui

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
)

func mediaConfigRequest(t *testing.T, s *Server, method, path string, value any) *httptest.ResponseRecorder {
	t.Helper()
	body := ""
	if value != nil {
		body = mustJSON(t, value)
	}
	r := httptest.NewRequest(method, "http://127.0.0.1:8080"+path, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	return w
}

func mediaConnectionDraft(kind string) map[string]any {
	return map[string]any{
		"name": "独立媒体连接", "kind": kind, "protocol": "openai-compatible",
		"baseURL": "http://127.0.0.1:1/v1", "model": "media-test-model",
		"apiKeyAction": "replace", "apiKey": "media-secret-never-in-response", "activate": true,
	}
}

func decodeMediaConfigResponse(t *testing.T, w *httptest.ResponseRecorder) struct {
	Connections         []mediaModelView  `json:"connections"`
	Selected            map[string]string `json:"selected"`
	GenerationAvailable bool              `json:"generationAvailable"`
	SavedID             string            `json:"savedID"`
} {
	t.Helper()
	var result struct {
		Connections         []mediaModelView  `json:"connections"`
		Selected            map[string]string `json:"selected"`
		GenerationAvailable bool              `json:"generationAvailable"`
		SavedID             string            `json:"savedID"`
	}
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &result) != nil {
		t.Fatalf("media response: %d %s", w.Code, w.Body.String())
	}
	return result
}

func TestMediaModelsPersistIndependentlyAndNeverExposeCredentials(t *testing.T) {
	s := newTestServer(t, false)
	if state := decodeMediaConfigResponse(t, mediaConfigRequest(t, s, "GET", "/api/media-models", nil)); len(state.Connections) != 0 || state.GenerationAvailable {
		t.Fatalf("unexpected initial media capabilities: %+v", state)
	}
	// Media configuration remains available while prose/style jobs are busy.
	s.mu.Lock()
	s.busy, s.styleBusy = true, true
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.busy, s.styleBusy = false, false
		s.mu.Unlock()
	}()
	draft := mediaConnectionDraft("image")
	// Even an accidentally pasted secret in an ordinary display field is hidden.
	draft["description"] = "key: media-secret-never-in-response"
	w := mediaConfigRequest(t, s, "POST", "/api/media-models", draft)
	state := decodeMediaConfigResponse(t, w)
	if strings.Contains(w.Body.String(), "media-secret-never-in-response") || strings.Contains(w.Body.String(), `"apiKey":`) {
		t.Fatal("media response exposed a stored credential")
	}
	if len(state.Connections) != 1 || !state.Connections[0].HasAPIKey || state.Selected["image"] != state.SavedID || state.GenerationAvailable {
		t.Fatalf("unexpected saved media state: %+v", state)
	}
	if s.rt != nil || s.baseCfg.Provider != "" || s.baseCfg.ModelName != "" || len(s.baseCfg.Providers) != 0 {
		t.Fatal("media save activated or changed text-generation configuration")
	}
	if _, err := os.Stat(s.configPath); !os.IsNotExist(err) {
		t.Fatalf("media save wrote the prose configuration: %v", err)
	}
	path := filepath.Join(filepath.Dir(s.configPath), "media-models.json")
	stored, err := loadMediaModelConfig(path)
	if err != nil || len(stored.Connections) != 1 || stored.Connections[0].APIKey != "media-secret-never-in-response" {
		t.Fatalf("media configuration did not persist, err = %v", err)
	}
	if info, err := os.Stat(path); err != nil || (runtime.GOOS != "windows" && info.Mode().Perm() != 0600) {
		t.Fatalf("credential file is not owner-only, err = %v", err)
	}
	restarted := newTestServer(t, false)
	restarted.configPath = s.configPath
	loaded := decodeMediaConfigResponse(t, mediaConfigRequest(t, restarted, "GET", "/api/media-models", nil))
	if len(loaded.Connections) != 1 || loaded.Selected["image"] != state.SavedID {
		t.Fatalf("media config was not retained across restart: %+v", loaded)
	}
}

func TestMediaModelCredentialActionsAreExplicitAndPreserveOtherConnections(t *testing.T) {
	s := newTestServer(t, false)
	draft := mediaConnectionDraft("image")
	id := decodeMediaConfigResponse(t, mediaConfigRequest(t, s, "POST", "/api/media-models", draft)).SavedID
	video := mediaConnectionDraft("video")
	video["apiKey"] = "video-secret-retain"
	videoID := decodeMediaConfigResponse(t, mediaConfigRequest(t, s, "POST", "/api/media-models", video)).SavedID
	path := filepath.Join(filepath.Dir(s.configPath), "media-models.json")
	draft["id"] = id
	for _, tc := range []struct{ action, submitted, want string }{
		{"keep", "ignored-draft-secret", "media-secret-never-in-response"},
		{"replace", "new-image-secret", "new-image-secret"},
		{"clear", "ignored-on-clear", ""},
	} {
		t.Run(tc.action, func(t *testing.T) {
			draft["apiKeyAction"], draft["apiKey"] = tc.action, tc.submitted
			state := decodeMediaConfigResponse(t, mediaConfigRequest(t, s, "POST", "/api/media-models", draft))
			stored, err := loadMediaModelConfig(path)
			if err != nil || stored.Connections[0].APIKey != tc.want || stored.Connections[1].APIKey != "video-secret-retain" {
				t.Fatalf("credential action did not preserve expected keys, err = %v", err)
			}
			if state.Selected["video"] != videoID || state.Selected["image"] != id || state.Connections[0].HasAPIKey != (tc.want != "") {
				t.Fatalf("credential action changed unrelated state: %+v", state)
			}
		})
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, action := range []string{"", "erase", "replace"} {
		draft["apiKeyAction"], draft["apiKey"] = action, ""
		w := mediaConfigRequest(t, s, "POST", "/api/media-models", draft)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("invalid credential action accepted: %q, %d", action, w.Code)
		}
	}
	after, err := os.ReadFile(path)
	if err != nil || string(before) != string(after) {
		t.Fatal("rejected credential actions modified the saved file")
	}
}

func TestMediaModelSelectionsStayWithinTheirCapabilityAndDeleteClearsReferences(t *testing.T) {
	s := newTestServer(t, false)
	draft := mediaConnectionDraft("image")
	id := decodeMediaConfigResponse(t, mediaConfigRequest(t, s, "POST", "/api/media-models", draft)).SavedID
	if w := mediaConfigRequest(t, s, "POST", "/api/media-models/select", map[string]any{"kind": "audio", "id": id}); w.Code != 400 {
		t.Fatalf("image connection selected as audio: %d", w.Code)
	}
	if w := mediaConfigRequest(t, s, "POST", "/api/media-models/select", map[string]any{"kind": "image", "id": "missing"}); w.Code != 400 {
		t.Fatalf("unknown media connection selected: %d", w.Code)
	}
	draft["id"], draft["kind"], draft["voice"] = id, "audio", "narrator"
	state := decodeMediaConfigResponse(t, mediaConfigRequest(t, s, "POST", "/api/media-models", draft))
	if state.Selected["image"] != "" || state.Selected["audio"] != id {
		t.Fatalf("capability change left invalid default selection: %+v", state.Selected)
	}
	state = decodeMediaConfigResponse(t, mediaConfigRequest(t, s, "POST", "/api/media-models/select", map[string]any{"kind": "audio", "id": ""}))
	if state.Selected["audio"] != "" {
		t.Fatal("explicit empty selection did not clear the default")
	}
	decodeMediaConfigResponse(t, mediaConfigRequest(t, s, "POST", "/api/media-models/select", map[string]any{"kind": "audio", "id": id}))
	state = decodeMediaConfigResponse(t, mediaConfigRequest(t, s, "DELETE", "/api/media-models/"+id, nil))
	if len(state.Connections) != 0 || state.Selected["audio"] != "" {
		t.Fatalf("deleting a connection left defaults pointing at it: %+v", state)
	}
	if w := mediaConfigRequest(t, s, "DELETE", "/api/media-models/"+id, nil); w.Code != 404 {
		t.Fatalf("deleting absent media connection: %d", w.Code)
	}
}

func TestMediaModelValidationRejectsUnsafeAndUnsupportedDraftsWithoutSaving(t *testing.T) {
	s := newTestServer(t, false)
	for i, tc := range []struct {
		field string
		value any
	}{
		{"name", ""}, {"name", "name\nvalue"}, {"model", ""}, {"model", strings.Repeat("长", 201)},
		{"kind", "text"}, {"protocol", "unimplemented"}, {"voice", "audio-only"},
		{"apiKey", "secret\nAuthorization: injected"}, {"apiKey", strings.Repeat("x", 4097)},
		{"baseURL", "file:///private"}, {"baseURL", "https://user:password@example.com/v1"},
		{"baseURL", "https://example.com/v1?key=secret"}, {"baseURL", "https://example.com/#secret"},
		{"baseURL", "http://127.0.0.1:99999/v1"}, {"baseURL", "http:///v1"},
		{"baseURL", "https://example.com/has space"}, {"baseURL", "https://example.com/v1?"},
	} {
		t.Run(fmt.Sprintf("%02d_%s", i, tc.field), func(t *testing.T) {
			draft := mediaConnectionDraft("image")
			draft[tc.field] = tc.value
			if w := mediaConfigRequest(t, s, "POST", "/api/media-models", draft); w.Code != 400 {
				t.Fatalf("invalid media draft accepted: %d %s", w.Code, w.Body.String())
			}
		})
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(s.configPath), "media-models.json")); !os.IsNotExist(err) {
		t.Fatalf("rejected draft created configuration file: %v", err)
	}

}

func TestMediaModelCorruptionIsNotOverwrittenOrExposed(t *testing.T) {
	s := newTestServer(t, false)
	path := filepath.Join(filepath.Dir(s.configPath), "media-models.json")
	broken := []byte(`{"apiKey":"private-secret-corrupt-file",`)
	if err := os.WriteFile(path, broken, 0600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		method, path string
		value        any
	}{
		{"GET", "/api/media-models", nil},
		{"POST", "/api/media-models", mediaConnectionDraft("image")},
	} {
		w := mediaConfigRequest(t, s, tc.method, tc.path, tc.value)
		if w.Code != 500 || strings.Contains(w.Body.String(), "private-secret-corrupt-file") {
			t.Fatalf("corrupt media config response: %d %s", w.Code, w.Body.String())
		}
	}
	after, err := os.ReadFile(path)
	if err != nil || string(after) != string(broken) {
		t.Fatal("corrupt original media file was overwritten")
	}
}

func TestConcurrentMediaModelSavesDoNotLoseConnections(t *testing.T) {
	s := newTestServer(t, false)
	other := newTestServer(t, false)
	other.configPath = s.configPath
	const count = 16
	var wg sync.WaitGroup
	results := make([]*httptest.ResponseRecorder, count)
	for i := range count {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			draft := mediaConnectionDraft("image")
			draft["name"] = fmt.Sprintf("image-%d", i)
			draft["activate"] = false
			server := s
			if i%2 != 0 {
				server = other
			}
			results[i] = mediaConfigRequest(t, server, "POST", "/api/media-models", draft)
		}(i)
	}
	wg.Wait()
	for _, w := range results {
		decodeMediaConfigResponse(t, w)
	}
	state := decodeMediaConfigResponse(t, mediaConfigRequest(t, s, "GET", "/api/media-models", nil))
	if len(state.Connections) != count || state.Selected["image"] != "" {
		t.Fatalf("concurrent media writes lost saved data: %+v", state)
	}

}

func TestMediaModelAtomicWriteFailureRetainsTargetAndRemovesTemporaryFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "media-models.json")
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(path, "retained"), []byte("preserve"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := writeMediaModelConfig(path, []byte(`{"secret":"must-not-replace"}`)); err == nil {
		t.Fatal("atomic write unexpectedly replaced an occupied directory")
	}
	if data, err := os.ReadFile(filepath.Join(path, "retained")); err != nil || string(data) != "preserve" {
		t.Fatal("failed write changed the original target")
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 1 || entries[0].Name() != "media-models.json" {
		t.Fatalf("temporary credential file left behind after write failure: %v, %v", entries, err)
	}
}

func TestMediaConfigurationMutationsRequireTrustedJSONRequests(t *testing.T) {
	s := newTestServer(t, false)
	for _, tc := range []struct {
		method, path, contentType, origin string
		want                              int
	}{
		{"POST", "/api/media-models", "text/plain", "", http.StatusUnsupportedMediaType},
		{"POST", "/api/media-models", "multipart/form-data; boundary=test", "", http.StatusUnsupportedMediaType},
		{"POST", "/api/media-models", "application/json", "https://foreign.example", http.StatusForbidden},
		{"DELETE", "/api/media-models/0123456789abcdef0123456789abcdef", "", "https://foreign.example", http.StatusForbidden},
	} {
		r := httptest.NewRequest(tc.method, "http://127.0.0.1:8080"+tc.path, strings.NewReader(`{}`))
		r.Header.Set("Content-Type", tc.contentType)
		r.Header.Set("Origin", tc.origin)
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, r)
		if w.Code != tc.want {
			t.Fatalf("%s %s boundary: %d, want %d", tc.method, tc.path, w.Code, tc.want)
		}
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(s.configPath), "media-models.json")); !os.IsNotExist(err) {
		t.Fatalf("untrusted request created media config: %v", err)
	}
}

func TestMediaModelsReadLegacyCanvasFieldAndRemoveItOnlyAfterSuccessfulSave(t *testing.T) {
	s := newTestServer(t, false)
	draft := mediaConnectionDraft("audio")
	draft["voice"] = "narrator"
	state := decodeMediaConfigResponse(t, mediaConfigRequest(t, s, "POST", "/api/media-models", draft))
	path := filepath.Join(filepath.Dir(s.configPath), "media-models.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var legacy map[string]any
	if err := json.Unmarshal(raw, &legacy); err != nil {
		t.Fatal(err)
	}
	legacy["canvasURL"] = "https://retired.example/path"
	legacyRaw, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, legacyRaw, 0600); err != nil {
		t.Fatal(err)
	}
	response := mediaConfigRequest(t, s, "GET", "/api/media-models", nil)
	loaded := decodeMediaConfigResponse(t, response)
	if len(loaded.Connections) != 1 || loaded.Selected["audio"] != state.SavedID || loaded.Connections[0].Voice != "narrator" || strings.Contains(response.Body.String(), "canvasURL") || strings.Contains(response.Body.String(), "retired.example") {
		t.Fatalf("legacy connection was not preserved or retired field exposed: %s", response.Body.String())
	}
	unchanged, err := os.ReadFile(path)
	if err != nil || string(unchanged) != string(legacyRaw) {
		t.Fatal("reading a legacy file rewrote it")
	}
	draft["id"], draft["apiKeyAction"], draft["apiKey"] = state.SavedID, "keep", ""
	decodeMediaConfigResponse(t, mediaConfigRequest(t, s, "POST", "/api/media-models", draft))
	saved, err := os.ReadFile(path)
	if err != nil || strings.Contains(string(saved), "canvasURL") || !strings.Contains(string(saved), "media-secret-never-in-response") {
		t.Fatal("migration failed to preserve credentials while dropping retired field")
	}
	if w := mediaConfigRequest(t, s, "PUT", "/api/media-models/canvas", map[string]any{"url": "https://retired.example"}); w.Code != http.StatusNotFound && w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("retired configuration endpoint remains available: %d", w.Code)
	}
}
