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

// album writes a test album of three FLACs under music and returns
// their contents by name.
func album(t *testing.T, music string) map[string][]byte {
	t.Helper()
	dir := filepath.Join(music, "Artist", "Testalbum")
	os.MkdirAll(dir, 0o755)
	originals := map[string][]byte{}
	for i, secs := range []uint64{61, 62, 63} {
		name := fmt.Sprintf("0%d - Track.flac", i+1)
		data := flac(secs, 300_000+i)
		originals[name] = data
		os.WriteFile(filepath.Join(dir, name), data, 0o644)
	}
	return originals
}

// pair starts a sharer with the test album and a fetcher.
func pair(t *testing.T, ctx context.Context, fetcherCfg func(*config.Config)) (sharer, fetcher *instance, originals map[string][]byte) {
	t.Helper()
	ts, err := testserver.Start()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ts.Close() })
	music := t.TempDir()
	originals = album(t, music)
	sharer = start(t, ctx, ts.Addr(), "sharer", func(c *config.Config) {
		c.Shares = []string{"Music=" + music}
	})
	fetcher = start(t, ctx, ts.Addr(), "fetcher", fetcherCfg)
	eventually(t, "the sharer's index", 10*time.Second, func() bool {
		var r struct{ Stats struct{ Files int } }
		sharer.call("GET", "/api/v1/shares", nil, &r)
		return r.Stats.Files == 3
	})
	eventually(t, "both on the server", 5*time.Second, func() bool { return ts.Online("sharer") && ts.Online("fetcher") })
	return sharer, fetcher, originals
}

