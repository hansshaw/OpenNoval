package desktopui

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"

	"github.com/hansshaw/OpenNovel/internal/fileio"
	"github.com/hansshaw/OpenNovel/internal/host"
)

const mediaModelConfigLimit = 1 << 20

// Media connections are independent of text-generation providers. Saving one
// must neither replace a Host nor interrupt a project that is writing prose.
// The file lock is shared by Servers that point at the same global config.
var mediaModelConfigLocks sync.Map

type mediaModelConnection struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Kind        string `json:"kind"`
	Protocol    string `json:"protocol"`
	BaseURL     string `json:"baseURL"`
	Model       string `json:"model"`
	Description string `json:"description,omitempty"`
	Voice       string `json:"voice,omitempty"`
	APIKey      string `json:"apiKey,omitempty"`
}

type mediaModelConfig struct {
	SchemaVersion int                    `json:"schemaVersion"`
	Connections   []mediaModelConnection `json:"connections"`
	Selected      map[string]string      `json:"selected"`
}

type mediaModelRequest struct {
	ID           string            `json:"id"`
	Name         string            `json:"name"`
	Kind         string            `json:"kind"`
	Protocol     string            `json:"protocol"`
	BaseURL      string            `json:"baseURL"`
	Model        string            `json:"model"`
	Description  string            `json:"description"`
	Voice        string            `json:"voice"`
	APIKey       string            `json:"apiKey"`
	APIKeyAction host.APIKeyAction `json:"apiKeyAction"`
	Activate     bool              `json:"activate"`
}

type mediaModelView struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Kind        string `json:"kind"`
	Protocol    string `json:"protocol"`
	BaseURL     string `json:"baseURL"`
	Model       string `json:"model"`
	Description string `json:"description"`
	Voice       string `json:"voice"`
	HasAPIKey   bool   `json:"hasAPIKey"`
	APIKeyHint  string `json:"apiKeyHint"`
}

func newMediaModelConfig() mediaModelConfig {
	return mediaModelConfig{SchemaVersion: 1, Connections: []mediaModelConnection{}, Selected: map[string]string{"image": "", "video": "", "audio": ""}}
}

func (s *Server) mediaModelConfigFile(w http.ResponseWriter) (string, *sync.RWMutex, bool) {
	s.mu.Lock()
	configPath, closed := s.configPath, s.closed
	s.mu.Unlock()
	if closed {
		fail(w, http.StatusServiceUnavailable, "工作台正在关闭，请重新打开后操作")
		return "", nil, false
	}
	if strings.TrimSpace(configPath) == "" {
		fail(w, http.StatusInternalServerError, "无法确定媒体模型配置目录")
		return "", nil, false
	}
	path, err := filepath.Abs(filepath.Join(filepath.Dir(configPath), "media-models.json"))
	if err != nil {
		fail(w, http.StatusInternalServerError, "无法确定媒体模型配置目录")
		return "", nil, false
	}
	lock, _ := mediaModelConfigLocks.LoadOrStore(path, &sync.RWMutex{})
	return path, lock.(*sync.RWMutex), true
}

func (s *Server) getMediaModels(w http.ResponseWriter, _ *http.Request) {
	path, mu, ok := s.mediaModelConfigFile(w)
	if !ok {
		return
	}
	mu.RLock()
	defer mu.RUnlock()
	cfg, err := loadMediaModelConfig(path)
	if err != nil {
		fail(w, http.StatusInternalServerError, "媒体模型配置读取失败，请检查 media-models.json；现有文件未修改")
		return
	}
	replyMediaModels(w, cfg, "")
}

func (s *Server) saveMediaModel(w http.ResponseWriter, r *http.Request) {
	var req mediaModelRequest
	if !readJSONLimit(w, r, &req, 32<<10) {
		return
	}
	path, mu, ok := s.mediaModelConfigFile(w)
	if !ok {
		return
	}
	mu.Lock()
	defer mu.Unlock()
	cfg, err := loadMediaModelConfig(path)
	if err != nil {
		fail(w, http.StatusInternalServerError, "媒体模型配置读取失败，未覆盖现有配置")
		return
	}
	index := -1
	for i := range cfg.Connections {
		if cfg.Connections[i].ID == req.ID {
			index = i
			break
		}
	}
	if req.ID != "" && index < 0 {
		fail(w, http.StatusNotFound, "该媒体连接已不存在，请刷新配置")
		return
	}
	connection := mediaModelConnection{
		ID: req.ID, Name: strings.TrimSpace(req.Name), Kind: strings.TrimSpace(req.Kind),
		Protocol: strings.TrimSpace(req.Protocol), BaseURL: strings.TrimSpace(req.BaseURL),
		Model: strings.TrimSpace(req.Model), Description: strings.TrimSpace(req.Description), Voice: strings.TrimSpace(req.Voice),
	}
	if index >= 0 {
		connection.APIKey = cfg.Connections[index].APIKey
	} else {
		if len(cfg.Connections) >= 100 {
			fail(w, http.StatusBadRequest, "最多可保存 100 个媒体模型连接")
			return
		}
		var random [16]byte
		if _, err := rand.Read(random[:]); err != nil {
			fail(w, http.StatusInternalServerError, "暂时无法创建媒体连接，请重试")
			return
		}
		connection.ID = hex.EncodeToString(random[:])
	}
	switch req.APIKeyAction {
	case host.APIKeyKeep:
		// A draft field must never replace a saved key when keep is explicit.
	case host.APIKeyReplace:
		connection.APIKey = strings.TrimSpace(req.APIKey)
		if connection.APIKey == "" {
			fail(w, http.StatusBadRequest, "替换密钥时请填写 API Key；移除密钥请使用清空操作")
			return
		}
	case host.APIKeyClear:
		connection.APIKey = ""
	default:
		fail(w, http.StatusBadRequest, "请明确选择保留、替换或清空 API Key")
		return
	}
	if err := validateMediaModelConnection(connection); err != nil {
		fail(w, http.StatusBadRequest, err.Error())
		return
	}
	if index < 0 {
		cfg.Connections = append(cfg.Connections, connection)
	} else {
		previous := cfg.Connections[index]
		if previous.Kind != connection.Kind && cfg.Selected[previous.Kind] == connection.ID {
			cfg.Selected[previous.Kind] = ""
		}
		cfg.Connections[index] = connection
	}
	if req.Activate {
		cfg.Selected[connection.Kind] = connection.ID
	}
	if !persistMediaModelConfig(w, path, cfg) {
		return
	}
	replyMediaModels(w, cfg, connection.ID)
}

