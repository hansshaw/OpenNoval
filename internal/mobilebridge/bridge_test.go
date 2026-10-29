package mobilebridge

import (
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestEmbeddedWorkbenchPersistsBooksAndConfig(t *testing.T) {
	dataDir, cacheDir := t.TempDir(), t.TempDir()
	beforeCWD, _ := os.Getwd()
	beforeHome := os.Getenv("HOME")
	entry, err := Start(dataDir, cacheDir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(Stop)
	if reused, err := Start(dataDir, cacheDir); err != nil || reused != entry || URL() != entry {
		t.Fatalf("repeated Start must reuse runtime: %q %v", reused, err)
	}
	origin := originOf(t, entry)
	client := authenticatedClient(t, entry)

	requestJSON(t, http.DefaultClient, http.MethodGet, origin+"/api/state", "", http.StatusForbidden)
	response := requestJSON(t, client, http.MethodPost, origin+"/api/manual/projects", `{"name":"手机上的小说"}`, http.StatusCreated)
	var project struct{ Directory string }
	if err := json.Unmarshal(response, &project); err != nil {
		t.Fatal(err)
	}
	// Start canonicalizes app storage, which may expand a Windows short path
	// or resolve a temporary-directory symlink. Compare resolved paths using
	// filesystem semantics rather than their original string spellings.
	booksDir, err := filepath.EvalSymlinks(filepath.Join(dataDir, "books"))
	if err != nil {
		t.Fatal(err)
	}
	projectDir, err := filepath.EvalSymlinks(project.Directory)
	if err != nil {
		t.Fatal(err)
	}
	relative, err := filepath.Rel(booksDir, projectDir)
	if err != nil || !filepath.IsLocal(relative) || relative == "." {
		t.Fatalf("project escaped app storage: %s (books: %s, relative: %q, error: %v)", project.Directory, booksDir, relative, err)
	}
	selectBody, _ := json.Marshal(map[string]string{"path": project.Directory})
	requestJSON(t, client, http.MethodPost, origin+"/api/workspaces", string(selectBody), http.StatusOK)
	body, _ := json.Marshal(map[string]string{"directory": project.Directory, "title": "第一章", "content": "清晨，作者拿起手机，写下了小说的第一句话。"})
	requestJSON(t, client, http.MethodPost, origin+"/api/manual/chapters", string(body), http.StatusCreated)
	for _, format := range []string{"txt", "epub"} {
		body, _ := json.Marshal(map[string]string{"directory": project.Directory, "format": format})
		exported := requestJSON(t, client, http.MethodPost, origin+"/api/export", string(body), http.StatusOK)
		if len(exported) == 0 {
			t.Fatal("empty manuscript export")
		}
	}
	if entries, err := os.ReadDir(cacheDir); err != nil || len(entries) != 0 {
		t.Fatalf("temporary exports should be cleaned up: %v %v", entries, err)
	}
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"data":[{"id":"phone-model"}]}`)
	}))
	defer provider.Close()
	config, _ := json.Marshal(map[string]any{
		"name": "phone-provider", "type": "openai", "model": "phone-model",
		"base_url": provider.URL + "/v1", "api_key": "private-test-key", "activate": true,
	})
	requestJSON(t, client, http.MethodPost, origin+"/api/config", string(config), http.StatusOK)
	if _, err := os.Stat(filepath.Join(dataDir, "config.json")); err != nil {
		t.Fatalf("configuration did not reach app storage: %v", err)
	}
	Stop()
	if URL() != "" {
		t.Fatal("stopped service still has a URL")
	}
	entry2, err := Start(dataDir, cacheDir)
	if err != nil {
		t.Fatal(err)
	}
	if entry2 == entry {
		t.Fatal("restart must rotate the credential")
	}
	origin2 := originOf(t, entry2)
	if origin2 != origin {
		t.Fatalf("restart should reuse available loopback port: %s -> %s", origin, origin2)
	}
	requestJSON(t, client, http.MethodGet, origin2+"/api/state", "", http.StatusForbidden)
	client2 := authenticatedClient(t, entry2)
	books := requestJSON(t, client2, http.MethodGet, origin2+"/api/workspaces", "", http.StatusOK)
	if !strings.Contains(string(books), "手机上的小说") {
		t.Fatalf("saved book missing after restart: %s", books)
	}
	requestJSON(t, client2, http.MethodPost, origin2+"/api/workspaces", string(selectBody), http.StatusOK)
	chapter := requestJSON(t, client2, http.MethodGet, origin2+"/api/chapters/1", "", http.StatusOK)
	if !strings.Contains(string(chapter), "小说的第一句话") {
		t.Fatalf("saved chapter missing after restart: %s", chapter)
	}
	configView := requestJSON(t, client2, http.MethodGet, origin2+"/api/config", "", http.StatusOK)
	if !strings.Contains(string(configView), "phone-provider") || strings.Contains(string(configView), "private-test-key") {
		t.Fatalf("model settings missing or exposed credential: %s", configView)
	}
	afterCWD, _ := os.Getwd()
	if beforeCWD != afterCWD || beforeHome != os.Getenv("HOME") {
		t.Fatal("embedding changed process home or working directory")
	}
}

func TestSessionCannotBeBypassedWithDesktopHeaderOrForeignOrigin(t *testing.T) {
	token := strings.Repeat("a", 64)
	handler := sessionHandler("127.0.0.1:5555", token, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-AInovel-Desktop-Session") != token {
			t.Error("authenticated request did not receive internal capability")
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	bootstrap := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:5555/__mobile/session?token="+token, nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, bootstrap)
	if w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/?mobile=1" {
		t.Fatalf("unexpected bootstrap response: %d %s", w.Code, w.Body.String())
	}
	cookies := w.Result().Cookies()
	if len(cookies) != 1 || !cookies[0].HttpOnly || cookies[0].SameSite != http.SameSiteStrictMode {
		t.Fatalf("session cookie lacks protection: %+v", cookies)
	}
	for _, test := range []struct {
		name, host, origin, fetchSite string
		cookie                        bool
		want                          int
	}{
		{"valid", "127.0.0.1:5555", "http://127.0.0.1:5555", "same-origin", true, http.StatusNoContent},
		{"header without cookie", "127.0.0.1:5555", "", "", false, http.StatusForbidden},
		{"foreign host", "evil.example", "", "", true, http.StatusForbidden},
		{"foreign origin", "127.0.0.1:5555", "https://evil.example", "", true, http.StatusForbidden},
		{"cross site", "127.0.0.1:5555", "", "cross-site", true, http.StatusForbidden},
	} {
		t.Run(test.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "http://"+test.host+"/api/state", nil)
			r.Header.Set("Origin", test.origin)
			r.Header.Set("Sec-Fetch-Site", test.fetchSite)
			r.Header.Set("X-AInovel-Desktop-Session", token)
			if test.cookie {
				r.AddCookie(cookies[0])
			}
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, r)
			if w.Code != test.want {
				t.Fatalf("status %d, want %d", w.Code, test.want)
			}
		})
	}
}

func TestStartRequiresPrivateAbsolutePathsAndHandlesOccupiedPort(t *testing.T) {
	if _, err := Start("relative", t.TempDir()); err == nil {
		t.Fatal("relative data directory accepted")
	}
	dataDir, cacheDir := t.TempDir(), t.TempDir()
	occupied, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer occupied.Close()
	_, port, _ := net.SplitHostPort(occupied.Addr().String())
	if err := os.WriteFile(filepath.Join(dataDir, "loopback-port"), []byte(port), 0600); err != nil {
		t.Fatal(err)
	}
	entry, err := Start(dataDir, cacheDir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(Stop)
	if strings.Contains(entry, ":"+port+"/") {
		t.Fatal("did not choose a free loopback port")
	}
	if _, err := Start(t.TempDir(), cacheDir); err == nil {
		t.Fatal("running service silently switched storage")
	}
	authenticatedClient(t, entry)
}

func authenticatedClient(t *testing.T, entry string) *http.Client {
	t.Helper()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Jar: jar, Timeout: 15 * time.Second}
	requestJSON(t, client, http.MethodGet, entry, "", http.StatusOK)
	return client
}

func originOf(t *testing.T, entry string) string {
	t.Helper()
	u, err := url.Parse(entry)
	if err != nil {
		t.Fatal(err)
	}
	return u.Scheme + "://" + u.Host
}

func requestJSON(t *testing.T, client *http.Client, method, endpoint, body string, want int) []byte {
	t.Helper()
	r, err := http.NewRequest(method, endpoint, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if body != "" {
		r.Header.Set("Content-Type", "application/json")
	}
	response, err := client.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	data, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != want {
		t.Fatalf("%s %s: got %d, want %d: %s", method, r.URL.Path, response.StatusCode, want, data)
	}
	return data
}
