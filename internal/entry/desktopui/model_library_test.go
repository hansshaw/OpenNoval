package desktopui

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hansshaw/AInoval-web-ui/internal/bootstrap"
	"github.com/hansshaw/AInoval-web-ui/internal/host"
)

const additionalProviderDraft = `{"name":"secondary","provider":"secondary","model":"fast","type":"openai","api":"chat","base_url":"http://127.0.0.1:2/v1","api_key":"secondary-secret-must-not-leak","models":["fast","careful"],"context_window":64000,"max_output_tokens":8000}`

func TestSaveModelLibraryDoesNotActivateNewProvider(t *testing.T) {
	s := newTestServer(t, true)
	previousHost := s.rt
	if w := request(s, "POST", "/api/config", additionalProviderDraft); w.Code != http.StatusOK {
		t.Fatalf("save library: %d %s", w.Code, w.Body.String())
	}
	if s.baseCfg.Provider != "local" || s.baseCfg.ModelName != "test-model" || s.rt != previousHost {
		t.Fatal("saving a provider changed the active model or replaced the Host")
	}
	provider, model, _ := s.rt.CurrentModelSelection("default")
	if provider != "local" || model != "test-model" {
		t.Fatalf("runtime selection = %s/%s", provider, model)
	}
	pc := s.baseCfg.Providers["secondary"]
	if len(pc.Models) != 2 || pc.Models[0].ContextWindow != 64000 || pc.Models[0].MaxOutputTokens != 8000 {
		t.Fatalf("saved model library = %+v", pc.Models)
	}
	w := request(s, "GET", "/api/config", "")
	if strings.Contains(w.Body.String(), pc.APIKey) || strings.Contains(w.Body.String(), "test-secret-do-not-return") {
		t.Fatal("model catalogue exposed a complete credential")
	}
	persisted, err := bootstrap.LoadConfigFile(s.configPath)
	if err != nil || persisted.Provider != "local" || persisted.ModelName != "test-model" || len(persisted.Providers) != 2 {
		t.Fatalf("persisted config = %+v, %v", persisted, err)
	}
}

func TestModelLibraryExplicitKeepIgnoresSubmittedCredential(t *testing.T) {
	s := newTestServer(t, true)
	body := `{"name":"local","model":"test-model","type":"openai","api":"chat","base_url":"http://127.0.0.1:1/v1","api_key_action":"keep","api_key":"draft-key-must-be-ignored"}`
	if w := request(s, "POST", "/api/config", body); w.Code != http.StatusOK {
		t.Fatalf("save with explicit keep: %d %s", w.Code, w.Body.String())
	}
	if got := s.baseCfg.Providers["local"].APIKey; got != "test-secret-do-not-return" {
		t.Fatal("explicit keep replaced the saved credential")
	}
	persisted, err := bootstrap.LoadConfigFile(s.configPath)
	if err != nil || persisted.Providers["local"].APIKey != "test-secret-do-not-return" {
		t.Fatalf("persisted credential changed after explicit keep, err = %v", err)
	}
}

func TestConnectionTestAcceptsKeylessCustomOpenAI(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer ainovel-local-no-key" {
			t.Error("keyless custom endpoint did not receive the runtime compatibility credential")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"test","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"OK"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`))
	}))
	defer upstream.Close()
	s := newTestServer(t, false)
	body := mustJSON(t, map[string]any{
		"name": "keyless", "model": "local-model", "type": "openai", "base_url": upstream.URL + "/v1", "api_key": "",
	})
	if w := request(s, "POST", "/api/config/test", body); w.Code != http.StatusOK {
		t.Fatalf("keyless connection test: %d %s", w.Code, w.Body.String())
	}
	if len(s.baseCfg.Providers) != 0 {
		t.Fatal("connection test persisted the runtime-only credential")
	}
	if w := request(s, "POST", "/api/config", body); w.Code != http.StatusOK {
		t.Fatalf("save keyless connection: %d %s", w.Code, w.Body.String())
	}
	persisted, err := bootstrap.LoadConfigFile(s.configPath)
	if err != nil || persisted.Providers["keyless"].APIKey != "" {
		t.Fatalf("runtime compatibility credential was persisted, err = %v", err)
	}
}

