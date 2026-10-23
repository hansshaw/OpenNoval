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
	"mime"
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
	"github.com/voocel/ainovel-cli/internal/host"
	"github.com/voocel/ainovel-cli/internal/host/exp"
	"github.com/voocel/ainovel-cli/internal/projectjobs"
	"github.com/voocel/ainovel-cli/internal/projectversion"
	"github.com/voocel/ainovel-cli/internal/store"
)

//go:embed static/*
var staticFiles embed.FS

type Options struct {
	Listen, OutputDir    string
	OpenBrowser          bool
	DesktopShutdownToken string
}

type message struct {
	Type string `json:"type"`
	Data any    `json:"data"`
}

type Server struct {
	background                                         *projectjobs.Scheduler
	projectRuntimes                                    map[string]*projectRuntime
	runSlots                                           map[*host.Host]func()
	closeOnce                                          sync.Once
	desktopShutdownOnce                                sync.Once
	desktopShutdownToken                               string
	desktopShutdownRequested                           chan struct{}
	mu                                                 sync.Mutex
	baseCfg                                            bootstrap.Config
	cfg                                                bootstrap.Config `json:"-"`
	rt                                                 *host.Host
	dir, homeDir, booksDir, configPath, hostError, job string
	busy, stopping, closed                             bool
	cancelJob                                          context.CancelFunc
	jobDone                                            chan struct{}
	styleCancel                                        context.CancelFunc
	styleDone                                          chan struct{}
	styleDirectory                                     string
	styleEntryID                                       string
	styleBusy                                          bool
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
	s := &Server{baseCfg: cfg, cfg: cfg, dir: dir, homeDir: dir, booksDir: root, configPath: bootstrap.EffectiveConfigPath(), ctx: ctx, cancel: cancel, clients: make(map[chan []byte]struct{})}
	s.desktopShutdownToken = opts.DesktopShutdownToken
	s.desktopShutdownRequested = make(chan struct{})
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
	project, err := readProject(s.dir)
	if err != nil {
		s.hostError = "项目资料读取失败：" + s.redactLocked(err.Error())
		return
	}
	cfg, rt, hostErr, err := s.buildHostLocked(s.dir, project)
	if err != nil {
		s.hostError = s.redactLocked(err.Error())
		return
	}
	s.cfg, s.hostError = cfg, hostErr
	if rt == nil {
		return
	}
	s.rt = rt
	s.wg.Add(1)
	go s.collect(rt)
}

