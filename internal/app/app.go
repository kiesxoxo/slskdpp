// Package app wires slskdpp together: the Soulseek session, searches,
// downloads and uploads, shares, the wishlist, hooks and the web API.
package app

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"github.com/kiesxoxo/slskdpp/internal/version"
	"log/slog"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/kiesxoxo/slskdpp/internal/api"
	"github.com/kiesxoxo/slskdpp/internal/client"
	"github.com/kiesxoxo/slskdpp/internal/config"
	"github.com/kiesxoxo/slskdpp/internal/events"
	"github.com/kiesxoxo/slskdpp/internal/hooks"
	"github.com/kiesxoxo/slskdpp/internal/protocol"
	"github.com/kiesxoxo/slskdpp/internal/search"
	"github.com/kiesxoxo/slskdpp/internal/shares"
	"github.com/kiesxoxo/slskdpp/internal/store"
	"github.com/kiesxoxo/slskdpp/internal/transfers"
	"github.com/kiesxoxo/slskdpp/internal/wishlist"
	"github.com/kiesxoxo/slskdpp/web"
)

// App is a whole slskdpp instance.
type App struct {
	cfg config.Config
	log *slog.Logger
	db  *sql.DB
	bus *events.Bus

	client    *client.Client
	searches  *search.Manager
	downloads *transfers.Manager
	uploads   *transfers.Uploads
	shares    *shares.Index
	wishlist  *wishlist.Manager
	hooks     *hooks.Runner
	respond   *Responder
	handler   http.Handler

	mu      sync.Mutex
	stop    context.CancelFunc // ends Run
	restart bool               // Stop asked for a restart
}

// Stop ends Run, as though its context were cancelled. With restart,
// RestartRequested reports it afterwards so the caller can start over.
func (a *App) Stop(restart bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.restart = a.restart || restart
	if a.stop != nil {
		a.stop()
	}
}

// RestartRequested reports whether Stop asked for a restart.
func (a *App) RestartRequested() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.restart
}

// New builds an instance from cfg. Nothing runs until Run.
func New(cfg config.Config, log *slog.Logger) (*App, error) {
	if log == nil {
		log = slog.Default()
	}
	db, err := store.Open(filepath.Join(cfg.Directories.Data, "slskdpp.db"))
	if err != nil {
		return nil, fmt.Errorf("database: %w", err)
	}
	a := &App{cfg: cfg, log: log, db: db, bus: events.NewBus()}
	if err := a.build(); err != nil {
		db.Close()
		return nil, err
	}
	return a, nil
}

func (a *App) publish(typ string, search uint32, data any) {
	a.bus.Publish(events.Event{Type: typ, Search: search, Data: data})
}

