package web

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/voocel/ainovel-cli/internal/bootstrap"
)

func TestDiscoverModelsProtocolsAndAuthentication(t *testing.T) {
	const secret = "model-discovery-secret"
	cases := []struct {
		name       string
		provider   string
		protocol   string
		path       string
		response   string
		wantModels []string
		check      func(*testing.T, *http.Request)
	}{
		{
			name:       "openai compatible",
			provider:   "compatible",
			protocol:   "openai",
			path:       "/v1/models",
			response:   `{"data":[{"id":"zeta"},{"id":"alpha"},{"id":"zeta"}]}`,
			wantModels: []string{"alpha", "zeta"},
			check: func(t *testing.T, r *http.Request) {
				t.Helper()
				if got := r.Header.Get("Authorization"); got != "Bearer "+secret {
					t.Fatalf("authorization = %q", got)
				}
			},
		},
		{
			name:       "known openai provider without explicit type",
			provider:   "openrouter",
			path:       "/v1/models",
			response:   `{"data":[{"id":"openrouter-model"}]}`,
			wantModels: []string{"openrouter-model"},
			check: func(t *testing.T, r *http.Request) {
				t.Helper()
				if got := r.Header.Get("Authorization"); got != "Bearer "+secret {
					t.Fatalf("authorization = %q", got)
				}
			},
		},
		{
			name:       "anthropic",
			provider:   "claude",
			protocol:   "anthropic",
			path:       "/v1/models",
			response:   `{"data":[{"id":"claude-sonnet"}]}`,
			wantModels: []string{"claude-sonnet"},
			check: func(t *testing.T, r *http.Request) {
				t.Helper()
				if got := r.Header.Get("x-api-key"); got != secret {
					t.Fatalf("x-api-key = %q", got)
				}
				if got := r.Header.Get("anthropic-version"); got == "" {
					t.Fatal("missing anthropic-version")
				}
			},
		},
		{
			name:       "gemini",
			provider:   "gemini",
			protocol:   "gemini",
			path:       "/v1beta/models",
			response:   `{"models":[{"name":"models/gemini-pro","supportedGenerationMethods":["generateContent"]},{"name":"models/embedding-only","supportedGenerationMethods":["embedContent"]}]}`,
			wantModels: []string{"gemini-pro"},
			check: func(t *testing.T, r *http.Request) {
				t.Helper()
				if got := r.URL.Query().Get("key"); got != secret {
					t.Fatalf("gemini key = %q", got)
				}
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != tc.path {
					t.Errorf("path = %q, want %q", r.URL.Path, tc.path)
				}
				tc.check(t, r)
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, tc.response)
			}))
			defer upstream.Close()

			s := newTestServer(t, false)
			baseURL := upstream.URL
			if tc.protocol == "gemini" {
				baseURL += "/v1beta"
			} else {
				baseURL += "/v1"
			}
			body, _ := json.Marshal(map[string]string{
				"name":     tc.provider,
				"type":     tc.protocol,
				"base_url": baseURL,
				"api_key":  secret,
			})
			w := request(s, "POST", "/api/config/models", string(body))
			if w.Code != http.StatusOK {
				t.Fatalf("discover models: %d %s", w.Code, w.Body.String())
			}
			if strings.Contains(w.Body.String(), secret) {
				t.Fatal("model discovery response leaked API key")
			}
			var result struct {
				Models []string `json:"models"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
				t.Fatal(err)
			}
			if strings.Join(result.Models, ",") != strings.Join(tc.wantModels, ",") {
				t.Fatalf("models = %v, want %v", result.Models, tc.wantModels)
			}
		})
	}
}

func TestDiscoverModelsReusesSavedKeyAndRedactsFailures(t *testing.T) {
	const secret = "saved-discovery-secret"
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+secret {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = io.WriteString(w, `{"error":"`+secret+`"}`)
			return
		}
		_, _ = io.WriteString(w, `{"data":[{"id":"saved-key-model"}]}`)
	}))
	defer upstream.Close()

	s := newTestServer(t, true)
	s.mu.Lock()
	s.baseCfg.Providers["local"] = bootstrap.ProviderConfig{
		Type:    "openai",
		APIKey:  secret,
		BaseURL: upstream.URL + "/v1",
	}
	s.mu.Unlock()
	body, _ := json.Marshal(map[string]string{
		"name":     "local",
		"type":     "openai",
		"base_url": upstream.URL + "/v1",
		"api_key":  "",
	})
	w := request(s, "POST", "/api/config/models", string(body))
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "saved-key-model") {
		t.Fatalf("saved key discovery: %d %s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), secret) {
		t.Fatal("successful discovery leaked saved key")
	}

	failing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, secret)
	}))
	defer failing.Close()
	body, _ = json.Marshal(map[string]string{
		"name":     "bad",
		"type":     "openai",
		"base_url": failing.URL + "/v1",
		"api_key":  secret,
	})
	w = request(s, "POST", "/api/config/models", string(body))
	if w.Code != http.StatusBadGateway || strings.Contains(w.Body.String(), secret) {
		t.Fatalf("failed discovery leaked key: %d %s", w.Code, w.Body.String())
	}
}
