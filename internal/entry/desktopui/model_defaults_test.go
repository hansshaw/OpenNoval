package desktopui

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/hansshaw/OpenNovel/internal/bootstrap"
)

func TestSaveAutomaticModelLimitsAndEditBackToAutomatic(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			t.Errorf("unexpected request %s", r.URL.Path)
		}
		w.Write([]byte(`{"data":[{"id":"auto-model","context_length":96000,"max_output_tokens":12000}]}`))
	}))
	defer upstream.Close()
	s := newTestServer(t, false)
	draft := map[string]any{"name": "proxy", "type": "openai", "base_url": upstream.URL + "/v1", "model": "auto-model", "context_window": 0, "max_output_tokens": 0}
	save := func() {
		t.Helper()
		w := request(s, "POST", "/api/config", mustJSON(t, draft))
		if w.Code != 200 {
			t.Fatalf("save=%d %s", w.Code, w.Body.String())
		}
	}
	save()
	assert := func(window, output int, contextSource, outputSource string) {
		t.Helper()
		w := request(s, "GET", "/api/config", "")
		var result struct {
			Providers []struct {
				ModelDetails []struct {
					Name          string `json:"name"`
					Context       int    `json:"context_window"`
					Output        int    `json:"max_output_tokens"`
					ContextSource string `json:"context_source"`
					OutputSource  string `json:"output_source"`
				} `json:"model_details"`
			} `json:"providers"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		got := result.Providers[0].ModelDetails[0]
		if got.Context != window || got.Output != output || got.ContextSource != contextSource || got.OutputSource != outputSource {
			t.Fatalf("detail=%+v", got)
		}
	}
	assert(96000, 12000, "upstream", "upstream")
	persisted, err := bootstrap.LoadConfigFile(s.configPath)
	if err != nil {
		t.Fatal(err)
	}
	mc := persisted.Providers["proxy"].Models[0]
	if mc.ContextWindow != 0 || mc.MaxOutputTokens != 0 || mc.UpstreamLimits == nil {
		t.Fatalf("automatic values persisted as overrides: %+v", mc)
	}
	draft["context_window"], draft["max_output_tokens"] = 48000, 4000
	save()
	assert(48000, 4000, "model_config", "model_config")
	draft["context_window"], draft["max_output_tokens"] = 0, 0
	save()
	assert(96000, 12000, "upstream", "upstream")
}

func TestAutomaticMetadataLookupDoesNotHoldWorkspaceLock(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(entered)
		<-release
		w.Write([]byte(`{"data":[{"id":"auto-model","context_length":64000}]}`))
	}))
	defer upstream.Close()
	s := newTestServer(t, false)
	done := make(chan int, 1)
	go func() {
		w := request(s, "POST", "/api/config", mustJSON(t, map[string]any{"name": "proxy", "type": "openai", "base_url": upstream.URL + "/v1", "model": "auto-model"}))
		done <- w.Code
	}()
	<-entered
	readDone := make(chan int, 1)
	go func() { readDone <- request(s, "GET", "/api/config", "").Code }()
	select {
	case status := <-readDone:
		if status != 200 {
			t.Fatalf("read config status=%d", status)
		}
	case <-time.After(time.Second):
		close(release)
		t.Fatal("metadata fetch held workspace lock")
	}
	close(release)
	if status := <-done; status != 200 {
		t.Fatalf("save status=%d", status)
	}
}

func TestMediaDiscoveryUsesSavedCredentialForEveryKind(t *testing.T) {
	const secret = "media-list-private-key"
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" || r.Header.Get("Authorization") != "Bearer "+secret {
			t.Error("media discovery did not use stored connection")
		}
		w.Write([]byte(`{"data":[{"id":"arbitrary-model-name"},{"id":"custom-voice"}]}`))
	}))
	defer upstream.Close()
	s := newTestServer(t, false)
	for _, kind := range []string{"image", "video", "audio"} {
		w := request(s, "POST", "/api/media-models", mustJSON(t, map[string]any{"name": "test " + kind, "kind": kind, "protocol": "openai-compatible", "baseURL": upstream.URL + "/v1", "model": "arbitrary-model-name", "apiKey": secret, "apiKeyAction": "replace"}))
		if w.Code != 200 {
			t.Fatalf("save media=%d %s", w.Code, w.Body.String())
		}
		var result struct {
			SavedID     string           `json:"savedId"`
			Connections []mediaModelView `json:"connections"`
		}
		json.Unmarshal(w.Body.Bytes(), &result)
		id := ""
		for _, c := range result.Connections {
			if c.Kind == kind {
				id = c.ID
			}
		}
		if id == "" {
			t.Fatalf("no saved media id: %s", w.Body.String())
		}
		w = request(s, "POST", "/api/media-models/discover", mustJSON(t, map[string]any{"id": id, "kind": kind, "protocol": "openai-compatible", "baseURL": upstream.URL + "/v1", "apiKeyAction": "keep", "apiKey": "ignored-draft"}))
		if w.Code != 200 || !strings.Contains(w.Body.String(), "arbitrary-model-name") || strings.Contains(w.Body.String(), secret) {
			t.Fatalf("discover %s=%d %s", kind, w.Code, w.Body.String())
		}
	}
}

func TestDiscoveryExplicitAutomaticProtocolOverridesSavedType(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("x-api-key") != "saved-key" || r.Header.Get("Authorization") != "" {
			t.Error("discovery used saved explicit type instead of requested inferred type")
		}
		w.Write([]byte(`{"data":[{"id":"claude-test","context_length":32000}]}`))
	}))
	defer upstream.Close()
	s := newTestServer(t, false)
	s.baseCfg.Providers["anthropic"] = bootstrap.ProviderConfig{Type: "openai", BaseURL: upstream.URL + "/v1", APIKey: "saved-key"}
	w := request(s, "POST", "/api/config/models", mustJSON(t, map[string]any{"name": "anthropic", "type": "", "api": "", "base_url": upstream.URL + "/v1", "api_key_action": "keep"}))
	if w.Code != 200 {
		t.Fatalf("discover=%d %s", w.Code, w.Body.String())
	}
}
