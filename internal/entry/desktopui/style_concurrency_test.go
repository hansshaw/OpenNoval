package desktopui

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hansshaw/OpenNovel/internal/bootstrap"
	"github.com/hansshaw/OpenNovel/internal/store"
	"github.com/hansshaw/OpenNovel/internal/stylelibrary"
)

const styleTestSourceResponse = `{"title":null,"summary":"清楚的语料摘要","style_observations":[],"common_words":[],"plot_patterns":[],"hook_patterns":[],"pacing_notes":[],"reader_appeal":[],"reusable_techniques":[],"warnings":[]}`
const styleTestMergeResponse = `{"style":{"narrative_voice":["贴近人物"],"sentence_rhythm":[],"prose_texture":[],"perspective":[],"mood":[],"do_not_copy":[]},"lexicon":{"common_words":[],"emotion_words":[],"scene_words":[],"transition_words":[],"signature_phrases":[]},"plot_design":{"opening_patterns":[],"escalation_patterns":[],"turning_point_patterns":[],"payoff_patterns":[]},"hook_design":{"hook_types":[],"placement":[],"cliffhanger_patterns":[],"payoff_rules":[]},"pacing_density":{"scene_density":[],"information_release":[],"dialogue_action_ratio":[],"compression_rules":[]},"reader_engagement":{"methods":[],"emotional_drivers":[],"progression_rewards":[],"anti_patterns":[]},"role_guidance":{"architect":[],"writer":[],"editor":[]}}`

func styleConcurrentServer(t *testing.T) (*Server, <-chan struct{}, <-chan struct{}, func()) {
	t.Helper()
	entered, canceled, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var enteredOnce, canceledOnce, releaseOnce sync.Once
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		response := styleTestMergeResponse
		if strings.Contains(string(body), "simulation_source_report") {
			enteredOnce.Do(func() { close(entered) })
			select {
			case <-release:
			case <-r.Context().Done():
				canceledOnce.Do(func() { close(canceled) })
				return
			}
			response = styleTestSourceResponse
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"id": "style", "choices": []any{map[string]any{"index": 0, "message": map[string]any{"role": "assistant", "content": response}, "finish_reason": "stop"}}})
	}))
	t.Cleanup(upstream.Close)
	s := newTestServer(t, false)
	enabled := true
	s.mu.Lock()
	s.baseCfg.Provider, s.baseCfg.ModelName = "style-local", "style-fixture"
	s.baseCfg.Providers = map[string]bootstrap.ProviderConfig{"style-local": {
		Type: "openai", BaseURL: upstream.URL,
		Models: []bootstrap.ModelConfig{{Name: "style-fixture", ContextWindow: 128000, JSONSchema: &enabled}},
	}}
	s.baseCfg.FillDefaults()
	s.cfg = bootstrap.CloneConfig(s.baseCfg)
	s.mu.Unlock()
	return s, entered, canceled, func() { releaseOnce.Do(func() { close(release) }) }
}

func waitStyleSignal(t *testing.T, signal <-chan struct{}, name string) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out waiting for %s", name)
	}
}

func createConcurrentStyleEntry(t *testing.T, s *Server, name string) (string, string) {
	t.Helper()
	response := request(s, "POST", "/api/style/entries", mustJSON(t, map[string]any{"name": name}))
	var entry stylelibrary.Entry
	if response.Code != http.StatusCreated || json.Unmarshal(response.Body.Bytes(), &entry) != nil || entry.ID == "" {
		t.Fatalf("create shared style entry: %d %s", response.Code, response.Body.String())
	}
	s.mu.Lock()
	library, err := s.sharedStyleLibraryLocked()
	s.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	directory, err := library.Directory(entry.ID)
	if err != nil {
		t.Fatal(err)
	}
	return entry.ID, directory
}

