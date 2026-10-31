// Package api serves the REST API (/api/v1), the live event stream and the
// embedded web UI.
package api

import (
	"bytes"
	"compress/gzip"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"net/netip"
	"strings"
	"sync/atomic"
	"time"

	"github.com/kiesxoxo/slskdpp/internal/client"
	"github.com/kiesxoxo/slskdpp/internal/config"
	"github.com/kiesxoxo/slskdpp/internal/events"
	"github.com/kiesxoxo/slskdpp/internal/hooks"
	"github.com/kiesxoxo/slskdpp/internal/logs"
	"github.com/kiesxoxo/slskdpp/internal/protocol"
	"github.com/kiesxoxo/slskdpp/internal/search"
	"github.com/kiesxoxo/slskdpp/internal/shares"
	"github.com/kiesxoxo/slskdpp/internal/transfers"
	"github.com/kiesxoxo/slskdpp/internal/wishlist"
)

// Network is the part of the Soulseek client the API uses.
type Network interface {
	Username() string
	LoggedIn() bool
	Browse(ctx context.Context, username string) (protocol.SharedFileListResponse, error)
	FolderContents(ctx context.Context, username, folder string) (protocol.FolderContentsResponse, error)
	UserInfo(ctx context.Context, username string) (protocol.UserInfoResponse, error)
	DistribParent() string
	SetListen(ip string, port uint32) error
	ListenAddr() (string, uint32)
	UserStatus(ctx context.Context, username string) (protocol.UserStatusResponse, error)
	PeerAddress(ctx context.Context, username string) (protocol.PeerAddress, error)
	BrowseStatus(username string) (client.BrowseProgress, bool)
	ServerStatus() client.ServerStatus
	Connect() bool
	Disconnect()
}

type Config struct {
	// Username and Password are the web UI login.
	Username string
	Password string
	// SessionKey signs session cookies; see LoadOrCreateKey.
	SessionKey []byte
	// APIKeys are accepted besides the keys created in the UI.
	APIKeys []string
	// TrustedProxies may set X-Forwarded-For; see clientIP. Empty means
	// loopback only.
	TrustedProxies []netip.Prefix
	// DownloadDir and IncompleteDir are reported to slskd clients.
	DownloadDir   string
	IncompleteDir string
	// RemoteFiles lets slskd clients delete files and folders in those
	// directories.
	RemoteFiles bool
	// UI is the built web app. Without an index.html a placeholder page
	// is served.
	UI     fs.FS
	Logger *slog.Logger
}

// Deps are the services the API exposes.
type Deps struct {
	Net       Network
	Searches  *search.Manager
	Downloads *transfers.Manager
	Uploads   *transfers.Uploads
	Shares    *shares.Index
	Hooks     *hooks.Runner
	Wishlist  *wishlist.Manager
	Bus       *events.Bus
	// DB stores API keys; without it only Config.APIKeys work.
	DB *sql.DB
	// Stop shuts the daemon down, or restarts it. Without it the API
	// cannot.
	Stop func(restart bool)
	// Settings is the configuration the daemon started with, shown to
	// slskd clients as their options.
	Settings config.Config
	// Logs serves recent log records and log files, if set.
	Logs LogSource
}

// LogSource is where the daemon's log goes; logs.Handler is one.
type LogSource interface {
	Recent() []logs.Record
	Dir() string
}

type API struct {
	cfg       Config
	log       *slog.Logger
	net       Network
	searches  *search.Manager
	downloads *transfers.Manager
	uploads   *transfers.Uploads
	shares    *shares.Index
	hooks     *hooks.Runner
	wishlist  *wishlist.Manager
	bus       *events.Bus
	stop      func(restart bool)
	settings  config.Config
	logs      LogSource
	keys      *keyring
	v0        v0Searches
	v0Events  *v0Events
	v0Opts    v0OptionsState
	// v0DisconnectMsg is the reason given to the latest DELETE
	// /api/v0/server, for the SoulseekClientDisconnected event.
	v0DisconnectMsg atomic.Pointer[string]
	limiter         *loginLimiter
	now             func() time.Time
}

