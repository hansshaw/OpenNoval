package web

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/voocel/ainovel-cli/internal/projectjobs"
	"github.com/voocel/ainovel-cli/internal/store"
)

// Exercise the real independent background Host after API-driven planning.
// gpt-4o-mini is only a local fixture identity with a built-in price; every
// request goes to httptest and reuses the deterministic creative tool mock.
func TestCreativeBackgroundWorkflow(t *testing.T) {
	creativeTestHome(t)
	model := &creativeWorkflowModel{t: t}
	writerEntered, writerRelease := make(chan struct{}), make(chan struct{})
	var enterOnce, releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(writerRelease) }) }
	t.Cleanup(release)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		originalBody, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
			http.Error(w, "bad mock request", 400)
			return
		}
		var req creativeWorkflowRequest
		if err := json.Unmarshal(originalBody, &req); err != nil {
			t.Error(err)
			http.Error(w, "bad mock request", 400)
			return
		}
		if req.Model != "gpt-4o-mini" {
			t.Errorf("background runtime selected unexpected model %q", req.Model)
			http.Error(w, "wrong model", 400)
			return
		}
		for _, tool := range req.Tools {
			if tool.Function.Name == "commit_chapter" {
				enterOnce.Do(func() { close(writerEntered) })
				select {
				case <-writerRelease:
				case <-r.Context().Done():
					return
				}
				break
			}
		}
		// Preserve the actual schemas, messages and generation options while
		// adapting only the fixture's accepted model identity.
		var original map[string]json.RawMessage
		json.Unmarshal(originalBody, &original)
		original["model"] = json.RawMessage(`"creative-workflow-mock"`)
		encoded, _ := json.Marshal(original)
		forward := r.Clone(r.Context())
		forward.Body = io.NopCloser(bytes.NewReader(encoded))
		recorder := httptest.NewRecorder()
		model.ServeHTTP(recorder, forward)
		for name, values := range recorder.Header() {
			w.Header()[name] = values
		}
		w.WriteHeader(recorder.Code)
		w.Write(bytes.ReplaceAll(recorder.Body.Bytes(), []byte("creative-workflow-mock"), []byte("gpt-4o-mini")))
	}))
	t.Cleanup(upstream.Close)
	s := newTestServer(t, false)
	server := httptest.NewServer(s.Handler())
	t.Cleanup(server.Close)
	client := &http.Client{Timeout: 5 * time.Second}
	api := func(method, route string, payload any, status int) []byte {
		t.Helper()
		var body io.Reader
		if payload != nil {
			encoded, err := json.Marshal(payload)
			if err != nil {
				t.Fatal(err)
			}
			body = bytes.NewReader(encoded)
		}
		r, err := http.NewRequest(method, server.URL+route, body)
		if err != nil {
			t.Fatal(err)
		}
		if payload != nil {
			r.Header.Set("Content-Type", "application/json")
		}
		response, err := client.Do(r)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		data, err := io.ReadAll(response.Body)
		if err != nil || response.StatusCode != status {
			t.Fatalf("%s %s = %d, want %d: %s (%v)", method, route, response.StatusCode, status, data, err)
		}
		return data
	}
	state := func() creativeWorkflowState {
		t.Helper()
		var result creativeWorkflowState
		if err := json.Unmarshal(api("GET", "/api/state", nil, 200), &result); err != nil {
			t.Fatal(err)
		}
		return result
	}
	wait := func(label string, condition func() bool) {
		t.Helper()
		deadline := time.Now().Add(8 * time.Second)
		for time.Now().Before(deadline) {
			if condition() {
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Fatalf("timed out waiting for %s", label)
	}
	api("POST", "/api/config", map[string]any{
		"name": "background-local", "type": "openai", "api": "chat",
		"base_url": upstream.URL + "/v1", "api_key": "", "model": "gpt-4o-mini",
		"model_configs": []map[string]any{{"name": "gpt-4o-mini", "context_window": 128000, "max_output_tokens": 16000, "json_schema": true}},
		"activate":      true,
	}, 200)
	source := s.dir
	api("PATCH", "/api/workspaces", map[string]any{"directory": source, "name": "后台规划来源", "targetChapters": 1}, 200)
	api("POST", "/api/cocreate", map[string]any{"directory": source, "text": "创作一章月球图书馆短篇，林舟寻找父亲旧信，第三人称，温暖结局。"}, 200)
	api("POST", "/api/cocreate/confirm", map[string]any{"directory": source}, 202)
	wait("real planning tools", func() bool {
		current := state()
		return !current.Busy && !current.Snapshot.IsRunning && current.Workflow.Stage == "plan-review" && current.Workflow.CanApprove
	})
	api("POST", "/api/action", map[string]any{"action": "approve-plan", "fingerprint": state().Workflow.Fingerprint}, 202)
	wait("exact plan approval", func() bool { current := state(); return !current.Busy && current.Workflow.Approved })
	// Wait for the real usage autosave so the copied project contains genuine
	// positive planning history, rather than seeding a synthetic cost fixture.
	var historicalCost float64
	wait("durable historical model usage", func() bool {
		usage, err := store.NewStore(source).Usage.Load()
		if err != nil {
			t.Fatal(err)
		}
		if usage == nil {
			return false
		}
		historicalCost = usage.Overall.Cost
		return historicalCost > 0
	})
	var copied struct{ Directory string }
	if err := json.Unmarshal(api("POST", "/api/workspaces/copy", map[string]any{"directory": source, "name": "后台独立副本"}, 201), &copied); err != nil {
		t.Fatal(err)
	}
	beforeUsage, err := store.NewStore(copied.Directory).Usage.Load()
	if err != nil || beforeUsage == nil || beforeUsage.Overall.Cost != historicalCost {
		t.Fatalf("copy did not preserve genuine usage history: %+v (%v)", beforeUsage, err)
	}
	var foreground struct{ Directory string }
	if err := json.Unmarshal(api("POST", "/api/workspaces", map[string]any{"create": true, "name": "空白前台"}, 200), &foreground); err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	baselineEvents, _ := json.Marshal(s.events)
	baselineStream := s.stream
	s.mu.Unlock()
	// Observe the actual foreground SSE endpoint while the independent Host is
	// busy. Its history/live deltas must not contain background model output.
	sseContext, stopSSE := context.WithCancel(t.Context())
	defer stopSSE()
	sseRequest, _ := http.NewRequestWithContext(sseContext, "GET", server.URL+"/api/events", nil)
	sseResponse, err := client.Do(sseRequest)
	if err != nil {
		t.Fatal(err)
	}
	defer sseResponse.Body.Close()
	var frames []message
	sseDone := make(chan struct{})
	sseHistory := make(chan struct{})
	go func() {
		defer close(sseDone)
		scanner := bufio.NewScanner(sseResponse.Body)
		for scanner.Scan() {
			line := scanner.Text()
			if !strings.HasPrefix(line, "data: ") {
				continue
			}
			var frame message
			if json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &frame) == nil {
				frames = append(frames, frame)
				if len(frames) == 1 {
					close(sseHistory)
				}
			}
		}
	}()
	select {
	case <-sseHistory:
	case <-time.After(time.Second):
		t.Fatal("foreground SSE did not deliver initial history")
	}
	var submitted projectjobs.Job
	if err := json.Unmarshal(api("POST", "/api/jobs", map[string]any{"directory": copied.Directory, "kind": "batch", "count": 1, "budgetUSD": 1}, 201), &submitted); err != nil {
		t.Fatal(err)
	}
	select {
	case <-writerEntered:
	case <-time.After(5 * time.Second):
		t.Fatal("independent background Host did not reach its writer model")
	}
	var jobsView struct {
		Jobs   []projectjobs.Job
		Events projectjobs.EventPage
	}
	readJobs := func() {
		t.Helper()
		if err := json.Unmarshal(api("GET", "/api/jobs", nil, 200), &jobsView); err != nil {
			t.Fatal(err)
		}
	}
	readJobs()
	if len(jobsView.Jobs) != 1 || jobsView.Jobs[0].Status != projectjobs.Running || jobsView.Jobs[0].CostUSD != 0 {
		t.Fatalf("running task charged copied historical usage: %+v", jobsView.Jobs)
	}
	active := state()
	if active.Busy || active.Snapshot.IsRunning || active.Snapshot.CompletedCount != 0 {
		t.Fatalf("background Host took over foreground runtime: %+v", active)
	}
	release()
	var completed projectjobs.Job
	wait("background batch completion", func() bool {
		readJobs()
		for _, job := range jobsView.Jobs {
			if job.ID == submitted.ID && job.Status.Terminal() {
				completed = job
				return true
			}
		}
		return false
	})
	if completed.Status != projectjobs.Succeeded {
		t.Fatalf("real background batch failed: %+v; events=%+v", completed, jobsView.Events.Events)
	}
	stopSSE()
	sseResponse.Body.Close()
	<-sseDone
	if len(frames) == 0 {
		t.Fatal("foreground SSE check did not observe any frames")
	}
	for _, frame := range frames {
		if frame.Type != "history" {
			t.Fatalf("background output leaked into foreground SSE: %+v", frame)
		}
	}
	s.mu.Lock()
	afterEvents, _ := json.Marshal(s.events)
	isolated := s.dir == foreground.Directory && s.stream == baselineStream && bytes.Equal(afterEvents, baselineEvents)
	s.mu.Unlock()
	if !isolated {
		t.Fatal("background events or token stream changed the foreground project")
	}
	for _, directory := range []string{source, foreground.Directory} {
		progress, err := store.NewStore(directory).Progress.Load()
		if err != nil || (progress != nil && len(progress.CompletedChapters) != 0) {
			t.Fatalf("background task wrote into a different project %s: %+v (%v)", directory, progress, err)
		}
	}
	target := store.NewStore(copied.Directory)
	chapter, err := target.Drafts.LoadChapterText(1)
	if err != nil || !strings.Contains(chapter, "原稿：林舟") {
		t.Fatalf("background target chapter missing: %q (%v)", chapter, err)
	}
	afterUsage, err := target.Usage.Load()
	if err != nil || afterUsage == nil {
		t.Fatalf("background usage missing: %+v (%v)", afterUsage, err)
	}
	expectedIncrement := afterUsage.Overall.Cost - beforeUsage.Overall.Cost
	if expectedIncrement <= 0 || math.Abs(completed.CostUSD-expectedIncrement) > 1e-12 || completed.CostUSD >= afterUsage.Overall.Cost {
		t.Fatalf("job cost repeated project history: job=%g increment=%g before=%g after=%g", completed.CostUSD, expectedIncrement, beforeUsage.Overall.Cost, afterUsage.Overall.Cost)
	}
	var charged float64
	hostEvents := 0
	writerEvent := false
	for _, event := range jobsView.Events.Events {
		if event.JobID != submitted.ID || event.ProjectID != copied.Directory || event.ProviderID != "background-local" {
			t.Fatalf("background task event has incorrect ownership: %+v", event)
		}
		if event.Kind == "host.event" {
			hostEvents++
			var payload struct{ Agent string }
			if err := json.Unmarshal(event.Payload, &payload); err != nil {
				t.Fatal(err)
			}
			writerEvent = writerEvent || payload.Agent == "writer"
		}
		if event.Kind == "job.cost" {
			var cost struct {
				Delta float64 `json:"delta_usd"`
			}
			if err := json.Unmarshal(event.Payload, &cost); err != nil {
				t.Fatal(err)
			}
			charged += cost.Delta
		}
	}
	if hostEvents == 0 || !writerEvent || math.Abs(charged-completed.CostUSD) > 1e-12 {
		t.Fatalf("job event/charge audit incomplete: hostEvents=%d charged=%g job=%g", hostEvents, charged, completed.CostUSD)
	}
	model.mu.Lock()
	defer model.mu.Unlock()
	if len(model.errors) != 0 || model.architectStep != 7 || model.writerStep != 6 {
		t.Fatalf("unexpected real tool workflow: architect=%d writer=%d errors=%v", model.architectStep, model.writerStep, model.errors)
	}
	t.Logf("real background batch succeeded: %d model calls, %d scoped Host events, historical cost=%g new job cost=%g; foreground SSE remained isolated", len(model.requests), hostEvents, historicalCost, completed.CostUSD)
}
