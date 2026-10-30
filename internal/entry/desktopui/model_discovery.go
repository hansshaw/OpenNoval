package desktopui

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/hansshaw/OpenNovel/internal/bootstrap"
	"github.com/hansshaw/OpenNovel/internal/host"
	"github.com/hansshaw/OpenNovel/internal/models"
)

type discoveredModelDetail struct {
	ID              string `json:"id"`
	ContextWindow   int    `json:"context_window,omitempty"`
	MaxOutputTokens int    `json:"max_output_tokens,omitempty"`
	Source          string `json:"source"`
	ContextSource   string `json:"context_source"`
	OutputSource    string `json:"output_source"`
}

// Discovery previews draft connections. Saving performs its own authenticated
// lookup; client-supplied values cannot masquerade as upstream declarations.
func (s *Server) discoverModels(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Provider     string            `json:"provider"`
		Name         string            `json:"name"`
		Type         *string           `json:"type"`
		API          *string           `json:"api"`
		BaseURL      *string           `json:"base_url"`
		APIKey       string            `json:"api_key"`
		APIKeyAction host.APIKeyAction `json:"api_key_action"`
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
	pc := bootstrap.CloneConfig(s.baseCfg).Providers[name]
	s.mu.Unlock()
	if req.Type != nil {
		pc.Type = strings.TrimSpace(*req.Type)
	}
	if req.API != nil {
		pc.API = strings.TrimSpace(*req.API)
	}
	if req.BaseURL != nil {
		pc.BaseURL = strings.TrimSpace(*req.BaseURL)
	}
	switch req.APIKeyAction {
	case host.APIKeyKeep:
	case host.APIKeyClear:
		pc.APIKey = ""
	case "", host.APIKeyReplace:
		if req.APIKeyAction == host.APIKeyReplace || strings.TrimSpace(req.APIKey) != "" {
			pc.APIKey = strings.TrimSpace(req.APIKey)
		}
	default:
		fail(w, 400, "未知 API Key 操作")
		return
	}
	protocol, err := pc.ProviderType(name)
	if err != nil {
		fail(w, 400, "无法确定模型协议，请选择 OpenAI、Anthropic 或 Gemini")
		return
	}
	replyDiscoveredModels(w, r, protocol, pc.BaseURL, pc.APIKey, pc.ModelCatalogueHeaders(), true)
}

func replyDiscoveredModels(w http.ResponseWriter, r *http.Request, protocol, baseURL, key string, headers map[string]any, withRegistry bool) {
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	catalogue, err := bootstrap.DiscoverModelCatalogue(ctx, protocol, baseURL, key, headers)
	if err != nil {
		fail(w, http.StatusBadGateway, err.Error())
		return
	}
	names := make([]string, 0, len(catalogue))
	details := make([]discoveredModelDetail, 0, len(catalogue))
	for _, model := range catalogue {
		names = append(names, model.ID)
		detail := discoveredModelDetail{ID: model.ID, ContextWindow: model.ContextWindow, MaxOutputTokens: model.MaxOutputTokens, ContextSource: "unknown", OutputSource: "unknown"}
		if detail.ContextWindow > 0 {
			detail.ContextSource = "upstream"
		}
		if detail.MaxOutputTokens > 0 {
			detail.OutputSource = "upstream"
		}
		if withRegistry {
			if entry, ok := models.DefaultRegistry().Resolve(model.ID); ok {
				if detail.ContextWindow == 0 && entry.ContextWindow > 0 {
					detail.ContextWindow, detail.ContextSource = entry.ContextWindow, "registry"
				}
				if detail.MaxOutputTokens == 0 && entry.MaxTokens > 0 {
					detail.MaxOutputTokens, detail.OutputSource = entry.MaxTokens, "registry"
				}
			}
		}
		detail.Source = detail.ContextSource
		if detail.Source == "unknown" {
			detail.Source = detail.OutputSource
		} else if detail.OutputSource != "unknown" && detail.OutputSource != detail.Source {
			detail.Source = "mixed"
		}
		details = append(details, detail)
	}
	reply(w, 200, map[string]any{"models": names, "details": details, "source": "upstream"})
}

func (s *Server) discoverMediaModels(w http.ResponseWriter, r *http.Request) {
	var req mediaModelRequest
	if !readJSONLimit(w, r, &req, 32<<10) {
		return
	}
	if !validMediaKind(req.Kind) {
		fail(w, 400, "媒体类型必须是 image、video 或 audio")
		return
	}
	if req.Protocol != "openai-compatible" && req.Protocol != "custom" {
		fail(w, 400, "接入协议必须是 openai-compatible 或 custom")
		return
	}
	if !validMediaURL(req.BaseURL) {
		fail(w, 400, "Base URL 必须是有效的 HTTP/HTTPS 地址")
		return
	}
	key := ""
	if req.ID != "" {
		path, mu, ok := s.mediaModelConfigFile(w)
		if !ok {
			return
		}
		mu.RLock()
		cfg, err := loadMediaModelConfig(path)
		mu.RUnlock()
		if err != nil {
			fail(w, 500, "媒体模型配置读取失败")
			return
		}
		found := false
		for _, connection := range cfg.Connections {
			if connection.ID == req.ID {
				key = connection.APIKey
				found = true
				break
			}
		}
		if !found {
			fail(w, 404, "该媒体连接已不存在，请刷新配置")
			return
		}
	}
	switch req.APIKeyAction {
	case host.APIKeyKeep:
	case host.APIKeyClear:
		key = ""
	case host.APIKeyReplace:
		key = strings.TrimSpace(req.APIKey)
		if key == "" {
			fail(w, 400, "替换密钥时请填写 API Key")
			return
		}
	default:
		fail(w, 400, "请明确选择保留、替换或清空 API Key")
		return
	}
	// Compatible catalogues often omit modalities. Return every ID instead of
	// guessing from names and hiding valid image, video, or speech models.
	replyDiscoveredModels(w, r, req.Protocol, req.BaseURL, key, nil, false)
}
