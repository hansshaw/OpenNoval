package desktopui

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hansshaw/AInoval-web-ui/internal/host"
)

// Use real Host engines, HTTP model streams and workbench endpoints to prove
// that changing the selected view does not cancel another project's creation.
// Startup contracts complete normally; only each architect response is held.
func TestProjectCreationConcurrencyRetainsIndependentHosts(t *testing.T) {
	creativeTestHome(t)
	fixture := &creativeWorkflowModel{t: t}
	type heldRequest struct {
		marker   string
		ctx      context.Context
		emit     chan string
		canceled chan struct{}
	}
	entered := make(chan *heldRequest, 4)
	releaseAll := make(chan struct{})
	defer close(releaseAll)
	var active, peak atomic.Int32
	writeChunk := func(w http.ResponseWriter, marker, text string) {
		chunk, _ := json.Marshal(map[string]any{
			"id": marker, "model": "creative-workflow-mock", "object": "chat.completion.chunk", "created": 1,
			"choices": []any{map[string]any{"index": 0, "delta": map[string]any{"role": "assistant", "content": text}, "finish_reason": nil}},
		})
		fmt.Fprintf(w, "data: %s\n\n", chunk)
		w.(http.Flusher).Flush()
	}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read local model request: %v", err)
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}
		var req creativeWorkflowRequest
		if err := json.Unmarshal(body, &req); err != nil {
			t.Errorf("decode local model request: %v", err)
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}
		marker := ""
		for i := 1; i <= 4; i++ {
			candidate := fmt.Sprintf("concurrency-project-%d", i)
			for _, message := range req.Messages {
				if strings.Contains(message.text(), candidate) {
					marker = candidate
					break
				}
			}
		}
		if req.ResponseFormat.JSONSchema.Name == "arbiter_plan_start" {
			if marker == "" {
				t.Error("startup decision lost its project-specific confirmed brief")
				http.Error(w, "missing project identity", http.StatusBadRequest)
				return
			}
			content, _ := json.Marshal(map[string]string{
				"planner": "architect_short", "task": "为 " + marker + " 规划一章月球图书馆短篇，逐项保存并审查设定。", "reason": marker,
			})
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{
				"id": "start-" + marker, "object": "chat.completion", "model": req.Model, "created": 1,
				"choices": []any{map[string]any{"index": 0, "message": map[string]any{"role": "assistant", "content": string(content)}, "finish_reason": "stop"}},
				"usage":   map[string]int{"prompt_tokens": 7, "completion_tokens": 11, "total_tokens": 18},
			})
			return
		}
		for _, tool := range req.Tools {
			if tool.Function.Name != "save_book" {
				continue
			}
			if marker == "" || !req.Stream {
				t.Errorf("architect request must retain project identity and streaming: marker=%q stream=%v", marker, req.Stream)
				http.Error(w, "invalid architect request", http.StatusBadRequest)
				return
			}
			call := &heldRequest{marker: marker, ctx: r.Context(), emit: make(chan string, 1), canceled: make(chan struct{})}
			count := active.Add(1)
			for previous := peak.Load(); previous < count; previous = peak.Load() {
				if peak.CompareAndSwap(previous, count) {
					break
				}
			}
			defer active.Add(-1)
			defer close(call.canceled)
			w.Header().Set("Content-Type", "text/event-stream")
			writeChunk(w, marker, marker+":initial-stream")
			entered <- call
			for {
				select {
				case text := <-call.emit:
					writeChunk(w, marker, text)
				case <-r.Context().Done():
					return
				case <-releaseAll:
					return
				}
			}
		}
		forward := r.Clone(r.Context())
		forward.Body = io.NopCloser(bytes.NewReader(body))
		fixture.ServeHTTP(w, forward)
	}))
	t.Cleanup(upstream.Close)
	s := newTestServer(t, false)
	apiServer := httptest.NewServer(s.Handler())
	t.Cleanup(apiServer.Close)
	api := func(method, route string, payload any, status int) []byte {
		t.Helper()
		body := ""
		if payload != nil {
			body = mustJSON(t, payload)
		}
		response := request(s, method, route, body)
		if response.Code != status {
			t.Fatalf("%s %s = %d, want %d: %s", method, route, response.Code, status, response.Body.String())
		}
		return response.Body.Bytes()
	}
	wait := func(label string, condition func() bool) {
		t.Helper()
		deadline := time.Now().Add(8 * time.Second)
		for time.Now().Before(deadline) {
			if condition() {
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
		t.Fatalf("timed out waiting for %s", label)
	}
	state := func() creativeWorkflowState {
		t.Helper()
		var result creativeWorkflowState
		if err := json.Unmarshal(api(http.MethodGet, "/api/state", nil, http.StatusOK), &result); err != nil {
			t.Fatal(err)
		}
		return result
	}
	history := func() ([]host.Event, string) {
		t.Helper()
		client := &http.Client{Timeout: 2 * time.Second}
		response, err := client.Get(apiServer.URL + "/api/events")
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		scanner := bufio.NewScanner(response.Body)
		if !scanner.Scan() {
			t.Fatalf("missing workbench SSE history: %v", scanner.Err())
		}
		var frame struct {
			Type string
			Data struct {
				Events []host.Event
				Stream string
			}
		}
		if err := json.Unmarshal(bytes.TrimPrefix(scanner.Bytes(), []byte("data: ")), &frame); err != nil || frame.Type != "history" {
			t.Fatalf("invalid workbench history: %s (%v)", scanner.Text(), err)
		}
		return frame.Data.Events, frame.Data.Stream
	}
	api(http.MethodPost, "/api/config", map[string]any{
		"name": "concurrent-local", "type": "openai", "api": "chat", "base_url": upstream.URL + "/v1",
		"api_key": "", "model": "creative-workflow-mock", "activate": true,
		"model_configs": []map[string]any{{"name": "creative-workflow-mock", "context_window": 128000, "max_output_tokens": 16000, "json_schema": true}},
	}, http.StatusOK)
	limits := readTestLimits(t, s)
	if limits.MaxProjects != 3 || limits.MaxConcurrent != 3 || limits.ProviderLimit != 3 {
		t.Fatalf("creation concurrency defaults = %+v, want three projects and three requests", limits)
	}
	projects := make([]string, 4)
	for i := range projects {
		var created struct{ Directory string }
		if err := json.Unmarshal(api(http.MethodPost, "/api/workspaces", map[string]any{"create": true, "name": fmt.Sprintf("并发验收项目%d", i+1)}, http.StatusOK), &created); err != nil {
			t.Fatal(err)
		}
		projects[i] = created.Directory
		api(http.MethodPost, "/api/cocreate", map[string]any{"directory": created.Directory, "text": "创作一章月球图书馆短篇，林舟寻找父亲旧信，第三人称，温暖结局。"}, http.StatusOK)
	}
	selectProject := func(directory string) {
		t.Helper()
		api(http.MethodPost, "/api/workspaces", map[string]any{"path": directory}, http.StatusOK)
	}
	calls := make([]*heldRequest, 3)
	hosts := make([]*host.Host, 3)
	for i := range calls {
		selectProject(projects[i])
		marker := fmt.Sprintf("concurrency-project-%d", i+1)
		api(http.MethodPost, "/api/cocreate/confirm", map[string]any{
			"directory": projects[i], "draftPrompt": marker + "：月球图书馆单章短篇，第三人称，温暖结局。",
		}, http.StatusAccepted)
		select {
		case calls[i] = <-entered:
			if calls[i].marker != marker {
				t.Fatalf("request project = %q, want %q", calls[i].marker, marker)
			}
		case <-time.After(8 * time.Second):
			t.Fatalf("project %d did not enter its real streaming model request", i+1)
		}
		wait("engine started and startup command released", func() bool {
			current := state()
			s.mu.Lock()
			defer s.mu.Unlock()
			return !current.Busy && !current.Stopping && current.Snapshot.IsRunning && strings.Contains(s.stream, marker+":initial-stream")
		})
		s.mu.Lock()
		hosts[i] = s.rt
		s.mu.Unlock()
	}
	if active.Load() != 3 || peak.Load() != 3 {
		t.Fatalf("real HTTP requests did not overlap: active=%d peak=%d", active.Load(), peak.Load())
	}
	for i := range hosts {
		if !hosts[i].Snapshot().IsRunning {
			t.Fatalf("switching projects stopped engine %d", i+1)
		}
		select {
		case <-calls[i].ctx.Done():
			t.Fatalf("switching projects canceled HTTP request %d", i+1)
		default:
		}
	}
	selectProject(projects[3])
	api(http.MethodPost, "/api/cocreate/confirm", map[string]any{"directory": projects[3]}, http.StatusConflict)
	if current := state(); current.Busy || current.Snapshot.IsRunning {
		t.Fatalf("fourth project consumed a slot despite the project limit: %+v", current)
	}
	if active.Load() != 3 {
		t.Fatalf("rejecting the fourth project disturbed existing model requests: %d", active.Load())
	}

	// Emit while the project is detached. Its real observer delta must be kept
	// for that Host, without appearing in the currently selected project's SSE.
	selectProject(projects[1])
	calls[0].emit <- ":background-update-one"
	wait("detached host stream collection", func() bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		view := s.projectRuntimes[projectKey(projects[0])]
		return view != nil && strings.Contains(view.stream, ":background-update-one")
	})
	for _, i := range []int{1, 0, 2} {
		selectProject(projects[i])
		s.mu.Lock()
		selected := s.rt
		s.mu.Unlock()
		if selected != hosts[i] {
			t.Fatalf("project %d was rebuilt instead of restoring its running Host", i+1)
		}
		events, stream := history()
		eventJSON, _ := json.Marshal(events)
		for other := range calls {
			marker := calls[other].marker
			if strings.Contains(stream, marker) != (other == i) || bytes.Contains(eventJSON, []byte(marker)) != (other == i) {
				t.Fatalf("project %d history leaked or lost project %d: stream=%q events=%s", i+1, other+1, stream, eventJSON)
			}
		}
		if i == 0 && !strings.Contains(stream, ":background-update-one") {
			t.Fatal("returning to a Host lost output received while its project was detached")
		}
	}

	selectProject(projects[0])
	api(http.MethodPost, "/api/action", map[string]any{"action": "pause", "directory": projects[0]}, http.StatusAccepted)
	wait("selected project paused", func() bool {
		current := state()
		return !current.Busy && !current.Stopping && !current.Snapshot.IsRunning && active.Load() == 2
	})
	select {
	case <-calls[0].canceled:
	case <-time.After(2 * time.Second):
		t.Fatal("pausing the selected project did not cancel its model request")
	}
	for i := 1; i < len(calls); i++ {
		if !hosts[i].Snapshot().IsRunning {
			t.Fatalf("pausing project one stopped engine %d", i+1)
		}
		select {
		case <-calls[i].canceled:
			t.Fatalf("pausing project one canceled model request %d", i+1)
		default:
		}
	}
	closed := make(chan struct{})
	go func() { s.Close(); close(closed) }()
	select {
	case <-closed:
	case <-time.After(8 * time.Second):
		t.Fatal("Server.Close did not finish after canceling all retained Hosts")
	}
	for i, call := range calls {
		select {
		case <-call.canceled:
		case <-time.After(2 * time.Second):
			t.Fatalf("Server.Close left model request %d running", i+1)
		}
	}
	if active.Load() != 0 {
		t.Fatalf("model requests still active after Server.Close: %d", active.Load())
	}
}
