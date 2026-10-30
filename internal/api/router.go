// Package api serves the REST API (/api/v1), the live event stream and the
// embedded web UI.
package api

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"io/fs"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/kiesxoxo/slskdpp/internal/events"
	"github.com/kiesxoxo/slskdpp/internal/protocol"
	"github.com/kiesxoxo/slskdpp/internal/search"
)

// Network is the part of the Soulseek client the API uses.
type Network interface {
	Username() string
	LoggedIn() bool
	Browse(ctx context.Context, username string) (protocol.SharedFileListResponse, error)
	FolderContents(ctx context.Context, username, folder string) (protocol.FolderContentsResponse, error)
	UserInfo(ctx context.Context, username string) (protocol.UserInfoResponse, error)
}

type Config struct {
	// Username and Password are the web UI login.
	Username string
	Password string
	// SessionKey signs session cookies; see LoadOrCreateKey.
	SessionKey []byte
	// UI is the built web app. Without an index.html a placeholder page
	// is served.
	UI     fs.FS
	Logger *slog.Logger
}

type API struct {
	cfg      Config
	log      *slog.Logger
	net      Network
	searches *search.Manager
	bus      *events.Bus
	limiter  *loginLimiter
	now      func() time.Time
}

// New returns the HTTP handler for the API and UI.
func New(cfg Config, net Network, searches *search.Manager, bus *events.Bus) http.Handler {
	log := cfg.Logger
	if log == nil {
		log = slog.Default()
	}
	a := &API{
		cfg:      cfg,
		log:      log.With("component", "api"),
		net:      net,
		searches: searches,
		bus:      bus,
		limiter:  newLoginLimiter(),
		now:      time.Now,
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/session", a.login)
	mux.HandleFunc("GET /api/v1/session", a.auth(a.session))
	mux.HandleFunc("DELETE /api/v1/session", a.logout)

	mux.HandleFunc("GET /api/v1/server", a.auth(a.serverState))
	mux.HandleFunc("GET /api/v1/events", a.auth(a.events))

	mux.HandleFunc("GET /api/v1/searches", a.auth(a.listSearches))
	mux.HandleFunc("POST /api/v1/searches", a.auth(a.startSearch))
	mux.HandleFunc("GET /api/v1/searches/{id}", a.auth(a.getSearch))
	mux.HandleFunc("DELETE /api/v1/searches/{id}", a.auth(a.deleteSearch))
	mux.HandleFunc("GET /api/v1/searches/{id}/hits", a.auth(a.searchHits))
	mux.HandleFunc("GET /api/v1/searches/{id}/albums", a.auth(a.searchAlbums))

	mux.HandleFunc("GET /api/v1/users/{user}/browse", a.auth(a.browse))
	mux.HandleFunc("GET /api/v1/users/{user}/folder", a.auth(a.folder))
	mux.HandleFunc("GET /api/v1/users/{user}/info", a.auth(a.userInfo))

	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		writeError(w, http.StatusNotFound, "no such endpoint")
	})
	mux.Handle("/", a.static())
	return securityHeaders(mux)
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "same-origin")
		h.Set("Content-Security-Policy",
			"default-src 'self'; img-src 'self' data:; style-src 'self' 'unsafe-inline'; frame-ancestors 'none'")
		next.ServeHTTP(w, r)
	})
}

// gzipMin is the smallest JSON body worth compressing.
const gzipMin = 1024

// writeJSON writes v as JSON, gzipped when it is large and the client
// accepts it. Browse and search results compress very well.
func writeJSON(w http.ResponseWriter, r *http.Request, status int, v any) {
	body, err := json.Marshal(v)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "encoding response: "+err.Error())
		return
	}
	h := w.Header()
	h.Set("Content-Type", "application/json")
	h.Set("Cache-Control", "no-store")
	h.Add("Vary", "Accept-Encoding")
	if len(body) >= gzipMin && r != nil && acceptsGzip(r) {
		var buf bytes.Buffer
		zw, _ := gzip.NewWriterLevel(&buf, gzip.BestSpeed)
		zw.Write(body)
		zw.Close()
		body = buf.Bytes()
		h.Set("Content-Encoding", "gzip")
	}
	w.WriteHeader(status)
	w.Write(body)
}

func acceptsGzip(r *http.Request) bool {
	for _, part := range strings.Split(r.Header.Get("Accept-Encoding"), ",") {
		enc, q, _ := strings.Cut(strings.TrimSpace(part), ";")
		if strings.EqualFold(strings.TrimSpace(enc), "gzip") && strings.TrimSpace(q) != "q=0" {
			return true
		}
	}
	return false
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, nil, status, map[string]string{"error": msg})
}

// readJSON decodes a small JSON request body into v.
func readJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10))
	if err := dec.Decode(v); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return false
	}
	return true
}