func TestSelectSavedModelPreservesProjectAndGlobalRoleOverrides(t *testing.T) {
	s := newTestServer(t, true)
	s.mu.Lock()
	s.baseCfg.ReasoningEffort = "high"
	s.baseCfg.Roles["writer"] = bootstrap.RoleConfig{
		Provider: "local", Model: "test-model", ReasoningEffort: "medium",
		Fallbacks: []bootstrap.ModelRef{{Provider: "local", Model: "test-model"}},
	}
	s.mu.Unlock()
	project := projectInfo{
		Name: "独立项目", RoleModels: map[string]bootstrap.ModelRef{"writer": {Provider: "secondary", Model: "fast"}},
		RoleDirectives: map[string]string{"writer": "保留叙事视角"},
	}
	if err := saveProject(s.dir, project); err != nil {
		t.Fatal(err)
	}
	if w := request(s, "POST", "/api/config", additionalProviderDraft); w.Code != http.StatusOK {
		t.Fatalf("save library: %d %s", w.Code, w.Body.String())
	}
	previousHost := s.rt
	s.mu.Lock()
	s.events = append(s.events, host.Event{Summary: "保留事件"})
	s.stream = "保留输出"
	s.mu.Unlock()
	if w := request(s, "POST", "/api/config/select", `{"provider":"secondary","model":"careful"}`); w.Code != http.StatusOK {
		t.Fatalf("select model: %d %s", w.Code, w.Body.String())
	}
	if s.rt != previousHost || s.stream != "保留输出" {
		t.Fatal("switching model discarded the existing Host or stream")
	}
	provider, model, _ := s.rt.CurrentModelSelection("default")
	if provider != "secondary" || model != "careful" {
		t.Fatalf("default = %s/%s", provider, model)
	}
	provider, model, explicit := s.rt.CurrentModelSelection("writer")
	if !explicit || provider != "secondary" || model != "fast" {
		t.Fatalf("project writer override changed: %s/%s, %v", provider, model, explicit)
	}
	if s.cfg.Roles["writer"].ReasoningEffort != "medium" || len(s.cfg.Roles["writer"].Fallbacks) != 1 || s.cfg.RoleDirectives["writer"] != "保留叙事视角" {
		t.Fatalf("project role metadata changed: %+v", s.cfg.Roles["writer"])
	}
	persisted, err := bootstrap.LoadConfigFile(s.configPath)
	if err != nil || persisted.Provider != "secondary" || persisted.ModelName != "careful" || persisted.Roles["writer"].Model != "test-model" || persisted.ReasoningEffort != "high" {
		t.Fatalf("persisted selection/role = %+v, %v", persisted, err)
	}
	unchanged, err := readProject(s.dir)
	if err != nil || unchanged.RoleDirectives["writer"] != "保留叙事视角" || unchanged.RoleModels["writer"].Provider != "secondary" {
		t.Fatalf("project config changed = %+v, %v", unchanged, err)
	}
}

func TestSelectedModelSurvivesServerRestart(t *testing.T) {
	s := newTestServer(t, true)
	if w := request(s, "POST", "/api/config", additionalProviderDraft); w.Code != http.StatusOK {
		t.Fatal(w.Body.String())
	}
	if w := request(s, "POST", "/api/config/select", `{"provider":"secondary","model":"careful"}`); w.Code != http.StatusOK {
		t.Fatal(w.Body.String())
	}
	persisted, err := bootstrap.LoadConfigFile(s.configPath)
	if err != nil {
		t.Fatal(err)
	}
	persisted.OutputDir = s.dir
	s.Close()
	restarted, err := New(persisted, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	if restarted.rt == nil {
		t.Fatalf("restarted host not ready: %s", restarted.hostError)
	}
	provider, model, _ := restarted.rt.CurrentModelSelection("default")
	if provider != "secondary" || model != "careful" || len(restarted.baseCfg.Providers) != 2 {
		t.Fatalf("restarted model selection = %s/%s", provider, model)
	}
}

func TestModelConfigurationPersistenceFailureKeepsRuntimeAndFile(t *testing.T) {
	for _, operation := range []string{"save", "select"} {
		t.Run(operation, func(t *testing.T) {
			s := newTestServer(t, true)
			if w := request(s, "POST", "/api/config", additionalProviderDraft); w.Code != http.StatusOK {
				t.Fatal(w.Body.String())
			}
			originalFile, err := os.ReadFile(s.configPath)
			if err != nil {
				t.Fatal(err)
			}
			originalHost := s.rt
			configFile := s.configPath
			// A regular file cannot contain a child config, even when tests run as root.
			s.configPath = filepath.Join(configFile, "cannot-write.json")
			path, body := "/api/config/select", `{"provider":"secondary","model":"careful"}`
			if operation == "save" {
				path = "/api/config"
				body = strings.Replace(additionalProviderDraft, `"model":"fast"`, `"model":"new-model","activate":true`, 1)
			}
			w := request(s, "POST", path, body)
			if w.Code != http.StatusInternalServerError {
				t.Fatalf("persistence failure: %d %s", w.Code, w.Body.String())
			}
			if s.rt != originalHost || s.baseCfg.Provider != "local" || s.baseCfg.ModelName != "test-model" || s.cfg.Provider != "local" {
				t.Fatal("failed persistence changed the active Host/config")
			}
			provider, model, _ := s.rt.CurrentModelSelection("default")
			if provider != "local" || model != "test-model" {
				t.Fatalf("failed persistence changed live model to %s/%s", provider, model)
			}
			current, err := os.ReadFile(configFile)
			if err != nil || string(current) != string(originalFile) {
				t.Fatal("failed persistence altered the saved configuration")
			}
			if _, ok := s.baseCfg.Providers["secondary"].ModelConfig("new-model"); ok {
				t.Fatal("failed provider draft contaminated saved model library")
			}
		})
	}
}

func TestModelLibraryProtectsReferencesInInactiveProjects(t *testing.T) {
	s := newTestServer(t, true)
	if w := request(s, "POST", "/api/config", additionalProviderDraft); w.Code != http.StatusOK {
		t.Fatal(w.Body.String())
	}
	inactive := filepath.Join(s.booksDir, "independent-copy")
	if err := saveProject(inactive, projectInfo{
		Name: "另一个副本", RoleModels: map[string]bootstrap.ModelRef{"writer": {Provider: "secondary", Model: "careful"}},
	}); err != nil {
		t.Fatal(err)
	}
	w := request(s, "POST", "/api/config", `{"name":"secondary","model":"fast","type":"openai","api":"chat","base_url":"http://127.0.0.1:2/v1","models":["fast"]}`)
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "另一个副本") || !strings.Contains(w.Body.String(), "careful") {
		t.Fatalf("inactive project reference was not protected: %d %s", w.Code, w.Body.String())
	}
	if _, ok := s.baseCfg.Providers["secondary"].ModelConfig("careful"); !ok {
		t.Fatal("referenced model was removed")
	}
}

