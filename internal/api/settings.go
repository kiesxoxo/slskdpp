package api

import (
	"context"
	"net/http"
	"time"

	"github.com/kiesxoxo/slskdpp/internal/hooks"
)

func (a *API) listHooks(w http.ResponseWriter, r *http.Request) {
	cfg := hooks.Config{}
	if a.hooks != nil {
		cfg = a.hooks.Config()
	}
	writeJSON(w, r, http.StatusOK, map[string]string{
		"on_download": cfg.OnDownload,
		"on_folder":   cfg.OnFolder,
		"webhook":     cfg.Webhook,
	})
}

// testHooks runs every configured hook once with a test event.
func (a *API) testHooks(w http.ResponseWriter, r *http.Request) {
	results := []hooks.Result{}
	if a.hooks != nil {
		ctx, cancel := context.WithTimeout(r.Context(), time.Minute)
		defer cancel()
		results = append(results, a.hooks.TestAll(ctx)...)
	}
	writeJSON(w, r, http.StatusOK, results)
}
