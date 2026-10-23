package desktopui

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/voocel/ainovel-cli/internal/models"
)

const maxModelListBytes = 2 << 20

type modelListResponse struct {
	Data []struct {
		ID                  string `json:"id"`
		MaxTokens           int    `json:"max_tokens"`
		ContextLength       int    `json:"context_length"`
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
}

type discoveredModelDetail struct {
	ID              string `json:"id"`
	ContextWindow   int    `json:"context_window,omitempty"`
	MaxOutputTokens int    `json:"max_output_tokens,omitempty"`
	Source          string `json:"source"`
}

// discoverModels reads the upstream catalogue without persisting the submitted
// endpoint or credential. Blank keys continue to use the same saved provider.
func (s *Server) discoverModels(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Provider string `json:"provider"`
		Name     string `json:"name"`
		Type     string `json:"type"`
		API      string `json:"api"`
		BaseURL  string `json:"base_url"`
		APIKey   string `json:"api_key"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		name = strings.TrimSpace(req.Provider)
	}
	if name == "" {
		fail(w, 400, "Provider 名称不能为空")
		return
	}
	s.mu.Lock()
	pc := s.baseCfg.Providers[name]
	s.mu.Unlock()
	pc.Type = strings.TrimSpace(req.Type)
	pc.API = strings.TrimSpace(req.API)
	if strings.TrimSpace(req.BaseURL) != "" {
		pc.BaseURL = strings.TrimSpace(req.BaseURL)
	}
	if strings.TrimSpace(req.APIKey) != "" {
		pc.APIKey = strings.TrimSpace(req.APIKey)
	}
	protocol, err := pc.ProviderType(name)
	if err != nil {
		fail(w, 400, "无法确定模型协议，请选择 OpenAI、Anthropic 或 Gemini")
		return
	}
	protocol, ok := modelCatalogueProtocol(protocol)
	if !ok {
		fail(w, 400, "当前协议暂不支持查询模型列表")
		return
	}
	modelsURL, err := modelCatalogueURL(protocol, pc.BaseURL)
	if err != nil {
		fail(w, 400, err.Error())
		return
	}
	if protocol == "gemini" && pc.APIKey != "" {
		u, parseErr := url.Parse(modelsURL)
		if parseErr != nil {
			fail(w, 400, "模型列表地址无效")
			return
		}
		query := u.Query()
		query.Set("key", pc.APIKey)
		u.RawQuery = query.Encode()
		modelsURL = u.String()
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, modelsURL, nil)
	if err != nil {
		fail(w, 400, "模型列表地址无效")
		return
	}
	request.Header.Set("Accept", "application/json")
	switch protocol {
	case "openai":
		if pc.APIKey != "" {
			request.Header.Set("Authorization", "Bearer "+pc.APIKey)
		}
	case "anthropic":
		if pc.APIKey != "" {
			request.Header.Set("x-api-key", pc.APIKey)
		}
		request.Header.Set("anthropic-version", "2023-06-01")
	}
	response, err := (&http.Client{Timeout: 20 * time.Second}).Do(request)
	if err != nil {
		fail(w, 502, safeModelDiscoveryError(pc.APIKey, "无法连接上游模型列表"))
		return
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, maxModelListBytes))
		fail(w, 502, fmt.Sprintf("上游返回 HTTP %d，请检查地址、协议和 API Key", response.StatusCode))
		return
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, maxModelListBytes+1))
	if err != nil || len(body) > maxModelListBytes {
		fail(w, 502, "读取上游模型列表失败或响应过大")
		return
	}
	var payload modelListResponse
	if err := json.Unmarshal(body, &payload); err != nil {
		fail(w, 502, safeModelDiscoveryError(pc.APIKey, "上游模型列表不是有效 JSON"))
		return
	}
	seen := make(map[string]bool)
	names := make([]string, 0, len(payload.Data)+len(payload.Models))
	details := make(map[string]discoveredModelDetail)
	add := func(model string, contextWindow, maxOutput int) {
		model = strings.TrimSpace(model)
		if model != "" && !seen[model] {
			seen[model] = true
			names = append(names, model)
		}
		if model == "" {
			return
		}
		detail := details[model]
		detail.ID = model
		if contextWindow > 0 && detail.ContextWindow == 0 {
			detail.ContextWindow = contextWindow
		}
		if maxOutput > 0 && detail.MaxOutputTokens == 0 {
			detail.MaxOutputTokens = maxOutput
		}
		details[model] = detail
	}
	if protocol == "gemini" {
		for _, model := range payload.Models {
			generate := false
			for _, method := range model.SupportedGenerationMethods {
				if method == "generateContent" {
					generate = true
					break
				}
			}
			if !generate {
				continue
			}
			add(strings.TrimPrefix(model.Name, "models/"), model.InputTokenLimit, model.OutputTokenLimit)
		}
	} else {
		for _, model := range payload.Data {
			contextWindow := firstPositive(model.ContextLength, model.TopProvider.ContextLength)
			maxOutput := firstPositive(model.MaxOutputTokens, model.MaxCompletionTokens, model.TopProvider.MaxCompletionTokens, model.MaxTokens)
			add(model.ID, contextWindow, maxOutput)
		}
	}
	if len(names) == 0 {
		fail(w, 502, "上游未返回可选择的模型")
		return
	}
	sort.Slice(names, func(i, j int) bool {
		return strings.ToLower(names[i]) < strings.ToLower(names[j])
	})
	result := make([]discoveredModelDetail, 0, len(names))
	for _, model := range names {
		detail := details[model]
		detail.ID = model
		detail.Source = "upstream"
		if detail.ContextWindow == 0 || detail.MaxOutputTokens == 0 {
			if entry, ok := models.DefaultRegistry().Resolve(model); ok {
				if detail.ContextWindow == 0 {
					detail.ContextWindow = entry.ContextWindow
				}
				if detail.MaxOutputTokens == 0 {
					detail.MaxOutputTokens = entry.MaxTokens
				}
				if detail.ContextWindow > 0 || detail.MaxOutputTokens > 0 {
					detail.Source = "registry"
				}
			}
		}
		if detail.ContextWindow == 0 && detail.MaxOutputTokens == 0 {
			detail.Source = "unknown"
		}
		result = append(result, detail)
	}
	reply(w, 200, map[string]any{"models": names, "details": result, "source": "upstream"})
}

func firstPositive(values ...int) int {
	for _, value := range values {
		if value > 0 {
			return value
		}
	}
	return 0
}

func modelCatalogueProtocol(providerType string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(providerType)) {
	case "openai", "openrouter", "deepseek", "qwen", "glm", "grok", "mimo", "ollama":
		return "openai", true
	case "anthropic", "gemini":
		return strings.ToLower(strings.TrimSpace(providerType)), true
	default:
		return "", false
	}
}

func modelCatalogueURL(protocol, baseURL string) (string, error) {
	baseURL = strings.TrimSpace(baseURL)
	if strings.ContainsAny(baseURL, "\r\n") {
		return "", fmt.Errorf("Base URL 无效")
	}
	u, err := url.Parse(baseURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return "", fmt.Errorf("Base URL 必须是有效的 HTTP/HTTPS 地址")
	}
	u.Path = strings.TrimRight(u.Path, "/") + "/models"
	return u.String(), nil
}

func safeModelDiscoveryError(secret, fallback string) string {
	if secret == "" {
		return fallback
	}
	return strings.ReplaceAll(fallback, secret, "[已隐藏]")
}
