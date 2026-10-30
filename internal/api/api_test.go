package api

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/kiesxoxo/slskdpp/internal/client"
	"github.com/kiesxoxo/slskdpp/internal/events"
	"github.com/kiesxoxo/slskdpp/internal/hooks"
	"github.com/kiesxoxo/slskdpp/internal/protocol"
	"github.com/kiesxoxo/slskdpp/internal/search"
	"github.com/kiesxoxo/slskdpp/internal/shares"
	"github.com/kiesxoxo/slskdpp/internal/store"
	"github.com/kiesxoxo/slskdpp/internal/transfers"
)

type fakeNet struct {
	loggedIn bool
	token    uint32
}

func (n *fakeNet) Username() string            { return "me" }
func (n *fakeNet) LoggedIn() bool              { return n.loggedIn }
func (n *fakeNet) NewToken() uint32            { n.token++; return n.token }
func (n *fakeNet) Search(uint32, string) error { return nil }
func (n *fakeNet) UserInfo(ctx context.Context, u string) (protocol.UserInfoResponse, error) {
	return protocol.UserInfoResponse{Description: "hello from " + u, UploadSlots: 3}, nil
}

func (n *fakeNet) Browse(ctx context.Context, u string) (protocol.SharedFileListResponse, error) {
	if u == "ghost" {
		return protocol.SharedFileListResponse{}, fmt.Errorf("browse: %w", client.ErrUserOffline)
	}
	return protocol.SharedFileListResponse{Dirs: []protocol.Directory{{
		Name:  `@@x\Music\Album`,
		Files: []protocol.File{{Name: `@@x\Music\Album\01.flac`, Size: 100, Attrs: []protocol.FileAttribute{{Code: protocol.AttrDuration, Value: 60}}}},
	}}}, nil
}

// FolderContents lists bare file names, like real clients. "@@x\\Gone"
// cannot be listed.
func (n *fakeNet) FolderContents(ctx context.Context, u, folder string) (protocol.FolderContentsResponse, error) {
	if folder == `@@x\Gone` {
		return protocol.FolderContentsResponse{}, errors.New("peer did not answer")
	}
	return protocol.FolderContentsResponse{Folder: folder, Dirs: []protocol.Directory{
		{Name: folder, Files: []protocol.File{{Name: "01.flac", Size: 10}, {Name: "02.flac", Size: 20}}},
		{Name: folder + `\Scans`, Files: []protocol.File{{Name: "front.jpg", Size: 5}}},
		{Name: `@@x\Elsewhere`, Files: []protocol.File{{Name: "nope.mp3", Size: 1}}},
	}}, nil
}

func (n *fakeNet) SendPeer(context.Context, string, protocol.PeerMessage) error { return nil }
func (n *fakeNet) WatchUser(string) error                                       { return nil }
func (n *fakeNet) ConnectFile(context.Context, string, uint32) (net.Conn, error) {
	return nil, errors.New("no file connections in tests")
}

type env struct {
	srv  *httptest.Server
	net  *fakeNet
	mgr  *search.Manager
	dl   *transfers.Manager
	up   *transfers.Uploads
	idx  *shares.Index
	dir  string
	bus  *events.Bus
	http *http.Client // carries the session cookie once logged in
}

