package app

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kiesxoxo/slskdpp/internal/config"
	"github.com/kiesxoxo/slskdpp/internal/search"
	"github.com/kiesxoxo/slskdpp/internal/testserver"
	"github.com/kiesxoxo/slskdpp/internal/transfers"
)

func freePort(t *testing.T) uint32 {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return uint32(ln.Addr().(*net.TCPAddr).Port)
}

// instance is a running App with an HTTP client for its API.
type instance struct {
	t   *testing.T
	app *App
	srv *httptest.Server
	dir string
}

const apiKey = "test-key"

func start(t *testing.T, ctx context.Context, server, name string, edit func(*config.Config)) *instance {
	t.Helper()
	dir := t.TempDir()
	cfg := config.Default()
	cfg.Soulseek.Server = server
	cfg.Soulseek.Username = name
	cfg.Soulseek.Password = "pw"
	cfg.Soulseek.ListenPort = freePort(t)
	cfg.Directories.Data = filepath.Join(dir, "data")
	cfg.Directories.Downloads = filepath.Join(dir, "downloads")
	cfg.Directories.Incomplete = filepath.Join(dir, "incomplete")
	cfg.Web.Listen = ""
	cfg.Web.Password = "pw"
	cfg.Web.APIKeys = []string{apiKey}
	if edit != nil {
		edit(&cfg)
	}
	a, err := New(cfg, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	runErr := make(chan error, 1)
	go func() { runErr <- a.Run(ctx) }()
	t.Cleanup(func() {
		select {
		case <-runErr:
		case <-time.After(10 * time.Second):
			t.Errorf("%s did not stop", name)
		}
	})
	if err := a.WaitLoggedIn(ctx); err != nil {
		t.Fatalf("%s login: %v", name, err)
	}
	srv := httptest.NewServer(a.Handler())
	t.Cleanup(srv.Close)
	return &instance{t: t, app: a, srv: srv, dir: dir}
}

// call does an API request with the API key and decodes the JSON reply.
func (in *instance) call(method, path string, body, out any) int {
	in.t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, in.srv.URL+path, rd)
	req.Header.Set("X-API-Key", apiKey)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		in.t.Fatal(err)
	}
	defer resp.Body.Close()
	if out != nil {
		json.NewDecoder(resp.Body).Decode(out)
	}
	return resp.StatusCode
}

func eventually(t *testing.T, what string, timeout time.Duration, ok func() bool) {
	t.Helper()
	for deadline := time.Now().Add(timeout); !ok(); time.Sleep(50 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
	}
}

// flac is a minimal 44.1 kHz/16-bit FLAC lasting secs.
func flac(secs uint64, audio int) []byte {
	b := append([]byte("fLaC"), 0x80, 0, 0, 34)
	si := make([]byte, 34)
	binary.BigEndian.PutUint64(si[10:], uint64(44100)<<44|uint64(1)<<41|uint64(15)<<36|44100*secs)
	b = append(b, si...)
	for i := range audio {
		b = append(b, byte(i*7))
	}
	return b
}

// TestEndToEnd runs two complete instances against a test server: one
// shares an album, the other finds it, browses it and downloads it.
func TestEndToEnd(t *testing.T) {
	ts, err := testserver.Start()
	if err != nil {
		t.Fatal(err)
	}
	defer ts.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	music := t.TempDir()
	album := filepath.Join(music, "Artist", "Testalbum")
	os.MkdirAll(album, 0o755)
	originals := map[string][]byte{}
	for i, secs := range []uint64{61, 62, 63} {
		name := fmt.Sprintf("0%d - Track.flac", i+1)
		data := flac(secs, 300_000+i)
		originals[name] = data
		os.WriteFile(filepath.Join(album, name), data, 0o644)
	}

	sharer := start(t, ctx, ts.Addr(), "sharer", func(c *config.Config) {
		c.Shares = []string{"Music=" + music}
	})
	hookOut := filepath.Join(t.TempDir(), "hook.txt")
	fetcher := start(t, ctx, ts.Addr(), "fetcher", func(c *config.Config) {
		c.Hooks.OnFolder = `echo "$SLSKDPP_EVENT $SLSKDPP_FILES $SLSKDPP_USERNAME" > "` + hookOut + `"`
	})
	eventually(t, "the sharer's index", 10*time.Second, func() bool {
		var r struct{ Stats struct{ Files int } }
		sharer.call("GET", "/api/v1/shares", nil, &r)
		return r.Stats.Files == 3
	})
	eventually(t, "both on the server", 5*time.Second, func() bool { return ts.Online("sharer") && ts.Online("fetcher") })

	// Search: the sharer answers from its index, with audio details.
	var s struct{ ID uint32 }
	if code := fetcher.call("POST", "/api/v1/searches", map[string]string{"query": "artist testalbum"}, &s); code != 201 {
		t.Fatalf("search: %d", code)
	}
	var hits []search.FolderHit
	eventually(t, "search results", 10*time.Second, func() bool {
		fetcher.call("GET", fmt.Sprintf("/api/v1/searches/%d/hits", s.ID), nil, &hits)
		return len(hits) == 1 && hits[0].FileCount == 3
	})
	hit := hits[0]
	if hit.User != "sharer" || hit.Folder != `Music\Artist\Testalbum` || !hit.Lossless {
		t.Errorf("hit %+v", hit)
	}
	if f := hit.Files[0]; f.Duration != 61 || f.SampleRate != 44100 || f.BitDepth != 16 {
		t.Errorf("audio details %+v", f)
	}

	// Browse.
	var browse struct {
		Dirs []struct {
			Name  string
			Files []search.File
		}
	}
	fetcher.call("GET", "/api/v1/users/sharer/browse", nil, &browse)
	if len(browse.Dirs) != 1 || len(browse.Dirs[0].Files) != 3 || browse.Dirs[0].Files[2].Path != `Music\Artist\Testalbum\03 - Track.flac` {
		t.Errorf("browse %+v", browse)
	}

	// Download the folder; the files arrive intact and the hook runs.
	if code := fetcher.call("POST", "/api/v1/downloads/folder", map[string]string{"username": "sharer", "folder": hit.Folder}, nil); code != 201 {
		t.Fatalf("download: %d", code)
	}
	eventually(t, "downloads to complete", 20*time.Second, func() bool {
		var list []transfers.View
		fetcher.call("GET", "/api/v1/downloads", nil, &list)
		done := 0
		for _, d := range list {
			if d.State == transfers.StateCompleted {
				done++
			}
		}
		return len(list) == 3 && done == 3
	})
	for name, want := range originals {
		got, err := os.ReadFile(filepath.Join(fetcher.dir, "downloads", "Testalbum", name))
		if err != nil || !bytes.Equal(got, want) {
			t.Errorf("%s: %d bytes, %v", name, len(got), err)
		}
	}
	eventually(t, "the folder hook", 5*time.Second, func() bool {
		b, _ := os.ReadFile(hookOut)
		return strings.TrimSpace(string(b)) == "folder.completed 3 sharer"
	})

	var up struct{ Uploads []transfers.UploadView }
	sharer.call("GET", "/api/v1/uploads", nil, &up)
	completed := 0
	for _, u := range up.Uploads {
		if u.State == transfers.UploadCompleted && u.Username == "fetcher" {
			completed++
		}
	}
	if completed != 3 {
		t.Errorf("sharer's uploads %+v", up.Uploads)
	}
}
