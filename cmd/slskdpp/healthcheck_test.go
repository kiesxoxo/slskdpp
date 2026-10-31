package main

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/kiesxoxo/slskdpp/internal/config"
)

func TestHealthURL(t *testing.T) {
	for listen, want := range map[string]string{
		":5030":          "http://127.0.0.1:5030/slskd/health",
		"0.0.0.0:5030":   "http://127.0.0.1:5030/slskd/health",
		"[::]:5030":      "http://[::1]:5030/slskd/health",
		"10.0.0.2:8080":  "http://10.0.0.2:8080/slskd/health",
		"localhost:5030": "http://localhost:5030/slskd/health",
	} {
		var cfg config.Config
		cfg.Web.Listen, cfg.Web.URLBase = listen, "/slskd"
		if got, err := healthURL(cfg); err != nil || got != want {
			t.Errorf("%s: %s %v, want %s", listen, got, err, want)
		}
	}
}

func TestHealthcheck(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/health" {
			http.NotFound(w, r)
		}
	}))
	var cfg config.Config
	cfg.Web.Listen = srv.Listener.Addr().String()
	if err := healthcheck(cfg); err != nil {
		t.Errorf("healthy: %v", err)
	}
	cfg.Web.URLBase = "/elsewhere"
	if healthcheck(cfg) == nil {
		t.Error("404 counted as healthy")
	}
	srv.Close()
	cfg.Web.URLBase = ""
	if healthcheck(cfg) == nil {
		t.Error("nothing listening counted as healthy")
	}
}