func (s *Server) selectMediaModel(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Kind string `json:"kind"`
		ID   string `json:"id"`
	}
	if !readJSONLimit(w, r, &req, 4<<10) {
		return
	}
	if !validMediaKind(req.Kind) {
		fail(w, http.StatusBadRequest, "媒体类型必须是 image、video 或 audio")
		return
	}
	path, mu, ok := s.mediaModelConfigFile(w)
	if !ok {
		return
	}
	mu.Lock()
	defer mu.Unlock()
	cfg, err := loadMediaModelConfig(path)
	if err != nil {
		fail(w, http.StatusInternalServerError, "媒体模型配置读取失败，未覆盖现有配置")
		return
	}
	if req.ID != "" {
		found := false
		for _, connection := range cfg.Connections {
			if connection.ID == req.ID && connection.Kind == req.Kind {
				found = true
				break
			}
		}
		if !found {
			fail(w, http.StatusBadRequest, "请选择该媒体类型下已保存的连接")
			return
		}
	}
	cfg.Selected[req.Kind] = req.ID
	if persistMediaModelConfig(w, path, cfg) {
		replyMediaModels(w, cfg, "")
	}
}

func (s *Server) deleteMediaModel(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !validMediaConnectionID(id) {
		fail(w, http.StatusBadRequest, "媒体连接标识无效")
		return
	}
	path, mu, ok := s.mediaModelConfigFile(w)
	if !ok {
		return
	}
	mu.Lock()
	defer mu.Unlock()
	cfg, err := loadMediaModelConfig(path)
	if err != nil {
		fail(w, http.StatusInternalServerError, "媒体模型配置读取失败，未覆盖现有配置")
		return
	}
	index := -1
	for i, connection := range cfg.Connections {
		if connection.ID == id {
			index = i
			break
		}
	}
	if index < 0 {
		fail(w, http.StatusNotFound, "媒体连接已不存在")
		return
	}
	cfg.Connections = append(cfg.Connections[:index], cfg.Connections[index+1:]...)
	for kind, selected := range cfg.Selected {
		if selected == id {
			cfg.Selected[kind] = ""
		}
	}
	if persistMediaModelConfig(w, path, cfg) {
		replyMediaModels(w, cfg, "")
	}
}

func validMediaKind(kind string) bool {
	return kind == "image" || kind == "video" || kind == "audio"
}

func validMediaConnectionID(id string) bool {
	if len(id) != 32 {
		return false
	}
	_, err := hex.DecodeString(id)
	return err == nil && strings.ToLower(id) == id
}

func validMediaText(value string, limit int, multiline bool) bool {
	if !utf8.ValidString(value) || utf8.RuneCountInString(value) > limit {
		return false
	}
	for _, r := range value {
		if unicode.IsControl(r) && !(multiline && (r == '\n' || r == '\t')) {
			return false
		}
	}
	return true
}

func validMediaURL(value string) bool {
	if value == "" || len(value) > 2048 || !validMediaText(value, 2048, false) || strings.ContainsAny(value, " \t\\") {
		return false
	}
	u, err := url.Parse(value)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.Opaque != "" {
		return false
	}
	if port := u.Port(); port != "" {
		n, err := strconv.Atoi(port)
		if err != nil || n < 1 || n > 65535 {
			return false
		}
	}
	return true
}