func (s *Server) buildHostLocked(target string, project projectInfo) (bootstrap.Config, *host.Host, string, error) {
	if pending, err := projectversion.HasPendingRecovery(target); err != nil && !errors.Is(err, os.ErrNotExist) {
		return bootstrap.Config{}, nil, "", err
	} else if pending {
		if err := withWorkspaceLease(target, func() error {
			_, err := projectversion.Recover(s.ctx, target)
			return err
		}); err != nil {
			return bootstrap.Config{}, nil, "", err
		}
	}
	if err := s.baseCfg.ValidateBase(); err != nil {
		cfg := bootstrap.CloneConfig(s.baseCfg)
		cfg.OutputDir = target
		return cfg, nil, "请先配置模型服务，再开始创作。", nil
	}
	cfg := bootstrap.CloneConfig(s.baseCfg)
	cfg.OutputDir = target
	cfg.StyleReferenceProfiles = s.styleReferenceProvider(target)
	if project.Archived {
		return cfg, nil, "项目已归档，请先取消归档后再打开。", nil
	}
	if project.StylePreset != "" {
		cfg.Style = project.StylePreset
	}
	applyProjectRoleConfigs(&cfg, project.RoleModels)
	if cfg.RoleDirectives == nil {
		cfg.RoleDirectives = make(map[string]string)
	}
	for role, directive := range project.RoleDirectives {
		cfg.RoleDirectives[role] = directive
	}
	if err := cfg.ValidateBase(); err != nil {
		return cfg, nil, s.redactLocked(err.Error()), nil
	}
	bundle := assets.Load(cfg.Style, assets.DefaultLoadOptions(target))
	if _, ok := bundle.Styles[cfg.Style]; !ok {
		return cfg, nil, "项目风格不可用，请重新编辑项目风格。", nil
	}
	cfg.AcquireModelRequest = s.acquireCreationModel
	// A process can host multiple projects. The desktop captures process logs;
	// each Host's structured events stay with its own workbench or job journal.
	rt, err := host.New(cfg, bundle)
	if err != nil {
		return cfg, nil, "", err
	}
	if err := rt.EnableWorkbench(); err != nil {
		rt.Close()
		return cfg, nil, "", err
	}
	return cfg, rt, "", nil
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
			current := s.rt == rt
			var buffer *[]host.Event
			if current {
				buffer = &s.events
			} else {
				for _, view := range s.projectRuntimes {
					if view.rt == rt {
						buffer = &view.events
						break
					}
				}
			}
			if buffer != nil {
				ev.Summary, ev.Detail = s.redactLocked(ev.Summary), s.redactLocked(ev.Detail)
				found := false
				if ev.ID != "" {
					for i := len(*buffer) - 1; i >= 0; i-- {
						if (*buffer)[i].ID == ev.ID {
							(*buffer)[i] = ev
							found = true
							break
						}
					}
				}
				if !found {
					*buffer = append(*buffer, ev)
					if len(*buffer) > 200 {
						*buffer = (*buffer)[len(*buffer)-200:]
					}
				}
				if current {
					s.broadcastLocked("event", ev)
				}
			}
			s.mu.Unlock()
		case delta, ok := <-stream:
			if !ok {
				stream = nil
				continue
			}
			s.mu.Lock()
			current := s.rt == rt
			var buffer *string
			if current {
				buffer = &s.stream
			} else {
				for _, view := range s.projectRuntimes {
					if view.rt == rt {
						buffer = &view.stream
						break
					}
				}
			}
			if buffer != nil {
				if delta == host.StreamClearSentinel {
					*buffer = ""
					if current {
						s.broadcastLocked("clear", nil)
					}
				} else {
					*buffer += delta
					if len(*buffer) > 200000 {
						*buffer = (*buffer)[len(*buffer)-180000:]
						for !utf8.ValidString(*buffer) && len(*buffer) > 0 {
							*buffer = (*buffer)[1:]
						}
					}
					if current {
						s.broadcastLocked("delta", delta)
					}
				}
			}
			s.mu.Unlock()
		case _, ok := <-done:
			if !ok {
				done = nil
			}
			s.mu.Lock()
			s.releaseIdleProjectRunLocked(rt)
			s.broadcastLocked("refresh", nil)
			s.mu.Unlock()
		}
	}
}

func (s *Server) redactLocked(text string) string {
	return redactConfig(s.baseCfg, text)
}

func (s *Server) projectConfigLocked() (bootstrap.Config, error) {
	cfg := bootstrap.CloneConfig(s.baseCfg)
	cfg.OutputDir = s.dir
	project, err := readProject(s.dir)
	if err != nil {
		return cfg, err
	}
	if project.StylePreset != "" {
		cfg.Style = project.StylePreset
	}
	applyProjectRoleConfigs(&cfg, project.RoleModels)
	if cfg.RoleDirectives == nil {
		cfg.RoleDirectives = make(map[string]string)
	}
	for role, directive := range project.RoleDirectives {
		cfg.RoleDirectives[role] = directive
	}
	var invalid []string
	for role, rc := range cfg.Roles {
		if _, ok := cfg.Providers[rc.Provider]; !ok {
			invalid = append(invalid, fmt.Sprintf("%s 的 Provider %s 已不存在", role, rc.Provider))
			continue
		}
		found := false
		for _, model := range cfg.Providers[rc.Provider].Models {
			if model.Name == rc.Model {
				found = true
				break
			}
		}
		if !found {
			invalid = append(invalid, fmt.Sprintf("%s 的模型 %s/%s 已不存在", role, rc.Provider, rc.Model))
		}
	}
	if len(invalid) > 0 {
		sort.Strings(invalid)
		return cfg, fmt.Errorf("%s；请编辑项目角色模型", strings.Join(invalid, "，"))
	}
	return cfg, nil
}

func (s *Server) replaceHostLocked(candidate bootstrap.Config) {
	s.cfg = candidate
	if s.rt != nil {
		s.rt.Close()
		s.rt = nil
	}
	s.events = nil
	s.stream = ""
	s.hostError = ""
	s.initLocked()
}