func newEnv(t *testing.T) *env {
	t.Helper()
	e := &env{net: &fakeNet{loggedIn: true}, bus: events.NewBus()}
	e.mgr = search.NewManager(e.net, search.Options{
		OnHit: func(s *search.Search, h search.FolderHit) {
			e.bus.Publish(events.Event{Type: events.TypeSearchHit, Search: s.ID, Data: events.SearchHit{SearchID: s.ID, Hit: h}})
		},
	})
	dir := t.TempDir()
	db, err := store.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	e.dl, err = transfers.New(transfers.Config{DownloadDir: dir, IncompleteDir: dir}, db, e.net, e.mgr)
	if err != nil {
		t.Fatal(err)
	}
	e.dir = dir
	os.MkdirAll(filepath.Join(dir, "music", "Album"), 0o755)
	os.WriteFile(filepath.Join(dir, "music", "Album", "song.mp3"), []byte("abc"), 0o644)
	e.idx, err = shares.Open(shares.Config{Roots: []shares.Root{shares.ParseRoot(filepath.Join(dir, "music"))}}, db)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go e.idx.Run(ctx)
	e.up = transfers.NewUploads(transfers.UploadConfig{}, e.net, e.idx)
	ui := fstest.MapFS{
		"index.html":    {Data: []byte("<!doctype html><title>app</title>")},
		"assets/app.js": {Data: []byte("console.log(1)")},
	}
	h := New(Config{Username: "admin", Password: "secret", SessionKey: []byte("0123456789abcdef0123456789abcdef"), UI: ui}, Deps{Net: e.net, Searches: e.mgr, Downloads: e.dl, Uploads: e.up, Shares: e.idx,
		Hooks: hooks.New(hooks.Config{OnDownload: "echo ok $SLSKDPP_EVENT"}), Bus: e.bus})
	e.srv = httptest.NewServer(h)
	t.Cleanup(e.srv.Close)
	jar, _ := cookiejar.New(nil)
	e.http = &http.Client{Jar: jar}
	return e
}

func (e *env) do(t *testing.T, method, path string, body any) (*http.Response, []byte) {
	t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = strings.NewReader(string(b))
	}
	req, _ := http.NewRequest(method, e.srv.URL+path, rd)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := e.http.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp, b
}

func (e *env) login(t *testing.T) {
	t.Helper()
	if resp, b := e.do(t, "POST", "/api/v1/session", credentials{"admin", "secret"}); resp.StatusCode != 200 {
		t.Fatalf("login: %d %s", resp.StatusCode, b)
	}
}

func TestAuth(t *testing.T) {
	e := newEnv(t)
	if resp, _ := e.do(t, "GET", "/api/v1/server", nil); resp.StatusCode != 401 {
		t.Errorf("unauthenticated: %d", resp.StatusCode)
	}
	if resp, _ := e.do(t, "POST", "/api/v1/session", credentials{"admin", "nope"}); resp.StatusCode != 401 {
		t.Errorf("wrong password: %d", resp.StatusCode)
	}
	resp, _ := e.do(t, "POST", "/api/v1/session", credentials{"admin", "secret"})
	if resp.StatusCode != 200 {
		t.Fatalf("login: %d", resp.StatusCode)
	}
	c := resp.Cookies()[0]
	if !c.HttpOnly || c.SameSite != http.SameSiteStrictMode {
		t.Errorf("cookie flags %+v", c)
	}
	resp, b := e.do(t, "GET", "/api/v1/server", nil)
	if resp.StatusCode != 200 || !strings.Contains(string(b), `"connected":true`) {
		t.Errorf("server state: %d %s", resp.StatusCode, b)
	}

	// A tampered cookie is refused.
	req, _ := http.NewRequest("GET", e.srv.URL+"/api/v1/session", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookie, Value: c.Value[:len(c.Value)-2] + "xx"})
	if resp, _ := http.DefaultClient.Do(req); resp.StatusCode != 401 {
		t.Errorf("tampered cookie: %d", resp.StatusCode)
	}

	// Cross-site writes are refused even with a valid cookie.
	req, _ = http.NewRequest("DELETE", e.srv.URL+"/api/v1/searches/1", nil)
	req.Header.Set("Sec-Fetch-Site", "cross-site")
	if resp, _ := e.http.Do(req); resp.StatusCode != 403 {
		t.Errorf("cross-site write: %d", resp.StatusCode)
	}

	if resp, _ := e.do(t, "DELETE", "/api/v1/session", nil); resp.StatusCode != 204 {
		t.Errorf("logout: %d", resp.StatusCode)
	}
	if resp, _ := e.do(t, "GET", "/api/v1/session", nil); resp.StatusCode != 401 {
		t.Errorf("after logout: %d", resp.StatusCode)
	}
}

func TestLoginRateLimit(t *testing.T) {
	e := newEnv(t)
	for range maxLoginFailures {
		e.do(t, "POST", "/api/v1/session", credentials{"admin", "guess"})
	}
	resp, _ := e.do(t, "POST", "/api/v1/session", credentials{"admin", "secret"})
	if resp.StatusCode != http.StatusTooManyRequests || resp.Header.Get("Retry-After") == "" {
		t.Errorf("after %d failures: %d", maxLoginFailures, resp.StatusCode)
	}
}

