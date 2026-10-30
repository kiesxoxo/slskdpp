// Command slskdpp is a headless Soulseek client.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/kiesxoxo/slskdpp/internal/client"
	"github.com/kiesxoxo/slskdpp/internal/config"
	"github.com/kiesxoxo/slskdpp/internal/protocol"
)

const usage = `usage: slskdpp [flags] [command]

With no command, slskdpp runs as a daemon.

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

	var c *client.Client
	c = client.New(srvCfg, client.Handlers{
		Server: func(m any) {
			switch m := m.(type) {
			case protocol.AdminMessage:
				log.Info("admin message", "text", m.Text)
			default:
				log.Debug("server message", "type", fmt.Sprintf("%T", m))
			}
		},
		Peer: func(user string, m any) { answerPeer(c, user, m) },
	})
	if err := c.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		fatal(err)
	}
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
