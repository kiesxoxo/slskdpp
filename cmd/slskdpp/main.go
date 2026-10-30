// Command slskdpp is a headless Soulseek client.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
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
	"github.com/kiesxoxo/slskdpp/web"
)

const usage = `usage: slskdpp [flags] [command]

With no command, slskdpp runs as a daemon with the web UI.

Debug commands:
  search [flags] <query>     search the network and print ranked albums
  browse [flags] <user>      list a user's shared folders
  folder <user> <folder>     list the files in one of a user's folders

Flags:
`

func main() {
	configPath := flag.String("config", "slskdpp.yaml", "path to config file")
	checkLogin := flag.Bool("check-login", false, "log in once, print the result and exit")
	flag.Usage = func() {
		fmt.Fprint(flag.CommandLine.Output(), usage)
		flag.PrintDefaults()
	}
	flag.Parse()

	cfg, err := config.Load(*configPath)
	if err != nil {
		fatal(err)
	}
	log := newLogger(cfg.LogLevel)
	slog.SetDefault(log)
	if err := cfg.Validate(); err != nil {
		fatal(err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	srvCfg := client.ServerConfig{
		Addr:       cfg.Soulseek.Server,
		Username:   cfg.Soulseek.Username,
		Password:   cfg.Soulseek.Password,
		ListenPort: cfg.Soulseek.ListenPort,
		Logger:     log,
	}

	if *checkLogin {
		ctx, cancel := context.WithTimeout(ctx, time.Minute)
		defer cancel()
		resp, err := client.CheckLogin(ctx, srvCfg)
		if err != nil {
			fatal(err)
		}
		fmt.Printf("login ok: user=%s ip=%s supporter=%v\ngreeting: %s\n",
			cfg.Soulseek.Username, resp.OwnIP, resp.Supporter, resp.Greeting)
		return
	}

	if args := flag.Args(); len(args) > 0 {
		cmd, ok := commands[args[0]]
		if !ok {
			flag.Usage()
			os.Exit(2)
		}
		if err := cmd(ctx, srvCfg, args[1:]); err != nil {
			fatal(err)
		}
		return
	}

	if err := runDaemon(ctx, cfg, srvCfg, log); err != nil && !errors.Is(err, context.Canceled) {
		fatal(err)
	}
}

// runDaemon runs the Soulseek session and the web UI until ctx is done.
func runDaemon(ctx context.Context, cfg config.Config, srvCfg client.ServerConfig, log *slog.Logger) error {
	bus := events.NewBus()
	db, err := store.Open(filepath.Join(cfg.Directories.Data, "slskdpp.db"))
	if err != nil {
		return fmt.Errorf("database: %w", err)
	}
	defer db.Close()

	var c *client.Client
	var searches *search.Manager
	var downloads *transfers.Manager
	var uploads *transfers.Uploads
	var index *shares.Index
	var respond *responder
	c = client.New(srvCfg, client.Handlers{
		Server: func(m any) {
			downloads.HandleServer(m)
			switch m := m.(type) {
			case protocol.ExcludedSearchPhrases:
				index.SetExcluded(m.Phrases)
			case protocol.AdminMessage:
				log.Info("admin message", "text", m.Text)
			default:
				log.Debug("server message", "type", fmt.Sprintf("%T", m))
			}
		},
		Peer: func(user string, m any) {
			if r, ok := m.(protocol.FileSearchResponse); ok {
				searches.HandleResponse(user, r)
				return
			}
			if downloads.HandlePeer(user, m) || uploads.HandlePeer(user, m) {
				return
			}
			respond.peer(user, m)
		},
		Search: func(s protocol.SearchRequest) { respond.search(s) },
		File: func(user string, token uint32, conn net.Conn) {
			downloads.HandleFileConn(user, token, conn)
		},
		State: func(loggedIn bool) {
			if loggedIn {
				downloads.LoggedIn()
			}
			bus.Publish(events.Event{
				Type: events.TypeServerState,
				Data: events.ServerState{Connected: loggedIn, Username: cfg.Soulseek.Username},
			})
		},
	})
	searches = search.NewManager(c, search.Options{
		OnHit: func(s *search.Search, h search.FolderHit) {
			bus.Publish(events.Event{Type: events.TypeSearchHit, Search: s.ID, Data: events.SearchHit{SearchID: s.ID, Hit: h}})
		},
		OnDone: func(s *search.Search) {
			bus.Publish(events.Event{Type: events.TypeSearchDone, Search: s.ID, Data: events.SearchDone{SearchID: s.ID, Stats: s.Stats()}})
		},
	})

	runner := hooks.New(hooks.Config{
		OnDownload: cfg.Hooks.OnDownload,
		OnFolder:   cfg.Hooks.OnFolder,
		Webhook:    cfg.Hooks.Webhook,
		Logger:     log,
	})
	downloads, err = transfers.New(transfers.Config{
		DownloadDir:      cfg.Directories.Downloads,
		IncompleteDir:    cfg.Directories.Incomplete,
		MaxAttempts:      cfg.Transfers.MaxAttempts,
		MaxSources:       cfg.Transfers.MaxSources,
		SpeedLimit:       cfg.Transfers.DownloadSpeedLimit * 1024,
		PerDownloadLimit: cfg.Transfers.PerDownloadSpeedLimit * 1024,
		OnUpdate: func(v transfers.View) {
			bus.Publish(events.Event{Type: events.TypeDownload, Data: v})
		},
		OnRemove: func(id int64) {
			bus.Publish(events.Event{Type: events.TypeDownloadRemoved, Data: events.DownloadRemoved{ID: id}})
		},
		OnComplete: func(v transfers.View, local string) {
			runner.Fire(hooks.Event{Event: hooks.DownloadCompleted, Path: local, Folder: filepath.Dir(local),
				Username: v.Username, Filename: v.Filename, Size: v.Size})
		},
		OnFolderComplete: func(f transfers.FolderDone) {
			runner.Fire(hooks.Event{Event: hooks.FolderCompleted, Path: f.Path, Folder: f.Path,
				Username: strings.Join(f.Users, ","), Size: f.Bytes, Files: f.Completed, Failed: f.Failed})
		},
		Logger: log,
	}, db, c, searches)
	if err != nil {
		return err
	}
	go downloads.Run(ctx)

	roots := make([]shares.Root, 0, len(cfg.Shares))
	for _, s := range cfg.Shares {
		roots = append(roots, shares.ParseRoot(s))
	}
	index, err = shares.Open(shares.Config{
		Roots: roots,
		OnChange: func() {
			st := index.Stats()
			c.SetShared(uint32(st.Folders), uint32(st.Files))
			bus.Publish(events.Event{Type: events.TypeShares, Data: st})
		},
		Logger: log,
	}, db)
	if err != nil {
		return fmt.Errorf("shares: %w", err)
	}
	st := index.Stats()
	c.SetShared(uint32(st.Folders), uint32(st.Files))
	go index.Run(ctx)

	uploads = transfers.NewUploads(transfers.UploadConfig{
		Slots:            cfg.Transfers.UploadSlots,
		MaxQueuedPerUser: cfg.Transfers.MaxQueuedPerUser,
		SpeedLimit:       cfg.Transfers.UploadSpeedLimit * 1024,
		OnUpdate: func(v transfers.UploadView) {
			bus.Publish(events.Event{Type: events.TypeUpload, Data: v})
		},
		OnRemove: func(id int64) {
			bus.Publish(events.Event{Type: events.TypeUploadRemoved, Data: events.DownloadRemoved{ID: id}})
		},
		OnSpeed: func(speed uint32) { c.Server().Send(protocol.SendUploadSpeed{Speed: speed}) },
		Logger:  log,
	}, c, index)
	go uploads.Run(ctx)
	respond = newResponder(c, index, uploads)

	stopWeb, err := serveWeb(ctx, cfg, api.Deps{Net: c, Searches: searches, Downloads: downloads, Uploads: uploads, Shares: index, Hooks: runner, Bus: bus}, log)
	if err != nil {
		return err
	}
	defer stopWeb()
	return c.Run(ctx)
}

// serveWeb starts the API and UI server and returns a func that stops it.
func serveWeb(ctx context.Context, cfg config.Config, deps api.Deps, log *slog.Logger) (func(), error) {
	password := cfg.Web.Password
	if password == "" {
		password = api.RandomPassword()
		log.Warn("web.password is not set; using a random password for this run",
			"username", cfg.Web.Username, "password", password)
	}
	key, err := api.LoadOrCreateKey(filepath.Join(cfg.Directories.Data, "session.key"))
	if err != nil {
		return nil, fmt.Errorf("session key: %w", err)
	}
	handler := api.New(api.Config{
		Username:   cfg.Web.Username,
		Password:   password,
		SessionKey: key,
		UI:         web.Dist(),
		Logger:     log,
	}, deps)

	ln, err := net.Listen("tcp", cfg.Web.Listen)
	if err != nil {
		return nil, fmt.Errorf("web listener: %w", err)
	}
	srv := &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       2 * time.Minute,
		// Requests end with the daemon, which also closes event streams.
		BaseContext: func(net.Listener) context.Context { return ctx },
		ErrorLog:    slog.NewLogLogger(log.Handler(), slog.LevelDebug),
	}
	log.Info("web UI listening", "addr", ln.Addr())
	go func() {
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("web server stopped", "err", err)
		}
	}()
	return func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		srv.Shutdown(ctx)
	}, nil
}

func newLogger(level string) *slog.Logger {
	var lv slog.Level
	if err := lv.UnmarshalText([]byte(level)); err != nil {
		lv = slog.LevelInfo
	}
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: lv}))
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "slskdpp:", err)
	os.Exit(1)
}
