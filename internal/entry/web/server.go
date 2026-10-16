// Package web exposes the existing Host through a local browser workbench.
package web

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/voocel/agentcore"
	"github.com/voocel/ainovel-cli/assets"
	"github.com/voocel/ainovel-cli/internal/bootstrap"
	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/entry/startup"
	"github.com/voocel/ainovel-cli/internal/host"
	"github.com/voocel/ainovel-cli/internal/host/exp"
	"github.com/voocel/ainovel-cli/internal/store"
)

//go:embed static/*
var staticFiles embed.FS

type Options struct {
	Listen, OutputDir string
	OpenBrowser       bool
}

type message struct {
	Type string `json:"type"`
	Data any    `json:"data"`
}

type Server struct {
	mu                                                 sync.Mutex
	cfg                                                bootstrap.Config
	rt                                                 *host.Host
	dir, homeDir, booksDir, configPath, hostError, job string
	busy, stopping, closed                             bool
	cancelJob                                          context.CancelFunc
	jobDone                                            chan struct{}
	ctx                                                context.Context
	cancel                                             context.CancelFunc
	events                                             []host.Event
	stream                                             string
	clients                                            map[chan []byte]struct{}
	wg                                                 sync.WaitGroup
}

// New leaves setup available even when credentials are absent or the book is locked.
func New(cfg bootstrap.Config, opts Options) (*Server, error) {
	cfg = bootstrap.CloneConfig(cfg)
	if opts.OutputDir != "" {
		cfg.OutputDir = opts.OutputDir
	}
	cfg.FillDefaults()
	dir, err := filepath.Abs(cfg.OutputDir)
	if err != nil {
		return nil, err
	}
	root, err := filepath.Abs(filepath.Join("output", "web-books"))
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	s := &Server{cfg: cfg, dir: dir, homeDir: dir, booksDir: root, configPath: bootstrap.EffectiveConfigPath(), ctx: ctx, cancel: cancel, clients: make(map[chan []byte]struct{})}
	s.cfg.OutputDir = dir
	s.mu.Lock()
	s.initLocked()
	s.mu.Unlock()
	return s, nil
}

func (s *Server) initLocked() {
	if s.rt != nil || s.closed {
		return
	}
	s.cfg.OutputDir = s.dir
	if err := s.cfg.ValidateBase(); err != nil {
		s.hostError = "请先配置模型服务，再开始创作。"
		return
	}
	rt, err := host.New(s.cfg, assets.Load(s.cfg.Style, assets.DefaultLoadOptions(s.dir)), host.WithFileLog("web.log", false))
	if err != nil {
		s.hostError = s.redactLocked(err.Error())
		return
	}
	if err := rt.EnableWorkbench(); err != nil {
		rt.Close()
		s.hostError = s.redactLocked(err.Error())
		return
	}
	s.rt, s.hostError = rt, ""
	s.wg.Add(1)
	go s.collect(rt)
}

func (s *Server) collect(rt *host.Host) {
	defer s.wg.Done()
	events, stream, done := rt.Events(), rt.Stream(), rt.Done()
	for events != nil || stream != nil {
		select {
		case <-s.ctx.Done():
			return
		case ev, ok := <-events:
			if !ok {
				events = nil
				continue
			}
			s.mu.Lock()
			if s.rt == rt {
				ev.Summary = s.redactLocked(ev.Summary)
				ev.Detail = s.redactLocked(ev.Detail)
				found := false
				if ev.ID != "" {
					for i := len(s.events) - 1; i >= 0; i-- {
						if s.events[i].ID == ev.ID {
							s.events[i] = ev
							found = true
							break
						}
					}
				}
				if !found {
					s.events = append(s.events, ev)
					if len(s.events) > 200 {
						s.events = s.events[len(s.events)-200:]
					}
				}
				s.broadcastLocked("event", ev)
			}
			s.mu.Unlock()
		case delta, ok := <-stream:
			if !ok {
				stream = nil
				continue
			}
			s.mu.Lock()
			if s.rt == rt {
				if delta == host.StreamClearSentinel {
					s.stream = ""
					s.broadcastLocked("clear", nil)
				} else {
					s.stream += delta
					if len(s.stream) > 200000 {
						s.stream = s.stream[len(s.stream)-180000:]
						for !utf8.ValidString(s.stream) && len(s.stream) > 0 {
							s.stream = s.stream[1:]
						}
					}
					s.broadcastLocked("delta", delta)
				}
			}
			s.mu.Unlock()
		case _, ok := <-done:
			if !ok {
				done = nil
			}
			s.mu.Lock()
			if s.rt == rt {
				s.broadcastLocked("refresh", nil)
			}
			s.mu.Unlock()
		}
	}
}