func (a *App) build() error {
	cfg, log := a.cfg, a.log
	a.client = client.New(client.ServerConfig{
		Addr:       cfg.Soulseek.Server,
		Username:   cfg.Soulseek.Username,
		Password:   cfg.Soulseek.Password,
		ListenPort: cfg.Soulseek.ListenPort,
		Logger:     log,
	}, client.Handlers{
		Server: a.onServer,
		Peer:   a.onPeer,
		Search: func(s protocol.SearchRequest) { a.respond.Search(s) },
		File: func(user string, token uint32, conn net.Conn) {
			a.downloads.HandleFileConn(user, token, conn)
		},
		State: func(loggedIn bool) {
			if loggedIn {
				a.downloads.LoggedIn()
			}
			a.publish(events.TypeServerState, 0, events.ServerState{Connected: loggedIn, Username: cfg.Soulseek.Username, Version: version.String()})
		},
	})
	c := a.client

	a.searches = search.NewManager(c, search.Options{
		OnHit: func(s *search.Search, h search.FolderHit) {
			a.publish(events.TypeSearchHit, s.ID, events.SearchHit{SearchID: s.ID, Hit: h})
		},
		OnDone: func(s *search.Search) {
			a.publish(events.TypeSearchDone, s.ID, events.SearchDone{SearchID: s.ID, Stats: s.Stats()})
		},
	})

	a.hooks = hooks.New(hooks.Config{
		OnDownload: cfg.Hooks.OnDownload,
		OnFolder:   cfg.Hooks.OnFolder,
		Webhook:    cfg.Hooks.Webhook,
		Logger:     log,
	})
	var err error
	a.downloads, err = transfers.New(transfers.Config{
		DownloadDir:      cfg.Directories.Downloads,
		IncompleteDir:    cfg.Directories.Incomplete,
		MaxAttempts:      cfg.Transfers.MaxAttempts,
		MaxSources:       cfg.Transfers.MaxSources,
		SpeedLimit:       cfg.Transfers.DownloadSpeedLimit * 1024,
		PerDownloadLimit: cfg.Transfers.PerDownloadSpeedLimit * 1024,
		OnUpdate:         func(v transfers.View) { a.publish(events.TypeDownload, 0, v) },
		OnRemove: func(id int64) {
			a.publish(events.TypeDownloadRemoved, 0, events.DownloadRemoved{ID: id})
		},
		OnComplete: func(v transfers.View, local string) {
			a.publish(events.TypeDownloadCompleted, 0, events.DownloadCompleted{Download: v, Local: local})
			a.hooks.Fire(hooks.Event{Event: hooks.DownloadCompleted, Path: local, Folder: filepath.Dir(local),
				Username: v.Username, Filename: v.Filename, Size: v.Size})
		},
		OnFolderComplete: func(f transfers.FolderDone) {
			a.hooks.Fire(hooks.Event{Event: hooks.FolderCompleted, Path: f.Path, Folder: f.Path,
				Username: strings.Join(f.Users, ","), Size: f.Bytes, Files: f.Completed, Failed: f.Failed})
		},
		Logger: log,
	}, a.db, c, a.searches)
	if err != nil {
		return err
	}

	a.wishlist = wishlist.New(wishlist.Config{
		Interval: time.Duration(cfg.Wishlist.Interval) * time.Minute,
		OnUpdate: func(e wishlist.Entry) { a.publish(events.TypeWishlist, 0, e) },
		Logger:   log,
	}, a.db, a.searches, c, a.downloads)

	roots := make([]shares.Root, 0, len(cfg.Shares))
	for _, s := range cfg.Shares {
		roots = append(roots, shares.ParseRoot(s))
	}
	a.shares, err = shares.Open(shares.Config{
		Roots: roots,
		OnChange: func() {
			st := a.shares.Stats()
			c.SetShared(uint32(st.Folders), uint32(st.Files))
			a.publish(events.TypeShares, 0, st)
		},
		Logger: log,
	}, a.db)
	if err != nil {
		return fmt.Errorf("shares: %w", err)
	}
	st := a.shares.Stats()
	c.SetShared(uint32(st.Folders), uint32(st.Files))

	a.uploads = transfers.NewUploads(transfers.UploadConfig{
		Slots:            cfg.Transfers.UploadSlots,
		MaxQueuedPerUser: cfg.Transfers.MaxQueuedPerUser,
		SpeedLimit:       cfg.Transfers.UploadSpeedLimit * 1024,
		OnUpdate:         func(v transfers.UploadView) { a.publish(events.TypeUpload, 0, v) },
		OnRemove: func(id int64) {
			a.publish(events.TypeUploadRemoved, 0, events.DownloadRemoved{ID: id})
		},
		OnComplete: func(v transfers.UploadView, local string) {
			a.publish(events.TypeUploadCompleted, 0, events.UploadCompleted{Upload: v, Local: local})
		},
		OnSpeed: func(speed uint32) { c.Server().Send(protocol.SendUploadSpeed{Speed: speed}) },
		Logger:  log,
	}, c, a.shares)
	a.respond = NewResponder(c, a.shares, a.uploads)

	password := cfg.Web.Password
	if password == "" {
		password = api.RandomPassword()
		log.Warn("web.password is not set; using a random password for this run",
			"username", cfg.Web.Username, "password", password)
	}
	key, err := api.LoadOrCreateKey(filepath.Join(cfg.Directories.Data, "session.key"))
	if err != nil {
		return fmt.Errorf("session key: %w", err)
	}
	proxies, err := api.ParseProxies(cfg.Web.TrustedProxies)
	if err != nil {
		return err
	}
	a.handler, err = api.New(api.Config{
		Username:       cfg.Web.Username,
		Password:       password,
		SessionKey:     key,
		APIKeys:        cfg.Web.APIKeys,
		TrustedProxies: proxies,
		DownloadDir:    cfg.Directories.Downloads,
		IncompleteDir:  cfg.Directories.Incomplete,
		RemoteFiles:    cfg.Web.RemoteFileManagement,
		UI:             web.Dist(),
		Logger:         log,
	}, api.Deps{
		Net: c, Searches: a.searches, Downloads: a.downloads, Uploads: a.uploads, Shares: a.shares,
		Hooks: a.hooks, Wishlist: a.wishlist, Bus: a.bus, DB: a.db, Stop: a.Stop,
		Settings: cfg, Logs: logSource(log),
	})
	return err
}

