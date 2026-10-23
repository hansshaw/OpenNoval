package desktopui

import (
	"archive/zip"
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"mime"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hansshaw/AInoval-web-ui/internal/bootstrap"
	"github.com/hansshaw/AInoval-web-ui/internal/domain"
	"github.com/hansshaw/AInoval-web-ui/internal/store"
)

func TestRunRejectsOccupiedPortBeforeLoadingConfig(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	err = Run(Options{Listen: ln.Addr().String(), DesktopSessionToken: testDesktopShutdownToken, DesktopShutdownToken: testDesktopShutdownToken})
	if err == nil || !strings.Contains(err.Error(), "无法监听") {
		t.Fatalf("expected occupied port error before config loading, got %v", err)
	}
}

func newTestServer(t *testing.T, configured bool) *Server {
	t.Helper()
	cfg := bootstrap.Config{OutputDir: filepath.Join(t.TempDir(), "book")}
	// Unit and end-to-end tests must not launch real desktop notification
	// processes. Doing so can outlive the test process on Windows and write into
	// the temporary HOME after Go has started its cleanup pass.
	notifyDisabled := false
	cfg.Notify.Enabled = &notifyDisabled
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
	if method == http.MethodPost || method == http.MethodPatch {
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
	if w := request(s, "POST", "/api/action", `{"action":"start","text":"写一部小说"}`); w.Code != http.StatusConflict {
		t.Fatalf("start must require confirmed cocreate: %d %s", w.Code, w.Body.String())
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

func TestConfigUpdatesSelectedModelLimitsAndPreservesOthers(t *testing.T) {
	s := newTestServer(t, true)
	s.mu.Lock()
	pc := s.baseCfg.Providers["local"]
	pc.Models = append(pc.Models, bootstrap.ModelConfig{
		Name: "other-model", ContextWindow: 50000, MaxOutputTokens: 9000,
	})
	s.baseCfg.Providers["local"] = pc
	s.mu.Unlock()

	body := mustJSON(t, map[string]any{
		"provider":          "local",
		"name":              "local",
		"model":             "test-model",
		"type":              "openai",
		"api":               "chat",
		"base_url":          "http://127.0.0.1:1/v1",
		"api_key":           "",
		"models":            []string{"test-model", "other-model"},
		"context_window":    64000,
		"max_output_tokens": 20000,
	})
	if w := request(s, "POST", "/api/config", body); w.Code != http.StatusOK {
		t.Fatalf("save config: %d %s", w.Code, w.Body.String())
	}

	s.mu.Lock()
	pc = s.baseCfg.Providers["local"]
	s.mu.Unlock()
	if pc.APIKey != "test-secret-do-not-return" || len(pc.Models) != 2 {
		t.Fatalf("credentials or model list lost: %+v", pc)
	}
	limits := make(map[string]bootstrap.ModelConfig)
	for _, model := range pc.Models {
		limits[model.Name] = model
	}
	if got := limits["test-model"]; got.ContextWindow != 64000 || got.MaxOutputTokens != 20000 {
		t.Fatalf("selected model limits = %+v", got)
	}
	if got := limits["other-model"]; got.ContextWindow != 50000 || got.MaxOutputTokens != 9000 {
		t.Fatalf("other model limits changed = %+v", got)
	}

	w := request(s, "GET", "/api/config", "")
	if w.Code != http.StatusOK {
		t.Fatalf("get config: %d %s", w.Code, w.Body.String())
	}
	var response struct {
		Providers []struct {
			Name         string `json:"name"`
			ModelDetails []struct {
				Name            string                        `json:"name"`
				ContextWindow   int                           `json:"context_window"`
				ContextSource   bootstrap.ContextWindowSource `json:"context_source"`
				MaxOutputTokens int                           `json:"max_output_tokens"`
				OutputSource    bootstrap.OutputTokenSource   `json:"output_source"`
			} `json:"model_details"`
		} `json:"providers"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, provider := range response.Providers {
		if provider.Name != "local" {
			continue
		}
		for _, detail := range provider.ModelDetails {
			if detail.Name != "test-model" {
				continue
			}
			found = true
			if detail.ContextWindow != 64000 || detail.ContextSource != bootstrap.CtxWindowModelConfig ||
				detail.MaxOutputTokens != 20000 || detail.OutputSource != bootstrap.OutputTokenModelConfig {
				t.Fatalf("model details = %+v", detail)
			}
		}
	}
	if !found {
		t.Fatal("GET /api/config did not return selected model details")
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
		_, params, err := mime.ParseMediaType(w.Header().Get("Content-Disposition"))
		if err != nil || params["filename"] != "测试小说."+format {
			t.Fatalf("download header = %q: %v", w.Header().Get("Content-Disposition"), err)
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

func TestExportWorksWithoutModelAndIncludesOnlyCompletedChapters(t *testing.T) {
	s := newTestServer(t, false)
	st := store.NewStore(s.dir)
	for _, err := range []error{
		st.Book.Save(domain.BookMetadata{Title: "未完稿", Synopsis: "用于验证未完结项目导出。"}),
		st.Drafts.SaveFinalChapter(1, "第一章终稿"),
		st.Drafts.SaveFinalChapter(2, "第二章终稿"),
		st.Drafts.SaveDraft(3, "第三章仍是草稿"),
		st.Progress.Save(&domain.Progress{
			Phase:             domain.PhaseWriting,
			CurrentChapter:    3,
			InProgressChapter: 3,
			TotalChapters:     4,
			CompletedChapters: []int{1, 2},
		}),
	} {
		if err != nil {
			t.Fatal(err)
		}
	}
	w := request(s, "POST", "/api/export", `{"format":"txt"}`)
	if w.Code != 200 {
		t.Fatalf("export without model: %d %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if !strings.Contains(body, "第一章终稿") || !strings.Contains(body, "第二章终稿") {
		t.Fatalf("completed chapters missing: %s", body)
	}
	if strings.Contains(body, "第三章仍是草稿") {
		t.Fatal("draft chapter leaked into export")
	}
}

func TestExportArchivedProjectByDirectory(t *testing.T) {
	s := newTestServer(t, false)
	if err := os.MkdirAll(s.booksDir, 0700); err != nil {
		t.Fatal(err)
	}
	target, err := os.MkdirTemp(s.booksDir, "archived-*")
	if err != nil {
		t.Fatal(err)
	}
	if err := saveProject(target, projectInfo{Name: "归档作品", Archived: true}); err != nil {
		t.Fatal(err)
	}
	st := store.NewStore(target)
	if err := st.Book.Save(domain.BookMetadata{Title: "归档作品", Synopsis: "归档后仍可导出。"}); err != nil {
		t.Fatal(err)
	}
	if err := st.Drafts.SaveFinalChapter(1, "归档项目正文"); err != nil {
		t.Fatal(err)
	}
	if err := st.Progress.Save(&domain.Progress{
		Phase:             domain.PhaseWriting,
		CurrentChapter:    1,
		CompletedChapters: []int{1},
	}); err != nil {
		t.Fatal(err)
	}
	w := request(s, "POST", "/api/export", mustJSON(t, map[string]string{
		"format":    "txt",
		"directory": target,
	}))
	if w.Code != 200 || !strings.Contains(w.Body.String(), "归档项目正文") {
		t.Fatalf("archived export: %d %s", w.Code, w.Body.String())
	}
}

func TestDeleteWorkspaceRequiresArchiveConfirmationAndSafePath(t *testing.T) {
	s := newTestServer(t, false)
	if err := os.MkdirAll(s.booksDir, 0700); err != nil {
		t.Fatal(err)
	}
	current := s.dir
	if err := saveProject(current, projectInfo{Name: "当前项目", Archived: true}); err != nil {
		t.Fatal(err)
	}
	if w := request(s, "POST", "/api/workspaces/delete", mustJSON(t, map[string]any{
		"directory": current,
		"confirmed": true,
	})); w.Code != 400 {
		t.Fatalf("delete current project: %d %s", w.Code, w.Body.String())
	}
	target, err := os.MkdirTemp(s.booksDir, "delete-*")
	if err != nil {
		t.Fatal(err)
	}
	if err := saveProject(target, projectInfo{Name: "待删除", Archived: false}); err != nil {
		t.Fatal(err)
	}
	body := func(confirmed bool) string {
		return mustJSON(t, map[string]any{
			"directory": target,
			"confirmed": confirmed,
		})
	}
	if w := request(s, "POST", "/api/workspaces/delete", body(false)); w.Code != 400 {
		t.Fatalf("delete without confirmation: %d %s", w.Code, w.Body.String())
	}
	if w := request(s, "POST", "/api/workspaces/delete", body(true)); w.Code != 409 {
		t.Fatalf("delete active project: %d %s", w.Code, w.Body.String())
	}
	if err := saveProject(target, projectInfo{Name: "待删除", Archived: true}); err != nil {
		t.Fatal(err)
	}
	if w := request(s, "POST", "/api/workspaces/delete", body(true)); w.Code != 200 {
		t.Fatalf("delete archived project: %d %s", w.Code, w.Body.String())
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatalf("deleted directory still exists: %v", err)
	}
	if w := request(s, "POST", "/api/workspaces/delete", mustJSON(t, map[string]any{
		"directory": s.booksDir,
		"confirmed": true,
	})); w.Code != 400 {
		t.Fatalf("books root must be protected: %d %s", w.Code, w.Body.String())
	}
	outside := t.TempDir()
	if err := saveProject(outside, projectInfo{Name: "外部", Archived: true}); err != nil {
		t.Fatal(err)
	}
	if w := request(s, "POST", "/api/workspaces/delete", mustJSON(t, map[string]any{
		"directory": outside,
		"confirmed": true,
	})); w.Code != 400 {
		t.Fatalf("outside directory must be rejected: %d %s", w.Code, w.Body.String())
	}

	otherCWD := t.TempDir()
	t.Chdir(otherCWD)
	legacy := filepath.Join(otherCWD, "output", "novel")
	if err := os.MkdirAll(legacy, 0700); err != nil {
		t.Fatal(err)
	}
	if err := saveProject(legacy, projectInfo{Name: "旧版项目", Archived: true}); err != nil {
		t.Fatal(err)
	}
	if w := request(s, "POST", "/api/workspaces/delete", mustJSON(t, map[string]any{
		"directory": legacy,
		"confirmed": true,
	})); w.Code != 400 {
		t.Fatalf("legacy directory must be rejected: %d %s", w.Code, w.Body.String())
	}

	link := filepath.Join(s.booksDir, "linked-project")
	if err := os.Symlink(outside, link); err == nil {
		if w := request(s, "POST", "/api/workspaces/delete", mustJSON(t, map[string]any{
			"directory": link,
			"confirmed": true,
		})); w.Code != 400 {
			t.Fatalf("symlink directory must be rejected: %d %s", w.Code, w.Body.String())
		}
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

func TestWorkspaceListTreatsLegacyBooksRootAsOneProject(t *testing.T) {
	s := newTestServer(t, false)
	s.dir, s.homeDir = s.booksDir, s.booksDir
	if err := saveProject(s.booksDir, projectInfo{Name: "知微的破碎", CreatedAt: "2026-10-19T21:22:58+08:00"}); err != nil {
		t.Fatal(err)
	}
	writeCopyFixture(t, s.booksDir, "meta/run.json", `{"started_at":"2026-10-19T21:26:10+08:00"}`+"\n")
	writeCopyFixture(t, s.booksDir, "outline.json", `[{"chapter":1,"title":"规矩","core_event":"测试"}]`)
	if err := os.MkdirAll(filepath.Join(s.booksDir, "chapters"), 0700); err != nil {
		t.Fatal(err)
	}
	w := request(s, http.MethodGet, "/api/workspaces", "")
	if w.Code != http.StatusOK {
		t.Fatalf("list projects: %d %s", w.Code, w.Body.String())
	}
	var projects []struct {
		Path     string `json:"path"`
		Title    string `json:"title"`
		Archived bool   `json:"archived"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &projects); err != nil {
		t.Fatal(err)
	}
	if len(projects) != 1 {
		t.Fatalf("legacy project listed as %d projects: %s", len(projects), w.Body.String())
	}
	if projects[0].Path != s.booksDir || projects[0].Title != "知微的破碎" || projects[0].Archived {
		t.Fatalf("legacy project metadata = %+v", projects[0])
	}
}

func TestWorkspaceListIncludesLegacyRootAndCopiedChild(t *testing.T) {
	s := newTestServer(t, false)
	s.dir, s.homeDir = s.booksDir, s.booksDir
	if err := saveProject(s.booksDir, projectInfo{Name: "知微的破碎"}); err != nil {
		t.Fatal(err)
	}
	copyDir := filepath.Join(s.booksDir, "copy-restored")
	if err := saveProject(copyDir, projectInfo{
		ID: "copy-id", Name: "知微的破碎（副本）", CopiedFrom: "legacy-id",
	}); err != nil {
		t.Fatal(err)
	}
	for _, auxiliary := range []string{"meta", "chapters", "logs"} {
		if err := os.MkdirAll(filepath.Join(s.booksDir, auxiliary), 0700); err != nil {
			t.Fatal(err)
		}
	}

	w := request(s, http.MethodGet, "/api/workspaces", "")
	if w.Code != http.StatusOK {
		t.Fatalf("list projects: %d %s", w.Code, w.Body.String())
	}
	var projects []struct {
		Path  string `json:"path"`
		Title string `json:"title"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &projects); err != nil {
		t.Fatal(err)
	}
	if len(projects) != 2 {
		t.Fatalf("root and copy listed as %d projects: %s", len(projects), w.Body.String())
	}
	titles := map[string]string{}
	for _, project := range projects {
		titles[project.Path] = project.Title
	}
	if titles[s.booksDir] != "知微的破碎" || titles[copyDir] != "知微的破碎（副本）" {
		t.Fatalf("root and copy titles = %+v", titles)
	}
	for _, auxiliary := range []string{"meta", "chapters", "logs"} {
		if _, ok := titles[filepath.Join(s.booksDir, auxiliary)]; ok {
			t.Fatalf("auxiliary directory %q listed as a project", auxiliary)
		}
	}
}

func TestWorkspaceListsAndSwitchesCopiedChildBesideLegacyRoot(t *testing.T) {
	s := newTestServer(t, false)
	activeRoot := t.TempDir()
	s.dir, s.homeDir = activeRoot, activeRoot
	if err := saveProject(activeRoot, projectInfo{Name: "知微的破碎"}); err != nil {
		t.Fatal(err)
	}
	copyDir := filepath.Join(activeRoot, "copy-restored")
	if err := saveProject(copyDir, projectInfo{
		ID: "copy-id", Name: "知微的破碎（副本）", CopiedFrom: activeRoot,
	}); err != nil {
		t.Fatal(err)
	}

	w := request(s, http.MethodGet, "/api/workspaces", "")
	if w.Code != http.StatusOK {
		t.Fatalf("list projects: %d %s", w.Code, w.Body.String())
	}
	var projects []struct {
		Path  string `json:"path"`
		Title string `json:"title"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &projects); err != nil {
		t.Fatal(err)
	}
	titles := make(map[string]string)
	for _, project := range projects {
		titles[project.Path] = project.Title
	}
	if titles[activeRoot] != "知微的破碎" || titles[copyDir] != "知微的破碎（副本）" {
		t.Fatalf("legacy root and copy under active root were not both listed: %+v", titles)
	}

	w = request(s, http.MethodPost, "/api/workspaces", mustJSON(t, map[string]string{"path": copyDir}))
	if w.Code != http.StatusOK {
		t.Fatalf("switch to copied child: %d %s", w.Code, w.Body.String())
	}
	s.mu.Lock()
	active := s.dir
	s.mu.Unlock()
	if active != copyDir {
		t.Fatalf("active workspace = %q, want %q", active, copyDir)
	}
	if w := request(s, http.MethodGet, "/api/state", ""); !strings.Contains(w.Body.String(), `"name":"知微的破碎（副本）"`) {
		t.Fatalf("switched project metadata missing: %s", w.Body.String())
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
	s.mu.Lock()
	pc := s.cfg.Providers["local"]
	pc.BaseURL = model.URL + "/v1"
	s.cfg.Providers["local"] = pc
	s.baseCfg.Providers["local"] = pc
	s.rt.Close()
	s.rt = nil
	s.initLocked()
	s.mu.Unlock()
	if err := saveWebCoCreate(s.dir, webCoCreateSession{
		DraftPrompt: "写一部关于月球图书馆的短篇小说，主角在闭馆前寻找一封来自地球的旧信。",
		Ready:       true,
		Confirmed:   true,
	}); err != nil {
		t.Fatal(err)
	}
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

func TestCoCreateGateAndProjectIsolation(t *testing.T) {
	s := newTestServer(t, false)
	if w := request(s, "GET", "/api/cocreate", ""); w.Code != http.StatusOK ||
		!strings.Contains(w.Body.String(), `"canConfirm":false`) {
		t.Fatalf("empty cocreate session: %d %s", w.Code, w.Body.String())
	}
	if w := request(s, "POST", "/api/cocreate/confirm", `{}`); w.Code != http.StatusConflict {
		t.Fatalf("empty session should not be confirmable: %d %s", w.Code, w.Body.String())
	}

	first := s.dir
	second := filepath.Join(t.TempDir(), "second-book")
	if err := saveWebCoCreate(first, webCoCreateSession{
		DraftPrompt: "第一本的完整创作指令",
		Ready:       true,
	}); err != nil {
		t.Fatal(err)
	}
	if err := saveWebCoCreate(second, webCoCreateSession{
		DraftPrompt: "第二本的完整创作指令",
		Ready:       false,
	}); err != nil {
		t.Fatal(err)
	}

	firstView, err := readWebCoCreate(first)
	if err != nil {
		t.Fatal(err)
	}
	secondView, err := readWebCoCreate(second)
	if err != nil {
		t.Fatal(err)
	}
	if firstView.DraftPrompt != "第一本的完整创作指令" ||
		!firstView.Ready ||
		secondView.DraftPrompt != "第二本的完整创作指令" ||
		secondView.Ready {
		t.Fatalf("cocreate sessions leaked across projects: first=%+v second=%+v", firstView, secondView)
	}

	confirmed := firstView
	confirmed.Confirmed = true
	if err := saveWebCoCreate(first, confirmed); err != nil {
		t.Fatal(err)
	}
	reloaded, err := readWebCoCreate(first)
	if err != nil {
		t.Fatal(err)
	}
	if !reloaded.Confirmed {
		t.Fatal("confirmation was not persisted")
	}
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

func TestProjectMetadataCreateEditArchiveAndRestoreBeforeModelSetup(t *testing.T) {
	s := newTestServer(t, false)
	w := request(s, "POST", "/api/workspaces", `{"create":true,"name":"初始项目"}`)
	if w.Code != 200 {
		t.Fatalf("create project: %d %s", w.Code, w.Body.String())
	}
	var created struct {
		Directory string `json:"directory"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil || created.Directory == "" {
		t.Fatalf("decode create response: %s (%v)", w.Body.String(), err)
	}

	patchBody, _ := json.Marshal(map[string]any{
		"directory": created.Directory,
		"name":      "改名后的项目",
		"genre":     "东方玄幻",
		"tags":      []string{"长篇", "连载"},
	})
	if w := request(s, "PATCH", "/api/workspaces", string(patchBody)); w.Code != 200 {
		t.Fatalf("edit project: %d %s", w.Code, w.Body.String())
	}
	project, err := readProject(created.Directory)
	if err != nil {
		t.Fatal(err)
	}
	if project.Name != "改名后的项目" || project.Genre != "东方玄幻" || len(project.Tags) != 2 {
		t.Fatalf("project metadata not saved: %+v", project)
	}

	if w := request(s, "POST", "/api/workspaces/archive", mustJSON(t, map[string]any{
		"directory": created.Directory,
		"archived":  true,
	})); w.Code != 200 {
		t.Fatalf("archive project: %d %s", w.Code, w.Body.String())
	}
	if w := request(s, "POST", "/api/workspaces", mustJSON(t, map[string]string{"path": created.Directory})); w.Code != 409 {
		t.Fatalf("archived project should reject opening: %d %s", w.Code, w.Body.String())
	}
	if w := request(s, "PATCH", "/api/workspaces", mustJSON(t, map[string]any{
		"directory": created.Directory,
		"archived":  false,
	})); w.Code != 400 {
		t.Fatalf("archive state must use dedicated endpoint: %d %s", w.Code, w.Body.String())
	}

	if w := request(s, "POST", "/api/workspaces/archive", mustJSON(t, map[string]any{
		"directory": created.Directory,
		"archived":  false,
	})); w.Code != 200 {
		t.Fatalf("restore project: %d %s", w.Code, w.Body.String())
	}
	if w := request(s, "POST", "/api/workspaces", mustJSON(t, map[string]string{"path": created.Directory})); w.Code != 200 {
		t.Fatalf("restored project should open without a model: %d %s", w.Code, w.Body.String())
	}
}

func TestProjectUpdateOnlyInvalidatesPlanSignificantChanges(t *testing.T) {
	s := newTestServer(t, true)
	project := projectInfo{Name: "审批测试", Genre: "悬疑"}
	if err := saveProject(s.dir, project); err != nil {
		t.Fatal(err)
	}
	st := store.NewStore(s.dir)
	if err := st.Init(); err != nil {
		t.Fatal(err)
	}
	if err := st.RunMeta.Init("default", "test", "test-model"); err != nil {
		t.Fatal(err)
	}
	if err := st.Book.Save(domain.BookMetadata{Title: "审批测试", Synopsis: "验证规划确认失效"}); err != nil {
		t.Fatal(err)
	}
	if err := st.Outline.SavePremise("雨夜中的失踪案"); err != nil {
		t.Fatal(err)
	}
	if err := st.Outline.SaveOutline([]domain.OutlineEntry{{Chapter: 1, Title: "雨夜", CoreEvent: "发现线索"}}); err != nil {
		t.Fatal(err)
	}
	if err := st.Characters.Save([]domain.Character{{Name: "主角", Role: "主角", Description: "测试角色"}}); err != nil {
		t.Fatal(err)
	}
	if err := st.World.SaveWorldRules([]domain.WorldRule{{Category: "society", Rule: "规则", Boundary: "边界"}}); err != nil {
		t.Fatal(err)
	}
	fingerprint, err := st.PlanningFingerprint()
	if err != nil {
		t.Fatal(err)
	}
	foundationFingerprint, err := st.FoundationFingerprint()
	if err != nil {
		t.Fatal(err)
	}
	if err := st.RunMeta.UpdateWorkbench(func(w *domain.WorkbenchControl) error {
		w.ApprovedFingerprint = fingerprint
		w.ApprovedFoundationFingerprint = foundationFingerprint
		w.BatchSize = 3
		w.BatchEnd = 3
		w.AcceptedChapter = 1
		w.Feedback = []domain.WorkbenchFeedback{{Kind: "chapter", Chapter: 1, Text: "保留"}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	if w := request(s, "PATCH", "/api/workspaces", mustJSON(t, map[string]any{
		"directory": s.dir,
		"name":      "仅改名",
		"tags":      []string{"归档标签"},
	})); w.Code != 200 {
		t.Fatalf("name/tag edit: %d %s", w.Code, w.Body.String())
	}
	meta, err := st.RunMeta.Load()
	if err != nil {
		t.Fatal(err)
	}
	if meta.Workbench.ApprovedFingerprint != fingerprint || meta.Workbench.ApprovedFoundationFingerprint != foundationFingerprint || meta.Workbench.BatchEnd != 3 || len(meta.Workbench.Feedback) != 1 {
		t.Fatalf("non-planning edits should preserve approval state: %+v", meta.Workbench)
	}

	if w := request(s, "PATCH", "/api/workspaces", mustJSON(t, map[string]any{
		"directory": s.dir,
		"genre":     "社会派推理",
	})); w.Code != 200 {
		t.Fatalf("planning edit: %d %s", w.Code, w.Body.String())
	}
	meta, err = st.RunMeta.Load()
	if err != nil {
		t.Fatal(err)
	}
	if meta.Workbench.ApprovedFingerprint != "" || meta.Workbench.ApprovedFoundationFingerprint != "" || meta.Workbench.BatchEnd != 0 || len(meta.Workbench.Feedback) != 1 || meta.Workbench.Feedback[0].Text != "保留" {
		t.Fatalf("planning edit should invalidate approval and batch while preserving user history: %+v", meta.Workbench)
	}
}

func TestWorkflowViewWaitsForWholeBatchBeforeReview(t *testing.T) {
	dir := t.TempDir()
	st := store.NewStore(dir)
	if err := st.Init(); err != nil {
		t.Fatal(err)
	}
	if err := st.RunMeta.Init("default", "test", "test"); err != nil {
		t.Fatal(err)
	}
	if err := st.Progress.Init(2); err != nil {
		t.Fatal(err)
	}
	if err := st.Progress.UpdatePhase(domain.PhaseWriting); err != nil {
		t.Fatal(err)
	}
	if err := st.Book.Save(domain.BookMetadata{Title: "批次审核测试", Synopsis: "验证整批完成后统一审核"}); err != nil {
		t.Fatal(err)
	}
	if err := st.Outline.SavePremise("premise"); err != nil {
		t.Fatal(err)
	}
	if err := st.Outline.SaveOutline([]domain.OutlineEntry{
		{Chapter: 1, Title: "一"},
		{Chapter: 2, Title: "二"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.Characters.Save([]domain.Character{{Name: "主角", Role: "主角", Description: "测试角色"}}); err != nil {
		t.Fatal(err)
	}
	if err := st.World.SaveWorldRules([]domain.WorldRule{{Category: "society", Rule: "规则", Boundary: "边界"}}); err != nil {
		t.Fatal(err)
	}
	fingerprint, err := st.PlanningFingerprint()
	if err != nil {
		t.Fatal(err)
	}
	foundationFingerprint, err := st.FoundationFingerprint()
	if err != nil {
		t.Fatal(err)
	}
	if err := st.RunMeta.UpdateWorkbench(func(w *domain.WorkbenchControl) error {
		w.ApprovedFingerprint = fingerprint
		w.ApprovedFoundationFingerprint = foundationFingerprint
		w.BatchSize, w.BatchEnd = 2, 2
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.Progress.MarkChapterComplete(1, 1000, "", ""); err != nil {
		t.Fatal(err)
	}
	workflow, err := workflowView(st)
	if err != nil {
		t.Fatal(err)
	}
	if workflow["stage"] != "writing" {
		t.Fatalf("批次内第一章完成后仍应显示写作中: %+v", workflow)
	}
	if err := st.Progress.MarkChapterComplete(2, 1000, "", ""); err != nil {
		t.Fatal(err)
	}
	workflow, err = workflowView(st)
	if err != nil {
		t.Fatal(err)
	}
	if workflow["stage"] != "chapter-review" {
		t.Fatalf("整批完成后应进入统一审核: %+v", workflow)
	}
}

func TestWorkflowViewAllowsRevisionBeforeAndAfterCompletedChapters(t *testing.T) {
	dir := t.TempDir()
	st := store.NewStore(dir)
	if err := st.Init(); err != nil {
		t.Fatal(err)
	}
	if err := st.RunMeta.Init("default", "test", "test"); err != nil {
		t.Fatal(err)
	}
	if err := st.Progress.Init(1); err != nil {
		t.Fatal(err)
	}
	if err := st.Progress.UpdatePhase(domain.PhaseOutline); err != nil {
		t.Fatal(err)
	}
	if err := st.Book.Save(domain.BookMetadata{Title: "规划修改测试", Synopsis: "验证首次正文前仍可修改"}); err != nil {
		t.Fatal(err)
	}
	if err := st.Outline.SavePremise("premise"); err != nil {
		t.Fatal(err)
	}
	if err := st.Outline.SaveOutline([]domain.OutlineEntry{{Chapter: 1, Title: "一"}}); err != nil {
		t.Fatal(err)
	}
	if err := st.Characters.Save([]domain.Character{{Name: "主角", Role: "主角", Description: "测试角色"}}); err != nil {
		t.Fatal(err)
	}
	if err := st.World.SaveWorldRules([]domain.WorldRule{{Category: "society", Rule: "规则", Boundary: "边界"}}); err != nil {
		t.Fatal(err)
	}
	fingerprint, err := st.PlanningFingerprint()
	if err != nil {
		t.Fatal(err)
	}
	foundationFingerprint, err := st.FoundationFingerprint()
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Outline.SaveFoundationAudit(domain.FoundationAudit{
		Fingerprint: foundationFingerprint, Ready: true, Summary: "规划一致",
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.RunMeta.UpdateWorkbench(func(w *domain.WorkbenchControl) error {
		w.ApprovedFingerprint = fingerprint
		w.ApprovedFoundationFingerprint = foundationFingerprint
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	workflow, err := workflowView(st)
	if err != nil {
		t.Fatal(err)
	}
	if workflow["approved"] != true || workflow["canRevise"] != true {
		t.Fatalf("尚未开写时应允许修改已确认规划: %+v", workflow)
	}

	if err := st.Progress.MarkChapterComplete(1, 1000, "", ""); err != nil {
		t.Fatal(err)
	}
	workflow, err = workflowView(st)
	if err != nil {
		t.Fatal(err)
	}
	if workflow["canRevise"] != true {
		t.Fatalf("正文开始后仍应允许修改规划并重新审查: %+v", workflow)
	}
}

func TestWorkflowViewAllowsRevisionAfterFailedFoundationAudit(t *testing.T) {
	dir := t.TempDir()
	st := store.NewStore(dir)
	if err := st.Init(); err != nil {
		t.Fatal(err)
	}
	if err := st.RunMeta.Init("default", "test", "test"); err != nil {
		t.Fatal(err)
	}
	if err := st.Progress.Init(1); err != nil {
		t.Fatal(err)
	}
	if err := st.Progress.UpdatePhase(domain.PhaseOutline); err != nil {
		t.Fatal(err)
	}
	if err := st.Book.Save(domain.BookMetadata{Title: "审计失败修改测试", Synopsis: "验证审计问题不会锁死规划修改"}); err != nil {
		t.Fatal(err)
	}
	if err := st.Outline.SavePremise("premise"); err != nil {
		t.Fatal(err)
	}
	if err := st.Outline.SaveOutline([]domain.OutlineEntry{{Chapter: 1, Title: "一"}}); err != nil {
		t.Fatal(err)
	}
	if err := st.Characters.Save([]domain.Character{{Name: "主角", Role: "主角", Description: "测试角色"}}); err != nil {
		t.Fatal(err)
	}
	if err := st.World.SaveWorldRules([]domain.WorldRule{{Category: "society", Rule: "规则", Boundary: "边界"}}); err != nil {
		t.Fatal(err)
	}
	foundationFingerprint, err := st.FoundationFingerprint()
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Outline.SaveFoundationAudit(domain.FoundationAudit{
		Fingerprint: foundationFingerprint,
		Ready:       false,
		Summary:     "规划存在冲突",
	}); err != nil {
		t.Fatal(err)
	}

	workflow, err := workflowView(st)
	if err != nil {
		t.Fatal(err)
	}
	if workflow["auditState"] != "failed" || workflow["canRepair"] != true || workflow["canRevise"] != true {
		t.Fatalf("审计失败时应同时提供重新审查和人工修改入口: %+v", workflow)
	}

	if err := st.Progress.MarkChapterComplete(1, 1000, "", ""); err != nil {
		t.Fatal(err)
	}
	workflow, err = workflowView(st)
	if err != nil {
		t.Fatal(err)
	}
	if workflow["canRevise"] != true {
		t.Fatalf("正文开始后审计失败仍应允许修正规划: %+v", workflow)
	}
}

func TestWorkflowViewDistinguishesLegacyWritingFromUnreviewedPlanning(t *testing.T) {
	setup := func(t *testing.T, phase domain.Phase) *store.Store {
		t.Helper()
		st := store.NewStore(t.TempDir())
		if err := st.Init(); err != nil {
			t.Fatal(err)
		}
		if err := st.RunMeta.Init("default", "test", "test"); err != nil {
			t.Fatal(err)
		}
		if err := st.Progress.Init(1); err != nil {
			t.Fatal(err)
		}
		if err := st.Progress.UpdatePhase(phase); err != nil {
			t.Fatal(err)
		}
		if err := st.Book.Save(domain.BookMetadata{Title: "流程状态", Synopsis: "验证旧项目兼容入口"}); err != nil {
			t.Fatal(err)
		}
		if err := st.Outline.SavePremise("premise"); err != nil {
			t.Fatal(err)
		}
		if err := st.Outline.SaveOutline([]domain.OutlineEntry{{Chapter: 1, Title: "一"}}); err != nil {
			t.Fatal(err)
		}
		if err := st.Characters.Save([]domain.Character{{Name: "主角", Role: "主角", Description: "测试角色"}}); err != nil {
			t.Fatal(err)
		}
		if err := st.World.SaveWorldRules([]domain.WorldRule{{Category: "society", Rule: "规则", Boundary: "边界"}}); err != nil {
			t.Fatal(err)
		}
		return st
	}

	t.Run("legacy writing project without audit", func(t *testing.T) {
		st := setup(t, domain.PhaseWriting)
		workflow, err := workflowView(st)
		if err != nil {
			t.Fatal(err)
		}
		if workflow["stage"] != "plan-review" || workflow["canApprove"] != true ||
			workflow["canRepair"] != false || workflow["recoverable"] != false {
			t.Fatalf("legacy writing project should enter plan review directly: %+v", workflow)
		}
	})

	t.Run("unreviewed planning project", func(t *testing.T) {
		st := setup(t, domain.PhaseOutline)
		workflow, err := workflowView(st)
		if err != nil {
			t.Fatal(err)
		}
		if workflow["stage"] != "foundation-audit" || workflow["canRepair"] != true ||
			workflow["canApprove"] != false {
			t.Fatalf("unreviewed planning project should require foundation audit: %+v", workflow)
		}
	})

	t.Run("legacy revision requires a new audit", func(t *testing.T) {
		st := setup(t, domain.PhaseWriting)
		if err := st.RunMeta.UpdateWorkbench(func(w *domain.WorkbenchControl) error {
			w.PlanningRevisionPending = true
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		workflow, err := workflowView(st)
		if err != nil {
			t.Fatal(err)
		}
		if workflow["canRevise"] != true || workflow["canApprove"] != false ||
			workflow["approved"] != false || workflow["canRepair"] != true {
			t.Fatalf("legacy revision must wait for audit without locking edits: %+v", workflow)
		}
	})

	t.Run("pending revision cannot reuse a ready audit", func(t *testing.T) {
		st := setup(t, domain.PhaseWriting)
		fp, err := st.PlanningFingerprint()
		if err != nil {
			t.Fatal(err)
		}
		foundationFP, err := st.FoundationFingerprint()
		if err != nil {
			t.Fatal(err)
		}
		if err := st.Outline.SaveFoundationAudit(domain.FoundationAudit{
			Fingerprint: foundationFP, Ready: true, Summary: "旧规划通过",
		}); err != nil {
			t.Fatal(err)
		}
		if err := st.RunMeta.UpdateWorkbench(func(w *domain.WorkbenchControl) error {
			w.ApprovedFingerprint, w.ApprovedFoundationFingerprint = fp, foundationFP
			w.PlanningRevisionPending = true
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		workflow, err := workflowView(st)
		if err != nil {
			t.Fatal(err)
		}
		if workflow["auditState"] != "stale" || workflow["approved"] != false ||
			workflow["canApprove"] != false || workflow["canRepair"] != true {
			t.Fatalf("pending intent must invalidate the previous approval and audit: %+v", workflow)
		}
	})

	t.Run("incomplete foundation can still be revised", func(t *testing.T) {
		st := setup(t, domain.PhaseOutline)
		if err := st.Characters.Save(nil); err != nil {
			t.Fatal(err)
		}
		workflow, err := workflowView(st)
		if err != nil {
			t.Fatal(err)
		}
		if workflow["canRevise"] != true || workflow["canApprove"] != false {
			t.Fatalf("missing artifacts must not lock the repair path: %+v", workflow)
		}
	})
}

func TestExplicitEmptyRoleConfigurationClearsProjectOverrides(t *testing.T) {
	s := newTestServer(t, true)
	project := projectInfo{
		Name:           "角色配置",
		RoleModels:     map[string]bootstrap.ModelRef{"writer": {Provider: "local", Model: "test-model"}},
		RoleDirectives: map[string]string{"writer": "保持冷峻"},
	}
	if err := saveProject(s.dir, project); err != nil {
		t.Fatal(err)
	}
	if w := request(s, "PATCH", "/api/workspaces", mustJSON(t, map[string]any{
		"directory":      s.dir,
		"roleModels":     map[string]any{},
		"roleDirectives": map[string]any{},
	})); w.Code != 200 {
		t.Fatalf("clear role overrides: %d %s", w.Code, w.Body.String())
	}
	got, err := readProject(s.dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.RoleModels) != 0 || len(got.RoleDirectives) != 0 {
		t.Fatalf("explicit empty maps should clear overrides: %+v", got)
	}
}

func TestProjectRoleModelsPreserveFallbackAndReasoning(t *testing.T) {
	cfg := bootstrap.Config{Roles: map[string]bootstrap.RoleConfig{
		"writer": {
			Provider: "default", Model: "default-model",
			Fallbacks:       []bootstrap.ModelRef{{Provider: "backup", Model: "backup-model"}},
			ReasoningEffort: "high",
		},
	}}
	applyProjectRoleConfigs(&cfg, map[string]bootstrap.ModelRef{
		"writer": {Provider: "local", Model: "style-model"},
	})
	got := cfg.Roles["writer"]
	if got.Provider != "local" || got.Model != "style-model" {
		t.Fatalf("project role override not applied: %+v", got)
	}
	if len(got.Fallbacks) != 1 || got.ReasoningEffort != "high" {
		t.Fatalf("provider/model override must preserve fallback and reasoning: %+v", got)
	}
}

func TestPlanningApprovalBindsExactVersionAndAllowsWriterGate(t *testing.T) {
	s := newTestServer(t, true)
	st := store.NewStore(s.dir)
	for _, err := range []error{
		st.Book.Save(domain.BookMetadata{Title: "审批测试", Synopsis: "验证规划确认"}),
		st.Outline.SavePremise("一个必须在确认后开写的故事"),
		st.Outline.SaveOutline([]domain.OutlineEntry{{Chapter: 1, Title: "起点", CoreEvent: "主角出发"}}),
		st.Characters.Save([]domain.Character{{Name: "林舟", Role: "主角", Description: "谨慎的调查者"}}),
		st.World.SaveWorldRules([]domain.WorldRule{{Category: "society", Rule: "证据必须可追溯", Boundary: "不能凭空破案"}}),
		st.Progress.Init(3),
		st.Progress.UpdatePhase(domain.PhaseWriting),
	} {
		if err != nil {
			t.Fatal(err)
		}
	}
	fingerprint, err := st.PlanningFingerprint()
	if err != nil {
		t.Fatal(err)
	}
	if err := s.rt.ApprovePlanning(""); err == nil {
		t.Fatal("empty fingerprint must not approve planning")
	}
	if err := s.rt.ApprovePlanning(fingerprint); err != nil {
		t.Fatal(err)
	}
	if err := st.Outline.SaveOutline([]domain.OutlineEntry{{Chapter: 1, Title: "改后的起点", CoreEvent: "主角改变路线"}}); err != nil {
		t.Fatal(err)
	}
	if err := s.rt.ApprovePlanning(fingerprint); err == nil {
		t.Fatal("stale approval should be rejected after plan changes")
	}
}

func TestStyleSourceAPIValidatesAndManagesSharedCorpus(t *testing.T) {
	s := newTestServer(t, false)
	created := request(s, "POST", "/api/style/entries", `{"name":"冷峻悬疑"}`)
	var entry struct {
		ID string `json:"id"`
	}
	if created.Code != 201 || json.Unmarshal(created.Body.Bytes(), &entry) != nil {
		t.Fatal(created.Code, created.Body.String())
	}
	if w := request(s, "POST", "/api/style/upload", `{"name":"../outside.md","content":"text"}`); w.Code != 400 {
		t.Fatalf("path traversal accepted: %d %s", w.Code, w.Body.String())
	}
	if w := request(s, "POST", "/api/style/upload", `{"name":"sample.pdf","content":"text"}`); w.Code != 400 {
		t.Fatalf("invalid extension accepted: %d %s", w.Code, w.Body.String())
	}
	if w := request(s, "POST", "/api/style/upload", mustJSON(t, map[string]string{"entryId": entry.ID, "name": "sample.md", "content": "冷雨敲窗。短句推进，避免解释。"})); w.Code != 200 {
		t.Fatalf("upload corpus: %d %s", w.Code, w.Body.String())
	}
	if w := request(s, "GET", "/api/style?entryId="+entry.ID, ""); w.Code != 200 || !strings.Contains(w.Body.String(), "sample.md") {
		t.Fatalf("list corpus: %d %s", w.Code, w.Body.String())
	}
	if w := request(s, "POST", "/api/style/delete", mustJSON(t, map[string]string{"entryId": entry.ID, "name": "sample.md"})); w.Code != 200 {
		t.Fatalf("delete corpus: %d %s", w.Code, w.Body.String())
	}
	if _, err := os.Stat(filepath.Join(styleRoot(s.dir), "sample.md")); !os.IsNotExist(err) {
		t.Fatal("shared corpus was written into a novel", err)
	}
	if err := validateStyleSourceContent(string([]byte{0xff, 0xfe})); err == nil {
		t.Fatal("invalid UTF-8 accepted")
	}
	if err := validateStyleSourceContent(strings.Repeat("a", maxStyleSourceBytes+1)); err == nil {
		t.Fatal("oversized corpus accepted")
	}
}

func TestStyleRootRejectsSymlink(t *testing.T) {
	projectDir := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, styleRoot(projectDir)); err != nil {
		t.Skipf("symlink unavailable on this host: %v", err)
	}
	if _, err := ensureStyleRoot(projectDir); err == nil {
		t.Fatal("symlinked corpus root accepted")
	}
}

func TestPlanningSurfacesPartialCorruptionWithWarnings(t *testing.T) {
	s := newTestServer(t, false)
	st := store.NewStore(s.dir)
	if err := st.Outline.SavePremise("仍然可读的故事内核"); err != nil {
		t.Fatal(err)
	}
	if err := st.Outline.SaveOutline([]domain.OutlineEntry{{Chapter: 1, Title: "可见目录"}}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(s.dir, "characters.json"), []byte("{broken"), 0o600); err != nil {
		t.Fatal(err)
	}
	w := request(s, "GET", "/api/planning", "")
	if w.Code != 200 {
		t.Fatalf("planning: %d %s", w.Code, w.Body.String())
	}
	var result struct {
		Premise  string   `json:"premise"`
		Outline  []any    `json:"outline"`
		Warnings []string `json:"warnings"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Premise != "仍然可读的故事内核" || len(result.Outline) != 1 || len(result.Warnings) == 0 {
		t.Fatalf("partial planning data not preserved with warnings: %+v", result)
	}
}

func TestAuditExcludesProseAndRedactsAPIKeys(t *testing.T) {
	s := newTestServer(t, true)
	const prose = "这段正文不应出现在审计响应中"
	const secret = "test-secret-do-not-return"
	st := store.NewStore(s.dir)
	if err := st.Drafts.SaveFinalChapter(1, prose); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Decisions.Append(store.DecisionRecord{
		Kind: "intervention", Decider: "arbiter",
		Input: "调用 http://example.test?key=" + secret,
	}); err != nil {
		t.Fatal(err)
	}
	w := request(s, "GET", "/api/audit?chapter=1", "")
	if w.Code != 200 {
		t.Fatalf("audit: %d %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if strings.Contains(body, prose) || strings.Contains(body, secret) {
		t.Fatalf("audit leaked prose or credential: %s", body)
	}
	if !strings.Contains(body, "[已隐藏]") {
		t.Fatalf("audit should expose redaction marker: %s", body)
	}
}

func TestStaticWorkbenchContract(t *testing.T) {
	appJS, err := staticFiles.ReadFile("static/app.js")
	if err != nil {
		t.Fatal(err)
	}
	appCSS, err := staticFiles.ReadFile("static/app.css")
	if err != nil {
		t.Fatal(err)
	}
	indexHTML, err := staticFiles.ReadFile("static/index.html")
	if err != nil {
		t.Fatal(err)
	}
	modelsJS, err := staticFiles.ReadFile("static/models.js")
	if err != nil {
		t.Fatal(err)
	}
	js := string(appJS) + string(modelsJS)
	if strings.Contains(js, "innerHTML") {
		t.Fatal("app.js must use DOM APIs instead of innerHTML")
	}
	if strings.Contains(js, "outline.slice(0, 7)") {
		t.Fatal("章节地图不得截断完整大纲")
	}
	for _, required := range []string{
		"/api/workspaces", "/api/planning", "/api/style/upload", "/api/audit",
		"/api/export", "/api/workspaces/delete", "/api/config/models",
		"approve-plan", "revise-plan", "repair-plan", "恢复旧项目并重新审查规划",
		"planning.layered", "estimatedChapters", "plan-arc", "历史骨架待补齐",
		"chapter-feedback", "accept-chapter", "batch",
		"等待批次完成", "/api/cocreate", "/api/cocreate/confirm",
		"cocreate-delta", "cocreate-updated",
		"download(\"txt\", project)", "download(\"epub\", project)",
		"永久删除", "saveExport", `filename\*`,
	} {
		if !strings.Contains(js, required) {
			t.Fatalf("app.js missing workbench contract %q", required)
		}
	}
	css := string(appCSS)
	for _, required := range []string{
		".plan-outline", "max-height: min(62vh, 720px)", "overflow-y: auto",
		".plan-volume-summary", ".plan-arc-summary", ".plan-skeleton-note",
		"#outline-preview", "max-height: min(48vh, 520px)",
		"scrollbar-gutter: stable",
	} {
		if !strings.Contains(css, required) {
			t.Fatalf("app.css missing planning scroll contract %q", required)
		}
	}
	html := string(indexHTML)
	for _, required := range []string{
		"rewrite-auto-apply", "chapter-version-list", "save-chapter-plan", "save-chapter-content", "可滚动查看全部章节目录与详细规划",
		"确认创意并生成规划", "从上游获取模型", "model-options",
	} {
		if !strings.Contains(html, required) {
			t.Fatalf("index.html missing workbench contract %q", required)
		}
	}
}

func mustJSON(t *testing.T, value any) string {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}
