package web

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/store"
)

// This is a protocol and persistence regression, not a literary-quality eval.
// All story artifacts below are written by the real Host/Worker tools after
// receiving OpenAI-compatible HTTP responses. The test never seeds story files.
func TestCreativeDesktopWorkflow(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	model := &creativeWorkflowModel{t: t}
	upstream := httptest.NewServer(model)
	t.Cleanup(upstream.Close)
	s := newTestServer(t, false)
	apiServer := httptest.NewServer(s.Handler())
	t.Cleanup(apiServer.Close)
	client := &http.Client{Timeout: 5 * time.Second}
	started := time.Now()
	steps := []map[string]any{}
	t.Cleanup(func() {
		model.mu.Lock()
		report := map[string]any{
			"schemaVersion": 1, "suite": "creative-desktop-workflow", "ok": !t.Failed(),
			"goVersion": runtime.Version(), "durationMs": time.Since(started).Milliseconds(),
			"mockVersion": 1, "model": "creative-workflow-mock", "steps": steps,
			"requests": model.requests, "errors": model.errors, "failures": len(model.errors),
			"mockUsage":   map[string]int{"inputTokens": len(model.requests) * 7, "outputTokens": len(model.requests) * 11},
			"limitations": []string{"Deterministic protocol fixtures verify integration, not real-model writing quality or real token costs."},
		}
		model.mu.Unlock()
		if filename := os.Getenv("AINOVEL_CREATIVE_E2E_REPORT"); filename != "" {
			data, err := json.MarshalIndent(report, "", "  ")
			if err == nil {
				err = os.MkdirAll(filepath.Dir(filename), 0700)
			}
			if err == nil {
				err = os.WriteFile(filename, append(data, '\n'), 0600)
			}
			if err != nil {
				t.Errorf("write creative workflow report: %v", err)
			}
		}
	})
	api := func(method, route string, payload any, status int) []byte {
		t.Helper()
		var body io.Reader
		if payload != nil {
			data, err := json.Marshal(payload)
			if err != nil {
				t.Fatal(err)
			}
			body = bytes.NewReader(data)
		}
		r, err := http.NewRequest(method, apiServer.URL+route, body)
		if err != nil {
			t.Fatal(err)
		}
		if payload != nil {
			r.Header.Set("Content-Type", "application/json")
		}
		resp, err := client.Do(r)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		data, err := io.ReadAll(resp.Body)
		if err != nil || resp.StatusCode != status {
			t.Fatalf("%s %s = %d, want %d: %s (%v)", method, route, resp.StatusCode, status, data, err)
		}
		return data
	}
	getState := func() creativeWorkflowState {
		t.Helper()
		var state creativeWorkflowState
		if err := json.Unmarshal(api("GET", "/api/state", nil, 200), &state); err != nil {
			t.Fatal(err)
		}
		return state
	}
	wait := func(label string, condition func(creativeWorkflowState) bool) creativeWorkflowState {
		t.Helper()
		deadline := time.Now().Add(8 * time.Second)
		var state creativeWorkflowState
		for time.Now().Before(deadline) {
			state = getState()
			if !state.Busy && !state.Stopping && !state.Snapshot.IsRunning && condition(state) {
				steps = append(steps, map[string]any{"name": label, "ok": true, "elapsedMs": time.Since(started).Milliseconds()})
				return state
			}
			time.Sleep(10 * time.Millisecond)
		}
		model.mu.Lock()
		trace, _ := json.Marshal(model.requests)
		model.mu.Unlock()
		t.Fatalf("timed out waiting for %s; state=%+v; mock trace=%s", label, state, trace)
		return state
	}
	api("POST", "/api/config", map[string]any{
		"name": "workflow-local", "type": "openai", "api": "chat",
		"base_url": upstream.URL + "/v1", "api_key": "", "model": "creative-workflow-mock",
		"model_configs": []map[string]any{{"name": "creative-workflow-mock", "context_window": 128000, "max_output_tokens": 16000, "json_schema": true}},
		"activate":      true,
	}, 200)
	api("PATCH", "/api/workspaces", map[string]any{"directory": s.dir, "name": "月球图书馆", "targetChapters": 1}, 200)
	co := api("POST", "/api/cocreate", map[string]any{
		"directory": s.dir, "text": "创作一章月球图书馆短篇，林舟寻找旧信，与父亲和解，第三人称，温暖结局。",
	}, 200)
	if !bytes.Contains(co, []byte(`"ready":true`)) || !bytes.Contains(co, []byte("月球图书馆")) {
		t.Fatalf("streaming co-create did not produce a confirmable brief: %s", co)
	}
	api("POST", "/api/cocreate/confirm", map[string]any{"directory": s.dir}, 202)
	planned := wait("co-create-confirm-and-tool-planning", func(state creativeWorkflowState) bool {
		return state.Workflow.Stage == "plan-review" && state.Workflow.CanApprove
	})
	if planned.Workflow.Approved || planned.Snapshot.CompletedCount != 0 {
		t.Fatalf("planning bypassed explicit approval: %+v", planned)
	}
	var planning struct {
		Book            *domain.BookMetadata    `json:"book"`
		Outline         []domain.OutlineEntry   `json:"outline"`
		FoundationAudit *domain.FoundationAudit `json:"foundationAudit"`
	}
	if err := json.Unmarshal(api("GET", "/api/planning", nil, 200), &planning); err != nil ||
		planning.Book == nil || planning.Book.Title != "月球图书馆" || len(planning.Outline) != 1 ||
		planning.FoundationAudit == nil || !planning.FoundationAudit.Ready {
		t.Fatalf("real planning tools did not persist the expected artifacts: %+v (%v)", planning, err)
	}
	api("POST", "/api/action", map[string]any{"action": "approve-plan", "fingerprint": planned.Workflow.Fingerprint}, 202)
	wait("approve-exact-plan", func(state creativeWorkflowState) bool { return state.Workflow.Approved })
	api("POST", "/api/action", map[string]any{"action": "batch", "count": 1}, 202)
	wait("write-and-commit-one-chapter", func(state creativeWorkflowState) bool {
		return state.Workflow.Stage == "chapter-review" && state.Snapshot.CompletedCount == 1
	})
	chapter := api("GET", "/api/chapters/1", nil, 200)
	if !bytes.Contains(chapter, []byte("原稿：林舟")) {
		t.Fatalf("writer did not persist its tool-generated draft: %s", chapter)
	}
	api("POST", "/api/action", map[string]any{"action": "chapter-feedback", "chapter": 1, "text": "请让林舟读信后主动给父亲回信，保留月球图书馆背景。"}, 202)
	wait("feedback-rewrites-and-recommits-chapter", func(state creativeWorkflowState) bool {
		return state.Workflow.Stage == "chapter-review" && len(state.Snapshot.PendingRewrites) == 0
	})
	chapter = api("GET", "/api/chapters/1", nil, 200)
	if !bytes.Contains(chapter, []byte("修订稿：林舟")) || bytes.Contains(chapter, []byte("原稿：林舟")) {
		t.Fatalf("chapter feedback did not produce an independent real rewrite: %s", chapter)
	}
	api("POST", "/api/action", map[string]any{"action": "accept-chapter", "chapter": 1}, 202)
	accepted := wait("explicit-chapter-acceptance", func(state creativeWorkflowState) bool { return state.Workflow.AcceptedChapter == 1 })
	if accepted.Snapshot.TotalInputTokens <= 0 || accepted.Snapshot.TotalOutputTokens <= 0 {
		t.Fatalf("real model usage did not reach the workbench snapshot: %+v", accepted.Snapshot)
	}
	st := store.NewStore(s.dir)
	record, err := st.ChapterRecords.Load(1)
	if err != nil || record == nil || record.Revision < 2 || !strings.Contains(record.Content, "修订稿：林舟") {
		t.Fatalf("rewrite revision record = %+v, err=%v", record, err)
	}
	meta, err := st.RunMeta.Load()
	if err != nil || meta == nil || meta.Workbench == nil || len(meta.Workbench.Feedback) != 1 ||
		!strings.Contains(meta.Workbench.Feedback[0].Text, "主动给父亲回信") {
		t.Fatalf("chapter feedback was not preserved as an audit fact: %+v (%v)", meta, err)
	}
	for _, format := range []string{"txt", "epub"} {
		data := api("POST", "/api/export", map[string]any{"format": format, "directory": s.dir}, 200)
		if format == "txt" {
			if !bytes.Contains(data, []byte("修订稿：林舟")) || bytes.Contains(data, []byte("原稿：林舟")) {
				t.Fatalf("TXT did not export accepted revision: %s", data)
			}
		} else {
			archive, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
			if err != nil {
				t.Fatal(err)
			}
			found := false
			for _, file := range archive.File {
				if !strings.HasSuffix(file.Name, ".xhtml") {
					continue
				}
				r, err := file.Open()
				if err != nil {
					t.Fatal(err)
				}
				content, err := io.ReadAll(r)
				r.Close()
				if err != nil {
					t.Fatal(err)
				}
				found = found || bytes.Contains(content, []byte("修订稿：林舟"))
			}
			if !found {
				t.Fatal("EPUB did not contain the accepted revised chapter")
			}
		}
		steps = append(steps, map[string]any{"name": "export-" + format, "ok": true, "bytes": len(data)})
	}
	model.mu.Lock()
	defer model.mu.Unlock()
	if len(model.errors) != 0 || model.architectStep != 7 || model.writerStep != 12 {
		t.Fatalf("unexpected mock protocol state: architect=%d writer=%d errors=%v", model.architectStep, model.writerStep, model.errors)
	}
	t.Logf("real API/tool workflow complete: %d model calls, one chapter plus rewrite, TXT and EPUB; deterministic fixtures do not evaluate literary quality", len(model.requests))
}