func TestSessionExpiry(t *testing.T) {
	a := &API{cfg: Config{Username: "admin", SessionKey: []byte("k")}, now: time.Now}
	tok := a.signSession("admin", time.Now().Add(time.Hour))
	if _, ok := a.verifySession(tok); !ok {
		t.Error("fresh token rejected")
	}
	if _, ok := a.verifySession(a.signSession("admin", time.Now().Add(-time.Second))); ok {
		t.Error("expired token accepted")
	}
	a.cfg.Username = "renamed"
	if _, ok := a.verifySession(tok); ok {
		t.Error("token for old username accepted")
	}
}

func TestSearches(t *testing.T) {
	e := newEnv(t)
	e.login(t)

	resp, b := e.do(t, "POST", "/api/v1/searches", map[string]string{"query": "portishead dummy"})
	if resp.StatusCode != 201 {
		t.Fatalf("start: %d %s", resp.StatusCode, b)
	}
	var sv searchView
	json.Unmarshal(b, &sv)
	if sv.Query != "portishead dummy" || !sv.Active {
		t.Errorf("view %+v", sv)
	}
	e.mgr.HandleResponse("bob", protocol.FileSearchResponse{Token: sv.ID, SlotFree: true, Files: []protocol.File{
		{Name: `@@b\Portishead\Dummy\01.mp3`, Size: 5e6, Attrs: []protocol.FileAttribute{{Code: protocol.AttrBitRate, Value: 320}}},
		{Name: `@@b\Portishead\Dummy\02.mp3`, Size: 5e6, Attrs: []protocol.FileAttribute{{Code: protocol.AttrBitRate, Value: 320}}},
	}})

	base := fmt.Sprintf("/api/v1/searches/%d", sv.ID)
	var hits []search.FolderHit
	_, b = e.do(t, "GET", base+"/hits", nil)
	json.Unmarshal(b, &hits)
	if len(hits) != 1 || hits[0].User != "bob" || hits[0].AlbumKey == "" {
		t.Fatalf("hits %s", b)
	}
	var albums []search.Album
	_, b = e.do(t, "GET", base+"/albums?formats=mp3&min_bitrate=256&free_slot_only=true", nil)
	json.Unmarshal(b, &albums)
	if len(albums) != 1 || len(albums[0].Sources) != 1 {
		t.Errorf("albums %s", b)
	}
	_, b = e.do(t, "GET", base+"/albums?formats=flac", nil)
	if strings.TrimSpace(string(b)) != "[]" {
		t.Errorf("filtered albums %s, want []", b)
	}
	if resp, _ := e.do(t, "GET", base+"/albums?min_files=x", nil); resp.StatusCode != 400 {
		t.Errorf("bad filter: %d", resp.StatusCode)
	}

	_, b = e.do(t, "GET", "/api/v1/searches", nil)
	if !strings.Contains(string(b), `"files":2`) {
		t.Errorf("list %s", b)
	}
	if resp, _ := e.do(t, "DELETE", base, nil); resp.StatusCode != 204 {
		t.Errorf("delete: %d", resp.StatusCode)
	}
	if resp, _ := e.do(t, "GET", base, nil); resp.StatusCode != 404 {
		t.Errorf("after delete: %d", resp.StatusCode)
	}

	e.net.loggedIn = false
	if resp, _ := e.do(t, "POST", "/api/v1/searches", map[string]string{"query": "x"}); resp.StatusCode != 503 {
		t.Errorf("search while offline: %d", resp.StatusCode)
	}
}

