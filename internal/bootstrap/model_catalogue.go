package bootstrap

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"
)

const modelCatalogueMaxBytes = 2 << 20

// UpstreamModelLimits is a cache of values actually declared by this connection.
// Zero means the catalogue does not declare that limit, never an unlimited model.
// Keeping this separate from user overrides lets blank form fields stay automatic.
type UpstreamModelLimits struct {
	ModelID         string    `json:"model_id"`
	ContextWindow   int       `json:"context_window,omitempty"`
	MaxOutputTokens int       `json:"max_output_tokens,omitempty"`
	CheckedAt       time.Time `json:"checked_at"`
	Connection      string    `json:"connection"`
}

type CatalogueModel struct {
	ID              string `json:"id"`
	ContextWindow   int    `json:"context_window,omitempty"`
	MaxOutputTokens int    `json:"max_output_tokens,omitempty"`
}

type modelCatalogueResponse struct {
	Data []struct {
		ID                  string `json:"id"`
		MaxTokens           int    `json:"max_tokens"`
		ContextWindow       int    `json:"context_window"`
		ContextLength       int    `json:"context_length"`
		MaxInputTokens      int    `json:"max_input_tokens"`
		MaxCompletionTokens int    `json:"max_completion_tokens"`
		MaxOutputTokens     int    `json:"max_output_tokens"`
		TopProvider         struct {
			ContextLength       int `json:"context_length"`
			MaxCompletionTokens int `json:"max_completion_tokens"`
		} `json:"top_provider"`
	} `json:"data"`
	Models []struct {
		Name                       string   `json:"name"`
		SupportedGenerationMethods []string `json:"supportedGenerationMethods"`
		InputTokenLimit            int      `json:"inputTokenLimit"`
		OutputTokenLimit           int      `json:"outputTokenLimit"`
	} `json:"models"`
	HasMore       bool   `json:"has_more"`
	LastID        string `json:"last_id"`
	NextPageToken string `json:"nextPageToken"`
}

func firstPositiveLimit(values ...int) int {
	for _, value := range values {
		if value > 0 {
			return value
		}
	}
	return 0
}

func catalogueProtocol(providerType string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(providerType)) {
	case "openai", "openai-compatible", "custom", "openrouter", "deepseek", "qwen", "glm", "grok", "mimo", "ollama":
		return "openai", nil
	case "anthropic", "gemini":
		return strings.ToLower(strings.TrimSpace(providerType)), nil
	default:
		return "", fmt.Errorf("当前协议暂不支持查询模型列表")
	}
}

func catalogueBaseURL(providerType, baseURL string) string {
	if strings.TrimSpace(baseURL) != "" {
		return strings.TrimSpace(baseURL)
	}
	switch providerType {
	case "openai":
		return "https://api.openai.com/v1"
	case "anthropic":
		return "https://api.anthropic.com/v1"
	case "gemini":
		return "https://generativelanguage.googleapis.com/v1beta"
	case "openrouter":
		return "https://openrouter.ai/api/v1"
	case "deepseek":
		return "https://api.deepseek.com/v1"
	case "qwen":
		return "https://dashscope.aliyuncs.com/compatible-mode/v1"
	case "glm":
		return "https://open.bigmodel.cn/api/paas/v4"
	case "grok":
		return "https://api.x.ai/v1"
	case "mimo":
		return "https://api.xiaomimimo.com/v1"
	case "ollama":
		return "http://localhost:11434/v1"
	}
	return ""
}

func catalogueURL(providerType, baseURL string) (*url.URL, error) {
	baseURL = catalogueBaseURL(providerType, baseURL)
	u, err := url.Parse(baseURL)
	if err != nil || strings.ContainsAny(baseURL, "\r\n\t \\") || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.Opaque != "" {
		return nil, fmt.Errorf("Base URL 必须是有效的 HTTP/HTTPS 地址，且不能包含凭证、查询参数或片段")
	}
	u.Path = strings.TrimRight(u.Path, "/")
	// The Gemini adapter accepts either its origin or a versioned base URL.
	if providerType == "gemini" && (u.Path == "" || u.Path == "/") {
		u.Path = "/v1beta"
	}
	if providerType == "anthropic" && !strings.HasSuffix(u.Path, "/v1") {
		u.Path += "/v1"
	}
	u.Path += "/models"
	return u, nil
}

