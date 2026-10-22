package web

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"

	"github.com/voocel/ainovel-cli/assets"
	"github.com/voocel/ainovel-cli/internal/bootstrap"
	"github.com/voocel/ainovel-cli/internal/host"
)

// Model is the model whose limits are being edited. Activation is a separate,
// explicit action; adding a provider never silently replaces a saved selection.
type modelLibraryRequest struct {
	Provider        string                  `json:"provider"`
	Model           string                  `json:"model"`
	Name            string                  `json:"name"`
	Type            string                  `json:"type"`
	API             string                  `json:"api"`
	BaseURL         string                  `json:"base_url"`
	APIKey          string                  `json:"api_key"`
	APIKeyAction    host.APIKeyAction       `json:"api_key_action"`
	Models          []string                `json:"models"`
	ModelConfigs    []bootstrap.ModelConfig `json:"model_configs"`
	ContextWindow   *int                    `json:"context_window"`
	MaxOutputTokens *int                    `json:"max_output_tokens"`
	Activate        bool                    `json:"activate"`
}

func (s *Server) modelConfigurationBusyLocked() bool {
	return s.closed || s.busy || s.stopping || s.styleBusy || s.backgroundAnyBusyLocked() || (s.rt != nil && s.rt.Snapshot().IsRunning)
}

func (s *Server) getConfig(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	type modelView struct {
		Name                      string                        `json:"name"`
		ContextWindow             int                           `json:"context_window"`
		ContextSource             bootstrap.ContextWindowSource `json:"context_source"`
		MaxOutputTokens           int                           `json:"max_output_tokens"`
		OutputSource              bootstrap.OutputTokenSource   `json:"output_source"`
		ConfiguredContextWindow   int                           `json:"configured_context_window"`
		ConfiguredMaxOutputTokens int                           `json:"configured_max_output_tokens"`
		JSONSchema                *bool                         `json:"json_schema,omitempty"`
	}
	type providerView struct {
		Name           string      `json:"name"`
		Type           string      `json:"type,omitempty"`
		API            string      `json:"api,omitempty"`
		BaseURL        string      `json:"base_url,omitempty"`
		Models         []string    `json:"models"`
		ModelDetails   []modelView `json:"model_details"`
		HasAPIKey      bool        `json:"has_api_key"`
		APIKeyHint     string      `json:"api_key_hint,omitempty"`
		RequiresAPIKey bool        `json:"requires_api_key"`
	}
	providers := make([]providerView, 0, len(s.baseCfg.Providers))
	styles := make([]string, 0)
	for name := range assets.Load(s.baseCfg.Style, assets.DefaultLoadOptions(s.dir)).Styles {
		styles = append(styles, name)
	}
	sort.Strings(styles)
	for name, pc := range s.baseCfg.Providers {
		names := s.baseCfg.CandidateModels(name)
		details := make([]modelView, 0, len(names))
		for _, nameModel := range names {
			mc, _ := pc.ModelConfig(nameModel)
			window, contextSource := s.baseCfg.ResolveContextWindow(name, nameModel)
			maxOutput, outputSource := s.baseCfg.ResolveMaxOutputTokens(name, nameModel)
			details = append(details, modelView{
				Name: nameModel, ContextWindow: window, ContextSource: contextSource,
				MaxOutputTokens: maxOutput, OutputSource: outputSource,
				ConfiguredContextWindow: mc.ContextWindow, ConfiguredMaxOutputTokens: mc.MaxOutputTokens,
				JSONSchema: mc.JSONSchema,
			})
		}
		protocol, _ := pc.ProviderType(name)
		providers = append(providers, providerView{
			Name: name, Type: protocol, API: pc.API, BaseURL: s.redactLocked(pc.BaseURL),
			Models: names, ModelDetails: details, HasAPIKey: pc.APIKey != "",
			APIKeyHint: host.MaskAPIKey(pc.APIKey), RequiresAPIKey: pc.RequiresAPIKey(name),
		})
	}
	sort.Slice(providers, func(i, j int) bool { return providers[i].Name < providers[j].Name })
	reply(w, 200, map[string]any{
		"provider": s.baseCfg.Provider, "model": s.baseCfg.ModelName, "providers": providers,
		"styles": styles, "config_path": s.configPath, "roles": s.cfg.Roles,
		"can_switch": !s.modelConfigurationBusyLocked(),
	})
}

func (s *Server) saveConfig(w http.ResponseWriter, r *http.Request) {
	var req modelLibraryRequest
	if !readJSON(w, r, &req) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.modelConfigurationBusyLocked() {
		fail(w, 409, "请先暂停创作并等待当前操作结束后再修改配置")
		return
	}
	candidate, err := s.prepareModelLibraryLocked(req)
	if err != nil {
		fail(w, 400, redactConfig(candidate, err.Error()))
		return
	}
	if err := s.commitModelConfigurationLocked(candidate); err != nil {
		s.modelConfigurationErrorLocked(w, candidate, err)
		return
	}
	s.broadcastLocked("workspace", nil)
	reply(w, 200, map[string]any{"ok": true, "error": s.hostError, "provider": s.baseCfg.Provider, "model": s.baseCfg.ModelName})
}