func TestEventStream(t *testing.T) {
	e := newEnv(t)
	e.login(t)
	s1, _ := e.mgr.Start("one")
	s2, _ := e.mgr.Start("two")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", fmt.Sprintf("%s/api/v1/events?search=%d", e.srv.URL, s1.ID), nil)
	resp, err := e.http.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("content type %q", ct)
	}
	rd := bufio.NewReader(resp.Body)
	next := func() (typ, data string) {
		for {
			line, err := rd.ReadString('\n')
			if err != nil {
				t.Fatalf("stream: %v", err)
			}
			line = strings.TrimRight(line, "\n")
			switch {
			case strings.HasPrefix(line, "event: "):
				typ = line[7:]
			case strings.HasPrefix(line, "data: "):
				data = line[6:]
			case line == "" && typ != "":
				return typ, data
			}
		}
	}
	if typ, data := next(); typ != events.TypeServerState || !strings.Contains(data, `"connected":true`) {
		t.Errorf("first event %s %s", typ, data)
	}
	// The other search's hit must not reach this stream.
	e.mgr.HandleResponse("x", protocol.FileSearchResponse{Token: s2.ID, Files: []protocol.File{{Name: `@@x\Other\a.mp3`}}})
	e.mgr.HandleResponse("y", protocol.FileSearchResponse{Token: s1.ID, Files: []protocol.File{{Name: `@@y\Mine\a.mp3`}}})
	typ, data := next()
	var hit events.SearchHit
	json.Unmarshal([]byte(data), &hit)
	if typ != events.TypeSearchHit || hit.SearchID != s1.ID || hit.Hit.User != "y" {
		t.Errorf("hit event %s %s", typ, data)
	}
}

func TestUsers(t *testing.T) {
	e := newEnv(t)
	e.login(t)
	resp, b := e.do(t, "GET", "/api/v1/users/bob/browse", nil)
	if resp.StatusCode != 200 || !strings.Contains(string(b), `"name":"01.flac"`) || !strings.Contains(string(b), `"duration":60`) {
		t.Errorf("browse: %d %s", resp.StatusCode, b)
	}
	if resp, _ := e.do(t, "GET", "/api/v1/users/ghost/browse", nil); resp.StatusCode != 404 {
		t.Errorf("offline browse: %d", resp.StatusCode)
	}
	path := `@@x\Music`
	resp, b = e.do(t, "GET", "/api/v1/users/bob/folder?path="+url.QueryEscape(path), nil)
	if resp.StatusCode != 200 || !strings.Contains(string(b), `"folder":"@@x\\Music"`) {
		t.Errorf("folder: %d %s", resp.StatusCode, b)
	}
	if resp, _ := e.do(t, "GET", "/api/v1/users/bob/folder", nil); resp.StatusCode != 400 {
		t.Errorf("folder without path: %d", resp.StatusCode)
	}
	resp, b = e.do(t, "GET", "/api/v1/users/a%20b/info", nil)
	if resp.StatusCode != 200 || !strings.Contains(string(b), "hello from a b") {
		t.Errorf("info: %d %s", resp.StatusCode, b)
	}
}

