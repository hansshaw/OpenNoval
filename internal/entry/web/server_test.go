package web

import (
	"archive/zip"
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/voocel/ainovel-cli/internal/bootstrap"
	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/store"
)

func TestRunRejectsOccupiedPortBeforeOpeningBrowser(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	err = Run(Options{Listen: ln.Addr().String(), OpenBrowser: true})
	if err == nil || !strings.Contains(err.Error(), "无法监听") {
		t.Fatalf("expected occupied port error before config loading or browser launch, got %v", err)
	}
}

func newTestServer(t *testing.T, configured bool) *Server {
	t.Helper()
	cfg := bootstrap.Config{OutputDir: filepath.Join(t.TempDir(), "book")}
	if configured {
		cfg.Provider = "local"
		cfg.ModelName = "test-model"
		cfg.ContextWindow = 32000
		cfg.Providers = map[string]bootstrap.ProviderConfig{"local": {Type: "openai", APIKey: "test-secret-do-not-return", BaseURL: "http://127.0.0.1:1/v1", Models: []bootstrap.ModelConfig{{Name: "test-model", ContextWindow: 32000}}}}
	}
	s, err := New(cfg, Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	s.configPath = filepath.Join(t.TempDir(), "config.json")
	s.booksDir = filepath.Join(t.TempDir(), "books")
	if configured && s.rt == nil {
		t.Fatalf("host not ready: %s", s.hostError)
	}
	return s
}
func request(s *Server, method, path, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, "http://127.0.0.1:8080"+path, strings.NewReader(body))
	if method == "POST" {
		r.Header.Set("Content-Type", "application/json")
	}
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	return w
}
func TestSetupAndRequestBoundaries(t *testing.T) {
	s := newTestServer(t, false)
	if w := request(s, "GET", "/", ""); w.Code != 200 || !strings.Contains(w.Body.String(), "AI 小说工作台") {
		t.Fatalf("index: %d", w.Code)
	}
	if w := request(s, "GET", "/api/state", ""); w.Code != 200 || !strings.Contains(w.Body.String(), `"ready":false`) {
		t.Fatal(w.Body.String())
	}
	cases := []struct {
		name, host, origin, site, content string
		want                              int
	}{
		{"foreign host", "evil.test", "", "", "application/json", 403},
		{"foreign origin", "127.0.0.1:8080", "http://evil.test", "", "application/json", 403},
		{"foreign site", "127.0.0.1:8080", "", "cross-site", "application/json", 403},
		{"form request", "127.0.0.1:8080", "", "", "text/plain", 415},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := httptest.NewRequest("POST", "http://127.0.0.1:8080/api/action", strings.NewReader(`{"action":"pause"}`))
			r.Host = c.host
			r.Header.Set("Origin", c.origin)
			r.Header.Set("Sec-Fetch-Site", c.site)
			r.Header.Set("Content-Type", c.content)
			w := httptest.NewRecorder()
			s.Handler().ServeHTTP(w, r)
			if w.Code != c.want {
				t.Fatalf("got %d want %d", w.Code, c.want)
			}
		})
	}
	for _, body := range []string{`{"action":"start","unknown":true}`, `{"action":"pause"} {}`, `{"action":"bogus"}`, `{"action":"start","text":""}`} {
		if w := request(s, "POST", "/api/action", body); w.Code != 400 {
			t.Fatalf("%s: %d", body, w.Code)
		}
	}
	if w := request(s, "GET", "/api/chapters/../config", ""); w.Code == 200 {
		t.Fatal("traversal accepted")
	}
	if w := request(s, "POST", "/api/workspaces", `{"path":"../../"}`); w.Code != 400 {
		t.Fatalf("unsafe directory: %d", w.Code)
	}
}
func TestConfigPreservesCredentialsAndCapabilities(t *testing.T) {
	s := newTestServer(t, true)
	if w := request(s, "GET", "/api/config", ""); strings.Contains(w.Body.String(), "test-secret-do-not-return") {
		t.Fatal("credential leaked")
	}
	w := request(s, "POST", "/api/config", `{"name":"local","model":"test-model","type":"openai","api":"chat","base_url":"http://127.0.0.1:1/v1","api_key":""}`)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	pc := s.cfg.Providers["local"]
	if pc.APIKey != "test-secret-do-not-return" || pc.Models[0].ContextWindow != 32000 {
		t.Fatal("lost stored credentials or model metadata")
	}
	if w := request(s, "POST", "/api/config", `{"name":"bad","model":"x","type":"unsupported"}`); w.Code != 400 {
		t.Fatal("invalid provider saved")
	}
	s.mu.Lock()
	s.stopping = true
	s.mu.Unlock()
	if w := request(s, "POST", "/api/action", `{"action":"retry"}`); w.Code != 409 {
		t.Fatal("retry accepted during shutdown")
	}
}
func TestBookLeaseCanBeRetried(t *testing.T) {
	first := newTestServer(t, true)
	second, err := New(first.cfg, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	if second.rt != nil || second.hostError == "" {
		t.Fatal("concurrent book lease accepted")
	}
	first.Close()
	if w := request(second, "POST", "/api/action", `{"action":"retry"}`); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
}
func TestChapterAndDownloads(t *testing.T) {
	s := newTestServer(t, true)
	st := store.NewStore(s.dir)
	for _, err := range []error{st.Book.Save(domain.BookMetadata{Title: "测试小说", Synopsis: "一个用于验证导出的故事。"}), st.Drafts.SaveFinalChapter(1, "雨落在窗前。"), st.Drafts.SaveDraft(2, "未完成的草稿"), st.Progress.Save(&domain.Progress{Phase: domain.PhaseWriting, CurrentChapter: 2, TotalChapters: 2, CompletedChapters: []int{1}, ChapterWordCounts: map[int]int{1: 6}})} {
		if err != nil {
			t.Fatal(err)
		}
	}
	if w := request(s, "GET", "/api/chapters/1", ""); w.Code != 200 || !strings.Contains(w.Body.String(), "雨落在窗前") {
		t.Fatal(w.Code, w.Body.String())
	}
	if w := request(s, "GET", "/api/chapters/2?source=draft", ""); w.Code != 200 || !strings.Contains(w.Body.String(), "未完成的草稿") {
		t.Fatal(w.Code, w.Body.String())
	}
	for _, format := range []string{"txt", "epub"} {
		w := request(s, "POST", "/api/export", `{"format":"`+format+`"}`)
		if w.Code != 200 {
			t.Fatal(format, w.Code, w.Body.String())
		}
		if !strings.Contains(w.Header().Get("Content-Disposition"), "novel."+format) {
			t.Fatal("missing download header")
		}
		if format == "txt" && !strings.Contains(w.Body.String(), "雨落在窗前") {
			t.Fatal("missing chapter")
		}
		if format == "epub" {
			if _, err := zip.NewReader(bytes.NewReader(w.Body.Bytes()), int64(w.Body.Len())); err != nil {
				t.Fatal(err)
			}
		}
	}
	if w := request(s, "POST", "/api/export", `{"format":"../json"}`); w.Code != 400 {
		t.Fatal("invalid format accepted")
	}
}
func TestSSEHistoryAndDisconnect(t *testing.T) {
	s := newTestServer(t, false)
	s.stream = "已有输出"
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()
	client := &http.Client{Timeout: 3 * time.Second}
	r, err := client.Get(ts.URL + "/api/events")
	if err != nil {
		t.Fatal(err)
	}
	reader := bufio.NewReader(r.Body)
	line, err := reader.ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	var m message
	if err := json.Unmarshal([]byte(strings.TrimPrefix(strings.TrimSpace(line), "data: ")), &m); err != nil || m.Type != "history" || !strings.Contains(line, "已有输出") {
		t.Fatal(line, err)
	}
	r.Body.Close()
	s.Close()
	r, err = client.Get(ts.URL + "/api/state")
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, r.Body)
	r.Body.Close()
}

