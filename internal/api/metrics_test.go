package api

import (
	"io"
	"net/http"
	"regexp"
	"strings"
	"testing"
)

func TestURLBase(t *testing.T) {
	e := newEnvWith(t, func(c *Config, d *Deps) { c.URLBase, d.Settings.Web.URLBase = "/slskd", "/slskd" })
	e.http.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }

	resp, _ := e.do(t, "GET", "/slskd?x=1", nil)
	if resp.StatusCode != http.StatusPermanentRedirect || resp.Header.Get("Location") != "/slskd/?x=1" {
		t.Errorf("bare base: %d %q", resp.StatusCode, resp.Header.Get("Location"))
	}
	if resp, b := e.do(t, "GET", "/slskd/", nil); resp.StatusCode != 200 || !strings.Contains(string(b), "<title>app</title>") {
		t.Errorf("index under base: %d %s", resp.StatusCode, b)
	}
	if resp, b := e.do(t, "GET", "/slskd/assets/app.js", nil); resp.StatusCode != 200 || string(b) != "console.log(1)" {
		t.Errorf("asset under base: %d %s", resp.StatusCode, b)
	}
	// A proxy that strips the prefix itself still works.
	if resp, _ := e.do(t, "GET", "/assets/app.js", nil); resp.StatusCode != 200 {
		t.Errorf("asset at root: %d", resp.StatusCode)
	}

	resp, b := e.do(t, "POST", "/slskd/api/v1/session", credentials{"admin", "secret"})
	if resp.StatusCode != 200 || len(resp.Cookies()) != 1 || resp.Cookies()[0].Path != "/slskd/" {
		t.Fatalf("login under base: %d %s %v", resp.StatusCode, b, resp.Cookies())
	}
	if resp, _ := e.do(t, "GET", "/slskd/api/v0/application", nil); resp.StatusCode != 200 {
		t.Errorf("v0 under base: %d", resp.StatusCode)
	}
	if resp, _ := e.do(t, "GET", "/api/v1/server", nil); resp.StatusCode != 401 {
		t.Errorf("the cookie is for the base only, but got %d at the root", resp.StatusCode)
	}
	if resp, b := e.do(t, "GET", "/slskd/api/v0/options", nil); resp.StatusCode != 200 || !strings.Contains(string(b), `"urlBase":"/slskd"`) {
		t.Errorf("options: %d %s", resp.StatusCode, b)
	}

	resp, _ = e.do(t, "DELETE", "/slskd/api/v1/session", nil)
	paths := []string{}
	for _, c := range resp.Cookies() {
		paths = append(paths, c.Path)
	}
	if strings.Join(paths, ",") != "/,/slskd/" {
		t.Errorf("logout clears %v", paths)
	}
	if resp, _ := e.do(t, "GET", "/slskd/api/v1/server", nil); resp.StatusCode != 401 {
		t.Errorf("after logout: %d", resp.StatusCode)
	}
}

func TestHealth(t *testing.T) {
	e := newEnvWith(t, func(c *Config, d *Deps) { c.URLBase = "/slskd" })
	for _, path := range []string{"/health", "/slskd/health"} {
		if resp, b := e.do(t, "GET", path, nil); resp.StatusCode != 200 || string(b) != "Healthy" ||
			!strings.HasPrefix(resp.Header.Get("Content-Type"), "text/plain") {
			t.Errorf("%s: %d %q", path, resp.StatusCode, b)
		}
	}
	e.net.loggedIn = false
	if resp, b := e.do(t, "GET", "/health", nil); resp.StatusCode != 200 || string(b) != "Degraded" {
		t.Errorf("disconnected: %d %q", resp.StatusCode, b)
	}
}

// promLine is one line of Prometheus's text format.
var promLine = regexp.MustCompile(`^(# (HELP|TYPE) [a-z_]+ .+|[a-z_]+(\{[a-z_]+="[^"]*"(,[a-z_]+="[^"]*")*\})? [-0-9.e+]+)$`)

func TestMetrics(t *testing.T) {
	get := func(e *env, path string, auth func(*http.Request)) (int, string) {
		t.Helper()
		req, _ := http.NewRequest("GET", e.srv.URL+path, nil)
		if auth != nil {
			auth(req)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}
	key := func(r *http.Request) { r.Header.Set("X-API-Key", "config-key-123") }

	off := newEnv(t)
	if code, body := get(off, "/metrics", key); strings.Contains(body, "slskdpp_info") {
		t.Errorf("served while disabled: %d", code)
	}

	e := newEnvWith(t, func(c *Config, d *Deps) { c.Metrics = MetricsConfig{Enabled: true} })
	if code, _ := get(e, "/metrics", nil); code != 401 {
		t.Errorf("no key: %d", code)
	}
	code, body := get(e, "/metrics", key)
	if code != 200 {
		t.Fatalf("with key: %d %s", code, body)
	}
	for _, want := range []string{"slskdpp_info{version=", "slskdpp_server_connected 1", "slskdpp_shared_files ", "go_goroutines ", "# TYPE go_gc_cycles_total counter"} {
		if !strings.Contains(body, want) {
			t.Errorf("no %q in\n%s", want, body)
		}
	}
	for _, line := range strings.Split(strings.TrimSuffix(body, "\n"), "\n") {
		if !promLine.MatchString(line) {
			t.Errorf("not Prometheus format: %q", line)
		}
	}

	basic := newEnvWith(t, func(c *Config, d *Deps) {
		c.Metrics = MetricsConfig{Enabled: true, Path: "/prom", Username: "prom", Password: "pw"}
	})
	if code, _ := get(basic, "/prom", key); code != 401 {
		t.Errorf("API key instead of basic auth: %d", code)
	}
	if code, _ := get(basic, "/prom", func(r *http.Request) { r.SetBasicAuth("prom", "nope") }); code != 401 {
		t.Errorf("wrong password: %d", code)
	}
	if code, body := get(basic, "/prom", func(r *http.Request) { r.SetBasicAuth("prom", "pw") }); code != 200 || !strings.Contains(body, "slskdpp_info") {
		t.Errorf("basic auth: %d", code)
	}
}
