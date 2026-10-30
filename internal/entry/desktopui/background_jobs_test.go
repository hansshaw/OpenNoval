package desktopui

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hansshaw/OpenNovel/internal/bootstrap"
	"github.com/hansshaw/OpenNovel/internal/domain"
	"github.com/hansshaw/OpenNovel/internal/host"
	"github.com/hansshaw/OpenNovel/internal/projectjobs"
	"github.com/hansshaw/OpenNovel/internal/store"
)

func backgroundProjectFixture(t *testing.T, s *Server, name string) string {
	t.Helper()
	dir := filepath.Join(s.booksDir, name)
	st := store.NewStore(dir)
	for _, err := range []error{
		st.Init(), st.RunMeta.Init("default", "local", "test-model"),
		st.Progress.Init(10), st.Progress.UpdatePhase(domain.PhaseWriting),
		st.Book.Save(domain.BookMetadata{Title: name, Synopsis: "后台测试"}),
		st.Outline.SavePremise("premise"),
		st.Outline.SaveOutline([]domain.OutlineEntry{{Chapter: 1, Title: "一"}, {Chapter: 2, Title: "二"}}),
		st.Characters.Save([]domain.Character{{Name: "主角", Role: "主角", Description: "测试角色"}}),
		st.World.SaveWorldRules([]domain.WorldRule{{Category: "society", Rule: "规则", Boundary: "边界"}}),
		saveProject(dir, projectInfo{Name: name}),
	} {
		if err != nil {
			t.Fatal(err)
		}
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
	return dir
}

func TestBackgroundHTTPAdmissionCancelAndOwnership(t *testing.T) {
	s := newTestServer(t, true)
	dir := backgroundProjectFixture(t, s, "queued")
	canonicalDir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	started := make(chan projectjobs.Job, 1)
	queue, err := projectjobs.New(projectjobs.Config{MaxConcurrent: 1}, projectjobs.RunnerFunc(func(ctx context.Context, job projectjobs.Job, report projectjobs.Reporter) error {
		started <- job
		if err := report.Emit("test", "only-background", nil); err != nil {
			return err
		}
		<-ctx.Done()
		return ctx.Err()
	}))
	if err != nil {
		t.Fatal(err)
	}
	s.background = queue
	body, _ := json.Marshal(map[string]any{"directory": dir, "kind": "batch", "count": 2, "budgetUSD": 1})
	w := request(s, "POST", "/api/jobs", string(body))
	if w.Code != 201 {
		t.Fatal(w.Code, w.Body.String())
	}
	var job projectjobs.Job
	if err := json.Unmarshal(w.Body.Bytes(), &job); err != nil {
		t.Fatal(err)
	}
	if job.ProjectID != canonicalDir || job.ProviderID != "local" || job.BudgetUSD != 1 {
		t.Fatalf("wrong job binding: %+v", job)
	}
	if strings.Contains(string(job.Payload), "test-secret-do-not-return") {
		t.Fatal("job journal exposed a credential")
	}
	select {
	case actual := <-started:
		if actual.ID != job.ID {
			t.Fatal("executed a different job")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("background runner did not start")
	}
	if duplicate := request(s, "POST", "/api/jobs", string(body)); duplicate.Code != 409 {
		t.Fatal("duplicate project jobs bypassed chapter review")
	}
	listed := request(s, "GET", "/api/jobs", "")
	if listed.Code != 200 || !strings.Contains(listed.Body.String(), `"after_sequence":`) {
		t.Fatal(listed.Code, listed.Body.String())
	}
	var listing struct {
		Jobs   []projectjobs.Job     `json:"jobs"`
		Events projectjobs.EventPage `json:"events"`
	}
	if err := json.Unmarshal(listed.Body.Bytes(), &listing); err != nil {
		t.Fatal(err)
	}
	if len(listing.Jobs) != 1 || listing.Jobs[0].ID != job.ID || listing.Jobs[0].ProjectID != canonicalDir {
		t.Fatalf("listed job lost its canonical project binding: %+v", listing.Jobs)
	}
	for _, event := range listing.Events.Events {
		if event.JobID != job.ID || event.ProjectID != canonicalDir || event.ProviderID != "local" {
			t.Fatalf("listed event lost its canonical task ownership: %+v", event)
		}
	}
	s.mu.Lock()
	guarded := s.backgroundProjectBusyLocked(dir) && s.backgroundAnyBusyLocked()
	isolated := len(s.events) == 0 && s.stream == "" && !s.busy
	s.mu.Unlock()
	if !guarded || !isolated {
		t.Fatal("background job ownership/guard leaked into foreground")
	}
	if testConfig := request(s, "POST", "/api/config/test", `{"name":"local","model":"test-model"}`); testConfig.Code != 409 {
		t.Fatal("model connection test bypassed active background provider quota")
	}
	if canceled := request(s, "POST", "/api/jobs/"+job.ID+"/cancel", `{}`); canceled.Code != 200 {
		t.Fatal(canceled.Code, canceled.Body.String())
	}
	if missing := request(s, "POST", "/api/jobs/missing/cancel", `{}`); missing.Code != 404 {
		t.Fatal(missing.Code, missing.Body.String())
	}
}

func TestBackgroundHTTPRejectsInvalidProjectsAndWorkflow(t *testing.T) {
	s := newTestServer(t, true)
	dir := backgroundProjectFixture(t, s, "unapproved")
	st := store.NewStore(dir)
	if err := st.RunMeta.UpdateWorkbench(func(w *domain.WorkbenchControl) error { w.ApprovedFingerprint = ""; return nil }); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		body any
		code int
	}{
		{map[string]any{"directory": dir, "kind": "start", "count": 1}, 400},
		{map[string]any{"directory": dir, "kind": "batch", "count": 101}, 400},
		{map[string]any{"directory": dir, "kind": "batch", "count": 1, "budgetUSD": -1}, 400},
		{map[string]any{"directory": dir, "kind": "batch", "count": 1, "provider": "local"}, 400},
		{map[string]any{"directory": dir, "kind": "batch", "count": 1}, 409},
		{map[string]any{"directory": s.dir, "kind": "batch", "count": 1}, 409},
		{map[string]any{"directory": t.TempDir(), "kind": "batch", "count": 1}, 400},
	} {
		body := mustJSON(t, test.body)
		w := request(s, "POST", "/api/jobs", body)
		if w.Code != test.code {
			t.Fatalf("%s: %d %s", body, w.Code, w.Body.String())
		}
	}
	if w := request(s, "GET", "/api/jobs?after=invalid", ""); w.Code != 400 {
		t.Fatal("invalid event cursor accepted")
	}
	if w := request(s, "GET", "/api/jobs/limits", ""); w.Code != 200 || !strings.Contains(w.Body.String(), `"providerLimit":3`) {
		t.Fatal(w.Code, w.Body.String())
	}
	if w := request(s, "POST", "/api/jobs/limits", `{"maxConcurrent":4,"providerLimit":4}`); w.Code != 200 {
		t.Fatal("valid limits were rejected", w.Code, w.Body.String())
	}
}

func TestBackgroundHTTPPinsDifferentProvidersWithoutChangingGlobalModel(t *testing.T) {
	s := newTestServer(t, true)
	dirA := backgroundProjectFixture(t, s, "provider-A")
	dirB := backgroundProjectFixture(t, s, "provider-B")
	s.baseCfg.Providers["second"] = bootstrap.ProviderConfig{Type: "openai", BaseURL: "http://127.0.0.1:1/v1", Models: []bootstrap.ModelConfig{{Name: "second-model"}}}
	started := make(chan projectjobs.Job, 2)
	queue, err := projectjobs.New(projectjobs.Config{MaxConcurrent: 2, DefaultProviderLimit: 1}, projectjobs.RunnerFunc(func(ctx context.Context, job projectjobs.Job, report projectjobs.Reporter) error {
		started <- job
		<-ctx.Done()
		return ctx.Err()
	}))
	if err != nil {
		t.Fatal(err)
	}
	s.background = queue
	for _, input := range []map[string]any{
		{"directory": dirA, "kind": "batch", "count": 1, "provider": "local", "model": "test-model"},
		{"directory": dirB, "kind": "batch", "count": 1, "provider": "second", "model": "second-model"},
	} {
		body, _ := json.Marshal(input)
		w := request(s, "POST", "/api/jobs", string(body))
		if w.Code != 201 {
			t.Fatal(w.Code, w.Body.String())
		}
		var job projectjobs.Job
		if err := json.Unmarshal(w.Body.Bytes(), &job); err != nil {
			t.Fatal(err)
		}
		var pinned backgroundInput
		if err := json.Unmarshal(job.Payload, &pinned); err != nil {
			t.Fatal(err)
		}
		if job.ProviderID != input["provider"] || pinned.Model != input["model"] {
			t.Fatalf("job ignored its explicit model selection: %+v / %+v", job, pinned)
		}
	}
	for i := 0; i < 2; i++ {
		select {
		case <-started:
		case <-time.After(3 * time.Second):
			t.Fatal("two different provider tasks did not run concurrently")
		}
	}
	if s.baseCfg.Provider != "local" || s.baseCfg.ModelName != "test-model" {
		t.Fatal("per-job selection changed global model configuration")
	}
}

func TestBackgroundModelsPinSelectionAndValidateAllProviders(t *testing.T) {
	s := newTestServer(t, true)
	cfg := bootstrap.CloneConfig(s.baseCfg)
	cfg.Providers["other"] = bootstrap.ProviderConfig{Type: "openai", Models: []bootstrap.ModelConfig{{Name: "other-model"}}}
	if err := validateBackgroundModels(cfg, "local", "test-model", nil); err != nil {
		t.Fatal(err)
	}
	for _, roles := range []map[string]bootstrap.RoleConfig{
		{"writer": {Provider: "other", Model: "other-model"}},
		{"writer": {Provider: "local", Model: "test-model", Fallbacks: []bootstrap.ModelRef{{Provider: "other", Model: "other-model"}}}},
	} {
		if err := validateBackgroundModels(cfg, "local", "test-model", roles); err != nil {
			t.Fatal("valid cross-provider role rejected", err)
		}
	}
	for _, roles := range []map[string]bootstrap.RoleConfig{
		{"writer": {Provider: "local", Model: "removed-model"}},
		{"writer": {Provider: "missing", Model: "test-model"}},
	} {
		if err := validateBackgroundModels(cfg, "local", "test-model", roles); err == nil {
			t.Fatal("unsupported provider/model binding accepted")
		}
	}
	if err := validateBackgroundModels(cfg, "local", "removed-model", nil); err == nil {
		t.Fatal("deleted pinned default model was silently substituted")
	}
	dir := backgroundProjectFixture(t, s, "binding")
	canonicalDir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	input := backgroundInput{Directory: canonicalDir, Model: "removed-model", Count: 1, Style: "default"}
	payload, _ := json.Marshal(input)
	err = s.runBackgroundJob(context.Background(), projectjobs.Job{ID: "test", Request: projectjobs.Request{ProjectID: canonicalDir, ProviderID: "local", Kind: "batch", Payload: payload}}, &backgroundReport{})
	if err == nil || !strings.Contains(err.Error(), "removed-model") {
		t.Fatalf("execution did not validate pinned model before creating Host: %v", err)
	}
}

func TestBackgroundRoleDirectivesPreserveGlobalAndApplyProjectOverrides(t *testing.T) {
	base := bootstrap.Config{RoleDirectives: map[string]string{"architect": "global architect", "writer": "global writer"}}
	cfg := bootstrap.CloneConfig(base)
	applyBackgroundDirectives(&cfg, map[string]string{"writer": "project writer", "editor": "project editor"})
	if cfg.RoleDirectives["architect"] != "global architect" || cfg.RoleDirectives["writer"] != "project writer" || cfg.RoleDirectives["editor"] != "project editor" {
		t.Fatalf("background instructions differ from foreground merge: %+v", cfg.RoleDirectives)
	}
	if base.RoleDirectives["writer"] != "global writer" || base.RoleDirectives["editor"] != "" {
		t.Fatal("project directives mutated global configuration")
	}
}

func TestBackgroundCredentialsAreRedactedBeforeJournalAndAPI(t *testing.T) {
	s := newTestServer(t, false)
	cfg := bootstrap.Config{Providers: map[string]bootstrap.ProviderConfig{
		"first": {APIKey: "short-key", Extra: map[string]any{"headers": map[string]any{
			"Authorization": "Bearer header-token-private", "X-API-Key": "header-key-private",
			"X-Password": "header-password-private", "Anthropic-Version": "2023-06-01",
		}}},
		"second": {APIKey: "short-key-with-private-suffix", Extra: map[string]any{"headers": map[string]string{
			"Proxy-Authorization": "Basic cHJveHktc2VjcmV0", "X-Auth-Token": "second-auth-private",
		}}},
	}}
	secrets := []string{"short-key", "short-key-with-private-suffix", "header-token-private", "header-key-private", "header-password-private", "cHJveHktc2VjcmV0", "second-auth-private"}
	raw := strings.Join(secrets, " | ") + " | Bearer header-token-private | 2023-06-01"
	redact := func(value string) string { return redactConfig(cfg, value) }
	clean := redact(raw)
	for _, secret := range secrets {
		if strings.Contains(clean, secret) {
			t.Fatalf("configured authentication credential was not redacted: %q", secret)
		}
	}
	if strings.Contains(clean, "with-private-suffix") || !strings.Contains(clean, "2023-06-01") {
		t.Fatal("prefix keys leaked a credential suffix, or non-sensitive header was hidden")
	}
	h := newFakeBackgroundHost()
	h.start = func() error {
		h.events <- host.Event{Summary: raw, Detail: raw, Category: "ERROR"}
		h.done <- struct{}{}
		return nil
	}
	journalPath := filepath.Join(t.TempDir(), "jobs.json")
	queue, err := projectjobs.New(projectjobs.Config{MaxConcurrent: 1, PersistencePath: journalPath}, projectjobs.RunnerFunc(func(ctx context.Context, job projectjobs.Job, report projectjobs.Reporter) error {
		return executeBackgroundHost(ctx, "batch", 1, h, report, redact)
	}))
	if err != nil {
		t.Fatal(err)
	}
	s.background = queue
	job, err := queue.Submit(context.Background(), projectjobs.Request{ProjectID: "safe-project", ProviderID: "first", Kind: "batch"})
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.After(3 * time.Second)
	for {
		current, err := queue.Get(context.Background(), job.ID)
		if err != nil {
			t.Fatal(err)
		}
		if current.Status.Terminal() {
			break
		}
		select {
		case <-deadline:
			t.Fatal("credential redaction task did not complete")
		case <-time.After(time.Millisecond):
		}
	}
	api := request(s, "GET", "/api/jobs", "")
	journal, err := os.ReadFile(journalPath)
	if err != nil || api.Code != 200 {
		t.Fatal(err, api.Code)
	}
	for _, secret := range secrets {
		if strings.Contains(api.Body.String(), secret) || strings.Contains(string(journal), secret) {
			t.Fatal("authentication credential escaped into journal or jobs API")
		}
	}
}

func TestForegroundCoCreateAdmitsDuringStyleAnalysis(t *testing.T) {
	s := newTestServer(t, true)
	s.styleBusy, s.styleDirectory = true, s.dir
	// The unavailable fixture endpoint proves admission reached the model layer;
	// a style task must no longer reject the independent creation request.
	w := request(s, "POST", "/api/cocreate", `{"text":"新的想法"}`)
	if w.Code != http.StatusBadGateway {
		t.Fatal(w.Code, w.Body.String())
	}
	s.styleBusy, s.styleDirectory = false, ""
}

func TestBackgroundWorkflowRechecksPlanningAndReview(t *testing.T) {
	s := newTestServer(t, true)
	dir := backgroundProjectFixture(t, s, "workflow")
	input := backgroundInput{Directory: dir, Count: 2}
	if err := prepareBackgroundWorkflow(&input, "batch"); err != nil || input.BatchEnd != 2 || input.PlanningFingerprint == "" {
		t.Fatalf("ready project rejected: %+v %v", input, err)
	}
	st := store.NewStore(dir)
	if err := st.Progress.MarkChapterComplete(1, 100, "", ""); err != nil {
		t.Fatal(err)
	}
	if err := prepareBackgroundWorkflow(&input, "batch"); err == nil {
		t.Fatal("new batch bypassed acceptance")
	}
	if err := prepareBackgroundWorkflow(&input, "resume"); err != nil {
		t.Fatal("incomplete authorized batch could not resume:", err)
	}
	if err := st.Progress.MarkChapterComplete(2, 100, "", ""); err != nil {
		t.Fatal(err)
	}
	if err := prepareBackgroundWorkflow(&input, "resume"); err == nil {
		t.Fatal("complete batch was resumed before review")
	}
	if err := st.Outline.SavePremise("changed premise"); err != nil {
		t.Fatal(err)
	}
	if err := prepareBackgroundWorkflow(&input, "batch"); err == nil {
		t.Fatal("changed planning reused old approval")
	}
}

func TestBackgroundBoundBatchRejectsNewlyAcceptedChapterOrReapprovedPlan(t *testing.T) {
	for _, change := range []string{"accepted chapter", "reapproved plan"} {
		t.Run(change, func(t *testing.T) {
			s := newTestServer(t, true)
			dir := backgroundProjectFixture(t, s, "binding-recheck")
			input := backgroundInput{Directory: dir, Count: 2}
			if err := prepareBackgroundWorkflow(&input, "batch"); err != nil {
				t.Fatal(err)
			}
			if err := validateBackgroundWorkflowBinding(input, "batch"); err != nil {
				t.Fatal("unchanged authorization was rejected:", err)
			}
			st := store.NewStore(dir)
			if change == "accepted chapter" {
				if err := st.Progress.MarkChapterComplete(1, 100, "", ""); err != nil {
					t.Fatal(err)
				}
				if err := st.RunMeta.UpdateWorkbench(func(w *domain.WorkbenchControl) error { w.AcceptedChapter = 1; return nil }); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := st.Outline.SavePremise("another process changed this premise"); err != nil {
					t.Fatal(err)
				}
				fingerprint, err := st.PlanningFingerprint()
				if err != nil {
					t.Fatal(err)
				}
				if err := st.RunMeta.UpdateWorkbench(func(w *domain.WorkbenchControl) error { w.ApprovedFingerprint = fingerprint; return nil }); err != nil {
					t.Fatal(err)
				}
			}
			if err := validateBackgroundWorkflowBinding(input, "batch"); err == nil {
				t.Fatal("previous job authorization silently expanded to a different plan/batch")
			}
		})
	}
}

func TestBackgroundJournalLazyInitializationOutsideProject(t *testing.T) {
	s := newTestServer(t, false)
	if s.background != nil {
		t.Fatal("queue initialized before booksDir was set")
	}
	w := request(s, "GET", "/api/jobs", "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"jobs":[]`) {
		t.Fatal(w.Code, w.Body.String())
	}
	if err := s.background.Sync(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(s.booksDir), "workbench-jobs.json")); err != nil {
		t.Fatal("journal was not saved alongside books root:", err)
	}
	if _, err := os.Stat(filepath.Join(s.dir, "workbench-jobs.json")); !os.IsNotExist(err) {
		t.Fatal("journal leaked into a project copy")
	}
	s.mu.Lock()
	busy := s.backgroundProjectBusyLocked(filepath.Join(s.booksDir, "missing"))
	s.mu.Unlock()
	if busy {
		t.Fatal("an empty queue reported a nonexistent idle home as busy")
	}
}

