package main

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"time"

	"github.com/kiesxoxo/slskdpp/internal/config"
)

// healthURL is the running daemon's /health, reached over loopback when
// it listens on every address.
func healthURL(cfg config.Config) (string, error) {
	host, port, err := net.SplitHostPort(cfg.Web.Listen)
	if err != nil {
		return "", fmt.Errorf("web.listen: %w", err)
	}
	switch host {
	case "", "0.0.0.0":
		host = "127.0.0.1"
	case "::":
		host = "::1"
	}
	return "http://" + net.JoinHostPort(host, port) + cfg.Web.URLBase + "/health", nil
}

// healthcheck asks a running daemon whether it is healthy, for Docker's
// HEALTHCHECK in images without curl.
func healthcheck(cfg config.Config) error {
	url, err := healthURL(cfg)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s: %s", url, resp.Status)
	}
	return nil
}
