package bootstrap

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/voocel/agentcore"
)

func TestCustomOpenAIWithoutKeyCanGenerateWithoutMutatingConfig(t *testing.T) {
	for _, configuredKey := range []string{"", "real-endpoint-key"} {
		t.Run(map[bool]string{true: "keyless", false: "configured-key"}[configuredKey == ""], func(t *testing.T) {
			var requests atomic.Int64
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				wantKey := configuredKey
				if wantKey == "" {
					wantKey = "ainovel-local-no-key"
				}
				if got := r.Header.Get("Authorization"); got != "Bearer "+wantKey {
					t.Errorf("unexpected Authorization header for custom endpoint")
				}
				var payload struct {
					Model string `json:"model"`
				}
				if err := json.NewDecoder(r.Body).Decode(&payload); err != nil || payload.Model != "local-model" {
					t.Errorf("custom request model = %q, decode error = %v", payload.Model, err)
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"id":"keyless-test","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"OK"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`))
			}))
			defer upstream.Close()
			cfg := Config{
				Provider: "custom", ModelName: "local-model",
				Providers: map[string]ProviderConfig{
					"custom": {Type: "openai", BaseURL: upstream.URL + "/v1", APIKey: configuredKey},
				},
			}
			if err := cfg.ValidateBase(); err != nil {
				t.Fatal(err)
			}
			set, err := NewModelSet(cfg)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			if _, err := set.Default.Generate(ctx, []agentcore.Message{agentcore.UserMsg("Reply OK.")}, nil); err != nil {
				t.Fatalf("custom endpoint could not generate: %v", err)
			}
			if requests.Load() != 1 || cfg.Providers["custom"].APIKey != configuredKey || set.config.Providers["custom"].APIKey != configuredKey {
				t.Fatal("runtime compatibility credential changed the saved configuration or request was not sent")
			}
		})
	}
}

func TestCustomOpenAIKeylessCompatibilityDoesNotApplyToOfficialEndpoints(t *testing.T) {
	for _, pc := range []ProviderConfig{
		{Type: "openai"},
		{Type: "openai", BaseURL: "https://api.openai.com/v1"},
		{Type: "openai", BaseURL: "https://API.OPENAI.COM./v1"},
		{BaseURL: "https://api.openai.com/v1"},
	} {
		model, err := createModelFromConfig("openai", "test", pc, 32, make(map[string]agentcore.ChatModel))
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		_, err = model.Generate(ctx, []agentcore.Message{agentcore.UserMsg("test")}, nil)
		cancel()
		if err == nil || !strings.Contains(err.Error(), "api key is required") {
			t.Fatalf("official OpenAI endpoint accepted a missing key, err = %v", err)
		}
	}
}
