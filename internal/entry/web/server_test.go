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
	s.baseCfg.Providers["local"] = pc
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
	if err := st.Outline.SavePremise("雨夜中的失踪案"); err != nil {
		t.Fatal(err)
	}
	if err := st.Outline.SaveOutline([]domain.OutlineEntry{{Chapter: 1, Title: "雨夜", CoreEvent: "发现线索"}}); err != nil {
		t.Fatal(err)
	}
	fingerprint, err := st.PlanningFingerprint()
	if err != nil {
		t.Fatal(err)
	}
	if err := st.RunMeta.UpdateWorkbench(func(w *domain.WorkbenchControl) error {
		w.ApprovedFingerprint = fingerprint
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
	if meta.Workbench.ApprovedFingerprint != fingerprint || meta.Workbench.BatchEnd != 3 || len(meta.Workbench.Feedback) != 1 {
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
	if meta.Workbench.ApprovedFingerprint != "" || meta.Workbench.BatchEnd != 0 || len(meta.Workbench.Feedback) != 0 {
		t.Fatalf("planning edit should invalidate approval, batch, and feedback: %+v", meta.Workbench)
	}
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

func TestStyleSourceAPIValidatesAndManagesProjectCorpus(t *testing.T) {
	s := newTestServer(t, false)
	if w := request(s, "POST", "/api/style/upload", `{"name":"../outside.md","content":"text"}`); w.Code != 400 {
		t.Fatalf("path traversal accepted: %d %s", w.Code, w.Body.String())
	}
	if w := request(s, "POST", "/api/style/upload", `{"name":"sample.pdf","content":"text"}`); w.Code != 400 {
		t.Fatalf("invalid extension accepted: %d %s", w.Code, w.Body.String())
	}
	if w := request(s, "POST", "/api/style/upload", `{"name":"sample.md","content":"冷雨敲窗。短句推进，避免解释。"}`); w.Code != 200 {
		t.Fatalf("upload corpus: %d %s", w.Code, w.Body.String())
	}
	if w := request(s, "GET", "/api/style", ""); w.Code != 200 || !strings.Contains(w.Body.String(), "sample.md") {
		t.Fatalf("list corpus: %d %s", w.Code, w.Body.String())
	}
	if w := request(s, "POST", "/api/style/delete", `{"name":"sample.md"}`); w.Code != 200 {
		t.Fatalf("delete corpus: %d %s", w.Code, w.Body.String())
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
	if _, err := staticFiles.ReadFile("static/app.css"); err != nil {
		t.Fatal(err)
	}
	js := string(appJS)
	if strings.Contains(js, "innerHTML") {
		t.Fatal("app.js must use DOM APIs instead of innerHTML")
	}
	for _, required := range []string{
		"/api/workspaces", "/api/planning", "/api/style/upload", "/api/audit",
		"approve-plan", "revise-plan", "chapter-feedback", "accept-chapter", "batch",
	} {
		if !strings.Contains(js, required) {
			t.Fatalf("app.js missing workbench contract %q", required)
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
