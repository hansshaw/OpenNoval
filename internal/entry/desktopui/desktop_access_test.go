package desktopui

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRunRequiresDesktopSessionAndShutdownCapabilities(t *testing.T) {
	t.Setenv("AINOVEL_DESKTOP_SESSION_TOKEN", "")
	t.Setenv("AINOVEL_DESKTOP_SHUTDOWN_TOKEN", "")
	for _, opts := range []Options{
		{},
		{DesktopSessionToken: testDesktopShutdownToken},
		{DesktopShutdownToken: testDesktopShutdownToken},
		{DesktopSessionToken: "short", DesktopShutdownToken: testDesktopShutdownToken},
		{DesktopSessionToken: strings.Repeat("z", 64), DesktopShutdownToken: testDesktopShutdownToken},
	} {
		if err := Run(opts); err == nil || !strings.Contains(err.Error(), "桌面应用启动") {
			t.Fatalf("Run accepted missing/invalid desktop capability: %v", err)
		}
	}
	for _, address := range []string{"0.0.0.0:0", "192.0.2.1:0", ":0"} {
		err := Run(Options{Listen: address, DesktopSessionToken: testDesktopShutdownToken, DesktopShutdownToken: testDesktopShutdownToken})
		if err == nil || !strings.Contains(err.Error(), "回环地址") {
			t.Fatalf("Run accepted external listener %q: %v", address, err)
		}
	}
}

func TestDesktopSessionProtectsDocumentsAssetsAndAPIs(t *testing.T) {
	s := newTestServer(t, false)
	s.desktopSessionToken = testDesktopShutdownToken
	handler := s.Handler()
	for _, route := range []string{"/", "/app.js", "/modules/media/media.js", "/api/state", "/api/events", "/api/chapters/1/media/image/content"} {
		for _, token := range []string{"", "wrong-session"} {
			r := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8080"+route, nil)
			r.Header.Set("X-AInovel-Desktop-Session", token)
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, r)
			if w.Code != http.StatusForbidden || strings.Contains(w.Body.String(), testDesktopShutdownToken) {
				t.Fatalf("unauthorized %s: %d %s", route, w.Code, w.Body.String())
			}
		}
	}
	for _, route := range []string{"/", "/app.js", "/api/state", "/api/config"} {
		r := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8080"+route, nil)
		r.Header.Set("X-AInovel-Desktop-Session", testDesktopShutdownToken)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != http.StatusOK || strings.Contains(w.Body.String(), testDesktopShutdownToken) {
			t.Fatalf("authenticated %s: %d %s", route, w.Code, w.Body.String())
		}
	}
	// A URL never substitutes for a private transport header.
	r := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8080/api/state?token="+testDesktopShutdownToken, nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	if w.Code != http.StatusForbidden {
		t.Fatalf("URL credential unexpectedly accepted: %d", w.Code)
	}
}
