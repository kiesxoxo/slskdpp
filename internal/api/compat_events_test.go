package api

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kiesxoxo/slskdpp/internal/events"
	"github.com/kiesxoxo/slskdpp/internal/transfers"
)

type v0EventRecord struct {
	ID, Timestamp, Type, Data string
}

func TestV0EventLog(t *testing.T) {
	var l v0Events
	at := time.Date(2026, 10, 31, 12, 0, 0, 123456789, time.UTC)
	for i := range v0EventsKept + 5 {
		l.add("Noop", at, map[string]any{"n": i})
	}
	page, total := l.page(0, 3)
	if total != v0EventsKept || len(page) != 3 {
		t.Fatalf("total %d, page %d", total, len(page))
	}
	// Newest first, each strictly later than the one before even though
	// they were all recorded at the same instant.
	if !page[0].At.After(page[1].At) || !page[1].At.After(page[2].At) {
		t.Errorf("not newest first with distinct times: %v %v %v", page[0].At, page[1].At, page[2].At)
	}
	if !strings.Contains(page[0].Data, `"n":1004`) {
		t.Errorf("newest event %s", page[0].Data)
	}
	if got := v0EventTime(at); got != "2026-10-31T12:00:00.1234567Z" {
		t.Errorf("timestamp %q", got)
	}
	if page, _ := l.page(v0EventsKept-1, 10); len(page) != 1 || !strings.Contains(page[0].Data, `"n":5,`) {
		t.Errorf("last page %v", page)
	}
	if page, _ := l.page(v0EventsKept, 10); len(page) != 0 {
		t.Errorf("past the end %v", page)
	}
}

func TestV0Duration(t *testing.T) {
	for d, want := range map[time.Duration]string{
		0:                                     "00:00:00.0000000",
		90*time.Second + 500*time.Millisecond: "00:01:30.5000000",
		26*time.Hour + 3*time.Minute:          "1.02:03:00.0000000",
		-time.Second:                          "00:00:00.0000000",
	} {
		if got := v0Duration(d); got != want {
			t.Errorf("%v: %q, want %q", d, got, want)
		}
	}
}

func TestV0Destination(t *testing.T) {
	root := t.TempDir()
	a := &API{cfg: Config{DownloadDir: root}}
	for in, want := range map[string]string{
		"":                                "",
		"Artist - Album":                  "Artist - Album",
		`Artist\Album: Deluxe`:            "Artist/Album_ Deluxe",
		filepath.Join(root, "X", "Album"): "X/Album",
		root:                              "",
	} {
		if got, err := a.v0Destination(in); err != nil || got != want {
			t.Errorf("%q: %q, %v; want %q", in, got, err, want)
		}
	}
	for _, in := range []string{"../escape", "a/../../b", filepath.Join(filepath.Dir(root), "elsewhere"), `C:\Music`} {
		if got, err := a.v0Destination(in); err == nil {
			t.Errorf("%q accepted as %q", in, got)
		}
	}
}

func TestV0Batches(t *testing.T) {
	e := newEnv(t)
	e.login(t)
	id := "3f2b8c1e-5d4a-4e6f-9a7b-1c2d3e4f5a6b"
	batch := map[string]any{
		"id":       strings.ToUpper(id),
		"username": "bob",
		"files": []map[string]any{
			{"filename": `@@x\Artist\Album\CD1\01.flac`, "size": 10},
			{"filename": `@@x\Artist\Album\CD2\01.flac`, "size": 11},
			{"filename": "", "size": 1},
		},
		"options": map[string]any{"externalId": "lidarr-42", "destination": "Artist - Album"},
	}
	resp, b := e.do(t, "POST", "/api/v0/transfers/downloads/batches", batch)
	if resp.StatusCode != 207 {
		t.Fatalf("batch: %d %s", resp.StatusCode, b)
	}
	var got struct {
		ID       string
		Enqueued []struct{ ID, Filename, BatchID string }
		Failures []struct{ Filename, Message string }
		Options  struct{ ExternalID, Destination string }
	}
	json.Unmarshal(b, &got)
	if got.ID != id || len(got.Enqueued) != 2 || len(got.Failures) != 1 || got.Failures[0].Message == "" ||
		got.Enqueued[0].BatchID != id || got.Options.Destination != "Artist - Album" || got.Options.ExternalID != "lidarr-42" {
		t.Errorf("batch response %s", b)
	}

	// Both discs go straight into the destination; the second 01.flac
	// gets a unique name when it lands.
	var views []transfers.View
	_, b = e.do(t, "GET", "/api/v1/downloads", nil)
	json.Unmarshal(b, &views)
	for _, v := range views {
		if v.Folder != "Artist - Album" || v.Name != "01.flac" || v.Batch != id {
			t.Errorf("download %+v", v)
		}
	}

	if resp, _ := e.do(t, "POST", "/api/v0/transfers/downloads/batches", batch); resp.StatusCode != 409 {
		t.Errorf("same id again: %d", resp.StatusCode)
	}
	bad := map[string]any{"username": "bob", "files": []map[string]any{{"filename": `@@x\a.mp3`, "size": 1}}, "options": map[string]any{"destination": "../out"}}
	if resp, _ := e.do(t, "POST", "/api/v0/transfers/downloads/batches", bad); resp.StatusCode != 400 {
		t.Errorf("escaping destination: %d", resp.StatusCode)
	}

	resp, b = e.do(t, "GET", "/api/v0/transfers/downloads/batches/"+id, nil)
	var fetched struct {
		Username string
		Options  struct{ Destination string }
		Files    []struct{ BatchID string }
	}
	json.Unmarshal(b, &fetched)
	if resp.StatusCode != 200 || fetched.Username != "bob" || fetched.Options.Destination != "Artist - Album" || len(fetched.Files) != 2 {
		t.Errorf("get batch: %d %s", resp.StatusCode, b)
	}
	var user struct {
		Directories []struct{ Files []struct{ BatchID string } }
	}
	_, b = e.do(t, "GET", "/api/v0/transfers/downloads/bob", nil)
	json.Unmarshal(b, &user)
	if len(user.Directories) != 2 || user.Directories[0].Files[0].BatchID != id {
		t.Errorf("transfers %s", b)
	}

	// Removing its downloads forgets the batch.
	for _, v := range views {
		e.do(t, "DELETE", "/api/v0/transfers/downloads/bob/"+v0TransferID(v.ID)+"?remove=true", nil)
	}
	if resp, _ := e.do(t, "GET", "/api/v0/transfers/downloads/batches/"+id, nil); resp.StatusCode != 404 {
		t.Errorf("batch after removing its downloads: %d", resp.StatusCode)
	}
}

