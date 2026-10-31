package api

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kiesxoxo/slskdpp/internal/events"
)

func TestV0Users(t *testing.T) {
	e := newEnv(t)
	e.login(t)
	get := func(path string, out any) int {
		t.Helper()
		resp, b := e.do(t, "GET", path, nil)
		if out != nil {
			json.Unmarshal(b, out)
		}
		return resp.StatusCode
	}

	var info map[string]any
	if code := get("/api/v0/users/bob/info", &info); code != 200 || info["description"] != "hello from bob" || info["uploadSlots"] != 3.0 ||
		info["hasPicture"] != false || info["hasFreeUploadSlot"] != false || info["queueLength"] != 0.0 {
		t.Errorf("info: %d %v", code, info)
	}
	if _, ok := info["picture"]; ok {
		t.Error("picture without one")
	}

	var status map[string]any
	if code := get("/api/v0/users/vip/status", &status); code != 200 || status["presence"] != "Online" || status["isPrivileged"] != true {
		t.Errorf("status: %d %v", code, status)
	}
	if get("/api/v0/users/ghost/status", &status); status["presence"] != "Offline" {
		t.Errorf("offline status %v", status)
	}

	var ep map[string]any
	if code := get("/api/v0/users/bob/endpoint", &ep); code != 200 || ep["address"] != "192.0.2.7" || ep["port"] != 2234.0 || ep["addressFamily"] != "InterNetwork" {
		t.Errorf("endpoint: %d %v", code, ep)
	}
	if code := get("/api/v0/users/ghost/endpoint", nil); code != 404 {
		t.Errorf("offline endpoint: %d", code)
	}

	var progress map[string]any
	if code := get("/api/v0/users/bob/browse/status", &progress); code != 200 || progress["percentComplete"] != 25.0 ||
		progress["bytesTransferred"] != 50.0 || progress["size"] != 200.0 || progress["username"] != "bob" {
		t.Errorf("browse status: %d %v", code, progress)
	}
	if code := get("/api/v0/users/carol/browse/status", nil); code != 404 {
		t.Errorf("no browse: %d", code)
	}
}

func TestV0Server(t *testing.T) {
	e := newEnv(t)
	e.login(t)
	var st map[string]any
	_, b := e.do(t, "GET", "/api/v0/server", nil)
	json.Unmarshal(b, &st)
	ep, _ := st["ipEndPoint"].(map[string]any)
	if st["state"] != "Connected, LoggedIn" || st["isConnected"] != true || st["isTransitioning"] != false ||
		st["address"] != "server.example:2242" || st["username"] != "me" || ep["address"] != "198.51.100.1" {
		t.Errorf("server %s", b)
	}

	if resp, _ := e.do(t, "DELETE", "/api/v0/server", "going away"); resp.StatusCode != 204 {
		t.Errorf("disconnect: %d", resp.StatusCode)
	}
	_, b = e.do(t, "GET", "/api/v0/server", nil)
	st = nil
	json.Unmarshal(b, &st)
	if st["state"] != "Disconnected" || st["isLoggedIn"] != false {
		t.Errorf("after disconnecting %s", b)
	}
	if _, ok := st["ipEndPoint"]; ok {
		t.Errorf("endpoint while disconnected %s", b)
	}
	// The reason is recorded with the disconnection.
	e.bus.Publish(events.Event{Type: events.TypeServerState, Data: events.ServerState{Connected: true}})
	e.bus.Publish(events.Event{Type: events.TypeServerState, Data: events.ServerState{Connected: false}})
	var list []v0EventRecord
	eventually(t, "the disconnection event", func() bool {
		_, b := e.do(t, "GET", "/api/v0/events", nil)
		json.Unmarshal(b, &list)
		return len(list) == 2
	})
	if list[0].Type != "SoulseekClientDisconnected" || !strings.Contains(list[0].Data, `"message":"going away"`) {
		t.Errorf("event %+v", list[0])
	}

	if resp, _ := e.do(t, "PUT", "/api/v0/server", nil); resp.StatusCode != 200 {
		t.Errorf("connect: %d", resp.StatusCode)
	}
	if e.net.ServerStatus().Paused {
		t.Error("still paused after connecting")
	}
	e.net.mu.Lock()
	e.net.loggedIn = false // as while reconnecting
	e.net.mu.Unlock()
	if resp, _ := e.do(t, "PUT", "/api/v0/server", nil); resp.StatusCode != 205 {
		t.Errorf("connect while reconnecting: %d", resp.StatusCode)
	}
}

