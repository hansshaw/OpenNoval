package desktopui

import (
	"bytes"
	"context"
	"io"
	"io/fs"
	"mime"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestEmbeddedAssetsIgnoreHostMIMERegistry(t *testing.T) {
	const childEnvironment = "AINOVEL_TEST_BROKEN_HOST_MIME"
	// mime has a process-global registry with no delete/reset operation. Isolate
	// the hostile host mappings so this test cannot alter other transport tests.
	if os.Getenv(childEnvironment) != "1" {
		executable, err := os.Executable()
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, executable, "-test.run=^TestEmbeddedAssetsIgnoreHostMIMERegistry$", "-test.count=1", "-test.v")
		cmd.Env = append(os.Environ(), childEnvironment+"=1")
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("isolated MIME regression failed: %v\n%s", err, output)
		}
		return
	}
	const incorrectType = "application/x-broken-host-type"
	for _, extension := range []string{".js", ".mjs", ".wasm"} {
		if err := mime.AddExtensionType(extension, incorrectType); err != nil {
			t.Fatal(err)
		}
	}
	files, err := fs.Sub(staticFiles, "static")
	if err != nil {
		t.Fatal(err)
	}
	plain := httptest.NewRecorder()
	http.FileServer(http.FS(files)).ServeHTTP(plain, httptest.NewRequest(http.MethodGet, "http://127.0.0.1/vendor/pdfjs/pdf.mjs", nil))
	if plain.Code != 200 || plain.Header().Get("Content-Type") != incorrectType {
		t.Fatalf("host MIME simulation did not reproduce the failure: %d %s", plain.Code, plain.Header().Get("Content-Type"))
	}

	s := newTestServer(t, false)
	s.desktopSessionToken = testDesktopShutdownToken
	server := httptest.NewServer(s.Handler())
	defer server.Close()
	client := server.Client()
	for _, test := range []struct {
		path, mime string
		wasm       bool
	}{
		{"/vendor/pdfjs/pdf.mjs", "text/javascript", false},
		{"/vendor/pdfjs/pdf.worker.mjs", "text/javascript", false},
		{"/vendor/pdfjs/wasm/jbig2.wasm", "application/wasm", true},
		{"/vendor/pdfjs/wasm/openjpeg.wasm", "application/wasm", true},
		{"/vendor/pdfjs/wasm/qcms_bg.wasm", "application/wasm", true},
		{"/vendor/pdfjs/wasm/quickjs-eval.wasm", "application/wasm", true},
		{"/app.js", "text/javascript", false},
		{"/modules/media/media.js", "text/javascript", false},
	} {
		t.Run(test.path, func(t *testing.T) {
			for _, method := range []string{http.MethodGet, http.MethodHead} {
				req, err := http.NewRequest(method, server.URL+test.path, nil)
				if err != nil {
					t.Fatal(err)
				}
				req.Header.Set("X-AInovel-Desktop-Session", testDesktopShutdownToken)
				response, err := client.Do(req)
				if err != nil {
					t.Fatal(err)
				}
				body, readErr := io.ReadAll(response.Body)
				response.Body.Close()
				if readErr != nil {
					t.Fatal(readErr)
				}
				mediaType, _, err := mime.ParseMediaType(response.Header.Get("Content-Type"))
				if response.StatusCode != 200 || err != nil || mediaType != test.mime {
					t.Fatalf("%s %s returned %d MIME %q: %v", method, test.path, response.StatusCode, response.Header.Get("Content-Type"), err)
				}
				if method == http.MethodGet && len(body) == 0 {
					t.Fatal("embedded asset body is empty")
				}
				if method == http.MethodGet && test.wasm && !bytes.HasPrefix(body, []byte{0, 'a', 's', 'm'}) {
					t.Fatal("WASM response is not the embedded binary")
				}
				if method == http.MethodHead && len(body) != 0 {
					t.Fatal("HEAD response contains a body")
				}
				if response.Header.Get("X-Content-Type-Options") != "nosniff" || !strings.Contains(response.Header.Get("Content-Security-Policy"), "script-src 'self';") {
					t.Fatalf("asset security headers changed: %v", response.Header)
				}
			}
		})
	}
	for _, route := range []string{"/vendor/pdfjs/pdf.mjs", "/vendor/pdfjs/pdf.worker.mjs", "/vendor/pdfjs/wasm/openjpeg.wasm"} {
		response, err := client.Get(server.URL + route)
		if err != nil {
			t.Fatal(err)
		}
		io.Copy(io.Discard, response.Body)
		response.Body.Close()
		if response.StatusCode != http.StatusForbidden {
			t.Fatalf("unauthenticated asset exposed: %s status %d", route, response.StatusCode)
		}
	}
	req, err := http.NewRequest(http.MethodGet, server.URL+"/vendor/pdfjs/missing.mjs", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("X-AInovel-Desktop-Session", testDesktopShutdownToken)
	response, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, response.Body)
	response.Body.Close()
	if response.StatusCode != 404 || !strings.HasPrefix(response.Header.Get("Content-Type"), "text/plain") {
		t.Fatalf("missing module received an executable success response: %d %s", response.StatusCode, response.Header.Get("Content-Type"))
	}
}