func (s *Server) prepareModelLibraryLocked(req modelLibraryRequest) (bootstrap.Config, error) {
	candidate := bootstrap.CloneConfig(s.baseCfg)
	name := strings.TrimSpace(req.Name)
	if name == "" {
		name = strings.TrimSpace(req.Provider)
	}
	model := strings.TrimSpace(req.Model)
	if name == "" {
		return candidate, fmt.Errorf("Provider 名称不能为空")
	}
	pc := candidate.Providers[name]
	pc.Type = strings.ToLower(strings.TrimSpace(req.Type))
	pc.API = strings.ToLower(strings.TrimSpace(req.API))
	pc.BaseURL = strings.TrimSpace(req.BaseURL)
	if pc.BaseURL != "" {
		u, err := url.Parse(pc.BaseURL)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
			return candidate, fmt.Errorf("Base URL 必须是有效的 HTTP/HTTPS 地址，且不能包含凭证、查询参数或片段")
		}
	}
	switch req.APIKeyAction {
	case "":
		if strings.TrimSpace(req.APIKey) != "" {
			pc.APIKey = strings.TrimSpace(req.APIKey)
		}
	case host.APIKeyKeep:
		// Explicit keep takes precedence over a draft credential field.
	case host.APIKeyReplace:
		pc.APIKey = strings.TrimSpace(req.APIKey)
	case host.APIKeyClear:
		pc.APIKey = ""
	default:
		return candidate, fmt.Errorf("未知 API Key 操作")
	}
	// Full object drafts can edit each model; the names-only compatibility form
	// retains saved capabilities and limits for every unchanged model.
	if req.ModelConfigs != nil && req.Models != nil {
		return candidate, fmt.Errorf("models 和 model_configs 只能提交其中一种")
	}
	if req.ModelConfigs != nil {
		pc.Models = append([]bootstrap.ModelConfig(nil), req.ModelConfigs...)
	} else if req.Models != nil {
		previous := pc
		pc.Models = make([]bootstrap.ModelConfig, 0, len(req.Models))
		for _, nameModel := range req.Models {
			nameModel = strings.TrimSpace(nameModel)
			mc, found := previous.ModelConfig(nameModel)
			if !found {
				mc = bootstrap.ModelConfig{Name: nameModel}
			}
			pc.Models = append(pc.Models, mc)
		}
	}
	for i := range pc.Models {
		pc.Models[i].Name = strings.TrimSpace(pc.Models[i].Name)
	}
	if model != "" {
		found := false
		for i := range pc.Models {
			if pc.Models[i].Name == model {
				found = true
				if req.ContextWindow != nil {
					pc.Models[i].ContextWindow = *req.ContextWindow
				}
				if req.MaxOutputTokens != nil {
					pc.Models[i].MaxOutputTokens = *req.MaxOutputTokens
				}
			}
		}
		if !found {
			mc := bootstrap.ModelConfig{Name: model}
			if req.ContextWindow != nil {
				mc.ContextWindow = *req.ContextWindow
			}
			if req.MaxOutputTokens != nil {
				mc.MaxOutputTokens = *req.MaxOutputTokens
			}
			pc.Models = append(pc.Models, mc)
		}
	}
	if len(pc.Models) == 0 {
		return candidate, fmt.Errorf("请至少配置一个模型")
	}
	if candidate.Providers == nil {
		candidate.Providers = make(map[string]bootstrap.ProviderConfig)
	}
	candidate.Providers[name] = pc
	if req.Activate || candidate.Provider == "" || candidate.ModelName == "" {
		if model == "" {
			model = pc.Models[0].Name
		}
		candidate.Provider, candidate.ModelName = name, model
	}
	candidate.FillDefaults()
	if err := s.validatePreservedModelReferencesLocked(candidate, name); err != nil {
		return candidate, err
	}
	// Validate the edited provider even when it is not used by any role yet.
	probe := bootstrap.CloneConfig(candidate)
	probe.Provider, probe.ModelName, probe.Roles = name, pc.Models[0].Name, nil
	if err := probe.ValidateBase(); err != nil {
		return candidate, err
	}
	if _, err := bootstrap.NewModelSet(probe); err != nil {
		return candidate, err
	}
	return candidate, nil
}

