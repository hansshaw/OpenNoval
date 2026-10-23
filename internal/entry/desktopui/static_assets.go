package desktopui

import (
	"io/fs"
	"net/http"
	"path"
	"strings"
)

// embeddedAssetServer keeps browser-executable assets independent of the host's
// MIME registry. In particular, Windows may associate .mjs with a non-JavaScript
// type; strict module loading then rejects PDF.js even though the file is served
// successfully. Set known types per response instead of changing process-wide
// mime mappings, and retain FileServer's status, range, and conditional handling.
func embeddedAssetServer(files fs.FS) http.Handler {
	server := http.FileServer(http.FS(files))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch strings.ToLower(path.Ext(r.URL.Path)) {
		case ".js", ".mjs":
			w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
		case ".wasm":
			w.Header().Set("Content-Type", "application/wasm")
		}
		server.ServeHTTP(w, r)
	})
}