type creativeWorkflowState struct {
	Busy, Stopping bool
	Snapshot       struct {
		IsRunning         bool
		CompletedCount    int
		PendingRewrites   []int
		TotalInputTokens  int
		TotalOutputTokens int
	}
	Workflow struct {
		Stage           string
		Fingerprint     string
		Approved        bool
		CanApprove      bool
		AcceptedChapter int
	}
}

type creativeWorkflowMessage struct {
	Role    string
	Content json.RawMessage
}

func (m creativeWorkflowMessage) text() string {
	var text string
	if json.Unmarshal(m.Content, &text) == nil {
		return text
	}
	var blocks []struct{ Text string }
	if json.Unmarshal(m.Content, &blocks) == nil {
		var joined strings.Builder
		for _, block := range blocks {
			joined.WriteString(block.Text)
		}
		return joined.String()
	}
	return string(m.Content)
}

type creativeWorkflowRequest struct {
	Model          string
	Stream         bool
	Messages       []creativeWorkflowMessage
	Tools          []struct{ Function struct{ Name string } }
	ResponseFormat struct {
		JSONSchema struct{ Name string } `json:"json_schema"`
	} `json:"response_format"`
}

type creativeWorkflowModel struct {
	t                         *testing.T
	mu                        sync.Mutex
	architectStep, writerStep int
	requests                  []map[string]any
	errors                    []string
}

