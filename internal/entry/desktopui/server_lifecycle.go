package desktopui

import (
	"context"
	"crypto/subtle"
	"errors"
	"net"
	"net/http"
	"time"
)

const desktopShutdownHeader = "X-AInovel-Desktop-Token"

// This capability belongs only to Electron's main process. It is not returned
// through configuration, state, preload IPC, logs, or a browser-readable URL.
func (s *Server) requestDesktopShutdown(w http.ResponseWriter, r *http.Request) {
	provided := r.Header.Get(desktopShutdownHeader)
	if s.desktopShutdownToken == "" || subtle.ConstantTimeCompare([]byte(provided), []byte(s.desktopShutdownToken)) != 1 {
		fail(w, http.StatusForbidden, "桌面退出凭证无效")
		return
	}
	var body struct{}
	if !readJSONLimit(w, r, &body, 1024) {
		return
	}
	s.desktopShutdownOnce.Do(func() { close(s.desktopShutdownRequested) })
	// Accepted is intentionally not a claim that project flushing is complete.
	// Electron waits for the sidecar's normal process exit before closing SQLite.
	reply(w, http.StatusAccepted, map[string]bool{"ok": true})
}

func serveWorkbench(parent context.Context, s *Server, server *http.Server, listener net.Listener) error {
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		select {
		case <-ctx.Done():
		case <-s.desktopShutdownRequested:
		}
		// Closing Hosts first cancels model requests, drains events, flushes
		// usage and the job journal, and releases project leases on every OS.
		s.Close()
		shutdown, stop := context.WithTimeout(context.Background(), 5*time.Second)
		defer stop()
		if err := server.Shutdown(shutdown); err != nil {
			_ = server.Close()
		}
	}()
	err := server.Serve(listener)
	cancel()
	// Serve returns as soon as Shutdown closes the listener. Wait for the
	// coordinator too so Run cannot return before resource/HTTP cleanup ends.
	<-finished
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}