func TestV0Events(t *testing.T) {
	e := newEnv(t)
	e.login(t)
	resp, b := e.do(t, "GET", "/api/v0/events?offset=0&limit=50", nil)
	if resp.StatusCode != 200 || strings.TrimSpace(string(b)) != "[]" || resp.Header.Get("X-Total-Count") != "0" {
		t.Fatalf("empty log: %d %q %q", resp.StatusCode, b, resp.Header.Get("X-Total-Count"))
	}
	if resp, _ := e.do(t, "GET", "/api/v0/events?limit=0", nil); resp.StatusCode != 400 {
		t.Errorf("limit 0: %d", resp.StatusCode)
	}

	// Two files of one remote folder; the folder is reported when the
	// second completes.
	views, err := e.dl.Enqueue("bob", []transfers.FileRequest{{Path: `@@x\Album\01.flac`, Size: 1}, {Path: `@@x\Album\02.flac`, Size: 1}}, "")
	if err != nil {
		t.Fatal(err)
	}
	local := filepath.Join(e.dir, "Album")
	now := time.Now()
	complete := func(v transfers.View, at time.Time) {
		v.State, v.Completed = transfers.StateCompleted, at
		e.bus.Publish(events.Event{Type: events.TypeDownloadCompleted, Data: events.DownloadCompleted{Download: v, Local: filepath.Join(local, v.Name)}})
	}
	complete(views[0], now)
	var list []v0EventRecord
	eventually(t, "the first event", func() bool {
		_, b = e.do(t, "GET", "/api/v0/events?offset=0&limit=50", nil)
		json.Unmarshal(b, &list)
		return len(list) == 1
	})
	if list[0].Type != "DownloadFileComplete" {
		t.Fatalf("events %s", b)
	}
	// The second is still queued as far as the manager knows, so the
	// folder is not done yet. Cancel it to finish the folder, and report
	// the first again as though it completed last.
	e.dl.Cancel(views[1].ID)
	complete(views[0], now.Add(time.Second))
	var resp2 []v0EventRecord
	eventually(t, "the directory event", func() bool {
		resp, b = e.do(t, "GET", "/api/v0/events?offset=0&limit=50", nil)
		json.Unmarshal(b, &resp2)
		return len(resp2) == 3
	})
	if resp.Header.Get("X-Total-Count") != "3" {
		t.Errorf("total %q", resp.Header.Get("X-Total-Count"))
	}
	dirEvent, fileEvent := resp2[0], resp2[1]
	if dirEvent.Type != "DownloadDirectoryComplete" || fileEvent.Type != "DownloadFileComplete" || !isUUID(dirEvent.ID) {
		t.Fatalf("events %s", b)
	}
	var dir struct{ LocalDirectoryName, RemoteDirectoryName, Username, Type string }
	json.Unmarshal([]byte(dirEvent.Data), &dir)
	if dir.LocalDirectoryName != local || dir.RemoteDirectoryName != `@@x\Album` || dir.Username != "bob" || dir.Type != "DownloadDirectoryComplete" {
		t.Errorf("directory event %s", dirEvent.Data)
	}
	var file struct {
		LocalFilename, RemoteFilename string
		Transfer                      struct{ Username, Filename, State, RequestedAt string }
	}
	json.Unmarshal([]byte(fileEvent.Data), &file)
	if file.LocalFilename != filepath.Join(local, "01.flac") || file.RemoteFilename != `@@x\Album\01.flac` ||
		file.Transfer.Username != "bob" || file.Transfer.State != "Completed, Succeeded" || file.Transfer.RequestedAt == "" {
		t.Errorf("file event %s", fileEvent.Data)
	}
	if !(dirEvent.Timestamp > fileEvent.Timestamp) {
		t.Errorf("timestamps %s, %s", dirEvent.Timestamp, fileEvent.Timestamp)
	}
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		if cond() {
			return
		}
	}
	t.Fatalf("timed out waiting for %s", what)
}