func (m *creativeWorkflowModel) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	started := time.Now()
	m.mu.Lock()
	defer m.mu.Unlock()
	var req creativeWorkflowRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		m.fail(w, "decode mock request: %v", err)
		return
	}
	if r.Method != "POST" || r.URL.Path != "/v1/chat/completions" || req.Model != "creative-workflow-mock" || r.Header.Get("Authorization") != "Bearer ainovel-local-no-key" {
		m.fail(w, "unexpected model endpoint or credential: %s %s model=%s", r.Method, r.URL.Path, req.Model)
		return
	}
	content, tool, args, err := m.respond(req)
	if err != nil {
		m.fail(w, "%v", err)
		return
	}
	usage := map[string]int{"prompt_tokens": 7, "completion_tokens": 11, "total_tokens": 18}
	message := map[string]any{"role": "assistant", "content": content}
	finish := "stop"
	if tool != "" {
		encoded, _ := json.Marshal(args)
		message["tool_calls"] = []any{map[string]any{
			"index": 0, "id": fmt.Sprintf("workflow_call_%d", len(m.requests)), "type": "function",
			"function": map[string]any{"name": tool, "arguments": string(encoded)},
		}}
		finish = "tool_calls"
	}
	base := map[string]any{"id": fmt.Sprintf("workflow_response_%d", len(m.requests)), "model": req.Model, "created": 1}
	if req.Stream {
		w.Header().Set("Content-Type", "text/event-stream")
		base["object"] = "chat.completion.chunk"
		base["choices"] = []any{map[string]any{"index": 0, "delta": message, "finish_reason": nil}}
		chunk, _ := json.Marshal(base)
		fmt.Fprintf(w, "data: %s\n\n", chunk)
		base["choices"] = []any{map[string]any{"index": 0, "delta": map[string]any{}, "finish_reason": finish}}
		base["usage"] = usage
		chunk, _ = json.Marshal(base)
		fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", chunk)
	} else {
		w.Header().Set("Content-Type", "application/json")
		base["object"] = "chat.completion"
		base["choices"] = []any{map[string]any{"index": 0, "message": message, "finish_reason": finish}}
		base["usage"] = usage
		json.NewEncoder(w).Encode(base)
	}
	m.requests = append(m.requests, map[string]any{"model": req.Model, "contract": req.ResponseFormat.JSONSchema.Name, "tool": tool, "stream": req.Stream, "usage": usage, "durationMs": time.Since(started).Milliseconds()})
}