func redactConfig(cfg bootstrap.Config, text string) string {
	var secrets []string
	add := func(value string) {
		if value != "" {
			secrets = append(secrets, value)
		}
	}
	addHeader := func(name, value string) {
		name = strings.ToLower(name)
		if strings.Contains(name, "auth") || strings.Contains(name, "token") || strings.Contains(name, "key") || strings.Contains(name, "secret") || strings.Contains(name, "password") || strings.Contains(name, "cookie") {
			add(value)
			// Error responses often echo only the credential rather than its
			// Authorization scheme. Hide both forms before persisting events.
			if parts := strings.Fields(value); len(parts) == 2 && (strings.EqualFold(parts[0], "Bearer") || strings.EqualFold(parts[0], "Basic")) {
				add(parts[1])
			}
		}
	}
	for _, p := range cfg.Providers {
		add(p.APIKey)
		switch headers := p.Extra["headers"].(type) {
		case map[string]string:
			for name, value := range headers {
				addHeader(name, value)
			}
		case map[string]any:
			for name, value := range headers {
				if value, ok := value.(string); ok {
					addHeader(name, value)
				}
			}
		}
	}
	// An API key may be a prefix of a different saved key. Replacing the
	// longest values first avoids exposing the longer credential's suffix.
	sort.Slice(secrets, func(i, j int) bool { return len(secrets[i]) > len(secrets[j]) })
	for _, secret := range secrets {
		text = strings.ReplaceAll(text, secret, "[已隐藏]")
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
	s.closeOnce.Do(s.closeResources)
}

func (s *Server) closeResources() {
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
	if s.styleCancel != nil {
		s.styleCancel()
	}
	styleDone := s.styleDone
	var coCreateDone <-chan struct{}
	if s.job == "cocreate" {
		coCreateDone = s.jobDone
	}
	background := s.background
	rt := s.rt
	s.rt = nil
	var retained []*host.Host
	for _, view := range s.projectRuntimes {
		if view.rt != nil {
			retained = append(retained, view.rt)
		}
	}
	s.projectRuntimes = nil
	for c := range s.clients {
		close(c)
		delete(s.clients, c)
	}
	s.mu.Unlock()
	// A co-creation handler persists its final transcript after cancellation.
	// Keep the current Host's directory lease until that save has completed.
	if coCreateDone != nil {
		<-coCreateDone
	}
	if background != nil {
		_ = background.Close(context.Background())
	}
	if styleDone != nil {
		<-styleDone
	}
	if rt != nil {
		rt.Close()
	}
	for _, retainedHost := range retained {
		retainedHost.Close()
	}
	s.wg.Wait()
}

func Run(opts Options) error {
	if opts.DesktopShutdownToken == "" {
		opts.DesktopShutdownToken = os.Getenv("AINOVEL_DESKTOP_SHUTDOWN_TOKEN")
	}
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
	webURL := fmt.Sprintf("http://%s/?ainovel_start=%d", ln.Addr().String(), time.Now().UnixNano())
	fmt.Printf("小说 Web 工作台：%s\n作品目录：%s\n按 Ctrl+C 停止服务。\n", webURL, s.dir)
	if opts.OpenBrowser {
		go func() {
			if err := openBrowser(webURL); err != nil {
				fmt.Fprintf(os.Stderr, "无法自动打开浏览器，请手动打开上方地址：%v\n", err)
			}
		}()
	}
	return serveWorkbench(ctx, s, httpServer, ln)
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
	if s.desktopShutdownToken != "" {
		mux.HandleFunc("POST /api/desktop/shutdown", s.requestDesktopShutdown)
	}
	mux.HandleFunc("GET /api/state", s.state)
	mux.HandleFunc("GET /api/model-requests", s.listModelRequests)
	mux.HandleFunc("GET /api/model-requests/{id}", s.getModelRequest)
	mux.HandleFunc("GET /api/jobs", s.listBackgroundJobs)
	mux.HandleFunc("POST /api/jobs", s.submitBackgroundJob)
	mux.HandleFunc("POST /api/jobs/{job}/cancel", s.cancelBackgroundJob)
	mux.HandleFunc("GET /api/jobs/limits", s.backgroundLimits)
	mux.HandleFunc("POST /api/jobs/limits", s.backgroundLimits)
	mux.HandleFunc("GET /api/config", s.getConfig)
	mux.HandleFunc("POST /api/config", s.saveConfig)
	mux.HandleFunc("POST /api/config/test", s.testConfig)
	mux.HandleFunc("POST /api/config/models", s.discoverModels)
	mux.HandleFunc("POST /api/config/select", s.selectModel)
	mux.HandleFunc("POST /api/action", s.action)
	mux.HandleFunc("GET /api/events", s.subscribe)
	mux.HandleFunc("GET /api/chapters/{chapter}", s.chapter)
	mux.HandleFunc("GET /api/chapters/{chapter}/edit", s.chapterEditor)
	mux.HandleFunc("POST /api/chapters/{chapter}/edit", s.chapterEditor)
	mux.HandleFunc("POST /api/chapter-rewrites", s.chapterRewrites)
	mux.HandleFunc("GET /api/chapters/{chapter}/versions", s.chapterVersions)
	mux.HandleFunc("POST /api/chapters/{chapter}/versions/{version}/apply", s.applyChapterVersion)
	mux.HandleFunc("POST /api/export", s.export)
	mux.HandleFunc("GET /api/workspaces", s.workspaces)
	mux.HandleFunc("POST /api/workspaces", s.switchWorkspace)
	mux.HandleFunc("POST /api/workspaces/copy", s.copyWorkspace)
	mux.HandleFunc("GET /api/versions", s.listVersions)
	mux.HandleFunc("POST /api/versions", s.createVersion)
	mux.HandleFunc("GET /api/versions/{version}/compare", s.compareVersion)
	mux.HandleFunc("POST /api/versions/restore", s.restoreVersion)
	mux.HandleFunc("PATCH /api/workspaces", s.updateProject)
	mux.HandleFunc("POST /api/workspaces/archive", s.archiveWorkspace)
	mux.HandleFunc("POST /api/workspaces/delete", s.deleteWorkspace)
	mux.HandleFunc("GET /api/cocreate", s.getCoCreate)
	mux.HandleFunc("POST /api/cocreate", s.postCoCreate)
	mux.HandleFunc("POST /api/cocreate/confirm", s.confirmCoCreate)
	mux.HandleFunc("POST /api/cocreate/reset", s.resetCoCreate)
	mux.HandleFunc("GET /api/planning", s.planning)
	mux.HandleFunc("GET /api/style", s.style)
	mux.HandleFunc("POST /api/style/entries", s.createStyleEntry)
	mux.HandleFunc("GET /api/style/references", s.styleReferences)
	mux.HandleFunc("POST /api/style/references", s.styleReferences)
	mux.HandleFunc("POST /api/style/upload", s.uploadStyleSource)
	mux.HandleFunc("POST /api/style/run", s.runStyleAnalysis)
	mux.HandleFunc("POST /api/style/delete", s.deleteStyleSource)
	mux.HandleFunc("GET /api/audit", s.audit)
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
		if (r.Method == http.MethodPost || r.Method == http.MethodPatch) && !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
			fail(w, http.StatusUnsupportedMediaType, "请提交 JSON 数据")
			return
		}
		mux.ServeHTTP(w, r)
	})
}

func readJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	return readJSONLimit(w, r, v, 2<<20)
}

func readJSONLimit(w http.ResponseWriter, r *http.Request, v any, limit int64) bool {
	r.Body = http.MaxBytesReader(w, r.Body, limit)
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
	p, _ := st.Progress.Load()
	outline, _ := st.Outline.LoadOutlineView()
	if s.rt == nil {
		if b, e := st.Book.Load(); e == nil && b != nil {
			snap.BookTitle = b.Title
		}
		for _, entry := range outline {
			snap.Outline = append(snap.Outline, host.OutlineSnapshot{
				Chapter: entry.Chapter, Title: entry.Title, CoreEvent: entry.CoreEvent,
				VolumeID: entry.VolumeID, VolumeTitle: entry.VolumeTitle,
				ArcID: entry.ArcID, ArcTitle: entry.ArcTitle,
			})
		}
		if p != nil {
			snap.Phase = string(p.Phase)
			snap.CurrentChapter = p.CurrentChapter
			snap.CompletedCount = len(p.CompletedChapters)
			snap.TotalChapters = p.TotalChapters
			snap.TotalWordCount = p.TotalWordCount
		}
	}
	chapters := chapterWorkbenchItems(st, outline, p)
	project, projectErr := readProject(s.dir)
	workflow, workflowErr := workflowView(st)
	if projectErr != nil {
		project.Name = "项目资料读取失败"
	}
	if workflowErr != nil {
		workflow["error"] = workflowErr.Error()
	}
	// Host lifecycle can become idle before its completion event releases the
	// project's run slot. Report that settling window as busy so controls and
	// API clients agree with projectRunningLocked. A running engine remains
	// navigable: its work is already represented by snapshot.isRunning.
	busy := s.busy || (s.rt != nil && !snap.IsRunning && s.runSlots[s.rt] != nil)
	reply(w, 200, map[string]any{"ready": s.rt != nil, "error": s.hostError, "busy": busy, "backgroundBusy": s.backgroundAnyBusyLocked(), "modelConfigurationBusy": s.modelConfigurationBusyLocked(), "job": s.job, "stopping": s.stopping, "directory": s.dir, "snapshot": snap, "chapters": chapters, "project": project, "workflow": workflow})
}