// New returns the HTTP handler for the API and UI.
func New(cfg Config, d Deps) (http.Handler, error) {
	log := cfg.Logger
	if log == nil {
		log = slog.Default()
	}
	a := &API{
		cfg:       cfg,
		log:       log.With("component", "api"),
		net:       d.Net,
		searches:  d.Searches,
		downloads: d.Downloads,
		uploads:   d.Uploads,
		shares:    d.Shares,
		hooks:     d.Hooks,
		wishlist:  d.Wishlist,
		bus:       d.Bus,
		stop:      d.Stop,
		settings:  d.Settings,
		logs:      d.Logs,
		limiter:   newLoginLimiter(),
		now:       time.Now,
		v0:        v0Searches{byID: map[string]*v0Search{}},
	}
	var err error
	if a.keys, err = newKeyring(d.DB, cfg.APIKeys); err != nil {
		return nil, fmt.Errorf("loading API keys: %w", err)
	}
	if a.v0Events, err = newV0Events(d.DB); err != nil {
		return nil, fmt.Errorf("loading slskd events: %w", err)
	}
	if a.bus != nil && a.downloads != nil {
		go a.watchV0Events(a.subscribeV0Events())
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

	mux.HandleFunc("GET /api/v1/downloads", a.auth(a.listDownloads))
	mux.HandleFunc("POST /api/v1/downloads", a.auth(a.enqueueFiles))
	mux.HandleFunc("POST /api/v1/downloads/folder", a.auth(a.enqueueFolder))
	mux.HandleFunc("POST /api/v1/downloads/clear", a.auth(a.clearDownloads))
	mux.HandleFunc("POST /api/v1/downloads/{id}/cancel", a.auth(a.cancelDownload))
	mux.HandleFunc("POST /api/v1/downloads/{id}/retry", a.auth(a.retryDownload))
	mux.HandleFunc("DELETE /api/v1/downloads/{id}", a.auth(a.removeDownload))

	mux.HandleFunc("GET /api/v1/uploads", a.auth(a.listUploads))
	mux.HandleFunc("POST /api/v1/uploads/clear", a.auth(a.clearUploads))
	mux.HandleFunc("POST /api/v1/uploads/{id}/cancel", a.auth(a.cancelUpload))
	mux.HandleFunc("DELETE /api/v1/uploads/{id}", a.auth(a.removeUpload))

	mux.HandleFunc("GET /api/v1/shares", a.auth(a.listShares))
	mux.HandleFunc("POST /api/v1/shares", a.auth(a.addShare))
	mux.HandleFunc("POST /api/v1/shares/rescan", a.auth(a.rescanShares))
	mux.HandleFunc("DELETE /api/v1/shares/{alias}", a.auth(a.removeShare))
	mux.HandleFunc("GET /api/v1/folders", a.auth(a.localFolders))

	mux.HandleFunc("GET /api/v1/apikeys", a.auth(a.sessionOnly(a.listKeys)))
	mux.HandleFunc("POST /api/v1/apikeys", a.auth(a.sessionOnly(a.createKey)))
	mux.HandleFunc("DELETE /api/v1/apikeys/{id}", a.auth(a.sessionOnly(a.revokeKey)))

	mux.HandleFunc("GET /api/v1/wishlist", a.auth(a.listWishlist))
	mux.HandleFunc("POST /api/v1/wishlist", a.auth(a.addWishlist))
	mux.HandleFunc("PATCH /api/v1/wishlist/{id}", a.auth(a.updateWishlist))
	mux.HandleFunc("DELETE /api/v1/wishlist/{id}", a.auth(a.removeWishlist))
	mux.HandleFunc("POST /api/v1/wishlist/{id}/search", a.auth(a.searchWishlist))

	mux.HandleFunc("GET /api/v1/hooks", a.auth(a.listHooks))
	mux.HandleFunc("POST /api/v1/hooks/test", a.auth(a.testHooks))

	a.routesV0(mux)

	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		writeError(w, http.StatusNotFound, "no such endpoint")
	})
	mux.Handle("/", a.static())
	return securityHeaders(mux), nil
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
