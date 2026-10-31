package api

import (
	"crypto/subtle"
	"fmt"
	"io"
	"maps"
	"net/http"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/kiesxoxo/slskdpp/internal/version"
)

// MetricsConfig serves Prometheus metrics, as slskd's metrics options do.
type MetricsConfig struct {
	Enabled bool
	Path    string
	// Username and Password, if both set, protect the metrics with basic
	// auth instead of the usual API key or login.
	Username string
	Password string
}

var startTime = time.Now()

// health answers as ASP.NET's health checks do for slskd: 200 with
// Healthy, or Degraded while the Soulseek server is not connected.
func (a *API) health(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	if a.net != nil && !a.net.LoggedIn() {
		io.WriteString(w, "Degraded")
		return
	}
	io.WriteString(w, "Healthy")
}

// metricsAuth checks basic auth if the metrics have their own login, and
// falls back to the usual API key or login otherwise.
func (a *API) metricsAuth(next http.HandlerFunc) http.HandlerFunc {
	m := a.cfg.Metrics
	if m.Username == "" || m.Password == "" {
		return a.auth(next)
	}
	return func(w http.ResponseWriter, r *http.Request) {
		ip := a.clientIP(r)
		if wait := a.limiter.blocked(ip, a.now()); wait > 0 {
			w.Header().Set("Retry-After", strconv.Itoa(int(wait.Seconds())+1))
			writeError(w, http.StatusTooManyRequests, "too many failed logins, try again later")
			return
		}
		user, pass, ok := r.BasicAuth()
		userOK := subtle.ConstantTimeCompare([]byte(user), []byte(m.Username)) == 1
		passOK := subtle.ConstantTimeCompare([]byte(pass), []byte(m.Password)) == 1
		if !ok || !userOK || !passOK {
			if ok {
				a.limiter.fail(ip, a.now())
			}
			w.Header().Set("WWW-Authenticate", `Basic realm="slskdpp metrics"`)
			writeError(w, http.StatusUnauthorized, "metrics need a username and password")
			return
		}
		a.limiter.reset(ip)
		next(w, r)
	}
}

// metrics writes gauges in Prometheus's text format.
func (a *API) metrics(w http.ResponseWriter, r *http.Request) {
	var b strings.Builder
	typed := map[string]bool{}
	gauge := func(name, help string, value float64, labels ...string) {
		if !typed[name] {
			typed[name] = true
			fmt.Fprintf(&b, "# HELP %s %s\n# TYPE %s gauge\n", name, help, name)
		}
		b.WriteString(name)
		if len(labels) > 0 {
			b.WriteByte('{')
			for i := 0; i < len(labels); i += 2 {
				if i > 0 {
					b.WriteByte(',')
				}
				fmt.Fprintf(&b, "%s=%q", labels[i], labels[i+1])
			}
			b.WriteByte('}')
		}
		b.WriteByte(' ')
		b.WriteString(strconv.FormatFloat(value, 'g', -1, 64))
		b.WriteByte('\n')
	}
	bool01 := func(v bool) float64 {
		if v {
			return 1
		}
		return 0
	}
	// byState writes one gauge per state, in a stable order.
	byState := func(name, help string, counts map[string]int) {
		for _, s := range slices.Sorted(maps.Keys(counts)) {
			gauge(name, help, float64(counts[s]), "state", s)
		}
	}

	gauge("slskdpp_info", "The running version.", 1, "version", version.String())
	if a.net != nil {
		gauge("slskdpp_server_connected", "Whether we are logged in to the Soulseek server.", bool01(a.net.LoggedIn()))
	}
	if a.downloads != nil {
		counts, speed := map[string]int{}, uint64(0)
		for _, d := range a.downloads.List() {
			counts[string(d.State)]++
			speed += d.Speed
		}
		byState("slskdpp_downloads", "Downloads by state.", counts)
		gauge("slskdpp_download_speed_bytes", "Download speed, in bytes per second.", float64(speed))
	}
	if a.uploads != nil {
		counts, speed := map[string]int{}, uint64(0)
		for _, u := range a.uploads.List() {
			counts[string(u.State)]++
			speed += u.Speed
		}
		byState("slskdpp_uploads", "Uploads by state.", counts)
		gauge("slskdpp_upload_speed_bytes", "Upload speed, in bytes per second.", float64(speed))
	}
	if a.shares != nil {
		st := a.shares.Stats()
		gauge("slskdpp_shared_files", "Files shared.", float64(st.Files))
		gauge("slskdpp_shared_folders", "Folders shared.", float64(st.Folders))
		gauge("slskdpp_shared_bytes", "Size of the shared files.", float64(st.Bytes))
	}
	if a.searches != nil {
		gauge("slskdpp_searches", "Searches kept in memory.", float64(len(a.searches.List())))
	}

	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	gauge("go_goroutines", "Goroutines that currently exist.", float64(runtime.NumGoroutine()))
	gauge("go_memstats_heap_alloc_bytes", "Heap bytes allocated and in use.", float64(ms.HeapAlloc))
	gauge("go_memstats_sys_bytes", "Bytes obtained from the system.", float64(ms.Sys))
	fmt.Fprintf(&b, "# HELP go_gc_cycles_total Completed GC cycles.\n# TYPE go_gc_cycles_total counter\ngo_gc_cycles_total %d\n", ms.NumGC)
	gauge("process_start_time_seconds", "Start time of the process since the Unix epoch, in seconds.", float64(startTime.Unix()))

	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	io.WriteString(w, b.String())
}
