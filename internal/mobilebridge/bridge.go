// Package mobilebridge embeds the novel workbench in an Android process.
// Only lifecycle/activity signals cross gomobile; project data and AI execution
// stay inside the application instead of depending on a desktop PC.
package mobilebridge

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/hansshaw/OpenNovel/internal/bootstrap"
	"github.com/hansshaw/OpenNovel/internal/entry/desktopui"
)

var process struct {
	sync.Mutex
	current *runtime
}

type runtime struct {
	dataDir, cacheDir, entryURL string
	workbench                   *desktopui.Server
	server                      *http.Server
	done                        chan struct{}
	observer                    WorkObserver
	requests                    atomic.Int64
}

// WorkObserver is called synchronously before authenticated model-related API
// requests begin. The Android service can acquire its power lease before a user
// switches apps; observing WebView timers would leave a race at that boundary.
type WorkObserver interface {
	OnWorkStarted()
}

// Start opens the embedded workbench on IPv4 loopback and returns the private
// WebView entry URL. Call it on a worker thread with Context.getFilesDir() and
// Context.getCacheDir() subdirectories. The URL contains an ephemeral credential
// and must not be logged, shared, or opened in an external browser.
// Repeated calls with the same directories reuse the existing runtime.
func Start(dataDirectory, cacheDirectory string) (string, error) {
	return StartWithObserver(dataDirectory, cacheDirectory, nil)
}

func StartWithObserver(dataDirectory, cacheDirectory string, observer WorkObserver) (string, error) {
	process.Lock()
	defer process.Unlock()

	dataDir, err := privateDirectory(dataDirectory)
	if err != nil {
		return "", fmt.Errorf("作品存储目录不可用：%w", err)
	}
	cacheDir, err := privateDirectory(cacheDirectory)
	if err != nil {
		return "", fmt.Errorf("缓存目录不可用：%w", err)
	}
	if running := process.current; running != nil {
		select {
		case <-running.done:
			running.close()
			process.current = nil
		default:
			if running.dataDir != dataDir || running.cacheDir != cacheDir {
				return "", errors.New("工作台正在运行，请先关闭后再切换存储目录")
			}
			return running.entryURL, nil
		}
	}

	cfg, err := bootstrap.LoadConfigFile(filepath.Join(dataDir, "config.json"))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("模型配置读取失败：%w", err)
	}
	// Android owns notifications and app lifetime. Desktop notification helpers
	// must never launch external commands from the embedded runtime.
	notifications := false
	cfg.Notify.Enabled = &notifications
	cfg.OutputDir = filepath.Join(dataDir, "books")

	var secret [32]byte
	if _, err := rand.Read(secret[:]); err != nil {
		return "", fmt.Errorf("无法创建安全会话：%w", err)
	}
	token := hex.EncodeToString(secret[:])
	listener, err := listenPrivate(dataDir)
	if err != nil {
		return "", fmt.Errorf("无法启动本机创作服务：%w", err)
	}
	workbench, err := desktopui.New(cfg, desktopui.Options{
		OutputDir: cfg.OutputDir, DataDir: dataDir, TempDir: cacheDir,
		DesktopSessionToken: token,
	})
	if err != nil {
		_ = listener.Close()
		return "", err
	}
	origin := "http://" + listener.Addr().String()
	running := &runtime{
		dataDir: dataDir, cacheDir: cacheDir,
		entryURL:  origin + "/__mobile/session?token=" + token,
		workbench: workbench, done: make(chan struct{}),
		observer: observer,
	}
	running.server = &http.Server{
		Handler:           sessionHandler(listener.Addr().String(), token, running.trackWork(workbench.Handler())),
		ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 60 * time.Second,
		MaxHeaderBytes: 32 << 10,
	}
	process.current = running
	go func() {
		defer close(running.done)
		_ = running.server.Serve(listener)
	}()
	return running.entryURL, nil
}

// HasActiveWork includes synchronous connection tests/discovery as well as
// detached generation jobs. No credentials or manuscript data cross JNI.
func HasActiveWork() bool {
	process.Lock()
	running := process.current
	process.Unlock()
	if running == nil {
		return false
	}
	select {
	case <-running.done:
		return false
	default:
		return running.requests.Load() > 0 || running.workbench.HasActiveWork()
	}
}

func (running *runtime) trackWork(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if startsNetworkWork(r) {
			running.requests.Add(1)
			defer running.requests.Add(-1)
			if running.observer != nil {
				running.observer.OnWorkStarted()
			}
		}
		next.ServeHTTP(w, r)
	})
}