func (s *Server) testConfig(w http.ResponseWriter, r *http.Request) {
	var req modelLibraryRequest
	if !readJSON(w, r, &req) {
		return
	}
	s.mu.Lock()
	if s.closed || s.busy || s.stopping || s.styleBusy || s.backgroundAnyBusyLocked() || (s.rt != nil && s.rt.Snapshot().IsRunning) {
		s.mu.Unlock()
		fail(w, http.StatusConflict, "请先停止前台和后台创作，再测试模型连接")
		return
	}
	candidate := bootstrap.CloneConfig(s.baseCfg)
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	jobDone := make(chan struct{})
	s.busy, s.job, s.cancelJob, s.jobDone = true, "连接测试", cancel, jobDone
	s.wg.Add(1)
	s.mu.Unlock()
	defer func() {
		cancel()
		s.mu.Lock()
		if s.jobDone == jobDone {
			s.busy, s.job, s.cancelJob, s.jobDone = false, "", nil, nil
		}
		close(jobDone)
		s.mu.Unlock()
		s.wg.Done()
	}()
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
		fail(w, 400, "未知 API Key 操作")
		return
	}
	mc := bootstrap.ModelConfig{Name: strings.TrimSpace(req.Model)}
	if previous, ok := pc.ModelConfig(mc.Name); ok {
		mc.ContextWindow = previous.ContextWindow
		mc.MaxOutputTokens = previous.MaxOutputTokens
		mc.JSONSchema = previous.JSONSchema
	}
	for _, configured := range req.ModelConfigs {
		if strings.TrimSpace(configured.Name) == mc.Name {
			mc = configured
			mc.Name = strings.TrimSpace(mc.Name)
			break
		}
	}
	if req.ContextWindow != nil {
		mc.ContextWindow = *req.ContextWindow
	}
	if req.MaxOutputTokens != nil {
		mc.MaxOutputTokens = *req.MaxOutputTokens
	}
	pc.Models = []bootstrap.ModelConfig{mc}
	candidate.Providers[name] = pc
	candidate.Provider = name
	candidate.ModelName = strings.TrimSpace(req.Model)
	candidate.Roles = nil
	candidate.FillDefaults()
	if err := candidate.ValidateBase(); err != nil {
		fail(w, 400, redactConfig(candidate, err.Error()))
		return
	}
	candidate.AcquireModelRequest = s.acquireCreationModel
	models, err := bootstrap.NewModelSet(candidate)
	if err != nil {
		fail(w, 400, "创建模型失败: "+redactConfig(candidate, err.Error()))
		return
	}
	if _, err = models.Default.Generate(ctx, []agentcore.Message{agentcore.UserMsg("Reply OK.")}, nil); err != nil {
		fail(w, 502, "连接测试失败: "+redactConfig(candidate, err.Error()))
		return
	}
	reply(w, 200, map[string]bool{"ok": true})
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
	allowed := map[string]bool{"start": true, "resume": true, "pause": true, "next": true, "review": true, "steer": true, "continue": true, "sync": true, "reopen": true, "retry": true, "approve-plan": true, "revise-plan": true, "repair-plan": true, "batch": true, "accept-chapter": true, "chapter-feedback": true}
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
	if (req.Action == "resume" || req.Action == "repair-plan") && s.rt == nil {
		s.initLocked()
	}
	if req.Action == "start" {
		session, readErr := readWebCoCreate(s.dir)
		if readErr != nil {
			s.mu.Unlock()
			fail(w, 500, "共创会话读取失败")
			return
		}
		if !session.Confirmed || strings.TrimSpace(session.DraftPrompt) == "" {
			s.mu.Unlock()
			fail(w, 409, "请先完成规划前的多轮共创并确认完整创作指令")
			return
		}
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
		coCreate := s.job == "cocreate"
		s.wg.Add(1)
		s.mu.Unlock()
		rt.Abort()
		go func() {
			defer s.wg.Done()
			if coCreate && jobDone != nil {
				<-jobDone
			}
			rt.Close()
			if jobDone != nil {
				<-jobDone
			}
			s.mu.Lock()
			s.releaseProjectRunLocked(rt)
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
	if err := s.reserveProjectRunLocked(rt); err != nil {
		message := s.projectRunErrorLocked(err)
		s.mu.Unlock()
		fail(w, http.StatusConflict, message)
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
			err = rt.ReviseWorkbenchPlanning(req.Text)
		case "repair-plan":
			var label string
			label, err = rt.Resume()
			if err == nil && label == "" {
				err = errors.New("当前没有可恢复任务")
			}
		case "chapter-feedback":
			err = rt.RewriteWorkbenchChapter(req.Chapter, req.Text)
		case "start":
			err = s.prepareAndStart(ctx)
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
		s.releaseIdleProjectRunLocked(rt)
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
	var req struct {
		Format    string `json:"format"`
		Directory string `json:"directory"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	if req.Format != "txt" && req.Format != "epub" {
		fail(w, 400, "仅支持 TXT / EPUB")
		return
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		fail(w, 409, "工作台已关闭")
		return
	}
	target := s.dir
	if strings.TrimSpace(req.Directory) != "" {
		var err error
		target, err = s.validWorkspaceLocked(req.Directory)
		if err != nil {
			s.mu.Unlock()
			fail(w, 400, err.Error())
			return
		}
	}
	st := store.NewStore(target)
	progress, err := st.Progress.Load()
	if err != nil || progress == nil || len(progress.CompletedChapters) == 0 {
		s.mu.Unlock()
		fail(w, 409, "暂无可导出的已完成章节")
		return
	}
	title := filepath.Base(target)
	if project, e := readProject(target); e == nil && strings.TrimSpace(project.Name) != "" {
		title = project.Name
	} else if book, e := st.Book.Load(); e == nil && book != nil && strings.TrimSpace(book.Title) != "" {
		title = book.Title
	}
	cfgSnapshot := bootstrap.CloneConfig(s.baseCfg)
	s.mu.Unlock()

	tmp, err := os.MkdirTemp("", "ainovel-export-*")
	if err != nil {
		fail(w, 500, "无法创建导出文件")
		return
	}
	defer os.RemoveAll(tmp)
	out := filepath.Join(tmp, "novel."+req.Format)
	_, err = exp.Run(r.Context(), exp.Deps{Store: st}, exp.Options{Format: exp.Format(req.Format), OutPath: out})
	if err != nil {
		fail(w, 409, redactConfig(cfgSnapshot, err.Error()))
		return
	}
	data, err := os.ReadFile(out)
	if err != nil {
		fail(w, 500, "读取导出失败")
		return
	}
	filename := exportFileBase(title) + "." + req.Format
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": filename}))
	if req.Format == "epub" {
		w.Header().Set("Content-Type", "application/epub+zip")
	} else {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	}
	_, _ = w.Write(data)
}

func exportFileBase(title string) string {
	title = strings.TrimSpace(title)
	title = strings.Map(func(r rune) rune {
		switch {
		case r < 0x20 || strings.ContainsRune(`<>:"/\|?*`, r):
			return '-'
		default:
			return r
		}
	}, title)
	title = strings.Trim(title, " .-")
	runes := []rune(title)
	if len(runes) > 80 {
		title = string(runes[:80])
	}
	if title == "" {
		return "novel"
	}
	return title
}