func TestBackgroundConnectionTestReservesForegroundUntilRequestReturns(t *testing.T) {
	s := newTestServer(t, true)
	dir := backgroundProjectFixture(t, s, "test-reservation")
	received, release := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	modelServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(received)
		<-release
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"test","object":"chat.completion","model":"test-model","choices":[{"index":0,"message":{"role":"assistant","content":"OK"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`))
	}))
	defer func() { unblock(); modelServer.Close() }()
	body, _ := json.Marshal(map[string]any{"name": "local", "model": "test-model", "type": "openai", "base_url": modelServer.URL + "/v1"})
	result := make(chan *httptest.ResponseRecorder, 1)
	go func() { result <- request(s, "POST", "/api/config/test", string(body)) }()
	select {
	case <-received:
	case <-time.After(3 * time.Second):
		t.Fatal("connection test did not reach local fake model")
	}
	jobBody, _ := json.Marshal(map[string]any{"directory": dir, "kind": "batch", "count": 1})
	if w := request(s, "POST", "/api/jobs", string(jobBody)); w.Code != http.StatusCreated {
		t.Fatal("independent project rejected during connection test", w.Code, w.Body.String())
	}
	unblock()
	select {
	case w := <-result:
		if w.Code != 200 {
			t.Fatal(w.Code, w.Body.String())
		}
	case <-time.After(3 * time.Second):
		t.Fatal("connection test did not release reservation")
	}
	s.mu.Lock()
	busy := s.busy || s.jobDone != nil || s.cancelJob != nil
	s.mu.Unlock()
	if busy {
		t.Fatal("connection test leaked foreground reservation")
	}
}

type backgroundReport struct {
	mu       sync.Mutex
	messages []string
	cost     float64
}

func (r *backgroundReport) Emit(kind, message string, payload json.RawMessage) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.messages = append(r.messages, message+string(payload))
	return nil
}

func (r *backgroundReport) RecordCost(delta float64) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.cost += delta
	return nil
}

type fakeBackgroundHost struct {
	mu        sync.Mutex
	cost      float64
	events    chan host.Event
	stream    chan string
	done      chan struct{}
	start     func() error
	onClose   func()
	closeOnce sync.Once
}

func newFakeBackgroundHost() *fakeBackgroundHost {
	return &fakeBackgroundHost{events: make(chan host.Event, 16), stream: make(chan string, 16), done: make(chan struct{}, 1)}
}

func (h *fakeBackgroundHost) Resume() (string, error) { return "checkpoint", h.BeginWorkbenchBatch(1) }
func (h *fakeBackgroundHost) BeginWorkbenchBatch(int) error {
	if h.start != nil {
		return h.start()
	}
	return nil
}
func (h *fakeBackgroundHost) TotalCostUSD() float64 {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.cost
}
func (h *fakeBackgroundHost) setCost(value float64)     { h.mu.Lock(); h.cost = value; h.mu.Unlock() }
func (h *fakeBackgroundHost) Events() <-chan host.Event { return h.events }
func (h *fakeBackgroundHost) Stream() <-chan string     { return h.stream }
func (h *fakeBackgroundHost) Done() <-chan struct{}     { return h.done }
func (h *fakeBackgroundHost) Close() {
	h.closeOnce.Do(func() {
		if h.onClose != nil {
			h.onClose()
		}
		close(h.events)
		close(h.stream)
		close(h.done)
	})
}

func TestBackgroundAdapterRecordsOnlyNewCostAndRedactsEvents(t *testing.T) {
	h := newFakeBackgroundHost()
	h.cost = 3
	started := make(chan struct{})
	h.start = func() error { close(started); return nil }
	report := &backgroundReport{}
	result := make(chan error, 1)
	go func() {
		result <- executeBackgroundHost(context.Background(), "batch", 2, h, report, func(v string) string { return strings.ReplaceAll(v, "secret", "[hidden]") })
	}()
	<-started
	h.setCost(3.25)
	h.events <- host.Event{Summary: "secret event", Detail: "secret detail", Category: "SYSTEM"}
	h.stream <- "large stream omitted from durable event journal"
	h.done <- struct{}{}
	select {
	case err := <-result:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("adapter failed to drain and close Host")
	}
	if math.Abs(report.cost-0.25) > 1e-9 || len(report.messages) != 1 || strings.Contains(report.messages[0], "secret") {
		t.Fatalf("wrong cost or unsafe event: %+v", report)
	}
}

func TestBackgroundAdapterCancellationWaitsForHostAndStartup(t *testing.T) {
	h := newFakeBackgroundHost()
	started, closing := make(chan struct{}), make(chan struct{})
	allowStart, allowClose := make(chan struct{}), make(chan struct{})
	h.start = func() error { close(started); <-allowStart; return nil }
	h.onClose = func() { close(closing); <-allowClose }
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		result <- executeBackgroundHost(ctx, "resume", 1, h, &backgroundReport{}, func(v string) string { return v })
	}()
	<-started
	cancel()
	<-closing
	select {
	case <-result:
		t.Fatal("adapter released the task while Host still owned its requests")
	default:
	}
	close(allowClose)
	select {
	case <-result:
		t.Fatal("adapter returned while startup was still running")
	default:
	}
	close(allowStart)
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("canceled adapter did not finish")
	}
}

func TestBackgroundAdapterBudgetCancellationReachesHost(t *testing.T) {
	h := newFakeBackgroundHost()
	started := make(chan struct{})
	h.start = func() error { close(started); return nil }
	queue, err := projectjobs.New(projectjobs.Config{MaxConcurrent: 1}, projectjobs.RunnerFunc(func(ctx context.Context, job projectjobs.Job, report projectjobs.Reporter) error {
		return executeBackgroundHost(ctx, "batch", 1, h, report, func(v string) string { return v })
	}))
	if err != nil {
		t.Fatal(err)
	}
	defer queue.Close(context.Background())
	job, err := queue.Submit(context.Background(), projectjobs.Request{ProjectID: "budget", ProviderID: "local", Kind: "batch", BudgetUSD: 0.1})
	if err != nil {
		t.Fatal(err)
	}
	<-started
	h.setCost(0.2)
	deadline := time.After(3 * time.Second)
	for {
		current, err := queue.Get(context.Background(), job.ID)
		if err != nil {
			t.Fatal(err)
		}
		if current.Status == projectjobs.Canceled {
			if current.CostUSD != 0.2 || current.Error != projectjobs.ErrBudgetExceeded.Error() {
				t.Fatalf("budget accounting lost: %+v", current)
			}
			return
		}
		select {
		case <-deadline:
			t.Fatal("budget did not cancel and close Host")
		case <-time.After(time.Millisecond):
		}
	}
}

func TestBackgroundAdapterNeverStartsPreCanceledTask(t *testing.T) {
	h := newFakeBackgroundHost()
	h.start = func() error { t.Error("pre-canceled job started model work"); return nil }
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := executeBackgroundHost(ctx, "batch", 1, h, &backgroundReport{}, func(v string) string { return v }); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, open := <-h.events; open {
		t.Fatal("pre-canceled task did not close Host")
	}
}

func TestBackgroundAdapterStartupErrorOrPanicClosesHostAndRecordsCharges(t *testing.T) {
	for _, panics := range []bool{false, true} {
		t.Run(strconv.FormatBool(panics), func(t *testing.T) {
			h := newFakeBackgroundHost()
			h.start = func() error {
				h.setCost(0.25)
				if panics {
					panic("startup broke")
				}
				return errors.New("startup failed")
			}
			report := &backgroundReport{}
			result := make(chan error, 1)
			go func() {
				result <- executeBackgroundHost(context.Background(), "batch", 1, h, report, func(v string) string { return v })
			}()
			select {
			case err := <-result:
				if err == nil || report.cost != 0.25 {
					t.Fatalf("startup failure lost cost/error: %v / %v", err, report.cost)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("startup failure leaked Host")
			}
		})
	}
}