func TestV0ShareID(t *testing.T) {
	// slskd's ID is the upper-case hex SHA-1 of the remote path.
	if got := v0ShareID("abc"); got != "A9993E364706816ABA3E25717850C26C9CD0D89D" {
		t.Errorf("id %s", got)
	}
}

func TestV0Shares(t *testing.T) {
	e := newEnv(t)
	e.login(t)
	eventually(t, "the share scan", func() bool { return e.idx.Stats().Files == 1 && !e.idx.Stats().Scanning })

	var all map[string][]map[string]any
	_, b := e.do(t, "GET", "/api/v0/shares", nil)
	json.Unmarshal(b, &all)
	if len(all["local"]) != 1 {
		t.Fatalf("shares %s", b)
	}
	sh := all["local"][0]
	if sh["id"] != v0ShareID("music") || sh["alias"] != "music" || sh["remotePath"] != "music" || sh["files"] != 1.0 ||
		sh["directories"] != 1.0 || sh["localPath"] != filepath.Join(e.dir, "music") || sh["isExcluded"] != false {
		t.Errorf("share %v", sh)
	}
	id := sh["id"].(string)
	if resp, _ := e.do(t, "GET", "/api/v0/shares/"+strings.ToLower(id), nil); resp.StatusCode != 200 {
		t.Errorf("get share: %d", resp.StatusCode)
	}
	if resp, _ := e.do(t, "GET", "/api/v0/shares/nope", nil); resp.StatusCode != 404 {
		t.Errorf("missing share: %d", resp.StatusCode)
	}

	// The tree is whole: "music" holds only a folder, so it is listed
	// empty. Files carry full paths.
	var dirs []struct {
		Name  string
		Files []struct{ Filename string }
	}
	for _, path := range []string{"/api/v0/shares/contents", "/api/v0/shares/" + id + "/contents"} {
		_, b = e.do(t, "GET", path, nil)
		dirs = nil
		json.Unmarshal(b, &dirs)
		if len(dirs) != 2 || dirs[0].Name != "music" || len(dirs[0].Files) != 0 || dirs[1].Name != `music\Album` ||
			len(dirs[1].Files) != 1 || dirs[1].Files[0].Filename != `music\Album\song.mp3` {
			t.Errorf("%s: %s", path, b)
		}
	}

	if resp, _ := e.do(t, "DELETE", "/api/v0/shares", nil); resp.StatusCode != 404 {
		t.Errorf("cancel without a scan: %d", resp.StatusCode)
	}
	if resp, _ := e.do(t, "PUT", "/api/v0/shares", nil); resp.StatusCode != 200 {
		t.Errorf("rescan: %d", resp.StatusCode)
	}
	eventually(t, "the rescan", func() bool {
		return !e.idx.Stats().Scanning && e.idx.Stats().LastScan.After(time.Now().Add(-time.Minute))
	})
}

func TestV0UploadsAndIDs(t *testing.T) {
	e := newEnv(t)
	e.login(t)
	if resp, b := e.do(t, "GET", "/api/v0/transfers/uploads", nil); resp.StatusCode != 200 || strings.TrimSpace(string(b)) != "[]" {
		t.Errorf("uploads: %d %s", resp.StatusCode, b)
	}
	if resp, _ := e.do(t, "GET", "/api/v0/transfers/uploads/bob", nil); resp.StatusCode != 404 {
		t.Errorf("no uploads to bob: %d", resp.StatusCode)
	}
	if resp, _ := e.do(t, "DELETE", "/api/v0/transfers/uploads/all/completed", nil); resp.StatusCode != 204 {
		t.Errorf("clear: %d", resp.StatusCode)
	}
	other := "6f1c1d0e-4b0a-11ef-9a4f-0242ac120002"
	for _, c := range []struct {
		method, path string
		want         int
	}{
		{"GET", "/api/v0/transfers/uploads/bob/not-a-guid", 400},
		{"GET", "/api/v0/transfers/uploads/bob/" + other, 404},
		{"DELETE", "/api/v0/transfers/uploads/bob/" + v0TransferID(99), 404},
		{"GET", "/api/v0/transfers/downloads/bob/not-a-guid", 400},
		{"DELETE", "/api/v0/transfers/downloads/bob/" + other, 404},
		{"GET", "/api/v0/transfers/downloads/bob/" + v0TransferID(99) + "/position", 404},
	} {
		if resp, _ := e.do(t, c.method, c.path, nil); resp.StatusCode != c.want {
			t.Errorf("%s %s: %d, want %d", c.method, c.path, resp.StatusCode, c.want)
		}
	}
}