func TestStyleAnalysisIndependentOfCreationAndProjectSelection(t *testing.T) {
	s, entered, _, release := styleConcurrentServer(t)
	entryID, entryDirectory := createConcurrentStyleEntry(t, s, "悬疑风格")
	otherEntryID, _ := createConcurrentStyleEntry(t, s, "另一个风格")
	s.mu.Lock()
	s.busy, s.job = true, "创作处理"
	projectDir := s.dir
	s.mu.Unlock()
	if response := request(s, "POST", "/api/style/upload", mustJSON(t, map[string]any{"entryId": entryID, "name": "sample.md", "content": "短句推进，雨夜动作。"})); response.Code != http.StatusOK {
		t.Fatalf("creation blocked corpus upload: %d %s", response.Code, response.Body.String())
	}
	if response := request(s, "POST", "/api/style/run", mustJSON(t, map[string]any{"entryId": entryID})); response.Code != http.StatusAccepted {
		t.Fatalf("creation blocked independent analysis without a Host: %d %s", response.Code, response.Body.String())
	}
	waitStyleSignal(t, entered, "real style HTTP request")
	s.mu.Lock()
	done := s.styleDone
	stateOK := s.busy && s.job == "创作处理" && s.rt == nil
	s.mu.Unlock()
	if !stateOK {
		t.Fatal("analysis overwrote creation state or required its Host")
	}
	for _, endpoint := range []string{"upload", "delete"} {
		body := mustJSON(t, map[string]any{"entryId": entryID, "name": "sample.md"})
		if endpoint == "upload" {
			body = mustJSON(t, map[string]any{"entryId": entryID, "name": "sample.md", "content": "change"})
		}
		response := request(s, "POST", "/api/style/"+endpoint, body)
		if response.Code != http.StatusConflict {
			t.Fatalf("analysis corpus could be changed: %s %d", endpoint, response.Code)
		}
	}
	other := t.TempDir()
	s.mu.Lock()
	s.dir = other
	s.mu.Unlock()
	response := request(s, "GET", "/api/style?entryId="+otherEntryID, "")
	var view struct {
		Busy            bool   `json:"busy"`
		AnalysisRunning bool   `json:"analysisRunning"`
		AnalysisEntryID string `json:"analysisEntryId"`
	}
	if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &view) != nil || view.Busy || !view.AnalysisRunning || view.AnalysisEntryID != entryID {
		t.Fatalf("shared library analysis identity changed on project switch: %d %s", response.Code, response.Body.String())
	}
	if response := request(s, "GET", "/api/style?entryId="+entryID, ""); response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"busy":true`) {
		t.Fatalf("active shared entry disappeared after project switch: %d %s", response.Code, response.Body.String())
	}
	if response := request(s, "POST", "/api/style/upload", mustJSON(t, map[string]any{"entryId": otherEntryID, "name": "other.md", "content": "另一个风格的语料。"})); response.Code != http.StatusOK {
		t.Fatalf("another entry's analysis blocked upload: %d %s", response.Code, response.Body.String())
	}
	if response := request(s, "POST", "/api/style/delete", mustJSON(t, map[string]any{"entryId": otherEntryID, "name": "other.md"})); response.Code != http.StatusOK {
		t.Fatalf("another entry's analysis blocked delete: %d %s", response.Code, response.Body.String())
	}
	if response := request(s, "POST", "/api/style/run", mustJSON(t, map[string]any{"entryId": otherEntryID})); response.Code != http.StatusConflict {
		t.Fatalf("global single-analysis slot was not preserved: %d %s", response.Code, response.Body.String())
	}
	release()
	waitStyleSignal(t, done, "shared style completion after switching projects")
	if profile, err := store.NewStore(entryDirectory).Simulation.Load(); err != nil || profile == nil || len(profile.Corpus.Sources) != 1 {
		t.Fatalf("shared analysis lost its entry workspace: %+v %v", profile, err)
	}
	if profile, err := store.NewStore(projectDir).Simulation.Load(); err != nil || profile != nil {
		t.Fatalf("shared analysis wrote a project-local profile: %+v %v", profile, err)
	}
	if profile, err := store.NewStore(other).Simulation.Load(); err != nil || profile != nil {
		t.Fatalf("analysis wrote to newly selected project: %+v %v", profile, err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.styleBusy || s.styleCancel != nil || s.styleDone != nil || !s.busy || s.job != "创作处理" {
		t.Fatal("analysis completion overwrote creation state or leaked its resources")
	}
}

func TestServerCloseCancelsAndWaitsForIndependentStyleAnalysis(t *testing.T) {
	s, entered, canceled, _ := styleConcurrentServer(t)
	entryID, entryDirectory := createConcurrentStyleEntry(t, s, "关闭取消")
	if response := request(s, "POST", "/api/style/upload", mustJSON(t, map[string]any{"entryId": entryID, "name": "sample.md", "content": "雨夜动作。"})); response.Code != http.StatusOK {
		t.Fatal(response.Code, response.Body.String())
	}
	if response := request(s, "POST", "/api/style/run", mustJSON(t, map[string]any{"entryId": entryID})); response.Code != http.StatusAccepted {
		t.Fatal(response.Code, response.Body.String())
	}
	waitStyleSignal(t, entered, "style HTTP request")
	closed := make(chan struct{})
	go func() { s.Close(); close(closed) }()
	waitStyleSignal(t, canceled, "style cancellation")
	waitStyleSignal(t, closed, "Server.Close")
	if profile, err := store.NewStore(entryDirectory).Simulation.Load(); err != nil || profile != nil {
		t.Fatalf("canceled style run published profile: %+v %v", profile, err)
	}
}

func TestCreationPauseLeavesWebStyleAnalysisRunning(t *testing.T) {
	s, entered, canceled, release := styleConcurrentServer(t)
	entryID, entryDirectory := createConcurrentStyleEntry(t, s, "独立风格分析")
	s.mu.Lock()
	s.initLocked()
	runtime := s.rt
	s.mu.Unlock()
	if runtime == nil {
		t.Fatal("creation Host was not initialized")
	}
	if response := request(s, "POST", "/api/style/upload", mustJSON(t, map[string]any{"entryId": entryID, "name": "sample.md", "content": "雨夜动作。"})); response.Code != http.StatusOK {
		t.Fatal(response.Code, response.Body.String())
	}
	if response := request(s, "POST", "/api/style/run", mustJSON(t, map[string]any{"entryId": entryID})); response.Code != http.StatusAccepted {
		t.Fatal(response.Code, response.Body.String())
	}
	waitStyleSignal(t, entered, "style HTTP request")
	s.mu.Lock()
	done := s.styleDone
	s.mu.Unlock()
	if response := request(s, "POST", "/api/action", `{"action":"pause"}`); response.Code != http.StatusAccepted {
		t.Fatal(response.Code, response.Body.String())
	}
	waitStyleSignal(t, runtime.Done(), "creation Host shutdown after pause")
	select {
	case <-canceled:
		t.Fatal("creation Pause canceled web style analysis")
	default:
	}
	s.mu.Lock()
	active := s.styleBusy && s.styleDone == done
	s.mu.Unlock()
	if !active {
		t.Fatal("creation Pause cleared analysis lifecycle")
	}
	release()
	waitStyleSignal(t, done, "style completion after creation pause")
	if profile, err := store.NewStore(entryDirectory).Simulation.Load(); err != nil || profile == nil {
		t.Fatalf("style could not complete after creation Pause: %+v %v", profile, err)
	}
}
