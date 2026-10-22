package host

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/voocel/ainovel-cli/assets"
	"github.com/voocel/ainovel-cli/internal/bootstrap"
	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/flow"
	"github.com/voocel/ainovel-cli/internal/host/sim"
)

const simulationTestSource = `{"title":null,"summary":"清楚的语料摘要","style_observations":[],"common_words":[],"plot_patterns":[],"hook_patterns":[],"pacing_notes":[],"reader_appeal":[],"reusable_techniques":[],"warnings":[]}`
const simulationTestSynthesis = `{"style":{"narrative_voice":["贴近人物"],"sentence_rhythm":[],"prose_texture":[],"perspective":[],"mood":[],"do_not_copy":[]},"lexicon":{"common_words":[],"emotion_words":[],"scene_words":[],"transition_words":[],"signature_phrases":[]},"plot_design":{"opening_patterns":[],"escalation_patterns":[],"turning_point_patterns":[],"payoff_patterns":[]},"hook_design":{"hook_types":[],"placement":[],"cliffhanger_patterns":[],"payoff_rules":[]},"pacing_density":{"scene_density":[],"information_release":[],"dialogue_action_ratio":[],"compression_rules":[]},"reader_engagement":{"methods":[],"emotional_drivers":[],"progression_rewards":[],"anti_patterns":[]},"role_guidance":{"architect":[],"writer":[],"editor":[]}}`

type simulationHTTPFixture struct {
	creationEntered  chan struct{}
	analysisEntered  chan struct{}
	analysisCanceled chan struct{}
	releaseAnalysis  chan struct{}
	creationOnce     sync.Once
	analysisOnce     sync.Once
	cancelOnce       sync.Once
}

func newSimulationHTTPFixture() *simulationHTTPFixture {
	return &simulationHTTPFixture{creationEntered: make(chan struct{}), analysisEntered: make(chan struct{}), analysisCanceled: make(chan struct{}), releaseAnalysis: make(chan struct{})}
}

func (f *simulationHTTPFixture) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		return
	}
	text := string(body)
	response := simulationTestSynthesis
	if strings.Contains(text, "STYLE_SOURCE_FIXTURE") {
		f.analysisOnce.Do(func() { close(f.analysisEntered) })
		select {
		case <-f.releaseAnalysis:
		case <-r.Context().Done():
			f.cancelOnce.Do(func() { close(f.analysisCanceled) })
			return
		}
		response = simulationTestSource
	} else if !strings.Contains(text, "STYLE_MERGE_FIXTURE") {
		f.creationOnce.Do(func() { close(f.creationEntered) })
		<-r.Context().Done()
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"id": "simulation", "choices": []any{map[string]any{"index": 0, "message": map[string]any{"role": "assistant", "content": response}, "finish_reason": "stop"}}})
}

func newSimulationHost(t *testing.T, upstream string, acquire func(context.Context, string) (func(), error)) *Host {
	t.Helper()
	disabled, enabled := false, true
	cfg := bootstrap.Config{
		Provider: "local", ModelName: "simulation-fixture", OutputDir: t.TempDir(),
		Providers:           map[string]bootstrap.ProviderConfig{"local": {Type: "openai", BaseURL: upstream, Models: []bootstrap.ModelConfig{{Name: "simulation-fixture", ContextWindow: 128000, JSONSchema: &enabled}}}},
		AcquireModelRequest: acquire,
	}
	cfg.Notify.Enabled = &disabled
	cfg.FillDefaults()
	bundle := assets.Load(cfg.Style, assets.DefaultLoadOptions(cfg.OutputDir))
	bundle.Prompts.SimulationSource, bundle.Prompts.SimulationMerge = "STYLE_SOURCE_FIXTURE", "STYLE_MERGE_FIXTURE"
	h, err := New(cfg, bundle)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(h.Close)
	root := filepath.Join(cfg.OutputDir, "simulate")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "source.md"), []byte("短句描写雨夜，保留动作推进。"), 0o600); err != nil {
		t.Fatal(err)
	}
	return h
}

