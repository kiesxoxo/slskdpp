package api

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

func b64(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }

// filesTree writes downloads/Album/{01.flac,Scans/front.jpg}, a hidden
// file, and a symlink out of the downloads directory.
func filesTree(t *testing.T, e *env) string {
	t.Helper()
	dl := filepath.Join(e.dir, "downloads")
	os.MkdirAll(filepath.Join(dl, "Album", "Scans"), 0o755)
	os.WriteFile(filepath.Join(dl, "Album", "01.flac"), []byte("12345"), 0o644)
	os.WriteFile(filepath.Join(dl, "Album", "Scans", "front.jpg"), []byte("jpg"), 0o644)
	os.WriteFile(filepath.Join(dl, ".hidden"), nil, 0o644)
	outside := filepath.Join(e.dir, "outside")
	os.MkdirAll(outside, 0o755)
	os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("x"), 0o644)
	os.Symlink(outside, filepath.Join(dl, "escape"))
	os.MkdirAll(filepath.Join(e.dir, "incomplete"), 0o755)
	return dl
}

type fsEntry struct {
	Name, FullName, Attributes, CreatedAt, ModifiedAt string
	Length                                            *int64
	Files, Directories                                []fsEntry
}

func names(es []fsEntry) string {
	var out []string
	for _, e := range es {
		out = append(out, e.FullName)
	}
	sort.Strings(out)
	return strings.Join(out, " ")
}

func TestV0FilesList(t *testing.T) {
	e := newEnv(t)
	e.login(t)
	filesTree(t, e)
	get := func(path string) (int, fsEntry) {
		t.Helper()
		resp, b := e.do(t, "GET", path, nil)
		var d fsEntry
		json.Unmarshal(b, &d)
		return resp.StatusCode, d
	}

	code, root := get("/api/v0/files/downloads/directories")
	if code != 200 || root.Name != "downloads" || root.FullName != "" || root.Attributes != "Directory" {
		t.Fatalf("root: %d %+v", code, root)
	}
	// The symlink to a folder outside is listed as a folder, like any other.
	if names(root.Files) != ".hidden" || names(root.Directories) != "Album escape" {
		t.Errorf("root files %q, directories %q", names(root.Files), names(root.Directories))
	}
	if root.Files[0].Attributes != "Hidden" {
		t.Errorf("hidden file attributes %q", root.Files[0].Attributes)
	}
	if _, err := time.Parse(time.RFC3339Nano, root.ModifiedAt); err != nil || !strings.HasSuffix(root.ModifiedAt, "Z") {
		t.Errorf("modifiedAt %q", root.ModifiedAt)
	}

	code, album := get("/api/v0/files/downloads/directories/" + b64("Album"))
	if code != 200 || album.Name != "Album" || names(album.Files) != "01.flac" || names(album.Directories) != "Scans" {
		t.Fatalf("album: %d %+v", code, album)
	}
	if f := album.Files[0]; f.Length == nil || *f.Length != 5 || f.Attributes != "Normal" {
		t.Errorf("file %+v", f)
	}
	if album.Directories[0].Files != nil || album.Directories[0].Length != nil {
		t.Errorf("nested folder carries contents or length: %+v", album.Directories[0])
	}

	// Recursive listings are flat, with paths relative to the folder.
	if _, rec := get("/api/v0/files/downloads/directories/" + b64(`\Album`) + "?recursive=true"); names(rec.Files) != "01.flac Scans/front.jpg" || names(rec.Directories) != "Scans" {
		t.Errorf("recursive files %q, directories %q", names(rec.Files), names(rec.Directories))
	}

	for _, c := range []struct {
		path string
		want int
	}{
		{"/api/v0/files/downloads/directories/" + b64("../outside"), 400},
		{"/api/v0/files/downloads/directories/" + b64("Album/../.."), 400},
		{"/api/v0/files/downloads/directories/" + b64("escape"), 403},
		{"/api/v0/files/downloads/directories/" + b64("nope"), 404},
		{"/api/v0/files/downloads/directories/" + b64("Album/01.flac"), 404},
		{"/api/v0/files/downloads/directories/!!!", 400},
		{"/api/v0/files/incomplete/directories", 200},
	} {
		if code, _ := get(c.path); code != c.want {
			t.Errorf("%s: %d, want %d", c.path, code, c.want)
		}
	}
	// URL-safe base64 with a "/" is fine when escaped, as clients send it.
	if code, _ := get("/api/v0/files/downloads/directories/" + strings.ReplaceAll(b64("Album/Scans"), "/", "%2F")); code != 200 {
		t.Errorf("escaped base64: %d", code)
	}
}