// DiscoverModelCatalogue performs read-only catalogue calls. It never invokes a
// model or copies registry guesses into upstream metadata. Pagination and bytes
// are bounded, and credentials cannot follow redirects to another origin.
func DiscoverModelCatalogue(ctx context.Context, providerType, baseURL, apiKey string, headers map[string]any) ([]CatalogueModel, error) {
	protocol, err := catalogueProtocol(providerType)
	if err != nil {
		return nil, err
	}
	endpoint, err := catalogueURL(providerType, baseURL)
	if err != nil {
		return nil, err
	}
	client := &http.Client{Timeout: 20 * time.Second, CheckRedirect: func(next *http.Request, via []*http.Request) error {
		if len(via) >= 10 {
			return fmt.Errorf("模型列表重定向次数过多")
		}
		original := via[0].URL
		if !strings.EqualFold(next.URL.Scheme, original.Scheme) || !strings.EqualFold(next.URL.Host, original.Host) || next.URL.User != nil {
			return fmt.Errorf("模型列表不能重定向到其他来源")
		}
		return nil
	}}
	found := make(map[string]CatalogueModel)
	seenPages := make(map[string]bool)
	for page := 0; page < 10; page++ {
		if seenPages[endpoint.String()] {
			return nil, fmt.Errorf("上游模型列表分页重复")
		}
		seenPages[endpoint.String()] = true
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
		if err != nil {
			return nil, fmt.Errorf("模型列表地址无效")
		}
		for name, value := range headers {
			if text, ok := value.(string); ok {
				request.Header.Set(name, text)
			}
		}
		request.Header.Set("Accept", "application/json")
		switch protocol {
		case "openai":
			if apiKey != "" {
				request.Header.Set("Authorization", "Bearer "+apiKey)
			}
		case "anthropic":
			if apiKey != "" {
				request.Header.Set("x-api-key", apiKey)
			}
			request.Header.Set("anthropic-version", "2023-06-01")
		case "gemini":
			// Query authentication retains compatibility with existing Gemini gateways.
			if apiKey != "" {
				query := request.URL.Query()
				query.Set("key", apiKey)
				request.URL.RawQuery = query.Encode()
			}
		}
		response, err := client.Do(request)
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			return nil, fmt.Errorf("无法连接上游模型列表")
		}
		body, readErr := io.ReadAll(io.LimitReader(response.Body, modelCatalogueMaxBytes+1))
		response.Body.Close()
		if response.StatusCode < 200 || response.StatusCode >= 300 {
			return nil, fmt.Errorf("上游返回 HTTP %d，请检查地址、协议和 API Key", response.StatusCode)
		}
		if readErr != nil || len(body) > modelCatalogueMaxBytes {
			return nil, fmt.Errorf("读取上游模型列表失败或响应过大")
		}
		var payload modelCatalogueResponse
		if json.Unmarshal(body, &payload) != nil {
			return nil, fmt.Errorf("上游模型列表不是有效 JSON")
		}
		add := func(model CatalogueModel) {
			model.ID = strings.TrimSpace(model.ID)
			if model.ID == "" {
				return
			}
			previous := found[model.ID]
			model.ContextWindow = firstPositiveLimit(previous.ContextWindow, model.ContextWindow)
			model.MaxOutputTokens = firstPositiveLimit(previous.MaxOutputTokens, model.MaxOutputTokens)
			found[model.ID] = model
		}
		if protocol == "gemini" {
			for _, model := range payload.Models {
				supported := false
				for _, method := range model.SupportedGenerationMethods {
					if method == "generateContent" {
						supported = true
					}
				}
				if supported {
					add(CatalogueModel{ID: strings.TrimPrefix(model.Name, "models/"), ContextWindow: model.InputTokenLimit, MaxOutputTokens: model.OutputTokenLimit})
				}
			}
		} else {
			for _, model := range payload.Data {
				add(CatalogueModel{ID: model.ID, ContextWindow: firstPositiveLimit(model.ContextWindow, model.ContextLength, model.MaxInputTokens, model.TopProvider.ContextLength), MaxOutputTokens: firstPositiveLimit(model.MaxOutputTokens, model.MaxCompletionTokens, model.TopProvider.MaxCompletionTokens, model.MaxTokens)})
			}
		}
		query := endpoint.Query()
		more := false
		if protocol == "gemini" && payload.NextPageToken != "" {
			query.Set("pageToken", payload.NextPageToken)
			more = true
		}
		if protocol == "anthropic" && payload.HasMore {
			if payload.LastID == "" {
				return nil, fmt.Errorf("上游模型列表缺少分页游标")
			}
			query.Set("after_id", payload.LastID)
			more = true
		}
		if !more {
			break
		}
		if page == 9 {
			return nil, fmt.Errorf("上游模型列表分页过多")
		}
		endpoint.RawQuery = query.Encode()
	}
	if len(found) == 0 {
		return nil, fmt.Errorf("上游未返回可选择的模型")
	}
	result := make([]CatalogueModel, 0, len(found))
	for _, model := range found {
		result = append(result, model)
	}
	sort.Slice(result, func(i, j int) bool {
		a, b := strings.ToLower(result[i].ID), strings.ToLower(result[j].ID)
		if a == b {
			return result[i].ID < result[j].ID
		}
		return a < b
	})
	return result, nil
}

