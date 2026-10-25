package bootstrap

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/voocel/agentcore"
)

func TestAutomaticModelLimitsPersistSeparatelyAndMatchConnection(t *testing.T) {
	var requests atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.URL.Path != "/v1/models" || r.Header.Get("Authorization") != "Bearer private-key" {
			t.Error("catalogue endpoint or credential mismatch")
		}
		w.Write([]byte(`{"data":[{"id":"custom-auto","context_length":64000,"max_output_tokens":8192},{"id":"custom-explicit","context_length":32000,"max_output_tokens":4000}]}`))
	}))
	defer upstream.Close()
	pc := ProviderConfig{Type: "openai", APIKey: "private-key", BaseURL: upstream.URL + "/v1", Models: []ModelConfig{{Name: "custom-auto"}, {Name: "custom-explicit", ContextWindow: 96000, MaxOutputTokens: 16000}}}
	updated, err := RefreshProviderModelLimits(t.Context(), "proxy", pc, false)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Models[0].ContextWindow != 0 || updated.Models[0].MaxOutputTokens != 0 || pc.Models[0].UpstreamLimits != nil {
		t.Fatal("refresh changed explicit overrides or original configuration")
	}
	cfg := Config{Provider: "proxy", ModelName: "custom-auto", Providers: map[string]ProviderConfig{"proxy": updated}}
	assertLimits := func(cfg Config, model string, wantWindow, wantOutput int, wantContext ContextWindowSource, wantSource OutputTokenSource) {
		t.Helper()
		window, cs := cfg.ResolveContextWindow("proxy", model)
		output, os := cfg.ResolveMaxOutputTokens("proxy", model)
		if window != wantWindow || output != wantOutput || cs != wantContext || os != wantSource {
			t.Fatalf("%s limits=(%d,%s,%d,%s)", model, window, cs, output, os)
		}
	}
	assertLimits(cfg, "custom-auto", 64000, 8192, CtxWindowUpstream, OutputTokenUpstream)
	assertLimits(cfg, "custom-explicit", 96000, 16000, CtxWindowModelConfig, OutputTokenModelConfig)
	encoded, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	var restored Config
	if err := json.Unmarshal(encoded, &restored); err != nil {
		t.Fatal(err)
	}
	assertLimits(restored, "custom-auto", 64000, 8192, CtxWindowUpstream, OutputTokenUpstream)
	if _, err := RefreshProviderModelLimits(t.Context(), "proxy", updated, false); err != nil || requests.Load() != 1 {
		t.Fatalf("fresh cache did not avoid catalogue call: requests=%d err=%v", requests.Load(), err)
	}
	clone := CloneConfig(cfg)
	clone.Providers["proxy"].Models[0].UpstreamLimits.ContextWindow = 123
	assertLimits(cfg, "custom-auto", 64000, 8192, CtxWindowUpstream, OutputTokenUpstream)
	for _, change := range []func(*ProviderConfig){func(p *ProviderConfig) { p.BaseURL += "/other" }, func(p *ProviderConfig) { p.APIKey = "another-key" }, func(p *ProviderConfig) { p.Type = "anthropic" }} {
		changed := CloneConfig(cfg)
		cp := changed.Providers["proxy"]
		change(&cp)
		changed.Providers["proxy"] = cp
		assertLimits(changed, "custom-auto", DefaultContextWindow, DefaultMaxOutputTokens, CtxWindowDefault, OutputTokenDefault)
	}
	set, err := NewModelSet(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer set.CloseRequests()
	window, cs := set.ResolveContextWindow("proxy", "custom-auto")
	output, os := set.ResolveMaxOutputTokens("proxy", "custom-auto")
	if window != 64000 || cs != CtxWindowUpstream || output != 8192 || os != OutputTokenUpstream {
		t.Fatal("runtime model set did not adopt upstream limits")
	}
}

func TestCatalogueMetadataPaginationAndNoInventedLimits(t *testing.T) {
	for _, protocol := range []string{"anthropic", "gemini"} {
		t.Run(protocol, func(t *testing.T) {
			calls := 0
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if protocol == "anthropic" {
					if calls == 1 {
						w.Write([]byte(`{"data":[{"id":"a","max_input_tokens":32000,"max_tokens":4096}],"has_more":true,"last_id":"a"}`))
						return
					}
					if r.URL.Query().Get("after_id") != "a" {
						t.Error("missing Anthropic pagination cursor")
					}
					w.Write([]byte(`{"data":[{"id":"b"}],"has_more":false}`))
					return
				}
				if calls == 1 {
					w.Write([]byte(`{"models":[{"name":"models/a","supportedGenerationMethods":["generateContent"],"inputTokenLimit":32000,"outputTokenLimit":4096}],"nextPageToken":"next"}`))
					return
				}
				if r.URL.Query().Get("pageToken") != "next" {
					t.Error("missing Gemini pagination cursor")
				}
				w.Write([]byte(`{"models":[{"name":"models/b","supportedGenerationMethods":["generateContent"]}]}`))
			}))
			defer upstream.Close()
			models, err := DiscoverModelCatalogue(t.Context(), protocol, upstream.URL+"/v1", "secret", nil)
			if err != nil || calls != 2 || len(models) != 2 {
				t.Fatalf("models=%v calls=%d err=%v", models, calls, err)
			}
			if models[0].ContextWindow != 32000 || models[0].MaxOutputTokens != 4096 || models[1].ContextWindow != 0 || models[1].MaxOutputTokens != 0 {
				t.Fatalf("metadata=%+v", models)
			}
		})
	}
}