// Reject removal of models used by a default, role, fallback, or another saved
// project. Otherwise editing the global library could break an inactive copy.
func (s *Server) validatePreservedModelReferencesLocked(candidate bootstrap.Config, provider string) error {
	pc := candidate.Providers[provider]
	check := func(label, nameProvider, model string) error {
		if nameProvider == provider {
			if _, ok := pc.ModelConfig(model); !ok {
				return fmt.Errorf("模型 %s/%s 仍被%s使用，请先切换后再删除", provider, model, label)
			}
		}
		return nil
	}
	if err := check("当前默认模型", candidate.Provider, candidate.ModelName); err != nil {
		return err
	}
	for role, rc := range candidate.Roles {
		if err := check("角色 "+role, rc.Provider, rc.Model); err != nil {
			return err
		}
		for _, ref := range rc.Fallbacks {
			if err := check("角色 "+role+" 的备用模型", ref.Provider, ref.Model); err != nil {
				return err
			}
		}
	}
	if _, err := os.ReadDir(s.booksDir); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("无法检查其他项目的模型引用")
	}
	for _, dir := range s.projectDirectoriesLocked() {
		project, err := readProject(dir)
		if err != nil {
			return fmt.Errorf("项目资料读取失败，无法检查模型引用")
		}
		for role, ref := range project.RoleModels {
			if err := check("项目 "+project.Name+" 的角色 "+role, ref.Provider, ref.Model); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *Server) selectModel(w http.ResponseWriter, r *http.Request) {
	var req bootstrap.ModelRef
	if !readJSON(w, r, &req) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.modelConfigurationBusyLocked() {
		fail(w, 409, "请先暂停创作并等待当前操作结束后再切换模型")
		return
	}
	req.Provider, req.Model = strings.TrimSpace(req.Provider), strings.TrimSpace(req.Model)
	pc, exists := s.baseCfg.Providers[req.Provider]
	found := false
	for _, model := range s.baseCfg.CandidateModels(req.Provider) {
		found = found || model == req.Model
	}
	if !exists || req.Model == "" || !found {
		fail(w, 400, "请选择已保存的 Provider 和模型")
		return
	}
	if pc.RequiresAPIKey(req.Provider) && pc.APIKey == "" {
		fail(w, 400, "该 Provider 尚未配置 API Key")
		return
	}
	candidate := bootstrap.CloneConfig(s.baseCfg)
	candidate.Provider, candidate.ModelName = req.Provider, req.Model
	if err := s.commitModelConfigurationLocked(candidate); err != nil {
		s.modelConfigurationErrorLocked(w, candidate, err)
		return
	}
	s.broadcastLocked("workspace", nil)
	reply(w, 200, map[string]any{"ok": true, "provider": candidate.Provider, "model": candidate.ModelName, "error": s.hostError})
}

type configPersistenceError struct{ err error }

func (e configPersistenceError) Error() string { return "保存配置失败" }
func (e configPersistenceError) Unwrap() error { return e.err }

func (s *Server) modelConfigurationErrorLocked(w http.ResponseWriter, candidate bootstrap.Config, err error) {
	var persistenceError configPersistenceError
	if errors.As(err, &persistenceError) {
		fail(w, 500, persistenceError.Error())
	} else if errors.Is(err, host.ErrModelConfigurationBusy) {
		fail(w, 409, err.Error())
	} else {
		fail(w, 400, redactConfig(candidate, err.Error()))
	}
}

func (s *Server) commitModelConfigurationLocked(candidate bootstrap.Config) error {
	candidate = bootstrap.CloneConfig(candidate)
	candidate.OutputDir = s.dir
	if err := candidate.ValidateBase(); err != nil {
		return err
	}
	project, err := readProject(s.dir)
	if err != nil {
		return fmt.Errorf("项目资料读取失败")
	}
	effective := bootstrap.CloneConfig(candidate)
	effective.AcquireModelRequest = s.acquireCreationModel
	effective.StyleReferenceProfiles = s.styleReferenceProvider(s.dir)
	if project.StylePreset != "" {
		effective.Style = project.StylePreset
	}
	applyProjectRoleConfigs(&effective, project.RoleModels)
	for role, directive := range project.RoleDirectives {
		effective.RoleDirectives[role] = directive
	}
	if err := effective.ValidateBase(); err != nil {
		return err
	}
	persist := func() error {
		if s.configPath == "" {
			return configPersistenceError{fmt.Errorf("无法定位配置文件路径")}
		}
		if err := bootstrap.SaveConfig(s.configPath, candidate); err != nil {
			return configPersistenceError{err}
		}
		return nil
	}
	if s.rt != nil {
		if err := s.rt.ReconfigureModels(effective, persist); err != nil {
			return err
		}
	} else {
		if _, err := bootstrap.NewModelSet(effective); err != nil {
			return err
		}
		if err := persist(); err != nil {
			return err
		}
	}
	// All active projects are guarded above. Drop idle retained model sets so
	// their next open receives the newly saved connection and model selection.
	for directory := range s.projectRuntimes {
		s.closeIdleProjectLocked(directory)
	}
	s.baseCfg, s.cfg = bootstrap.CloneConfig(candidate), effective
	if s.rt == nil {
		s.initLocked()
	}
	return nil
}