func (s *Server) redactLocked(text string) string {
	for _, p := range s.cfg.Providers {
		if p.APIKey != "" {
			text = strings.ReplaceAll(text, p.APIKey, "[已隐藏]")
		}
	}
	return text
}

func (s *Server) broadcastLocked(kind string, data any) {
	b, err := json.Marshal(message{kind, data})
	if err != nil {
		return
	}
	for c := range s.clients {
		select {
		case c <- b:
		default:
			close(c)
			delete(s.clients, c)
		}
	}
}

func (s *Server) Close() {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	s.cancel()
	if s.cancelJob != nil {
		s.cancelJob()
	}
	rt := s.rt
	s.rt = nil
	for c := range s.clients {
		close(c)
		delete(s.clients, c)
	}
	s.mu.Unlock()
	if rt != nil {
		rt.Close()
	}
	s.wg.Wait()
}

func Run(opts Options) error {
	if opts.Listen == "" {
		opts.Listen = "127.0.0.1:17680"
	}
	h, _, err := net.SplitHostPort(opts.Listen)
	if err != nil {
		return fmt.Errorf("监听地址无效: %w", err)
	}
	if h != "localhost" && (net.ParseIP(h) == nil || !net.ParseIP(h).IsLoopback()) {
		return errors.New("Web 工作台仅支持 localhost / 回环地址")
	}
	ln, err := net.Listen("tcp", opts.Listen)
	if err != nil {
		return fmt.Errorf("无法监听 %s（端口可能已被其他程序占用，请使用 --listen 更换端口）: %w", opts.Listen, err)
	}
	defer ln.Close()
	cfg, err := bootstrap.LoadConfig()
	if err != nil {
		return err
	}
	s, err := New(cfg, opts)
	if err != nil {
		return err
	}
	defer s.Close()
	httpServer := &http.Server{Handler: s.Handler(), ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 32 << 10}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	go func() {
		<-ctx.Done()
		s.cancel()
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = httpServer.Shutdown(shutdown)
	}()
	webURL := fmt.Sprintf("http://%s/?ainovel_start=%d", ln.Addr().String(), time.Now().UnixNano())
	fmt.Printf("小说 Web 工作台：%s\n作品目录：%s\n按 Ctrl+C 停止服务。\n", webURL, s.dir)
	if opts.OpenBrowser {
		go func() {
			if err := openBrowser(webURL); err != nil {
				fmt.Fprintf(os.Stderr, "无法自动打开浏览器，请手动打开上方地址：%v\n", err)
			}
		}()
	}
	err = httpServer.Serve(ln)
	stop()
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

func openBrowser(address string) error {
	switch runtime.GOOS {
	case "windows":
		return exec.Command("rundll32.exe", "url.dll,FileProtocolHandler", address).Run()
	case "darwin":
		return exec.Command("open", address).Run()
	default:
		return exec.Command("xdg-open", address).Run()
	}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/state", s.state)
	mux.HandleFunc("GET /api/config", s.getConfig)
	mux.HandleFunc("POST /api/config", s.saveConfig)
	mux.HandleFunc("POST /api/config/test", s.testConfig)
	mux.HandleFunc("POST /api/action", s.action)
	mux.HandleFunc("GET /api/events", s.subscribe)
	mux.HandleFunc("GET /api/chapters/{chapter}", s.chapter)
	mux.HandleFunc("POST /api/export", s.export)
	mux.HandleFunc("GET /api/workspaces", s.workspaces)
	mux.HandleFunc("POST /api/workspaces", s.switchWorkspace)
	mux.HandleFunc("GET /api/planning", s.planning)
	mux.HandleFunc("POST /api/project", s.updateProject)
	files, _ := fs.Sub(staticFiles, "static")
	mux.Handle("/", http.FileServer(http.FS(files)))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
		w.Header().Set("Cache-Control", "no-store")
		requestHost, _, err := net.SplitHostPort(r.Host)
		if err != nil {
			requestHost = r.Host
		}
		ip := net.ParseIP(requestHost)
		if requestHost != "localhost" && (ip == nil || !ip.IsLoopback()) {
			fail(w, http.StatusForbidden, "仅允许本机访问")
			return
		}
		if origin := r.Header.Get("Origin"); origin != "" {
			u, err := url.Parse(origin)
			if err != nil || u.Scheme != "http" || u.Host != r.Host {
				fail(w, http.StatusForbidden, "请求来源不匹配")
				return
			}
		}
		if r.Header.Get("Sec-Fetch-Site") == "cross-site" {
			fail(w, http.StatusForbidden, "不接受跨站请求")
			return
		}
		if r.Method == http.MethodPost && !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
			fail(w, http.StatusUnsupportedMediaType, "请提交 JSON 数据")
			return
		}
		mux.ServeHTTP(w, r)
	})
}

func readJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 2<<20)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if err := d.Decode(v); err != nil {
		fail(w, 400, "请求内容无效或过大")
		return false
	}
	if d.Decode(&struct{}{}) != io.EOF {
		fail(w, 400, "只能提交一个 JSON 对象")
		return false
	}
	return true
}
func reply(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
func fail(w http.ResponseWriter, status int, msg string) {
	reply(w, status, map[string]string{"error": msg})
}

func (s *Server) state(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var snap host.UISnapshot
	if s.rt != nil {
		snap = s.rt.Snapshot()
	}
	st := store.NewStore(s.dir)
	p, err := st.Progress.Load()
	if s.rt == nil {
		if b, e := st.Book.Load(); e == nil && b != nil {
			snap.BookTitle = b.Title
		}
		if o, e := st.Outline.LoadOutline(); e == nil {
			for _, entry := range o {
				snap.Outline = append(snap.Outline, host.OutlineSnapshot{Chapter: entry.Chapter, Title: entry.Title, CoreEvent: entry.CoreEvent})
			}
		}
		if p != nil {
			snap.Phase = string(p.Phase)
			snap.CurrentChapter = p.CurrentChapter
			snap.CompletedCount = len(p.CompletedChapters)
			snap.TotalChapters = p.TotalChapters
			snap.TotalWordCount = p.TotalWordCount
		}
	}
	chapters := []map[string]any{}
	if err == nil && p != nil {
		for _, n := range p.CompletedChapters {
			title := fmt.Sprintf("第 %d 章", n)
			if sum, e := st.Summaries.LoadSummary(n); e == nil && sum != nil && sum.Title != "" {
				title = sum.Title
			}
			chapters = append(chapters, map[string]any{"number": n, "title": title, "words": p.ChapterWordCounts[n]})
		}
		if p.InProgressChapter > 0 {
			if draft, e := st.Drafts.LoadDraft(p.InProgressChapter); e == nil && draft != "" {
				chapters = append(chapters, map[string]any{"number": p.InProgressChapter, "title": fmt.Sprintf("第 %d 章 · 草稿", p.InProgressChapter), "words": domain.WordCount(draft), "source": "draft"})
			}
		}
	}
	project, projectErr := readProject(s.dir)
	workflow, workflowErr := workflowView(st)
	if projectErr != nil {
		project.Name = "项目资料读取失败"
	}
	if workflowErr != nil {
		workflow["error"] = workflowErr.Error()
	}
	reply(w, 200, map[string]any{"ready": s.rt != nil, "error": s.hostError, "busy": s.busy, "job": s.job, "stopping": s.stopping, "directory": s.dir, "snapshot": snap, "chapters": chapters, "project": project, "workflow": workflow})
}

func (s *Server) getConfig(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	type providerView struct {
		Name       string   `json:"name"`
		Type       string   `json:"type,omitempty"`
		API        string   `json:"api,omitempty"`
		BaseURL    string   `json:"base_url,omitempty"`
		Models     []string `json:"models"`
		HasAPIKey  bool     `json:"has_api_key"`
		APIKeyHint string   `json:"api_key_hint,omitempty"`
	}
	providers := make([]providerView, 0, len(s.cfg.Providers))
	for name, p := range s.cfg.Providers {
		models := make([]string, 0, len(p.Models))
		for _, m := range p.Models {
			models = append(models, m.Name)
		}
		protocol, _ := p.ProviderType(name)
		providers = append(providers, providerView{Name: name, Type: protocol, API: p.API, BaseURL: p.BaseURL, Models: models, HasAPIKey: p.APIKey != "", APIKeyHint: host.MaskAPIKey(p.APIKey)})
	}
	sort.Slice(providers, func(i, j int) bool { return providers[i].Name < providers[j].Name })
	reply(w, 200, map[string]any{"provider": s.cfg.Provider, "model": s.cfg.ModelName, "providers": providers, "config_path": s.configPath})
}

func (s *Server) saveConfig(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Provider string   `json:"provider"`
		Model    string   `json:"model"`
		Name     string   `json:"name"`
		Type     string   `json:"type"`
		API      string   `json:"api"`
		BaseURL  string   `json:"base_url"`
		APIKey   string   `json:"api_key"`
		Models   []string `json:"models"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.busy || s.stopping || (s.rt != nil && s.rt.Snapshot().IsRunning) {
		fail(w, 409, "请先暂停创作后再修改配置")
		return
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		name = strings.TrimSpace(req.Provider)
	}
	if name == "" || strings.TrimSpace(req.Model) == "" {
		fail(w, 400, "Provider 名称和默认模型不能为空")
		return
	}
	candidate := bootstrap.CloneConfig(s.cfg)
	candidate.Provider = strings.TrimSpace(req.Provider)
	if candidate.Provider == "" {
		candidate.Provider = name
	}
	candidate.ModelName = strings.TrimSpace(req.Model)
	if candidate.Providers == nil {
		candidate.Providers = map[string]bootstrap.ProviderConfig{}
	}
	pc := candidate.Providers[name]
	pc.Type = strings.TrimSpace(req.Type)
	pc.API = strings.TrimSpace(req.API)
	pc.BaseURL = strings.TrimSpace(req.BaseURL)
	if strings.TrimSpace(req.APIKey) != "" {
		pc.APIKey = req.APIKey
	}
	// Keep existing model capabilities and role-specific configuration.
	if len(req.Models) > 0 {
		old := pc.Models
		pc.Models = nil
		for _, m := range req.Models {
			m = strings.TrimSpace(m)
			if m == "" {
				continue
			}
			mc := bootstrap.ModelConfig{Name: m}
			for _, prev := range old {
				if prev.Name == m {
					mc = prev
					break
				}
			}
			pc.Models = append(pc.Models, mc)
		}
	}
	found := false
	for _, m := range pc.Models {
		if m.Name == candidate.ModelName {
			found = true
		}
	}
	if !found {
		pc.Models = append(pc.Models, bootstrap.ModelConfig{Name: candidate.ModelName})
	}
	candidate.Providers[name] = pc
	candidate.FillDefaults()
	if err := candidate.ValidateBase(); err != nil {
		fail(w, 400, redactConfig(candidate, err.Error()))
		return
	}
	if _, err := bootstrap.NewModelSet(candidate); err != nil {
		fail(w, 400, redactConfig(candidate, err.Error()))
		return
	}
	if err := bootstrap.SaveConfig(s.configPath, candidate); err != nil {
		fail(w, 500, "保存配置失败")
		return
	}
	if s.rt != nil {
		s.rt.Close()
		s.rt = nil
	}
	s.cfg = candidate
	s.dir = candidate.OutputDir
	s.events = nil
	s.stream = ""
	s.hostError = ""
	s.initLocked()
	s.broadcastLocked("workspace", nil)
	reply(w, 200, map[string]any{"ok": true, "error": s.hostError})
}

func (s *Server) testConfig(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Provider string `json:"provider"`
		Model    string `json:"model"`
		Name     string `json:"name"`
		Type     string `json:"type"`
		API      string `json:"api"`
		BaseURL  string `json:"base_url"`
		APIKey   string `json:"api_key"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	s.mu.Lock()
	candidate := bootstrap.CloneConfig(s.cfg)
	s.mu.Unlock()
	name := strings.TrimSpace(req.Name)
	if name == "" {
		name = strings.TrimSpace(req.Provider)
	}
	if name == "" || strings.TrimSpace(req.Model) == "" {
		fail(w, 400, "Provider 和模型不能为空")
		return
	}
	if candidate.Providers == nil {
		candidate.Providers = map[string]bootstrap.ProviderConfig{}
	}
	pc := candidate.Providers[name]
	pc.Type = strings.TrimSpace(req.Type)
	pc.API = strings.TrimSpace(req.API)
	pc.BaseURL = strings.TrimSpace(req.BaseURL)
	if strings.TrimSpace(req.APIKey) != "" {
		pc.APIKey = req.APIKey
	}
	pc.Models = []bootstrap.ModelConfig{{Name: strings.TrimSpace(req.Model)}}
	candidate.Providers[name] = pc
	candidate.Provider = name
	candidate.ModelName = strings.TrimSpace(req.Model)
	candidate.Roles = nil
	candidate.FillDefaults()
	if err := candidate.ValidateBase(); err != nil {
		fail(w, 400, redactConfig(candidate, err.Error()))
		return
	}
	models, err := bootstrap.NewModelSet(candidate)
	if err != nil {
		fail(w, 400, "创建模型失败: "+redactConfig(candidate, err.Error()))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	if _, err = models.Default.Generate(ctx, []agentcore.Message{agentcore.UserMsg("Reply OK.")}, nil); err != nil {
		fail(w, 502, "连接测试失败: "+redactConfig(candidate, err.Error()))
		return
	}
	reply(w, 200, map[string]bool{"ok": true})
}

func redactConfig(cfg bootstrap.Config, text string) string {
	for _, p := range cfg.Providers {
		if p.APIKey != "" {
			text = strings.ReplaceAll(text, p.APIKey, "[已隐藏]")
		}
	}
	return text
}

func (s *Server) subscribe(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		fail(w, 500, "不支持流式连接")
		return
	}
	c := make(chan []byte, 128)
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.clients[c] = struct{}{}
	initial, _ := json.Marshal(message{"history", map[string]any{"events": s.events, "stream": s.stream}})
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		if _, ok := s.clients[c]; ok {
			delete(s.clients, c)
			close(c)
		}
		s.mu.Unlock()
	}()
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("X-Accel-Buffering", "no")
	rc := http.NewResponseController(w)
	send := func(b []byte) bool {
		_ = rc.SetWriteDeadline(time.Now().Add(10 * time.Second))
		_, err := fmt.Fprintf(w, "data: %s\n\n", b)
		flusher.Flush()
		return err == nil
	}
	if !send(initial) {
		return
	}
	tick := time.NewTicker(15 * time.Second)
	defer tick.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-s.ctx.Done():
			return
		case b, ok := <-c:
			if !ok || !send(b) {
				return
			}
		case <-tick.C:
			if !send([]byte(`{"type":"ping"}`)) {
				return
			}
		}
	}
}