func (a *App) onServer(m any) {
	a.downloads.HandleServer(m)
	switch m := m.(type) {
	case protocol.ExcludedSearchPhrases:
		a.shares.SetExcluded(m.Phrases)
	case protocol.WishlistInterval:
		a.wishlist.SetServerInterval(time.Duration(m.Seconds) * time.Second)
	case protocol.AdminMessage:
		a.log.Info("admin message", "text", m.Text)
	default:
		a.log.Debug("server message", "type", fmt.Sprintf("%T", m))
	}
}

func (a *App) onPeer(user string, m any) {
	if r, ok := m.(protocol.FileSearchResponse); ok {
		a.searches.HandleResponse(user, r)
		return
	}
	if a.downloads.HandlePeer(user, m) || a.uploads.HandlePeer(user, m) {
		return
	}
	a.respond.Peer(user, m)
}

// Handler serves the web UI and API.
func (a *App) Handler() http.Handler { return a.handler }

// logSource is log's handler if it keeps records for the API.
func logSource(log *slog.Logger) api.LogSource {
	if src, ok := log.Handler().(api.LogSource); ok {
		return src
	}
	return nil
}

// WaitLoggedIn blocks until the Soulseek session is up or ctx is done.
func (a *App) WaitLoggedIn(ctx context.Context) error { return a.client.Server().WaitLoggedIn(ctx) }

// Run runs the instance until ctx is done or the server rejects the
// login for good, then stops everything and closes the database. The web
// UI is served on web.listen unless it is empty.
func (a *App) Run(ctx context.Context) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	a.mu.Lock()
	a.stop = cancel
	a.mu.Unlock()
	var wg sync.WaitGroup
	wg.Go(func() { a.downloads.Run(ctx) })
	wg.Go(func() { a.uploads.Run(ctx) })
	wg.Go(func() { a.shares.Run(ctx) })
	wg.Go(func() { a.wishlist.Run(ctx, a.client.Server().WaitLoggedIn) })
	defer func() {
		cancel()
		wg.Wait()
		a.db.Close()
	}()
	if a.cfg.Web.Listen != "" {
		stop, err := a.serveWeb(ctx)
		if err != nil {
			return err
		}
		defer stop()
	}
	return a.client.Run(ctx)
}

// serveWeb starts the web server and returns a func that stops it.
func (a *App) serveWeb(ctx context.Context) (func(), error) {
	ln, err := net.Listen("tcp", a.cfg.Web.Listen)
	if err != nil {
		return nil, fmt.Errorf("web listener: %w", err)
	}
	srv := &http.Server{
		Handler:           a.handler,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       2 * time.Minute,
		// Requests end with the daemon, which also closes event streams.
		BaseContext: func(net.Listener) context.Context { return ctx },
		ErrorLog:    slog.NewLogLogger(a.log.Handler(), slog.LevelDebug),
	}
	a.log.Info("web UI listening", "addr", ln.Addr())
	go func() {
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			a.log.Error("web server stopped", "err", err)
		}
	}()
	return func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		srv.Shutdown(ctx)
	}, nil
}
