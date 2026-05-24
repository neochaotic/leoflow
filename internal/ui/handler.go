package ui

import (
	"bytes"
	"compress/gzip"
	"io/fs"
	"mime"
	"net/http"
	"path"
	"path/filepath"
	"strconv"
	"strings"
)

// baseHrefPlaceholder is the Jinja token Airflow leaves in index.html for the
// server to fill with the deployment base path. Leoflow substitutes it at
// request time, mirroring Airflow's TemplateResponse.
const baseHrefPlaceholder = "{{ backend_server_base_url }}"

// devBannerHTML is a discreet, translucent-yellow "DEV" pill fixed at top-center,
// injected into the served shell only in dev mode so a developer never mistakes
// the local environment for production. pointer-events:none keeps it click-through.
const devBannerHTML = `<div id="leoflow-dev-banner">DEV</div>` +
	`<style>#leoflow-dev-banner{position:fixed;top:0;left:50%;transform:translateX(-50%);` +
	`z-index:2147483647;background:rgba(255,193,7,.85);color:#1a1a1a;` +
	`font:600 11px/1.7 system-ui,-apple-system,sans-serif;padding:1px 16px;` +
	`border-radius:0 0 6px 6px;letter-spacing:3px;pointer-events:none}</style>`

// Server serves the embedded Airflow 3.2.1 SPA: static assets under a prefix and
// an index.html fallback for client-side routes.
type Server struct {
	fsys      fs.FS
	version   string
	devBanner bool
}

// SetDevBanner toggles injection of the DEV overlay into the served shell. It is
// enabled only by `leoflow dev` (dev mode); the demo and production never set it.
func (s *Server) SetDevBanner(on bool) { s.devBanner = on }

// New builds a Server over the embedded, pinned SPA bundle.
func New() *Server { return NewFromFS(Assets(), Version()) }

// NewFromFS builds a Server over an arbitrary asset filesystem, so tests can
// inject a fixture instead of the embedded bundle.
func NewFromFS(fsys fs.FS, version string) *Server {
	return &Server{fsys: fsys, version: version}
}

// Version returns the pinned upstream Airflow tag the bundle was built from.
func (s *Server) Version() string { return s.version }

// StaticHandler serves the bundle as static files from the embedded FS. The
// caller mounts it with the /static prefix already stripped. Content-hashed
// chunks (under assets/) are marked immutable; index.html is never cached;
// everything else gets a short cache. Compressible assets are gzipped when the
// client accepts it. Missing files yield 404 (no SPA fallback here); directories
// are not listed.
func (s *Server) StaticHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(path.Clean("/"+strings.TrimPrefix(r.URL.Path, "/")), "/")
		if name == "" {
			name = "index.html"
		}
		data, err := fs.ReadFile(s.fsys, name)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Cache-Control", cacheControl(r.URL.Path))
		w.Header().Set("Content-Type", contentType(name, data))
		if acceptsGzip(r) && compressible(name) {
			writeGzip(w, data)
			return
		}
		writeIdentity(w, data)
	})
}

// contentType resolves a response Content-Type, forcing application/wasm (which
// Go's MIME table omits, and browsers require for streaming instantiation) and
// sniffing only as a last resort.
func contentType(name string, data []byte) string {
	if strings.HasSuffix(name, ".wasm") {
		return "application/wasm"
	}
	if ct := mime.TypeByExtension(filepath.Ext(name)); ct != "" {
		return ct
	}
	return http.DetectContentType(data)
}

// acceptsGzip reports whether the client advertised gzip support.
func acceptsGzip(r *http.Request) bool {
	return strings.Contains(r.Header.Get("Accept-Encoding"), "gzip")
}

// compressibleExts are asset types worth gzipping; already-compressed binaries
// (png, woff2) are skipped.
var compressibleExts = map[string]bool{
	".js": true, ".css": true, ".json": true, ".html": true, ".svg": true,
	".wasm": true, ".map": true, ".txt": true, ".ttf": true,
}

func compressible(name string) bool {
	return compressibleExts[strings.ToLower(filepath.Ext(name))]
}

// writeGzip compresses data and writes it with the gzip Content-Encoding and the
// compressed Content-Length, falling back to identity on a compression error.
func writeGzip(w http.ResponseWriter, data []byte) {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	if _, err := gz.Write(data); err != nil {
		writeIdentity(w, data)
		return
	}
	if err := gz.Close(); err != nil {
		writeIdentity(w, data)
		return
	}
	w.Header().Set("Content-Encoding", "gzip")
	w.Header().Add("Vary", "Accept-Encoding")
	w.Header().Set("Content-Length", strconv.Itoa(buf.Len()))
	w.WriteHeader(http.StatusOK)
	// The payload is the pinned, compile-time-embedded SPA bundle served with an
	// explicit Content-Type — a trusted static asset, not user-controlled input.
	if _, err := w.Write(buf.Bytes()); err != nil { //nolint:gosec // trusted embedded asset
		return // client hung up mid-write.
	}
}

// writeIdentity writes data uncompressed with its Content-Length.
func writeIdentity(w http.ResponseWriter, data []byte) {
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	w.WriteHeader(http.StatusOK)
	// Trusted compile-time-embedded asset served with an explicit Content-Type.
	if _, err := w.Write(data); err != nil { //nolint:gosec // trusted embedded asset
		return // client hung up mid-write.
	}
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
	// The bundle's main <script src> is rewritten to ./static/assets/, but its
	// modulepreload hints keep a bare ./assets/ prefix that we do not serve under
	// (it collides with the SPA's own /assets route). Point those preloads at the
	// served /static/assets path so they resolve to JS instead of the index.html
	// SPA fallback (a text/html MIME type that breaks module preloading).
	body = strings.ReplaceAll(body, `"./assets/`, `"./static/assets/`)
	if s.devBanner {
		body = injectDevBanner(body)
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	if _, err := w.Write([]byte(body)); err != nil {
		return // client hung up mid-write; nothing actionable to do.
	}
}

// injectDevBanner places the DEV overlay just before </body> so it renders over
// the SPA; if there is no </body> it appends to the end.
func injectDevBanner(body string) string {
	if i := strings.LastIndex(body, "</body>"); i >= 0 {
		return body[:i] + devBannerHTML + body[i:]
	}
	return body + devBannerHTML
}

// cacheControl picks a Cache-Control value for a static path. Content-hashed
// chunks may be cached forever; the HTML shell never; other files briefly.
func cacheControl(urlPath string) string {
	trimmed := strings.TrimPrefix(urlPath, "/")
	switch {
	case trimmed == "index.html" || trimmed == "":
		return "no-cache"
	case strings.HasPrefix(trimmed, "assets/"):
		return "public, max-age=31536000, immutable"
	default:
		return "public, max-age=3600"
	}
}
