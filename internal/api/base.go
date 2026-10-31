package api

import (
	"context"
	"net/http"
	"strings"
)

type baseKey struct{}

// withBase serves next under base as well as at the root, as ASP.NET's
// UsePathBase does for slskd's web.url_base: a request under base has it
// stripped, and any other request is served as it is, so a proxy that
// strips the prefix itself and a healthcheck on /health both work.
func withBase(base string, next http.Handler) http.Handler {
	if base == "" {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == base {
			// Relative links in the UI need the trailing slash.
			target := base + "/"
			if r.URL.RawQuery != "" {
				target += "?" + r.URL.RawQuery
			}
			http.Redirect(w, r, target, http.StatusPermanentRedirect)
			return
		}
		rest, ok := strings.CutPrefix(r.URL.Path, base+"/")
		if !ok {
			next.ServeHTTP(w, r)
			return
		}
		r2 := r.WithContext(context.WithValue(r.Context(), baseKey{}, base))
		u := *r.URL
		u.Path = "/" + rest
		u.RawPath = ""
		if raw, ok := strings.CutPrefix(r.URL.RawPath, base+"/"); ok {
			u.RawPath = "/" + raw
		}
		r2.URL = &u
		next.ServeHTTP(w, r2)
	})
}

// cookiePath is where the session cookie applies: the URL base if the
// request came through it, else everything.
func cookiePath(r *http.Request) string {
	if base, ok := r.Context().Value(baseKey{}).(string); ok {
		return base + "/"
	}
	return "/"
}
