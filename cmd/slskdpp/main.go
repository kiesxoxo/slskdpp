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
	"syscall"
	"time"

	"github.com/kiesxoxo/slskdpp/internal/api"
	"github.com/kiesxoxo/slskdpp/internal/client"
	"github.com/kiesxoxo/slskdpp/internal/config"
	"github.com/kiesxoxo/slskdpp/internal/events"
	"github.com/kiesxoxo/slskdpp/internal/protocol"
	"github.com/kiesxoxo/slskdpp/internal/search"
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
	var c *client.Client
	var searches *search.Manager
	c = client.New(srvCfg, client.Handlers{
		Server: func(m any) {
			switch m := m.(type) {
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
			answerPeer(c, user, m)
		},
		State: func(loggedIn bool) {
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

	stopWeb, err := serveWeb(ctx, cfg, c, searches, bus, log)
	if err != nil {
		return err
	}
	defer stopWeb()
	return c.Run(ctx)
}

// serveWeb starts the API and UI server and returns a func that stops it.
func serveWeb(ctx context.Context, cfg config.Config, c *client.Client, searches *search.Manager, bus *events.Bus, log *slog.Logger) (func(), error) {
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
	}, c, searches, bus)

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

// answerPeer handles the peer requests every mode answers the same way.
func answerPeer(c *client.Client, user string, m any) {
	switch m.(type) {
	case protocol.UserInfoRequest:
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
			defer cancel()
			c.SendPeer(ctx, user, protocol.UserInfoResponse{Description: "slskdpp"})
		}()
	default:
		slog.Debug("peer message", "user", user, "type", fmt.Sprintf("%T", m))
	}
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