func TestModelMetadataRefreshPreservesMatchingCacheOnFailureAndHonorsCancellation(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() }))
	defer upstream.Close()
	pc := ProviderConfig{Type: "openai", BaseURL: upstream.URL + "/v1", Models: []ModelConfig{{Name: "offline"}}}
	pc.Models[0].UpstreamLimits = &UpstreamModelLimits{ModelID: "offline", ContextWindow: 10000, MaxOutputTokens: 2000, CheckedAt: time.Now().Add(-48 * time.Hour), Connection: pc.catalogueConnection("local")}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	updated, err := RefreshProviderModelLimits(ctx, "local", pc, false)
	if err == nil || !strings.Contains(err.Error(), "deadline") || updated.Models[0].UpstreamLimits.ContextWindow != 10000 {
		t.Fatalf("refresh result=%+v err=%v", updated.Models, err)
	}
}

func TestOutputLimitCapsLargerTaskBudget(t *testing.T) {
	limited := &outputLimitedModel{limit: 4096}
	for _, requested := range []int{0, 2048, 100000} {
		got := agentcore.ResolveCallConfig(limited.callOptions([]agentcore.CallOption{agentcore.WithMaxTokens(requested)})).MaxTokens
		want := 4096
		if requested == 2048 {
			want = requested
		}
		if got != want {
			t.Fatalf("requested=%d actual=%d want=%d", requested, got, want)
		}
	}
}

func TestStartupMetadataRefreshCoversUnlistedRolesAndParallelProviders(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"data":[{"id":"default-model","context_length":64000,"max_output_tokens":8000},{"id":"writer-model","context_length":128000,"max_output_tokens":16000}]}`))
	}))
	defer upstream.Close()
	cfg := Config{Provider: "p0", ModelName: "default-model", Providers: map[string]ProviderConfig{}, Roles: map[string]RoleConfig{"writer": {Provider: "p1", Model: "writer-model", Fallbacks: []ModelRef{{Provider: "p2", Model: "default-model"}}}}}
	for i := 0; i < 20; i++ {
		cfg.Providers[fmt.Sprintf("p%d", i)] = ProviderConfig{Type: "openai", BaseURL: upstream.URL + "/v1", Models: []ModelConfig{{Name: "default-model"}}}
	}
	p := cfg.Providers["p1"]
	p.Models = nil
	cfg.Providers["p1"] = p
	refreshed := RefreshUpstreamModelLimits(t.Context(), cfg)
	if len(cfg.Providers["p1"].Models) != 0 {
		t.Fatal("startup refresh mutated caller configuration")
	}
	if window, src := refreshed.ResolveContextWindow("p1", "writer-model"); window != 128000 || src != CtxWindowUpstream {
		t.Fatalf("unlisted role metadata=%d %s", window, src)
	}
	for name := range cfg.Providers {
		if name == "p1" {
			continue
		}
		if output, src := refreshed.ResolveMaxOutputTokens(name, "default-model"); output != 8000 || src != OutputTokenUpstream {
			t.Fatalf("%s output=%d %s", name, output, src)
		}
	}
	pc := refreshed.Providers["p1"]
	pc.Models[0].Name = "renamed-model"
	refreshed.Providers["p1"] = pc
	if _, src := refreshed.ResolveContextWindow("p1", "renamed-model"); src == CtxWindowUpstream {
		t.Fatal("renaming model reused another model's cached limits")
	}
}

func TestAnthropicOriginCatalogueAndTransportHeaders(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/proxy/v1/models" || r.Header.Get("User-Agent") != "test-client" || r.Header.Get("X-Proxy-Auth") != "token" {
			t.Errorf("catalogue did not mirror adapter transport: path=%s", r.URL.Path)
		}
		w.Write([]byte(`{"data":[{"id":"claude-test","max_input_tokens":128000,"max_tokens":8192}]}`))
	}))
	defer upstream.Close()
	pc := ProviderConfig{Type: "anthropic", BaseURL: upstream.URL + "/proxy", APIKey: "secret", Models: []ModelConfig{{Name: "claude-test"}}, Extra: map[string]any{"user_agent": "test-client", "headers": map[string]any{"X-Proxy-Auth": "token"}}}
	refreshed, err := RefreshProviderModelLimits(t.Context(), "claude", pc, false)
	if err != nil || refreshed.Models[0].UpstreamLimits.ContextWindow != 128000 {
		t.Fatalf("refresh=%+v err=%v", refreshed.Models, err)
	}
}