func TestV0FilesDelete(t *testing.T) {
	e := newEnv(t)
	e.login(t)
	dl := filesTree(t, e)
	if resp, _ := e.do(t, "DELETE", "/api/v0/files/downloads/files/"+b64("Album/01.flac"), nil); resp.StatusCode != 403 {
		t.Errorf("delete while remote file management is off: %d", resp.StatusCode)
	}
	if _, err := os.Stat(filepath.Join(dl, "Album", "01.flac")); err != nil {
		t.Error("file deleted while remote file management is off")
	}

	e = newEnvWith(t, func(c *Config, _ *Deps) { c.RemoteFiles = true })
	e.login(t)
	dl = filesTree(t, e)
	del := func(path string) int {
		t.Helper()
		resp, _ := e.do(t, "DELETE", path, nil)
		return resp.StatusCode
	}
	for _, c := range []struct {
		path string
		want int
	}{
		{"/api/v0/files/downloads/directories/" + b64("/"), 400}, // the root itself
		{"/api/v0/files/downloads/files/" + b64("../outside/secret.txt"), 400},
		{"/api/v0/files/downloads/files/" + b64("escape/secret.txt"), 403},
		{"/api/v0/files/downloads/files/" + b64("Album"), 400},
		{"/api/v0/files/downloads/directories/" + b64("Album/01.flac"), 400},
		{"/api/v0/files/downloads/files/" + b64("Album/nope.flac"), 404},
	} {
		if code := del(c.path); code != c.want {
			t.Errorf("DELETE %s: %d, want %d", c.path, code, c.want)
		}
	}
	if code := del("/api/v0/files/downloads/files/" + b64("Album/01.flac")); code != 204 {
		t.Errorf("delete file: %d", code)
	}
	if _, err := os.Stat(filepath.Join(dl, "Album", "01.flac")); err == nil {
		t.Error("file still there")
	}
	// Deleting the symlinked folder removes the link, not what it points to.
	if code := del("/api/v0/files/downloads/directories/" + b64("escape")); code != 204 {
		t.Errorf("delete link: %d", code)
	}
	if _, err := os.Stat(filepath.Join(e.dir, "outside", "secret.txt")); err != nil {
		t.Error("deleting the link removed its target")
	}
	if code := del("/api/v0/files/downloads/directories/" + b64("Album")); code != 204 {
		t.Errorf("delete folder: %d", code)
	}
	if _, err := os.Stat(filepath.Join(dl, "Album")); err == nil {
		t.Error("folder still there")
	}
	if _, err := os.Stat(dl); err != nil {
		t.Error("downloads directory removed")
	}
}

func TestV0Application(t *testing.T) {
	var mu sync.Mutex
	var stops []bool
	e := newEnvWith(t, func(_ *Config, d *Deps) {
		d.Stop = func(restart bool) { mu.Lock(); stops = append(stops, restart); mu.Unlock() }
	})
	e.login(t)

	var st struct {
		Version struct {
			Full, Current     string
			IsUpdateAvailable bool
		}
		Server struct {
			State                                 string
			IsLoggedIn, IsConnecting, IsLoggingIn bool
		}
		ConnectionWatchdog struct{ IsEnabled bool }
		User               struct {
			Username   string
			Statistics struct{ FileCount int }
		}
		DistributedNetwork struct {
			HasParent bool
			Parent    string
		}
		Shares struct {
			Hosts []string
			Ready bool
		}
		Rooms []string
	}
	resp, b := e.do(t, "GET", "/api/v0/application", nil)
	json.Unmarshal(b, &st)
	if resp.StatusCode != 200 || st.Version.Current != compatVersion || st.Server.State != "Connected, LoggedIn" || !st.Server.IsLoggedIn ||
		st.ConnectionWatchdog.IsEnabled || st.User.Username != "me" || !st.DistributedNetwork.HasParent || st.DistributedNetwork.Parent != "parent" ||
		len(st.Shares.Hosts) != 1 || st.Rooms == nil {
		t.Errorf("application: %d %s", resp.StatusCode, b)
	}
	if resp, b := e.do(t, "GET", "/api/v0/application/version/latest?forceCheck=true", nil); resp.StatusCode != 200 || !strings.Contains(string(b), `"current":"`+compatVersion+`"`) {
		t.Errorf("latest: %d %s", resp.StatusCode, b)
	}
	if resp, _ := e.do(t, "POST", "/api/v0/application/gc", nil); resp.StatusCode != 200 {
		t.Errorf("gc: %d", resp.StatusCode)
	}
	if resp, _ := e.do(t, "POST", "/api/v0/application/loopback", map[string]int{"a": 1}); resp.StatusCode != 200 {
		t.Errorf("loopback: %d", resp.StatusCode)
	}

	// Restart and shutdown take a login, never an API key.
	if code, _ := e.bearer(t, "PUT", "/api/v0/application", "config-key-123", ""); code != 403 {
		t.Errorf("restart with an API key: %d", code)
	}
	if resp, _ := e.do(t, "PUT", "/api/v0/application", nil); resp.StatusCode != 204 {
		t.Errorf("restart: %d", resp.StatusCode)
	}
	_, tb := e.bearer(t, "POST", "/api/v0/session", "", `{"username":"admin","password":"secret"}`)
	var tok struct{ Token string }
	json.Unmarshal(tb, &tok)
	if code, _ := e.bearer(t, "DELETE", "/api/v0/application", tok.Token, ""); code != 204 {
		t.Errorf("shutdown with a token: %d", code)
	}
	eventually(t, "both stops", func() bool { mu.Lock(); defer mu.Unlock(); return len(stops) == 2 })
	mu.Lock()
	if !(stops[0] != stops[1]) {
		t.Errorf("stops %v, want one restart and one shutdown", stops)
	}
	mu.Unlock()

	// Without a way to stop, there is none.
	e = newEnv(t)
	e.login(t)
	if resp, _ := e.do(t, "DELETE", "/api/v0/application", nil); resp.StatusCode != 501 {
		t.Errorf("shutdown without Stop: %d", resp.StatusCode)
	}
}