func TestGzip(t *testing.T) {
	e := newEnv(t)
	e.login(t)
	// Go's client adds Accept-Encoding: gzip and transparently inflates;
	// Uncompressed reports that the server sent it compressed.
	big := strings.Repeat("x", 4096)
	s, _ := e.mgr.Start(big)
	e.mgr.HandleResponse("u", protocol.FileSearchResponse{Token: s.ID, Files: []protocol.File{{Name: `@@u\` + big + `\a.mp3`}}})
	resp, _ := e.do(t, "GET", fmt.Sprintf("/api/v1/searches/%d/hits", s.ID), nil)
	if !resp.Uncompressed {
		t.Error("large response not gzipped")
	}
}

func TestStatic(t *testing.T) {
	e := newEnv(t)
	for path, want := range map[string]string{
		"/":              "<title>app</title>",
		"/search/12":     "<title>app</title>",
		"/assets/app.js": "console.log",
	} {
		resp, b := e.do(t, "GET", path, nil)
		if resp.StatusCode != 200 || !strings.Contains(string(b), want) {
			t.Errorf("%s: %d %s", path, resp.StatusCode, b)
		}
		if path == "/assets/app.js" && !strings.Contains(resp.Header.Get("Cache-Control"), "immutable") {
			t.Errorf("asset cache header %q", resp.Header.Get("Cache-Control"))
		}
	}
	if resp, b := e.do(t, "GET", "/api/v1/nope", nil); resp.StatusCode != 404 || !strings.Contains(string(b), "error") {
		t.Errorf("unknown API path: %d %s", resp.StatusCode, b)
	}

	h := New(Config{}, Deps{Net: e.net, Searches: e.mgr, Downloads: e.dl, Bus: e.bus})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	if rec.Code != 503 || !strings.Contains(rec.Body.String(), "not built") {
		t.Errorf("placeholder: %d %s", rec.Code, rec.Body)
	}
}

func TestDownloads(t *testing.T) {
	e := newEnv(t)
	e.login(t)
	var views []transfers.View

	// A folder download asks the peer for the full listing (bare names,
	// subfolders included) instead of trusting the search's files.
	resp, b := e.do(t, "POST", "/api/v1/downloads/folder", map[string]any{
		"username": "bob", "folder": `@@x\Album`,
		"files": []transfers.FileRequest{{Path: `@@x\Album\01.flac`, Size: 10}},
	})
	if resp.StatusCode != 201 {
		t.Fatalf("folder: %d %s", resp.StatusCode, b)
	}
	json.Unmarshal(b, &views)
	var paths []string
	for _, v := range views {
		paths = append(paths, v.Filename+" -> "+v.Folder+"/"+v.Name)
	}
	want := []string{`@@x\Album\01.flac -> Album/01.flac`, `@@x\Album\02.flac -> Album/02.flac`, `@@x\Album\Scans\front.jpg -> Album/Scans/front.jpg`}
	if strings.Join(paths, "|") != strings.Join(want, "|") {
		t.Errorf("folder files\n got %v\nwant %v", paths, want)
	}

	// When the peer cannot list the folder, the request's files are used.
	resp, b = e.do(t, "POST", "/api/v1/downloads/folder", map[string]any{
		"username": "bob", "folder": `@@x\Gone`,
		"files": []transfers.FileRequest{{Path: `@@x\Gone\a.mp3`, Size: 3}},
	})
	if resp.StatusCode != 201 || !strings.Contains(string(b), `a.mp3`) {
		t.Errorf("fallback: %d %s", resp.StatusCode, b)
	}
	if resp, _ := e.do(t, "POST", "/api/v1/downloads/folder", map[string]any{"username": "bob", "folder": `@@x\Gone`}); resp.StatusCode != 502 {
		t.Errorf("unlistable folder without files: %d", resp.StatusCode)
	}

	resp, b = e.do(t, "POST", "/api/v1/downloads", map[string]any{"username": "carol", "files": []transfers.FileRequest{{Path: `@@c\Single\x.mp3`, Size: 9}}})
	if resp.StatusCode != 201 {
		t.Fatalf("files: %d %s", resp.StatusCode, b)
	}
	json.Unmarshal(b, &views)
	id := views[0].ID

	_, b = e.do(t, "GET", "/api/v1/downloads", nil)
	json.Unmarshal(b, &views)
	if len(views) != 5 {
		t.Errorf("%d downloads listed", len(views))
	}

	base := fmt.Sprintf("/api/v1/downloads/%d", id)
	if resp, _ := e.do(t, "POST", base+"/cancel", nil); resp.StatusCode != 204 {
		t.Errorf("cancel: %d", resp.StatusCode)
	}
	if resp, _ := e.do(t, "POST", base+"/cancel", nil); resp.StatusCode != 409 {
		t.Errorf("cancel twice: %d", resp.StatusCode)
	}
	if resp, _ := e.do(t, "POST", base+"/retry", nil); resp.StatusCode != 204 {
		t.Errorf("retry: %d", resp.StatusCode)
	}
	e.do(t, "POST", base+"/cancel", nil)
	if resp, b := e.do(t, "POST", "/api/v1/downloads/clear", map[string]any{"states": []string{"cancelled"}}); resp.StatusCode != 200 || !strings.Contains(string(b), `"removed":1`) {
		t.Errorf("clear: %d %s", resp.StatusCode, b)
	}
	if resp, _ := e.do(t, "POST", "/api/v1/downloads/clear", map[string]any{"states": []string{"queued"}}); resp.StatusCode != 400 {
		t.Errorf("clearing active downloads: %d", resp.StatusCode)
	}
	if resp, _ := e.do(t, "DELETE", fmt.Sprintf("/api/v1/downloads/%d", views[0].ID), nil); resp.StatusCode != 204 {
		t.Errorf("remove: %d", resp.StatusCode)
	}
	if resp, _ := e.do(t, "DELETE", base, nil); resp.StatusCode != 404 {
		t.Errorf("remove missing: %d", resp.StatusCode)
	}
}

func TestBrowseBuildsFullPaths(t *testing.T) {
	e := newEnv(t)
	e.login(t)
	_, b := e.do(t, "GET", "/api/v1/users/bob/folder?path="+url.QueryEscape(`@@x\Album`), nil)
	if !strings.Contains(string(b), `"path":"@@x\\Album\\01.flac"`) {
		t.Errorf("folder listing paths: %s", b)
	}
}

func TestShares(t *testing.T) {
	e := newEnv(t)
	e.login(t)
	deadline := time.Now().Add(5 * time.Second)
	for e.idx.Stats().Files != 1 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	_, b := e.do(t, "GET", "/api/v1/shares", nil)
	if !strings.Contains(string(b), `"alias":"music"`) || !strings.Contains(string(b), `"files":1`) || !strings.Contains(string(b), `"from_config":true`) {
		t.Errorf("shares: %s", b)
	}

	other := filepath.Join(e.dir, "other")
	os.MkdirAll(other, 0o755)
	if resp, b := e.do(t, "POST", "/api/v1/shares", map[string]string{"path": "relative/dir"}); resp.StatusCode != 400 {
		t.Errorf("relative path: %d %s", resp.StatusCode, b)
	}
	if resp, b := e.do(t, "POST", "/api/v1/shares", map[string]string{"path": other, "alias": "Extra"}); resp.StatusCode != 201 || !strings.Contains(string(b), `"alias":"Extra"`) {
		t.Errorf("add: %d %s", resp.StatusCode, b)
	}
	if resp, _ := e.do(t, "DELETE", "/api/v1/shares/music", nil); resp.StatusCode != 409 {
		t.Errorf("removing config share: %d", resp.StatusCode)
	}
	if resp, _ := e.do(t, "DELETE", "/api/v1/shares/Extra", nil); resp.StatusCode != 204 {
		t.Errorf("remove: %d", resp.StatusCode)
	}
	if resp, _ := e.do(t, "POST", "/api/v1/shares/rescan", nil); resp.StatusCode != 202 {
		t.Errorf("rescan: %d", resp.StatusCode)
	}

	resp, b := e.do(t, "GET", "/api/v1/folders?path="+url.QueryEscape(e.dir), nil)
	if resp.StatusCode != 200 || !strings.Contains(string(b), `"folders":["music","other"]`) {
		t.Errorf("folders: %d %s", resp.StatusCode, b)
	}
}

func TestUploadsEndpoint(t *testing.T) {
	e := newEnv(t)
	e.login(t)
	deadline := time.Now().Add(5 * time.Second)
	for e.idx.Stats().Files != 1 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	e.up.HandlePeer("bob", protocol.QueueUpload{Filename: `music\Album\song.mp3`})
	_, b := e.do(t, "GET", "/api/v1/uploads", nil)
	if !strings.Contains(string(b), `"username":"bob"`) || !strings.Contains(string(b), `"slots":2`) {
		t.Errorf("uploads: %s", b)
	}
	if resp, _ := e.do(t, "POST", "/api/v1/uploads/1/cancel", nil); resp.StatusCode != 204 {
		t.Errorf("cancel: %d", resp.StatusCode)
	}
	if resp, b := e.do(t, "POST", "/api/v1/uploads/clear", nil); !strings.Contains(string(b), `"removed":1`) {
		t.Errorf("clear: %d %s", resp.StatusCode, b)
	}
}

func TestHooksEndpoints(t *testing.T) {
	e := newEnv(t)
	e.login(t)
	if _, b := e.do(t, "GET", "/api/v1/hooks", nil); !strings.Contains(string(b), `"on_download":"echo ok $SLSKDPP_EVENT"`) {
		t.Errorf("hooks: %s", b)
	}
	if _, b := e.do(t, "POST", "/api/v1/hooks/test", nil); !strings.Contains(string(b), `"output":"ok test"`) || !strings.Contains(string(b), `"ok":true`) {
		t.Errorf("test hooks: %s", b)
	}
}