func waitSimulationSignal(t *testing.T, signal <-chan struct{}, name string) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out waiting for %s", name)
	}
}

func TestSimulationOverlapsCreationAndSurvivesCreationAbort(t *testing.T) {
	fixture := newSimulationHTTPFixture()
	upstream := httptest.NewServer(fixture)
	t.Cleanup(upstream.Close)
	var acquisitions atomic.Int32
	permit := make(chan struct{}, 1)
	h := newSimulationHost(t, upstream.URL, func(ctx context.Context, provider string) (func(), error) {
		select {
		case permit <- struct{}{}:
			acquisitions.Add(1)
			return func() { <-permit }, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	})
	if err := h.store.Progress.Init(1); err != nil {
		t.Fatal(err)
	}
	if err := h.store.Progress.UpdatePhase(domain.PhaseOutline); err != nil {
		t.Fatal(err)
	}
	if err := h.store.RunMeta.SetStartPrompt("创作一个雨夜短篇"); err != nil {
		t.Fatal(err)
	}
	if !h.startEngine(&flow.Instruction{Agent: "architect", Task: "创作短篇的完整规划"}) {
		t.Fatal("creation did not start")
	}
	waitSimulationSignal(t, fixture.creationEntered, "real creation HTTP request")
	events, err := h.Simulate(t.Context())
	if err != nil {
		t.Fatalf("analysis rejected running creation: %v", err)
	}
	waitSimulationSignal(t, fixture.analysisEntered, "analysis HTTP request while creation owns limit 1")
	if !h.Snapshot().IsRunning || acquisitions.Load() != 1 {
		t.Fatalf("analysis changed creation state or consumed its permit: running=%v acquisitions=%d", h.Snapshot().IsRunning, acquisitions.Load())
	}
	if _, err := h.Simulate(t.Context()); err == nil {
		t.Fatal("duplicate profile writer was admitted")
	}
	if !h.Abort() {
		t.Fatal("creation pause was not accepted")
	}
	h.engine.wait()
	select {
	case <-fixture.analysisCanceled:
		t.Fatal("creation Abort canceled independent analysis")
	default:
	}
	close(fixture.releaseAnalysis)
	var last sim.Event
	for event := range events {
		if event.Err != nil {
			t.Fatal(event.Err)
		}
		last = event
	}
	if last.Stage != sim.StageDone {
		t.Fatalf("analysis did not complete after creation pause: %+v", last)
	}
	if profile, err := h.store.Simulation.Load(); err != nil || profile == nil || len(profile.Corpus.Sources) != 1 {
		t.Fatalf("analysis profile not persisted: %+v %v", profile, err)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.exclusive != "" || h.simulationDone != nil || h.lifecycle == lifecycleRunning {
		t.Fatalf("analysis polluted generation state: %q/%v/%s", h.exclusive, h.simulationDone, h.lifecycle)
	}
}

func TestHostCloseCancelsAndWaitsForIndependentSimulation(t *testing.T) {
	fixture := newSimulationHTTPFixture()
	upstream := httptest.NewServer(fixture)
	t.Cleanup(upstream.Close)
	h := newSimulationHost(t, upstream.URL, nil)
	events, err := h.Simulate(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	waitSimulationSignal(t, fixture.analysisEntered, "analysis HTTP request")
	closed := make(chan struct{})
	go func() { h.Close(); close(closed) }()
	waitSimulationSignal(t, fixture.analysisCanceled, "analysis HTTP cancellation")
	waitSimulationSignal(t, closed, "Host.Close")
	for range events {
	}
	if profile, err := h.store.Simulation.Load(); err != nil || profile != nil {
		t.Fatalf("canceled analysis published a profile: %+v %v", profile, err)
	}
}