func startsNetworkWork(r *http.Request) bool {
	if r.Method != http.MethodPost {
		return false
	}
	switch r.URL.Path {
	case "/api/action", "/api/cocreate", "/api/cocreate/confirm", "/api/chapter-rewrites", "/api/style/run", "/api/jobs",
		"/api/config", "/api/config/select", "/api/config/test", "/api/config/models", "/api/media-models/discover":
		return true
	}
	return strings.HasPrefix(r.URL.Path, "/api/chapters/") && strings.HasSuffix(r.URL.Path, "/media/generate")
}

// URL returns the entry URL for the active runtime, or an empty string when it
// is stopped. Like Start's result, this value must remain private to the app.
func URL() string {
	process.Lock()
	defer process.Unlock()
	if process.current == nil {
		return ""
	}
	select {
	case <-process.current.done:
		return ""
	default:
		return process.current.entryURL
	}
}

// Stop cancels model calls and queued jobs, flushes workbench state, and releases
// project leases. Reopening must not automatically resume paid queued work.
// It is safe to call repeatedly. Run it off Android's main thread.
func Stop() {
	process.Lock()
	defer process.Unlock()
	if process.current != nil {
		process.current.close()
		process.current = nil
	}
}

func (running *runtime) close() {
	running.workbench.CloseStoppingQueued()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := running.server.Shutdown(ctx); err != nil {
		_ = running.server.Close()
	}
	<-running.done
}

func privateDirectory(directory string) (string, error) {
	if directory == "" || !filepath.IsAbs(directory) {
		return "", errors.New("必须提供应用私有目录的绝对路径")
	}
	if err := os.MkdirAll(directory, 0700); err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(directory)
}

// Reuse a previously assigned random port when available. This keeps the
// WebView origin stable across ordinary app restarts; a busy port falls back
// to a fresh OS-assigned port rather than preventing access to saved books.
func listenPrivate(dataDir string) (net.Listener, error) {
	portPath := filepath.Join(dataDir, "loopback-port")
	var listener net.Listener
	if saved, err := os.ReadFile(portPath); err == nil {
		if port, err := strconv.Atoi(strings.TrimSpace(string(saved))); err == nil && port >= 1024 && port <= 65535 {
			listener, _ = net.Listen("tcp4", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
		}
	}
	if listener == nil {
		var err error
		listener, err = net.Listen("tcp4", "127.0.0.1:0")
		if err != nil {
			return nil, err
		}
	}
	_, port, _ := net.SplitHostPort(listener.Addr().String())
	// A failed preference write does not risk the manuscript and must not stop
	// startup. Android also persists its UI drafts independently of this origin.
	_ = os.WriteFile(portPath, []byte(port+"\n"), 0600)
	return listener, nil
}

func sessionHandler(address, token string, next http.Handler) http.Handler {
	cookieName := "ainovel_mobile_" + token[:12]
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		if r.Host != address || r.Header.Get("Sec-Fetch-Site") == "cross-site" {
			http.Error(w, "仅允许应用内访问", http.StatusForbidden)
			return
		}
		if raw := r.Header.Get("Origin"); raw != "" {
			origin, err := url.Parse(raw)
			if err != nil || origin.Scheme != "http" || origin.Host != address {
				http.Error(w, "请求来源不匹配", http.StatusForbidden)
				return
			}
		}
		if r.URL.Path == "/__mobile/session" {
			if r.Method != http.MethodGet || !sameToken(r.URL.Query().Get("token"), token) {
				http.Error(w, "会话无效，请重新打开应用", http.StatusForbidden)
				return
			}
			http.SetCookie(w, &http.Cookie{
				Name: cookieName, Value: token, Path: "/", HttpOnly: true,
				SameSite: http.SameSiteStrictMode,
			})
			http.Redirect(w, r, "/?mobile=1", http.StatusSeeOther)
			return
		}
		cookie, err := r.Cookie(cookieName)
		if err != nil || !sameToken(cookie.Value, token) {
			http.Error(w, "会话已过期，请重新打开应用", http.StatusForbidden)
			return
		}
		// The existing workbench's desktop capability remains enabled. Only this
		// authenticated wrapper can translate the app cookie into that header.
		request := r.Clone(r.Context())
		request.Header.Set("X-AInovel-Desktop-Session", token)
		next.ServeHTTP(w, request)
	})
}

func sameToken(provided, expected string) bool {
	return subtle.ConstantTimeCompare([]byte(provided), []byte(expected)) == 1
}
