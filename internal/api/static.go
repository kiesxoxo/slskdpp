package api

import (
	"io/fs"
	"net/http"
	"path"
	"strings"
)

const uiMissing = `<!doctype html><meta charset="utf-8"><title>slskdpp</title>
<p>The web UI is not built into this binary. Run <code>make ui build</code>.
The API is available under <code>/api/v1</code>.</p>`

// static serves the single-page app. Unknown paths get index.html so
// client-side routes survive a reload; hashed assets are cached forever.
func (a *API) static() http.Handler {
	ui := a.cfg.UI
	if ui == nil {
		return placeholder()
	}
	if _, err := fs.Stat(ui, "index.html"); err != nil {
		return placeholder()
	}
	files := http.FileServerFS(ui)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
		if name == "" || name == "index.html" {
			serveIndex(w, r, ui)
			return
		}
		if st, err := fs.Stat(ui, name); err != nil || st.IsDir() {
			serveIndex(w, r, ui)
			return
		}
		if strings.HasPrefix(name, "assets/") {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		}
		files.ServeHTTP(w, r)
	})
}

func serveIndex(w http.ResponseWriter, r *http.Request, ui fs.FS) {
	b, err := fs.ReadFile(ui, "index.html")
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Write(b)
}

func placeholder() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusServiceUnavailable)
		w.Write([]byte(uiMissing))
	})
}