func validateMediaModelConnection(connection mediaModelConnection) error {
	if !validMediaConnectionID(connection.ID) {
		return errors.New("媒体连接标识无效")
	}
	if connection.Name == "" || !validMediaText(connection.Name, 80, false) {
		return errors.New("连接名称必填，且不能超过 80 字或包含控制字符")
	}
	if !validMediaKind(connection.Kind) {
		return errors.New("媒体类型必须是 image、video 或 audio")
	}
	if connection.Protocol != "openai-compatible" && connection.Protocol != "custom" {
		return errors.New("接入协议必须是 openai-compatible 或 custom")
	}
	if !validMediaURL(connection.BaseURL) {
		return errors.New("Base URL 必须是 HTTP/HTTPS 地址，且不能包含凭证、查询参数或片段")
	}
	if connection.Model == "" || !validMediaText(connection.Model, 200, false) {
		return errors.New("模型名称必填，且不能超过 200 字或包含控制字符")
	}
	if !validMediaText(connection.Description, 2000, true) {
		return errors.New("接入说明不能超过 2000 字或包含无效字符")
	}
	if !validMediaText(connection.Voice, 120, false) || (connection.Voice != "" && connection.Kind != "audio") {
		return errors.New("音色仅适用于语音连接，且不能超过 120 字或包含控制字符")
	}
	if !validMediaText(connection.APIKey, 4096, false) {
		return errors.New("API Key 不能超过 4096 字或包含控制字符")
	}
	return nil
}

func loadMediaModelConfig(path string) (mediaModelConfig, error) {
	cfg := newMediaModelConfig()
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return cfg, nil
	}
	if err != nil {
		return cfg, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, mediaModelConfigLimit+1))
	if err != nil || len(data) > mediaModelConfigLimit {
		return cfg, errors.New("media config could not be read")
	}
	d := json.NewDecoder(strings.NewReader(string(data)))
	d.DisallowUnknownFields()
	// Recognize the retired field only while reading old files. It is neither
	// exposed nor written back, and its value cannot affect connection use.
	legacy := struct {
		*mediaModelConfig
		CanvasURL json.RawMessage `json:"canvasURL"`
	}{mediaModelConfig: &cfg}
	if err := d.Decode(&legacy); err != nil {
		return cfg, errors.New("invalid media config")
	}
	if d.Decode(&struct{}{}) != io.EOF || cfg.SchemaVersion != 1 || len(cfg.Connections) > 100 {
		return cfg, errors.New("unsupported media config")
	}
	ids := make(map[string]string, len(cfg.Connections))
	for _, connection := range cfg.Connections {
		if err := validateMediaModelConnection(connection); err != nil {
			return cfg, err
		}
		if _, exists := ids[connection.ID]; exists {
			return cfg, errors.New("duplicate media connection")
		}
		ids[connection.ID] = connection.Kind
	}
	if cfg.Connections == nil {
		cfg.Connections = []mediaModelConnection{}
	}
	if cfg.Selected == nil {
		cfg.Selected = make(map[string]string)
	}
	for kind, id := range cfg.Selected {
		if !validMediaKind(kind) || (id != "" && ids[id] != kind) {
			return cfg, errors.New("invalid selected media connection")
		}
	}
	for _, kind := range []string{"image", "video", "audio"} {
		if _, found := cfg.Selected[kind]; !found {
			cfg.Selected[kind] = ""
		}
	}
	return cfg, nil
}

func persistMediaModelConfig(w http.ResponseWriter, path string, cfg mediaModelConfig) bool {
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil || len(data) > mediaModelConfigLimit {
		fail(w, http.StatusBadRequest, "媒体模型配置过大，请减少连接或说明内容")
		return false
	}
	if err := writeMediaModelConfig(path, append(data, '\n')); err != nil {
		// Filesystem errors are intentionally not echoed: neither saved nor draft
		// credentials should escape through provider names, paths or JSON errors.
		fail(w, http.StatusInternalServerError, "媒体模型配置保存失败，原配置未修改")
		return false
	}
	return true
}

func writeMediaModelConfig(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".media-models-*.tmp")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Chmod(0600); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return fileio.Rename(tmp, path)
}

func replyMediaModels(w http.ResponseWriter, cfg mediaModelConfig, savedID string) {
	secrets := make([]string, 0, len(cfg.Connections))
	for _, connection := range cfg.Connections {
		if connection.APIKey != "" {
			secrets = append(secrets, connection.APIKey)
		}
	}
	sort.Slice(secrets, func(i, j int) bool { return len(secrets[i]) > len(secrets[j]) })
	redact := func(value string) string {
		for _, secret := range secrets {
			value = strings.ReplaceAll(value, secret, "[redacted]")
		}
		return value
	}
	connections := make([]mediaModelView, 0, len(cfg.Connections))
	for _, c := range cfg.Connections {
		connections = append(connections, mediaModelView{
			ID: c.ID, Name: redact(c.Name), Kind: c.Kind, Protocol: c.Protocol,
			BaseURL: redact(c.BaseURL), Model: redact(c.Model), Description: redact(c.Description), Voice: redact(c.Voice),
			HasAPIKey: c.APIKey != "", APIKeyHint: host.MaskAPIKey(c.APIKey),
		})
	}
	reply(w, http.StatusOK, map[string]any{
		"connections": connections, "selected": cfg.Selected,
		"generationAvailable": true, "savedID": savedID,
	})
}