func (m *creativeWorkflowModel) fail(w http.ResponseWriter, format string, args ...any) {
	message := fmt.Sprintf(format, args...)
	m.errors = append(m.errors, message)
	m.t.Error(message)
	http.Error(w, message, http.StatusBadRequest)
}

func (m *creativeWorkflowModel) respond(req creativeWorkflowRequest) (string, string, any, error) {
	jsonText := func(value any) (string, string, any, error) {
		data, err := json.Marshal(value)
		return string(data), "", nil, err
	}
	switch req.ResponseFormat.JSONSchema.Name {
	case "userrules_normalize":
		return jsonText(map[string]any{"structured": map[string]any{"genre": "科幻", "forbidden_chars": []string{}, "forbidden_phrases": []string{}, "fatigue_words": []any{}}, "preferences": "第三人称，温暖结局，一章短篇", "uncertain": []string{}})
	case "arbiter_plan_start":
		return jsonText(map[string]any{"planner": "architect_short", "task": "规划一章月球图书馆短篇，林舟在闭馆前找回父亲旧信。逐项落盘并完成设定审查。", "reason": "用户明确要求一章短篇"})
	}
	available := map[string]bool{}
	for _, tool := range req.Tools {
		available[tool.Function.Name] = true
	}
	if available["save_book"] {
		step := m.architectStep
		m.architectStep++
		switch step {
		case 0:
			return "", "save_book", map[string]any{"title": "月球图书馆", "synopsis": "闭馆前，林舟在月球书库寻找一封被遗忘的来信。"}, nil
		case 1:
			return "", "save_foundation", map[string]any{"type": "premise", "scale": "short", "content": "# 月球图书馆\n林舟在月球图书馆闭馆前找到父亲的旧信，理解误会并决定回信。"}, nil
		case 2:
			return "", "save_foundation", map[string]any{"type": "outline", "scale": "short", "content": []any{map[string]any{"chapter": 1, "title": "旧信", "core_event": "林舟找到父亲旧信并和解", "hook": "他开始写回信", "scenes": []string{"书库寻找", "读信和解"}}}}, nil
		case 3:
			return "", "save_foundation", map[string]any{"type": "characters", "content": []any{map[string]any{"name": "林舟", "role": "主角", "description": "月球图书管理员，因误会疏远父亲", "arc": "读信后学会主动沟通", "traits": []string{"细心"}, "tier": "core"}}}, nil
		case 4:
			return "", "save_foundation", map[string]any{"type": "world_rules", "content": []any{map[string]any{"category": "technology", "rule": "月球图书馆保存地球来信", "boundary": "信件必须由收件人开启"}}}, nil
		case 5:
			return "", "novel_context", map[string]any{}, nil
		case 6:
			for i := len(req.Messages) - 1; i >= 0; i-- {
				if req.Messages[i].Role != "tool" {
					continue
				}
				var result struct {
					FoundationMemory struct {
						Foundation struct{ Fingerprint string } `json:"foundation_status"`
					} `json:"foundation_memory"`
				}
				if json.Unmarshal([]byte(req.Messages[i].text()), &result) == nil && result.FoundationMemory.Foundation.Fingerprint != "" {
					return "", "audit_foundation", map[string]any{"fingerprint": result.FoundationMemory.Foundation.Fingerprint, "ready": true, "summary": "书名、前提、单章目录、主角与世界规则一致。", "issues": []any{}}, nil
				}
			}
			return "", "", nil, fmt.Errorf("novel_context did not expose a persisted foundation fingerprint")
		default:
			return "", "", nil, fmt.Errorf("unexpected architect dispatch %d", step)
		}
	}
	if available["commit_chapter"] {
		step, revision := m.writerStep%6, m.writerStep/6
		m.writerStep++
		marker := "原稿：林舟"
		if revision > 0 {
			marker = "修订稿：林舟"
		}
		switch step {
		case 0:
			return "", "novel_context", map[string]any{"chapter": 1}, nil
		case 1:
			return "", "plan_chapter", map[string]any{"chapter": 1, "title": "旧信", "goal": "找到父亲旧信并和解", "conflict": "书库即将关闭", "hook": "主动寄出回信"}, nil
		case 2:
			content := marker + "在月球图书馆找到父亲的信。窗外地球的蓝光照在纸面，他读懂了藏在字缝里的牵挂。\n"
			if revision > 0 {
				content += "林舟提起笔，主动给父亲回信：我明白了，下次回地球时我们一起看海。\n"
			}
			return "", "draft_chapter", map[string]any{"chapter": 1, "mode": "write", "content": content}, nil
		case 3:
			return "", "read_chapter", map[string]any{"chapter": 1, "source": "draft"}, nil
		case 4:
			return "", "check_consistency", map[string]any{"chapter": 1}, nil
		case 5:
			return "", "commit_chapter", map[string]any{"chapter": 1, "title": "旧信", "summary": "林舟找到旧信并决定给父亲回信", "characters": []string{"林舟"}, "key_events": []string{"找到旧信", "主动回信"}, "timeline_events": []any{}, "foreshadow_updates": []any{}, "relationship_changes": []any{}, "state_changes": []any{}, "cast_intros": []any{}, "hook_type": "emotion", "dominant_strand": "quest", "feedback": nil}, nil
		}
	}
	for _, message := range req.Messages {
		if message.Role == "system" && strings.Contains(message.text(), "小说共创助手") {
			return "<reply>信息已齐备，可以确认。</reply><draft>## 作品定位\n月球图书馆单章短篇，林舟寻找父亲旧信，第三人称，温暖结局。\n## 核心故事\n林舟找到旧信后主动回信，与父亲和解。</draft><ready>true</ready><suggestions></suggestions>", "", nil, nil
		}
	}
	return "", "", nil, fmt.Errorf("unhandled model request: contract=%s tools=%v", req.ResponseFormat.JSONSchema.Name, available)
}