// ModelCatalogueHeaders mirrors transport headers used by the model adapter.
func (pc ProviderConfig) ModelCatalogueHeaders() map[string]any {
	headers := make(map[string]any)
	if saved, ok := pc.Extra["headers"].(map[string]any); ok {
		for name, value := range saved {
			headers[name] = value
		}
	}
	if value, ok := pc.Extra["user_agent"].(string); ok && value != "" {
		headers["User-Agent"] = value
	}
	if value, ok := pc.Extra["anthropic_beta"].(string); ok && value != "" {
		headers["anthropic-beta"] = value
	}
	return headers
}

func (pc ProviderConfig) catalogueConnection(name string) string {
	protocol, _ := pc.ProviderType(name)
	payload, _ := json.Marshal([]any{protocol, pc.API, catalogueBaseURL(protocol, pc.BaseURL), pc.APIKey, pc.ModelCatalogueHeaders()})
	hash := sha256.Sum256(payload)
	return hex.EncodeToString(hash[:])
}

func (pc ProviderConfig) upstreamLimits(name string, mc ModelConfig) *UpstreamModelLimits {
	if mc.UpstreamLimits != nil && mc.UpstreamLimits.ModelID == mc.Name && mc.UpstreamLimits.Connection == pc.catalogueConnection(name) {
		return mc.UpstreamLimits
	}
	return nil
}

// RefreshProviderModelLimits fills automatic limits and leaves explicit values
// intact. Failed refreshes preserve matching last-known declarations; changing
// the endpoint, protocol or credential invalidates old cached limits immediately.
func RefreshProviderModelLimits(ctx context.Context, name string, pc ProviderConfig, force bool) (ProviderConfig, error) {
	pc.Models = append([]ModelConfig(nil), pc.Models...)
	needsRefresh := false
	for i := range pc.Models {
		mc := &pc.Models[i]
		limits := pc.upstreamLimits(name, *mc)
		if limits == nil {
			mc.UpstreamLimits = nil
		}
		if mc.ContextWindow > 0 && mc.MaxOutputTokens > 0 {
			continue
		}
		if force || limits == nil || time.Since(limits.CheckedAt) >= 24*time.Hour {
			needsRefresh = true
		}
	}
	if !needsRefresh {
		return pc, nil
	}
	protocol, err := pc.ProviderType(name)
	if err != nil {
		return pc, err
	}
	catalogue, err := DiscoverModelCatalogue(ctx, protocol, pc.BaseURL, pc.APIKey, pc.ModelCatalogueHeaders())
	if err != nil {
		return pc, err
	}
	index := make(map[string]CatalogueModel, len(catalogue))
	for _, model := range catalogue {
		index[model.ID] = model
	}
	now, fingerprint := time.Now(), pc.catalogueConnection(name)
	for i := range pc.Models {
		model := index[pc.Models[i].Name]
		pc.Models[i].UpstreamLimits = &UpstreamModelLimits{ModelID: pc.Models[i].Name, ContextWindow: model.ContextWindow, MaxOutputTokens: model.MaxOutputTokens, CheckedAt: now, Connection: fingerprint}
	}
	return pc, nil
}

// RefreshUpstreamModelLimits is called once before starting the desktop host.
// The shared deadline and four-worker ceiling keep offline startup bounded.
func RefreshUpstreamModelLimits(ctx context.Context, cfg Config) Config {
	cfg = CloneConfig(cfg)
	// Older configurations may name their active or role model without a
	// providers.models entry. Discover those references too, not just UI rows.
	ensure := func(provider, model string) {
		pc, ok := cfg.Providers[provider]
		if !ok || strings.TrimSpace(model) == "" {
			return
		}
		if _, found := pc.ModelConfig(model); !found {
			pc.Models = append(pc.Models, ModelConfig{Name: model})
			cfg.Providers[provider] = pc
		}
	}
	ensure(cfg.Provider, cfg.ModelName)
	for _, role := range cfg.Roles {
		ensure(role.Provider, role.Model)
		for _, fallback := range role.Fallbacks {
			ensure(fallback.Provider, fallback.Model)
		}
	}
	type job struct {
		name string
		pc   ProviderConfig
	}
	jobs := make([]job, 0, len(cfg.Providers))
	for name, pc := range cfg.Providers {
		jobs = append(jobs, job{name, pc})
	}
	var mu sync.Mutex
	var wg sync.WaitGroup
	slots := make(chan struct{}, 4)
	for _, task := range jobs {
		wg.Add(1)
		go func(name string, pc ProviderConfig) {
			defer wg.Done()
			select {
			case slots <- struct{}{}:
				defer func() { <-slots }()
			case <-ctx.Done():
				return
			}
			updated, _ := RefreshProviderModelLimits(ctx, name, pc, false)
			mu.Lock()
			cfg.Providers[name] = updated
			mu.Unlock()
		}(task.name, task.pc)
	}
	wg.Wait()
	return cfg
}