// TestEndToEnd runs two complete instances against a test server: one
// shares an album, the other finds it, browses it and downloads it.
func TestEndToEnd(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	hookOut := filepath.Join(t.TempDir(), "hook.txt")
	sharer, fetcher, originals := pair(t, ctx, func(c *config.Config) {
		c.Hooks.OnFolder = `echo "$SLSKDPP_EVENT $SLSKDPP_FILES $SLSKDPP_USERNAME" > "` + hookOut + `"`
	})

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

// TestSlskdCompat walks through what soularr does with slskd's API:
// search, read responses, list the directory, enqueue, poll each file,
// clear completed.
func TestSlskdCompat(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	_, fetcher, originals := pair(t, ctx, nil)

	var version string
	fetcher.call("GET", "/api/v0/application/version", nil, &version)
	if version != compatVersionForTest() {
		t.Errorf("version %q", version)
	}

	id := "6f1c1d0e-4b0a-11ef-9a4f-0242ac120002"
	var state map[string]any
	if code := fetcher.call("POST", "/api/v0/searches", map[string]any{"id": id, "searchText": "artist testalbum", "searchTimeout": 1500}, &state); code != 200 || state["id"] != id || state["state"] != "InProgress" {
		t.Fatalf("search: %d %v", code, state)
	}
	eventually(t, "the search to finish", 10*time.Second, func() bool {
		fetcher.call("GET", "/api/v0/searches/"+id+"?includeResponses=false", nil, &state)
		return state["state"] != "InProgress"
	})
	var responses []struct {
		Username          string
		HasFreeUploadSlot bool
		Files             []struct {
			Filename   string
			Size       uint64
			SampleRate int
			BitDepth   int
			Length     int
		}
	}
	fetcher.call("GET", "/api/v0/searches/"+id+"/responses", nil, &responses)
	if len(responses) != 1 || responses[0].Username != "sharer" || len(responses[0].Files) != 3 {
		t.Fatalf("responses %+v", responses)
	}
	f := responses[0].Files[0]
	if f.SampleRate != 44100 || f.BitDepth != 16 || f.Length != 61 || !strings.HasPrefix(f.Filename, `Music\Artist\Testalbum\`) {
		t.Errorf("file %+v", f)
	}

	// soularr lists the directory (bare names, as a list) and queues it
	// with full paths.
	dir := f.Filename[:strings.LastIndex(f.Filename, `\`)]
	var dirs []struct {
		Name  string
		Files []struct {
			Filename string
			Size     uint64
		}
	}
	fetcher.call("POST", "/api/v0/users/sharer/directory", map[string]string{"directory": dir}, &dirs)
	if len(dirs) != 1 || len(dirs[0].Files) != 3 || dirs[0].Files[0].Filename != "01 - Track.flac" {
		t.Fatalf("directory %+v", dirs)
	}
	var queue []map[string]any
	for _, df := range dirs[0].Files {
		queue = append(queue, map[string]any{"filename": dir + `\` + df.Filename, "size": df.Size})
	}
	if code := fetcher.call("POST", "/api/v0/transfers/downloads/sharer", queue, nil); code != 201 {
		t.Fatalf("enqueue: %d", code)
	}
	var transfer struct {
		Username    string
		Directories []struct {
			Directory string
			Files     []struct{ ID, Filename, State string }
		}
	}
	fetcher.call("GET", "/api/v0/transfers/downloads/sharer", nil, &transfer)
	if len(transfer.Directories) != 1 || transfer.Directories[0].Directory != dir || len(transfer.Directories[0].Files) != 3 {
		t.Fatalf("transfers %+v", transfer)
	}
	for _, tf := range transfer.Directories[0].Files {
		eventually(t, tf.Filename, 20*time.Second, func() bool {
			var st struct{ State string }
			fetcher.call("GET", "/api/v0/transfers/downloads/sharer/"+tf.ID, nil, &st)
			return st.State == "Completed, Succeeded"
		})
	}
	// Where soularr looks: downloads/<last folder>/<file name>.
	for name, want := range originals {
		got, _ := os.ReadFile(filepath.Join(fetcher.dir, "downloads", "Testalbum", name))
		if !bytes.Equal(got, want) {
			t.Errorf("%s not downloaded intact", name)
		}
	}
	if code := fetcher.call("DELETE", "/api/v0/transfers/downloads/all/completed", nil, nil); code != 204 {
		t.Errorf("clear completed: %d", code)
	}
	var all []any
	fetcher.call("GET", "/api/v0/transfers/downloads", nil, &all)
	if len(all) != 0 {
		t.Errorf("downloads after clearing %v", all)
	}
	if code := fetcher.call("DELETE", "/api/v0/searches/"+id, nil, nil); code != 204 {
		t.Errorf("delete search: %d", code)
	}
}

func compatVersionForTest() string { return "0.24.0" }

// TestTubifarryCompat walks through what Lidarr's Tubifarry plugin does:
// read the options, queue an album as a batch into a folder it names,
// poll the transfers, and learn from the event log where files landed.
func TestTubifarryCompat(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	sharer, fetcher, originals := pair(t, ctx, nil)
	downloads := filepath.Join(fetcher.dir, "downloads")

	var options struct {
		Directories struct{ Downloads string }
		Transfers   struct {
			Download struct{ Destination struct{ Subdirectory string } }
		}
	}
	fetcher.call("GET", "/api/v0/options", nil, &options)
	if options.Directories.Downloads != downloads || options.Transfers.Download.Destination.Subdirectory != "${SOURCE_DIRECTORY}" {
		t.Errorf("options %+v", options)
	}
	type event struct{ ID, Timestamp, Type, Data string }
	var events []event
	if code := fetcher.call("GET", "/api/v0/events?offset=0&limit=50", nil, &events); code != 200 || len(events) != 1 || events[0].Type != "SoulseekClientConnected" {
		t.Fatalf("events before downloading: %d %v", code, events)
	}

	id := "0b6f7c2a-9d1e-4f3b-8a5c-6e7d8f9a0b1c"
	dir := `Music\Artist\Testalbum`
	var files []map[string]any
	for name, data := range originals {
		files = append(files, map[string]any{"filename": dir + `\` + name, "size": len(data)})
	}
	batch := map[string]any{"id": id, "username": "sharer", "files": files,
		"options": map[string]any{"externalId": "lidarr-1", "destination": "Artist - Testalbum (2026)"}}
	if code := fetcher.call("POST", "/api/v0/transfers/downloads/batches", batch, nil); code != 201 {
		t.Fatalf("batch: %d", code)
	}
	var transfer struct {
		Directories []struct {
			Files []struct{ ID, State, BatchID, StartedAt, ElapsedTime string }
		}
	}
	eventually(t, "the batch to download", 20*time.Second, func() bool {
		fetcher.call("GET", "/api/v0/transfers/downloads/sharer", nil, &transfer)
		if len(transfer.Directories) != 1 || len(transfer.Directories[0].Files) != 3 {
			return false
		}
		for _, f := range transfer.Directories[0].Files {
			if f.State != "Completed, Succeeded" {
				return false
			}
		}
		return true
	})
	for _, f := range transfer.Directories[0].Files {
		if f.BatchID != id || f.StartedAt == "" || f.ElapsedTime == "" {
			t.Errorf("transfer %+v", f)
		}
	}
	local := filepath.Join(downloads, "Artist - Testalbum (2026)")
	for name, want := range originals {
		if got, _ := os.ReadFile(filepath.Join(local, name)); !bytes.Equal(got, want) {
			t.Errorf("%s not downloaded intact into the destination", name)
		}
	}

	eventually(t, "the completion events", 5*time.Second, func() bool {
		fetcher.call("GET", "/api/v0/events?offset=0&limit=50", nil, &events)
		return len(events) == 5
	})
	events = events[:4] // the connection came first
	// Newest first: the directory, then its files.
	if events[0].Type != "DownloadDirectoryComplete" {
		t.Fatalf("events %+v", events)
	}
	var d struct{ LocalDirectoryName, RemoteDirectoryName, Username string }
	json.Unmarshal([]byte(events[0].Data), &d)
	if d.LocalDirectoryName != local || d.RemoteDirectoryName != dir || d.Username != "sharer" {
		t.Errorf("directory event %s", events[0].Data)
	}
	for _, e := range events[1:] {
		var f struct {
			LocalFilename  string
			RemoteFilename string
			Transfer       struct{ Username, BatchID string }
		}
		json.Unmarshal([]byte(e.Data), &f)
		want, ok := originals[filepath.Base(f.LocalFilename)]
		got, _ := os.ReadFile(f.LocalFilename)
		if e.Type != "DownloadFileComplete" || !ok || !bytes.Equal(got, want) || filepath.Dir(f.LocalFilename) != local ||
			!strings.HasPrefix(f.RemoteFilename, dir+`\`) || f.Transfer.Username != "sharer" || f.Transfer.BatchID != id {
			t.Errorf("file event %s %s", e.Type, e.Data)
		}
	}

	var got struct {
		Options struct{ ExternalID, Destination string }
	}
	if code := fetcher.call("GET", "/api/v0/transfers/downloads/batches/"+id, nil, &got); code != 200 ||
		got.Options.Destination != "Artist - Testalbum (2026)" || got.Options.ExternalID != "lidarr-1" {
		t.Errorf("batch: %d %+v", code, got)
	}

	// The sharer logged each file it sent.
	var sent []event
	eventually(t, "the sharer's upload events", 5*time.Second, func() bool {
		sharer.call("GET", "/api/v0/events", nil, &sent)
		return len(sent) == 4
	})
	for _, e := range sent[:3] {
		var u struct {
			LocalFilename string
			Transfer      struct{ Direction, Username string }
		}
		json.Unmarshal([]byte(e.Data), &u)
		if _, ok := originals[filepath.Base(u.LocalFilename)]; e.Type != "UploadFileComplete" || !ok || u.Transfer.Direction != "Upload" || u.Transfer.Username != "fetcher" {
			t.Errorf("upload event %s %s", e.Type, e.Data)
		}
	}
}

// TestSlskdCompatMore covers the rest of the slskd API tools may use:
// uploads, user details, our shares and the server connection.
func TestSlskdCompatMore(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	sharer, fetcher, originals := pair(t, ctx, nil)
	dir := `Music\Artist\Testalbum`

	// Shares, as the sharer lists them.
	var shares map[string][]struct {
		ID, Alias, RemotePath string
		Files, Directories    int
	}
	sharer.call("GET", "/api/v0/shares", nil, &shares)
	if len(shares["local"]) != 1 || shares["local"][0].Alias != "Music" || shares["local"][0].Files != 3 {
		t.Fatalf("shares %+v", shares)
	}
	var contents []struct {
		Name  string
		Files []struct{ Filename string }
	}
	sharer.call("GET", "/api/v0/shares/"+shares["local"][0].ID+"/contents", nil, &contents)
	var names []string
	for _, d := range contents {
		names = append(names, fmt.Sprintf("%s:%d", d.Name, len(d.Files)))
	}
	if got := strings.Join(names, " "); got != `Music:0 Music\Artist:0 Music\Artist\Testalbum:3` {
		t.Errorf("contents %s", got)
	} else if !strings.HasPrefix(contents[2].Files[0].Filename, dir+`\`) {
		t.Errorf("file %+v", contents[2].Files[0])
	}

	// User details, as the fetcher sees the sharer.
	var status struct {
		Presence     string
		IsPrivileged bool
	}
	if code := fetcher.call("GET", "/api/v0/users/sharer/status", nil, &status); code != 200 || status.Presence != "Online" {
		t.Errorf("status: %d %+v", code, status)
	}
	var ep struct {
		Address string
		Port    int
	}
	if code := fetcher.call("GET", "/api/v0/users/sharer/endpoint", nil, &ep); code != 200 || ep.Port != int(sharer.app.cfg.Soulseek.ListenPort) {
		t.Errorf("endpoint: %d %+v", code, ep)
	}
	var info struct{ UploadSlots int }
	if code := fetcher.call("GET", "/api/v0/users/sharer/info", nil, &info); code != 200 || info.UploadSlots == 0 {
		t.Errorf("info: %d %+v", code, info)
	}
	if code := fetcher.call("GET", "/api/v0/users/sharer/browse", nil, nil); code != 200 {
		t.Fatalf("browse: %d", code)
	}
	var progress struct {
		BytesTransferred, Size int
		PercentComplete        float64
	}
	if code := fetcher.call("GET", "/api/v0/users/sharer/browse/status", nil, &progress); code != 200 || progress.PercentComplete != 100 || progress.Size == 0 {
		t.Errorf("browse status: %d %+v", code, progress)
	}

	// Uploads, as the sharer sees them.
	var queue []map[string]any
	for name, data := range originals {
		queue = append(queue, map[string]any{"filename": dir + `\` + name, "size": len(data)})
	}
	if code := fetcher.call("POST", "/api/v0/transfers/downloads/sharer", queue, nil); code != 201 {
		t.Fatalf("enqueue: %d", code)
	}
	var uploads []struct {
		Username    string
		Directories []struct {
			Directory string
			Files     []struct{ ID, State, Direction string }
		}
	}
	eventually(t, "the uploads", 20*time.Second, func() bool {
		sharer.call("GET", "/api/v0/transfers/uploads", nil, &uploads)
		if len(uploads) != 1 || len(uploads[0].Directories) != 1 || len(uploads[0].Directories[0].Files) != 3 {
			return false
		}
		for _, f := range uploads[0].Directories[0].Files {
			if f.State != "Completed, Succeeded" {
				return false
			}
		}
		return true
	})
	up := uploads[0].Directories[0].Files[0]
	if uploads[0].Username != "fetcher" || uploads[0].Directories[0].Directory != dir || up.Direction != "Upload" {
		t.Errorf("uploads %+v", uploads)
	}
	if code := sharer.call("GET", "/api/v0/transfers/uploads/fetcher/"+up.ID, nil, nil); code != 200 {
		t.Errorf("one upload: %d", code)
	}
	if code := sharer.call("DELETE", "/api/v0/transfers/uploads/all/completed", nil, nil); code != 204 {
		t.Errorf("clear uploads: %d", code)
	}
	if code := sharer.call("GET", "/api/v0/transfers/uploads/fetcher", nil, nil); code != 404 {
		t.Errorf("uploads after clearing: %d", code)
	}

	// The server connection.
	type server struct {
		State                       string
		IsLoggedIn, IsTransitioning bool
		IPEndPoint                  *struct{ Port int }
	}
	var srv server
	fetcher.call("GET", "/api/v0/server", nil, &srv)
	if !srv.IsLoggedIn || srv.State != "Connected, LoggedIn" || srv.IPEndPoint == nil {
		t.Errorf("server %+v", srv)
	}
	if code := fetcher.call("DELETE", "/api/v0/server", "maintenance", nil); code != 204 {
		t.Fatalf("disconnect: %d", code)
	}
	eventually(t, "the disconnection", 5*time.Second, func() bool {
		srv = server{}
		fetcher.call("GET", "/api/v0/server", nil, &srv)
		return srv.State == "Disconnected"
	})
	if code := fetcher.call("PUT", "/api/v0/server", nil, nil); code != 200 {
		t.Fatalf("connect: %d", code)
	}
	eventually(t, "the reconnection", 10*time.Second, func() bool {
		fetcher.call("GET", "/api/v0/server", nil, &srv)
		return srv.IsLoggedIn
	})
	var events []struct{ Type, Data string }
	eventually(t, "the connection events", 5*time.Second, func() bool {
		fetcher.call("GET", "/api/v0/events?limit=2", nil, &events)
		return len(events) == 2 && events[0].Type == "SoulseekClientConnected"
	})
	if events[1].Type != "SoulseekClientDisconnected" || !strings.Contains(events[1].Data, `"message":"maintenance"`) {
		t.Errorf("events %+v", events)
	}
}

func TestStopAndRestart(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	ts, err := testserver.Start()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ts.Close() })
	in := start(t, ctx, ts.Addr(), "stopper", nil)
	if in.app.RestartRequested() {
		t.Fatal("restart requested before Stop")
	}
	// Asked through the API, with a login, as slskd requires.
	var tok struct{ Token string }
	req, _ := http.NewRequest("POST", in.srv.URL+"/api/v0/session", strings.NewReader(`{"username":"slskdpp","password":"pw"}`))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	json.NewDecoder(resp.Body).Decode(&tok)
	resp.Body.Close()
	req, _ = http.NewRequest("PUT", in.srv.URL+"/api/v0/application", nil)
	req.Header.Set("Authorization", "Bearer "+tok.Token)
	if resp, err := http.DefaultClient.Do(req); err != nil || resp.StatusCode != 204 {
		t.Fatalf("restart: %v %v", resp, err)
	}
	eventually(t, "the restart request", 5*time.Second, in.app.RestartRequested)
	// Run has returned: the instance no longer answers its peers' server.
	eventually(t, "the session to end", 5*time.Second, func() bool { return !in.app.client.LoggedIn() })
}