func TestWorkspaceCreationBeforeModelSetup(t *testing.T) {
	s := newTestServer(t, false)
	w := request(s, "POST", "/api/workspaces", `{"create":true}`)
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	if st, err := os.Stat(s.dir); err != nil || !st.IsDir() {
		t.Fatal("workspace was not created", err)
	}
	if w := request(s, "GET", "/api/workspaces", ""); !strings.Contains(w.Body.String(), filepath.Base(s.dir)) {
		t.Fatal("new workspace missing")
	}
}

func TestWorkspaceSwitchKeepsProjectContentIsolated(t *testing.T) {
	s := newTestServer(t, false)
	create := func(name string) string {
		t.Helper()
		w := request(s, "POST", "/api/workspaces", `{"create":true,"name":"`+name+`"}`)
		if w.Code != 200 {
			t.Fatalf("create %q: %d %s", name, w.Code, w.Body.String())
		}
		var result struct {
			Directory string `json:"directory"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil || result.Directory == "" {
			t.Fatalf("create response: %s (%v)", w.Body.String(), err)
		}
		return result.Directory
	}
	first := create("第一本")
	if w := request(s, "POST", "/api/workspaces", `{"path":"`+strings.ReplaceAll(first, `\`, `\\`)+`"}`); w.Code != 200 {
		t.Fatalf("switch to first: %d %s", w.Code, w.Body.String())
	}
	firstStore := store.NewStore(first)
	if err := firstStore.Init(); err != nil {
		t.Fatal(err)
	}
	if err := firstStore.Book.Save(domain.BookMetadata{Title: "第一本", Synopsis: "第一本的故事简介"}); err != nil {
		t.Fatal(err)
	}
	if err := firstStore.Drafts.SaveFinalChapter(1, "第一本的独立正文"); err != nil {
		t.Fatal(err)
	}
	second := create("第二本")
	secondStore := store.NewStore(second)
	if err := secondStore.Init(); err != nil {
		t.Fatal(err)
	}
	if err := secondStore.Book.Save(domain.BookMetadata{Title: "第二本", Synopsis: "第二本的故事简介"}); err != nil {
		t.Fatal(err)
	}
	if err := secondStore.Drafts.SaveFinalChapter(1, "第二本的独立正文"); err != nil {
		t.Fatal(err)
	}
	switchTo := func(path string) {
		t.Helper()
		b, _ := json.Marshal(map[string]string{"path": path})
		if w := request(s, "POST", "/api/workspaces", string(b)); w.Code != 200 {
			t.Fatalf("switch to %q: %d %s", path, w.Code, w.Body.String())
		}
	}
	switchTo(first)
	if w := request(s, "GET", "/api/state", ""); !strings.Contains(w.Body.String(), `"name":"第一本"`) {
		t.Fatalf("first project metadata leaked or missing: %s", w.Body.String())
	}
	if w := request(s, "GET", "/api/chapters/1", ""); w.Code != 200 || !strings.Contains(w.Body.String(), "第一本的独立正文") || strings.Contains(w.Body.String(), "第二本的独立正文") {
		t.Fatalf("first project chapter mismatch: %d %s", w.Code, w.Body.String())
	}
	switchTo(second)
	if w := request(s, "GET", "/api/chapters/1", ""); w.Code != 200 || !strings.Contains(w.Body.String(), "第二本的独立正文") || strings.Contains(w.Body.String(), "第一本的独立正文") {
		t.Fatalf("second project chapter mismatch: %d %s", w.Code, w.Body.String())
	}
}

func TestPauseCancelsPendingStartup(t *testing.T) {
	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case entered <- struct{}{}:
		default:
		}
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}))
	defer model.Close()
	defer close(release)
	s := newTestServer(t, true)
	pc := s.cfg.Providers["local"]
	pc.BaseURL = model.URL + "/v1"
	s.cfg.Providers["local"] = pc
	s.rt.Close()
	s.rt = nil
	s.mu.Lock()
	s.initLocked()
	s.mu.Unlock()
	if w := request(s, "POST", "/api/action", `{"action":"start","text":"写一部关于月球图书馆的短篇小说"}`); w.Code != 202 {
		t.Fatal(w.Body.String())
	}
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("startup did not reach model")
	}
	if w := request(s, "POST", "/api/action", `{"action":"pause"}`); w.Code != 202 {
		t.Fatal(w.Body.String())
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		s.mu.Lock()
		stopped := !s.busy && !s.stopping && s.rt == nil
		s.mu.Unlock()
		if stopped {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("startup was not cancelled promptly")
}

func TestConnectionTestRedactsSubmittedKey(t *testing.T) {
	const secret = "new-key-must-never-return"
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(401); io.WriteString(w, secret) }))
	defer model.Close()
	s := newTestServer(t, false)
	body, _ := json.Marshal(map[string]string{"name": "test", "model": "test", "type": "openai", "base_url": model.URL + "/v1", "api_key": secret})
	w := request(s, "POST", "/api/config/test", string(body))
	if w.Code != 502 || strings.Contains(w.Body.String(), secret) {
		t.Fatalf("unsafe connection response: %d %s", w.Code, w.Body.String())
	}
}