func TestSelectModelRejectsUnknownModelsAndBusyOperations(t *testing.T) {
	s := newTestServer(t, true)
	if w := request(s, "POST", "/api/config/select", `{"provider":"local","model":"unsaved"}`); w.Code != http.StatusBadRequest {
		t.Fatalf("unsaved model accepted: %d %s", w.Code, w.Body.String())
	}
	if _, err := os.Stat(s.configPath); !os.IsNotExist(err) {
		t.Fatalf("rejected selection created a config file: %v", err)
	}
	for _, flag := range []string{"busy", "stopping", "styleBusy"} {
		t.Run(flag, func(t *testing.T) {
			s.mu.Lock()
			s.busy, s.stopping, s.styleBusy = flag == "busy", flag == "stopping", flag == "styleBusy"
			s.mu.Unlock()
			for _, path := range []string{"/api/config", "/api/config/select"} {
				body := additionalProviderDraft
				if path == "/api/config/select" {
					body = `{"provider":"local","model":"test-model"}`
				}
				if w := request(s, "POST", path, body); w.Code != http.StatusConflict {
					t.Fatalf("%s accepted while %s: %d %s", path, flag, w.Code, w.Body.String())
				}
			}
		})
	}
}

func TestInitialLibraryAndPerModelConfiguration(t *testing.T) {
	s := newTestServer(t, false)
	body := `{"name":"local","type":"openai","base_url":"http://127.0.0.1:1/v1","model_configs":[{"name":"small","context_window":32000,"max_output_tokens":4000,"json_schema":false},{"name":"large","context_window":128000,"max_output_tokens":12000}]}`
	if w := request(s, "POST", "/api/config", body); w.Code != http.StatusOK {
		t.Fatalf("initial setup: %d %s", w.Code, w.Body.String())
	}
	if s.rt == nil || s.baseCfg.Provider != "local" || s.baseCfg.ModelName != "small" {
		t.Fatal("first model configuration did not make setup usable")
	}
	w := request(s, "GET", "/api/config", "")
	var response struct {
		Providers []struct {
			Models       []string `json:"models"`
			ModelDetails []struct {
				ConfiguredContextWindow   int   `json:"configured_context_window"`
				ConfiguredMaxOutputTokens int   `json:"configured_max_output_tokens"`
				JSONSchema                *bool `json:"json_schema"`
			} `json:"model_details"`
		} `json:"providers"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if len(response.Providers) != 1 || len(response.Providers[0].Models) != 2 || len(response.Providers[0].ModelDetails) != 2 {
		t.Fatalf("model catalogue = %s", w.Body.String())
	}
	detail := response.Providers[0].ModelDetails[0]
	if detail.ConfiguredContextWindow != 32000 || detail.ConfiguredMaxOutputTokens != 4000 || detail.JSONSchema == nil || *detail.JSONSchema {
		t.Fatalf("configured capabilities were lost: %+v", detail)
	}
}

func TestInvalidLibraryDraftNeverChangesSelection(t *testing.T) {
	s := newTestServer(t, true)
	for _, body := range []string{
		`{"name":"bad","model":"x","type":"unsupported","activate":true}`,
		`{"name":"bad","model":"x","type":"openai","models":["x","x"]}`,
		`{"name":"bad","model":"x","type":"openai","context_window":-1}`,
		`{"name":"bad","model":"x","type":"openai","max_output_tokens":-1}`,
		`{"name":"bad","model":"x","type":"openai","base_url":"https://user:secret@example.com/v1"}`,
	} {
		if w := request(s, "POST", "/api/config", body); w.Code != http.StatusBadRequest {
			t.Fatalf("invalid draft accepted: %d %s", w.Code, w.Body.String())
		}
		if s.baseCfg.Provider != "local" || s.baseCfg.ModelName != "test-model" || len(s.baseCfg.Providers) != 1 {
			t.Fatal("invalid draft mutated selection or provider map")
		}
	}
}
