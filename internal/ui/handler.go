package ui

import (
	"io/fs"
	"net/http"
	"strings"
)

// baseHrefPlaceholder is the Jinja token Airflow leaves in index.html for the
// server to fill with the deployment base path. Leoflow substitutes it at
// request time, mirroring Airflow's TemplateResponse.
const baseHrefPlaceholder = "{{ backend_server_base_url }}"

// Server serves the embedded Airflow 3.2.1 SPA: static assets under a prefix and
// an index.html fallback for client-side routes.
type Server struct {
	fsys    fs.FS
	version string
}

// New builds a Server over the embedded, pinned SPA bundle.
func New() *Server { return NewFromFS(Assets(), Version()) }

// NewFromFS builds a Server over an arbitrary asset filesystem, so tests can
// inject a fixture instead of the embedded bundle.
func NewFromFS(fsys fs.FS, version string) *Server {
	return &Server{fsys: fsys, version: version}
}

// Version returns the pinned upstream Airflow tag the bundle was built from.
func (s *Server) Version() string { return s.version }

// StaticHandler serves the bundle as static files. The caller mounts it with
// the /static prefix already stripped. Content-hashed bundle chunks (under
// assets/) are marked immutable; index.html is never cached; everything else
// gets a short cache. Missing files yield 404 (no SPA fallback here).
func (s *Server) StaticHandler() http.Handler {
	fileServer := http.FileServer(http.FS(s.fsys))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", cacheControl(r.URL.Path))
		// The SPA streams a WebAssembly module (sqlparser); browsers reject it
		// unless served as application/wasm, which Go's default MIME table omits.
		// Pre-setting it stops http.ServeContent from sniffing a wrong type.
		if strings.HasSuffix(r.URL.Path, ".wasm") {
			w.Header().Set("Content-Type", "application/wasm")
		}
		fileServer.ServeHTTP(w, r)
	})
}

// Index writes the SPA shell with <base href> set to basePath, so the bundled
// React router resolves routes and asset URLs against the deployment root. It
// is the fallback for any non-static, non-API path. basePath defaults to "/".
func (s *Server) Index(w http.ResponseWriter, basePath string) {
	if basePath == "" {
		basePath = "/"
	}
	data, err := fs.ReadFile(s.fsys, "index.html")
	if err != nil {
		http.Error(w, "UI bundle missing index.html", http.StatusInternalServerError)
		return
	}
	body := strings.ReplaceAll(string(data), baseHrefPlaceholder, basePath)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	if _, err := w.Write([]byte(body)); err != nil {
		return // client hung up mid-write; nothing actionable to do.
	}
}

// cacheControl picks a Cache-Control value for a static path. Content-hashed
// chunks may be cached forever; the HTML shell never; other files briefly.
func cacheControl(path string) string {
	trimmed := strings.TrimPrefix(path, "/")
	switch {
	case trimmed == "index.html" || trimmed == "":
		return "no-cache"
	case strings.HasPrefix(trimmed, "assets/"):
		return "public, max-age=31536000, immutable"
	default:
		return "public, max-age=3600"
	}
}
