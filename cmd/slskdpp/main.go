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

	"github.com/kiesxoxo/slskdpp/internal/app"
	"github.com/kiesxoxo/slskdpp/internal/client"
	"github.com/kiesxoxo/slskdpp/internal/config"
	"github.com/kiesxoxo/slskdpp/internal/logs"
	"github.com/kiesxoxo/slskdpp/internal/version"
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
	showVersion := flag.Bool("version", false, "print the version and exit")
	flag.Usage = func() {
		fmt.Fprint(flag.CommandLine.Output(), usage)
		flag.PrintDefaults()
	}
	flag.Parse()
	if *showVersion {
		fmt.Println("slskdpp", version.String())
		return
	}

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

	// The daemon also logs to files and keeps recent records for the API.
	h, err := logs.New(logs.Config{Level: logLevel(cfg.LogLevel), Dir: logDir(cfg), KeepDays: cfg.LogKeepDays})
	log = slog.New(h)
	slog.SetDefault(log)
	if err != nil {
		log.Warn("cannot write log files; logging to stderr only", "err", err)
	}
	log.Info("starting slskdpp", "version", version.String())
	a, err := app.New(cfg, log)
	if err != nil {
		fatal(err)
	}
	if err := a.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		fatal(err)
	}
	if a.RestartRequested() {
		restart(log)
	}
}

// restart replaces this process with a fresh copy of itself, same
// arguments and environment, once everything has shut down.
func restart(log *slog.Logger) {
	exe, err := os.Executable()
	if err != nil {
		fatal(fmt.Errorf("restarting: %w", err))
	}
	log.Info("restarting slskdpp")
	if err := syscall.Exec(exe, os.Args, os.Environ()); err != nil {
		fatal(fmt.Errorf("restarting: %w", err))
	}
}

func newLogger(level string) *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: logLevel(level)}))
}

func logLevel(level string) slog.Level {
	var lv slog.Level
	if err := lv.UnmarshalText([]byte(level)); err != nil {
		lv = slog.LevelInfo
	}
	return lv
}

func logDir(cfg config.Config) string {
	if !cfg.LogFiles {
		return ""
	}
	return cfg.LogDir()
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "slskdpp:", err)
	os.Exit(1)
}
