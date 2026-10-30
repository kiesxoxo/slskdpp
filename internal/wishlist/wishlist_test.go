package wishlist

import (
	"context"
	"errors"
	"log/slog"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kiesxoxo/slskdpp/internal/protocol"
	"github.com/kiesxoxo/slskdpp/internal/search"
	"github.com/kiesxoxo/slskdpp/internal/store"
	"github.com/kiesxoxo/slskdpp/internal/transfers"
)

type sent struct {
	token    uint32
	query    string
	wishlist bool
}

type fakeNet struct {
	token atomic.Uint32
	sent  chan sent
}

func (n *fakeNet) NewToken() uint32 { return n.token.Add(1) }
func (n *fakeNet) Search(t uint32, q string) error {
	n.sent <- sent{t, q, false}
	return nil
}
func (n *fakeNet) WishlistSearch(t uint32, q string) error {
	n.sent <- sent{t, q, true}
	return nil
}

type fakeLister struct{ fail bool }

func (l fakeLister) FolderContents(_ context.Context, user, folder string) (protocol.FolderContentsResponse, error) {
	if l.fail {
		return protocol.FolderContentsResponse{}, errors.New("no answer")
	}
	return protocol.FolderContentsResponse{Folder: folder, Dirs: []protocol.Directory{
		{Name: folder, Files: []protocol.File{{Name: "01.flac", Size: 1}, {Name: "02.flac", Size: 2}, {Name: "03.flac", Size: 3}}},
	}}, nil
}

type fakeDownloads struct {
	mu   sync.Mutex
	got  []string
	done chan struct{}
}

func (d *fakeDownloads) Enqueue(user string, files []transfers.FileRequest, folder string) ([]transfers.View, error) {
	d.mu.Lock()
	for _, f := range files {
		d.got = append(d.got, user+" "+f.Path)
	}
	d.mu.Unlock()
	d.done <- struct{}{}
	return nil, nil
}

type env struct {
	t   *testing.T
	net *fakeNet
	s   *search.Manager
	dl  *fakeDownloads
	m   *Manager
}

func newEnv(t *testing.T, interval time.Duration, ls Lister) *env {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	e := &env{t: t, net: &fakeNet{sent: make(chan sent, 10)}, dl: &fakeDownloads{done: make(chan struct{}, 10)}}
	e.s = search.NewManager(e.net, search.Options{})
	e.m = New(Config{Interval: interval, Window: 100 * time.Millisecond, Logger: slog.New(slog.DiscardHandler)}, db, e.s, ls, e.dl)
	e.m.SetServerInterval(time.Millisecond)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go e.m.Run(ctx, nil)
	return e
}

func (e *env) next() sent {
	e.t.Helper()
	select {
	case s := <-e.net.sent:
		return s
	case <-time.After(5 * time.Second):
		e.t.Fatal("no search sent")
	}
	return sent{}
}

// answer feeds a search response from user with an album in folder.
func (e *env) answer(token uint32, user, folder string, ext string, free bool) {
	var files []protocol.File
	for i := range 2 {
		files = append(files, protocol.File{Name: folder + `\0` + string(rune('1'+i)) + "." + ext, Size: 30_000_000,
			Attrs: []protocol.FileAttribute{{Code: protocol.AttrDuration, Value: 200}}})
	}
	e.s.HandleResponse(user, protocol.FileSearchResponse{Token: token, Files: files, SlotFree: free, AvgSpeed: 1 << 20})
}

func TestAutoDownloadBestMatch(t *testing.T) {
	e := newEnv(t, 50*time.Millisecond, fakeLister{})
	entry, err := e.m.Add("  portishead   dummy ", search.Filter{Formats: []string{"flac"}}, true)
	if err != nil || entry.Query != "portishead dummy" || !entry.Enabled {
		t.Fatalf("Add = %+v, %v", entry, err)
	}
	s := e.next()
	if !s.wishlist || s.query != "portishead dummy" {
		t.Errorf("sent %+v", s)
	}
	e.answer(s.token, "mp3guy", `@@m\Dummy`, "mp3", true) // filtered out
	e.answer(s.token, "busy", `@@b\Dummy`, "flac", false)
	e.answer(s.token, "best", `@@x\Portishead\Dummy`, "flac", true)
	select {
	case <-e.dl.done:
	case <-time.After(5 * time.Second):
		t.Fatal("nothing downloaded")
	}
	// The whole folder, from the best source.
	if len(e.dl.got) != 3 || e.dl.got[0] != `best @@x\Portishead\Dummy\01.flac` {
		t.Errorf("downloaded %v", e.dl.got)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		got, _ := e.m.Get(entry.ID)
		// Both flac copies are the same album, so one match.
		if got.GotUser == "best" && !got.Enabled && got.Matches == 1 && got.LastSearch == s.token {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("entry after download %+v", got)
		}
		time.Sleep(10 * time.Millisecond)
	}
	// Done entries are not searched again.
	select {
	case s := <-e.net.sent:
		t.Errorf("searched again: %+v", s)
	case <-time.After(200 * time.Millisecond):
	}
}

func TestScheduleRespectsInterval(t *testing.T) {
	e := newEnv(t, 400*time.Millisecond, fakeLister{})
	a, _ := e.m.Add("first", search.Filter{}, false)
	e.m.Add("second", search.Filter{}, false)
	start := time.Now()
	first := e.next()
	second := e.next()
	third := e.next()
	if first.query != "first" || second.query != "second" || third.query != "first" {
		t.Errorf("order %q %q %q", first.query, second.query, third.query)
	}
	// Two full intervals between three searches, even though adding
	// entries wakes the scheduler.
	if d := time.Since(start); d < 750*time.Millisecond {
		t.Errorf("three searches in %v", d)
	}
	// The server's interval wins when it is longer.
	e.m.SetServerInterval(time.Hour)
	if e.m.Interval() != time.Hour {
		t.Errorf("interval %v", e.m.Interval())
	}

	// Disabled entries are skipped; "search now" sends a normal search
	// (once the scheduled one has finished: an entry runs once at a time).
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		if got, _ := e.m.Get(a.ID); !got.Running {
			break
		}
	}
	off := false
	if _, err := e.m.Update(a.ID, Change{Enabled: &off}); err != nil {
		t.Fatal(err)
	}
	e.m.RunNow(a.ID)
	if s := e.next(); s.wishlist || s.query != "first" {
		t.Errorf("search now sent %+v", s)
	}
	if err := e.m.Remove(a.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.m.Get(a.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("Get after remove: %v", err)
	}
}

func TestAutoDownloadFallsBackToSearchFiles(t *testing.T) {
	e := newEnv(t, time.Hour, fakeLister{fail: true})
	e.m.Add("dummy", search.Filter{}, true)
	s := e.next()
	e.answer(s.token, "u", `@@u\Dummy`, "flac", true)
	<-e.dl.done
	if len(e.dl.got) != 2 {
		t.Errorf("downloaded %v", e.dl.got)
	}
}