func (s *Server) action(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Action, Text, Mode, Directory, Fingerprint string
		Count, Chapter                             int
	}
	if !readJSON(w, r, &req) {
		return
	}
	allowed := map[string]bool{"start": true, "resume": true, "pause": true, "next": true, "review": true, "steer": true, "continue": true, "sync": true, "reopen": true, "retry": true, "approve-plan": true, "revise-plan": true, "batch": true, "accept-chapter": true, "chapter-feedback": true}
	if !allowed[req.Action] {
		fail(w, 400, "未知操作")
		return
	}
	if (req.Action == "start" || req.Action == "steer" || req.Action == "continue" || req.Action == "reopen") && strings.TrimSpace(req.Text) == "" {
		fail(w, 400, "请填写创作需求或修改意见")
		return
	}
	if req.Action == "review" && !domain.ChapterAdvanceMode(req.Mode).Valid() {
		fail(w, 400, "无效推进模式")
		return
	}
	s.mu.Lock()
	if req.Directory != "" && req.Directory != s.dir {
		s.mu.Unlock()
		fail(w, 409, "当前项目已切换，请刷新后重试")
		return
	}
	if s.closed || s.stopping {
		s.mu.Unlock()
		fail(w, 409, "工作台正在停止，请稍后重试")
		return
	}
	if req.Action == "retry" {
		if s.busy {
			s.mu.Unlock()
			fail(w, 409, "请等待当前操作结束")
			return
		}
		s.initLocked()
		err := s.hostError
		s.mu.Unlock()
		if err != "" {
			fail(w, 409, err)
		} else {
			reply(w, 200, map[string]bool{"ok": true})
		}
		return
	}
	if req.Action == "resume" && s.rt == nil {
		s.initLocked()
	}
	rt := s.rt
	if rt == nil {
		s.mu.Unlock()
		fail(w, 409, "请先配置模型，或解除作品目录占用后重新连接")
		return
	}
	if req.Action == "pause" {
		s.stopping = true
		if s.cancelJob != nil {
			s.cancelJob()
		}
		jobDone := s.jobDone
		s.wg.Add(1)
		s.mu.Unlock()
		rt.Abort()
		go func() {
			defer s.wg.Done()
			rt.Close()
			if jobDone != nil {
				<-jobDone
			}
			s.mu.Lock()
			if s.rt == rt {
				s.rt = nil
				s.hostError = "已暂停，请点击重新连接以恢复工作台。"
			}
			s.stopping = false
			s.broadcastLocked("refresh", nil)
			s.mu.Unlock()
		}()
		reply(w, 202, map[string]bool{"ok": true})
		return
	}
	if s.busy || s.stopping {
		s.mu.Unlock()
		fail(w, 409, "正在处理操作，请稍后再试")
		return
	}
	if req.Action == "start" && rt.Snapshot().IsRunning {
		s.mu.Unlock()
		fail(w, 409, "作品正在创作")
		return
	}
	if req.Action == "start" {
		meta, e := store.NewStore(s.dir).RunMeta.Load()
		if e != nil || (meta != nil && meta.StartPrompt != "") {
			s.mu.Unlock()
			fail(w, 409, "此项目已有创作记录，请恢复创作或新建项目")
			return
		}
	}
	if req.Action == "review" || req.Action == "next" {
		s.mu.Unlock()
		fail(w, 409, "请使用大纲确认、正文批次和章节验收操作")
		return
	}
	s.busy = true
	s.job = req.Action
	ctx, cancel := context.WithCancel(s.ctx)
	s.cancelJob = cancel
	jobDone := make(chan struct{})
	s.jobDone = jobDone
	s.wg.Add(1)
	s.mu.Unlock()
	go func() {
		defer s.wg.Done()
		defer close(jobDone)
		defer cancel()
		var err error
		switch req.Action {
		case "approve-plan":
			err = rt.ApprovePlanning(req.Fingerprint)
		case "batch":
			err = rt.BeginWorkbenchBatch(req.Count)
		case "accept-chapter":
			err = rt.AcceptWorkbenchChapter(req.Chapter)
		case "revise-plan":
			err = rt.RecordWorkbenchFeedback("planning", 0, req.Text)
			if err == nil {
				err = rt.Steer("用户要求修改大纲和目录规划，必须实际更新落盘规划并完成基础审查；不要生成正文，修改后等待用户重新确认。修改意见：" + req.Text)
			}
		case "chapter-feedback":
			err = rt.RecordWorkbenchFeedback("chapter", req.Chapter, req.Text)
			if err == nil {
				_, err = rt.Resume()
			}
		case "start":
			var prompt string
			prompt, err = startup.PrepareQuick(req.Text)
			if err == nil {
				err = rt.PrepareUserRules(prompt)
			}
			if err == nil && ctx.Err() == nil {
				err = rt.StartPrepared(prompt)
			}
		case "resume":
			var label string
			label, err = rt.Resume()
			if err == nil && label == "" {
				err = errors.New("当前没有可恢复任务，请先填写创作需求")
			}
		case "next":
			err = rt.AdvanceOneChapter()
		case "review":
			err = rt.SetAdvanceMode(domain.ChapterAdvanceMode(req.Mode))
		case "steer":
			err = rt.Steer(req.Text)
		case "continue":
			err = rt.Continue(req.Text)
		case "sync":
			_, err = rt.SyncChapterRevisions(ctx)
		case "reopen":
			err = rt.Reopen(req.Text)
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		s.busy = false
		s.job = ""
		s.cancelJob = nil
		s.jobDone = nil
		if ctx.Err() != nil {
			err = ctx.Err()
		}
		result := map[string]any{"action": req.Action, "ok": err == nil}
		if err != nil {
			result["error"] = s.redactLocked(err.Error())
		}
		s.broadcastLocked("result", result)
	}()
	reply(w, 202, map[string]bool{"ok": true})
}

