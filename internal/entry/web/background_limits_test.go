package web

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/voocel/ainovel-cli/internal/bootstrap"
	"github.com/voocel/ainovel-cli/internal/projectjobs"
)

func limitsTestServer(t *testing.T) *Server {
	t.Helper()
	s := newTestServer(t, false)
	s.baseCfg.Providers = map[string]bootstrap.ProviderConfig{"local": {}, "second": {}}
	return s
}

func readTestLimits(t *testing.T, s *Server) projectjobs.Limits {
	t.Helper()
	w := request(s, http.MethodGet, "/api/jobs/limits", "")
	if w.Code != http.StatusOK {
		t.Fatal(w.Code, w.Body.String())
	}
	var view struct {
		projectjobs.Limits
		Editable     bool `json:"editable"`
		MaxSupported int  `json:"maxSupported"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	if !view.Editable || view.MaxSupported != projectjobs.MaxConcurrency {
		t.Fatalf("limits are not editable: %s", w.Body.String())
	}
	return view.Limits
}

func TestBackgroundLimitsPersistAndAppearInJobList(t *testing.T) {
	s := limitsTestServer(t)
	initial := readTestLimits(t, s)
	if initial.MaxConcurrent != 2 || initial.ProviderLimit != 1 {
		t.Fatalf("legacy defaults changed: %+v", initial)
	}
	want := projectjobs.Limits{MaxConcurrent: 128, ProviderLimit: 64, ProviderLimits: map[string]int{"local": 96}}
	w := request(s, http.MethodPost, "/api/jobs/limits", mustJSON(t, want))
	if w.Code != http.StatusOK {
		t.Fatal(w.Code, w.Body.String())
	}
	if got := readTestLimits(t, s); !reflect.DeepEqual(got, want) {
		t.Fatalf("saved limits = %+v, want %+v", got, want)
	}
	var list struct{ Limits projectjobs.Limits }
	w = request(s, http.MethodGet, "/api/jobs", "")
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &list) != nil || !reflect.DeepEqual(list.Limits, want) {
		t.Fatalf("job list did not expose actual limits: %s", w.Body.String())
	}
	s.Close()
	restarted := limitsTestServer(t)
	restarted.booksDir = s.booksDir
	if got := readTestLimits(t, restarted); !reflect.DeepEqual(got, want) {
		t.Fatalf("restart lost limits: %+v", got)
	}
}

func TestBackgroundLimitsInvalidChangesKeepSavedValue(t *testing.T) {
	s := limitsTestServer(t)
	want := readTestLimits(t, s)
	for _, body := range []string{
		`{}`, `{"maxConcurrent":0,"providerLimit":1}`,
		`{"maxConcurrent":2.5,"providerLimit":1}`, `{"maxConcurrent":4097,"providerLimit":1}`,
		`{"maxConcurrent":8,"providerLimit":-1}`, `{"maxConcurrent":8,"providerLimit":4097}`,
		`{"maxConcurrent":8,"providerLimit":2,"providerLimits":{"local":0}}`,
		`{"maxConcurrent":8,"providerLimit":2,"providerLimits":{"missing":4}}`,
		`{"maxConcurrent":8,"providerLimit":2,"providerLimits":[]}`,
		`{"maxConcurrent":8,"providerLimit":2,"editable":true}`,
	} {
		w := request(s, http.MethodPost, "/api/jobs/limits", body)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("invalid limits accepted %s: %d %s", body, w.Code, w.Body.String())
		}
		if got := readTestLimits(t, s); !reflect.DeepEqual(got, want) {
			t.Fatalf("rejected change modified limits: %+v", got)
		}
	}
}

func TestBackgroundLimitsDiskFailureDoesNotCancelRunningTask(t *testing.T) {
	s := limitsTestServer(t)
	path := filepath.Join(filepath.Dir(s.booksDir), "workbench-jobs.json")
	started := make(chan context.Context, 1)
	queue, err := projectjobs.New(projectjobs.Config{MaxConcurrent: 8, DefaultProviderLimit: 4, PersistencePath: path}, projectjobs.RunnerFunc(func(ctx context.Context, job projectjobs.Job, report projectjobs.Reporter) error {
		started <- ctx
		<-ctx.Done()
		return ctx.Err()
	}))
	if err != nil {
		t.Fatal(err)
	}
	s.background = queue
	if _, err := queue.Submit(t.Context(), projectjobs.Request{ProjectID: "project", ProviderID: "local", Kind: "batch"}); err != nil {
		t.Fatal(err)
	}
	var running context.Context
	select {
	case running = <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("task did not start")
	}
	want := readTestLimits(t, s)
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	// Restore the journal before the server cleanup attempts its final flush.
	defer func() {
		if err := os.Remove(path); err != nil {
			t.Error(err)
		}
		if err := os.WriteFile(path, before, 0600); err != nil {
			t.Error(err)
		}
	}()
	w := request(s, http.MethodPost, "/api/jobs/limits", `{"maxConcurrent":128,"providerLimit":64}`)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("failed write reported success: %d %s", w.Code, w.Body.String())
	}
	if got := readTestLimits(t, s); !reflect.DeepEqual(got, want) {
		t.Fatalf("failed write changed live limits: %+v", got)
	}
	select {
	case <-running.Done():
		t.Fatal("failed settings save canceled existing work")
	default:
	}
	// Configuration remains adjustable while a task is active; lowering its
	// limits must not treat the in-flight task as an error or cancel it.
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, before, 0600); err != nil {
		t.Fatal(err)
	}
	w = request(s, http.MethodPost, "/api/jobs/limits", `{"maxConcurrent":1,"providerLimit":1}`)
	if w.Code != http.StatusOK {
		t.Fatal(w.Code, w.Body.String())
	}
	select {
	case <-running.Done():
		t.Fatal("lowering limits canceled an active task")
	default:
	}
}

func TestBackgroundEventTextBoundsUTF8Diagnostics(t *testing.T) {
	text := ""
	for i := 0; i < 100; i++ {
		text += "费用与项目活动"
	}
	bounded, truncated := backgroundEventText(text, 64)
	if !truncated || len(bounded) > 64 || !utf8.ValidString(bounded) {
		t.Fatalf("invalid bounded diagnostic: %q", bounded)
	}
	if unchanged, truncated := backgroundEventText("正常", 64); unchanged != "正常" || truncated {
		t.Fatal("small diagnostics were modified")
	}
}