func (s *Server) chapter(w http.ResponseWriter, r *http.Request) {
	n, err := strconv.Atoi(r.PathValue("chapter"))
	if err != nil || n <= 0 {
		fail(w, 400, "无效章节")
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if d := r.URL.Query().Get("directory"); d != "" && d != s.dir {
		fail(w, 409, "项目已切换")
		return
	}
	st := store.NewStore(s.dir)
	var text string
	if r.URL.Query().Get("source") == "draft" {
		text, err = st.Drafts.LoadDraft(n)
	} else {
		text, err = st.Drafts.LoadChapterText(n)
	}
	if err != nil || text == "" {
		fail(w, 404, "该章节尚未生成")
		return
	}
	sum, _ := st.Summaries.LoadSummary(n)
	review, _ := st.World.LoadReview(n)
	reply(w, 200, map[string]any{"number": n, "content": text, "summary": sum, "review": review})
}

func (s *Server) export(w http.ResponseWriter, r *http.Request) {
	var req struct{ Format string }
	if !readJSON(w, r, &req) {
		return
	}
	if req.Format != "txt" && req.Format != "epub" {
		fail(w, 400, "仅支持 TXT / EPUB")
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.rt == nil || s.closed {
		fail(w, 409, "请先连接作品")
		return
	}
	tmp, err := os.MkdirTemp("", "ainovel-export-*")
	if err != nil {
		fail(w, 500, "无法创建导出文件")
		return
	}
	defer os.RemoveAll(tmp)
	out := filepath.Join(tmp, "novel."+req.Format)
	_, err = s.rt.Export(r.Context(), exp.Options{Format: exp.Format(req.Format), OutPath: out})
	if err != nil {
		fail(w, 409, s.redactLocked(err.Error()))
		return
	}
	data, err := os.ReadFile(out)
	if err != nil {
		fail(w, 500, "读取导出失败")
		return
	}
	w.Header().Set("Content-Disposition", `attachment; filename="novel.`+req.Format+`"`)
	if req.Format == "epub" {
		w.Header().Set("Content-Type", "application/epub+zip")
	} else {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	}
	_, _ = w.Write(data)
}

func (s *Server) workspaces(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	dirs := map[string]bool{s.dir: true, s.homeDir: true}
	legacy, _ := filepath.Abs(filepath.Join("output", "novel"))
	if st, e := os.Stat(legacy); e == nil && st.IsDir() {
		dirs[legacy] = true
	}
	entries, _ := os.ReadDir(s.booksDir)
	for _, e := range entries {
		if e.IsDir() && e.Type()&os.ModeSymlink == 0 {
			dirs[filepath.Join(s.booksDir, e.Name())] = true
		}
	}
	list := []map[string]any{}
	for dir := range dirs {
		title := filepath.Base(dir)
		st := store.NewStore(dir)
		if b, err := st.Book.Load(); err == nil && b != nil && b.Title != "" {
			title = b.Title
		}
		project, _ := readProject(dir)
		if project.Name != "" {
			title = project.Name
		}
		completed, words := 0, 0
		if p, e := st.Progress.Load(); e == nil && p != nil {
			completed = len(p.CompletedChapters)
			words = p.TotalWordCount
		}
		status := "paused"
		if dir == s.dir && (s.busy || s.stopping || (s.rt != nil && s.rt.Snapshot().IsRunning)) {
			status = "running"
		}
		workflow, _ := workflowView(st)
		list = append(list, map[string]any{"path": dir, "title": title, "completed": completed, "words": words, "status": status, "stage": workflow["stage"], "createdAt": project.CreatedAt})
	}
	sort.Slice(list, func(i, j int) bool { return list[i]["path"].(string) < list[j]["path"].(string) })
	reply(w, 200, list)
}

func (s *Server) switchWorkspace(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Path   string
		Name   string
		Create bool
	}
	if !readJSON(w, r, &req) {
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	if req.Create && req.Name != "" && !validProjectName(req.Name) {
		fail(w, 400, "请输入 1–80 字的项目名称")
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.busy || s.stopping || s.closed || (s.rt != nil && s.rt.Snapshot().IsRunning) {
		fail(w, 409, "请先暂停并等待当前操作结束")
		return
	}
	target := req.Path
	if req.Create {
		if err := os.MkdirAll(s.booksDir, 0700); err != nil {
			fail(w, 500, "无法创建项目目录")
			return
		}
		var err error
		target, err = os.MkdirTemp(s.booksDir, time.Now().Format("20060102-150405")+"-*")
		if err != nil {
			fail(w, 500, "无法创建项目目录")
			return
		}
	} else {
		abs, err := filepath.Abs(target)
		if err != nil {
			fail(w, 400, "无效作品目录")
			return
		}
		target = abs
		legacy, _ := filepath.Abs(filepath.Join("output", "novel"))
		rel, err := filepath.Rel(s.booksDir, target)
		if target != s.dir && target != s.homeDir && target != legacy && (err != nil || rel == "." || strings.ContainsAny(rel, "/\\") || rel == "..") {
			fail(w, 400, "只能打开工作台作品目录")
			return
		}
		st, err := os.Lstat(target)
		if err != nil || !st.IsDir() || st.Mode()&os.ModeSymlink != 0 {
			fail(w, 400, "作品目录不存在或是链接")
			return
		}
	}
	if req.Create {
		if req.Name == "" {
			req.Name = "未命名项目 " + time.Now().Format("01-02 15:04:05")
		}
		if err := saveProject(target, projectInfo{Name: req.Name, CreatedAt: time.Now().Format(time.RFC3339)}); err != nil {
			fail(w, 500, "无法创建作品目录")
			return
		}
	}
	if s.rt != nil {
		rt := s.rt
		s.rt = nil
		s.stopping = true
		s.mu.Unlock()
		rt.Close()
		s.mu.Lock()
		s.stopping = false
		if s.closed {
			fail(w, 409, "工作台已关闭")
			return
		}
	}
	s.dir = target
	s.cfg.OutputDir = target
	s.events = nil
	s.stream = ""
	s.initLocked()
	s.broadcastLocked("workspace", nil)
	reply(w, 200, map[string]any{"ok": true, "directory": s.dir, "error": s.hostError})
}
