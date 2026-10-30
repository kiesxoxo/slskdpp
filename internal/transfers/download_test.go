package transfers

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/binary"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kiesxoxo/slskdpp/internal/client"
	"github.com/kiesxoxo/slskdpp/internal/protocol"
	"github.com/kiesxoxo/slskdpp/internal/search"
	"github.com/kiesxoxo/slskdpp/internal/store"
)

type sent struct {
	user string
	msg  protocol.PeerMessage
}

type fakeNet struct {
	mu      sync.Mutex
	offline map[string]bool
	sent    chan sent
}

func (n *fakeNet) LoggedIn() bool         { return true }
func (n *fakeNet) WatchUser(string) error { return nil }
func (n *fakeNet) setOffline(u string, v bool) {
	n.mu.Lock()
	n.offline[u] = v
	n.mu.Unlock()
}

func (n *fakeNet) SendPeer(ctx context.Context, user string, m protocol.PeerMessage) error {
	n.mu.Lock()
	off := n.offline[user]
	n.mu.Unlock()
	if off {
		return fmt.Errorf("connect: %w", client.ErrUserOffline)
	}
	n.sent <- sent{user, m}
	return nil
}

type searchNet struct {
	token   atomic.Uint32
	queries chan string
}

func (n *searchNet) NewToken() uint32 { return n.token.Add(1) }
func (n *searchNet) Search(token uint32, q string) error {
	n.queries <- q
	return nil
}

type env struct {
	t      *testing.T
	dir    string
	db     *sql.DB
	net    *fakeNet
	snet   *searchNet
	search *search.Manager
	m      *Manager
	stop   context.CancelFunc
}

var fast = timing{
	tick:          10 * time.Millisecond,
	backoffBase:   20 * time.Millisecond,
	backoffMax:    100 * time.Millisecond,
	initTimeout:   time.Second,
	idleTimeout:   2 * time.Second,
	placeEvery:    time.Hour,
	offlineAlt:    time.Hour,
	altWait:       200 * time.Millisecond,
	altGap:        300 * time.Millisecond,
	progressEvery: 10 * time.Millisecond,
	requestLimit:  8,
}

func newEnv(t *testing.T) *env {
	t.Helper()
	dir := t.TempDir()
	db, err := store.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	e := &env{
		t:    t,
		dir:  dir,
		db:   db,
		net:  &fakeNet{offline: map[string]bool{}, sent: make(chan sent, 100)},
		snet: &searchNet{queries: make(chan string, 10)},
	}
	e.search = search.NewManager(e.snet, search.Options{})
	e.start()
	return e
}

// start (re)creates the manager on the same database, as after a restart.
func (e *env) start() {
	e.t.Helper()
	if e.stop != nil {
		e.stop()
	}
	m, err := New(Config{
		DownloadDir:   filepath.Join(e.dir, "downloads"),
		IncompleteDir: filepath.Join(e.dir, "incomplete"),
		MaxAttempts:   2,
		Logger:        slog.New(slog.DiscardHandler),
	}, e.db, e.net, e.search)
	if err != nil {
		e.t.Fatal(err)
	}
	m.t = fast
	ctx, cancel := context.WithCancel(context.Background())
	e.m, e.stop = m, cancel
	e.t.Cleanup(cancel)
	go m.Run(ctx)
}

// expect waits for the next message sent to a peer.
func (e *env) expect(user string, want protocol.PeerMessage) {
	e.t.Helper()
	select {
	case s := <-e.net.sent:
		if s.user != user || fmt.Sprintf("%#v", s.msg) != fmt.Sprintf("%#v", want) {
			e.t.Fatalf("sent %s %#v, want %s %#v", s.user, s.msg, user, want)
		}
	case <-time.After(5 * time.Second):
		e.t.Fatalf("nothing sent; want %s %#v", user, want)
	}
}

func (e *env) waitState(id int64, want State) View {
	e.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		v, _ := e.m.Get(id)
		if v.State == want {
			return v
		}
		if time.Now().After(deadline) {
			e.t.Fatalf("download %d is %s (%s), want %s", id, v.State, v.Error, want)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// upload plays the uploader on a file connection: it reads our offset and
// sends data from there up to cut (or the end if cut < 0).
func (e *env) upload(user string, token uint32, data []byte, cut int) uint64 {
	e.t.Helper()
	ours, theirs := net.Pipe()
	done := make(chan struct{})
	go func() {
		e.m.HandleFileConn(user, token, ours)
		close(done)
	}()
	var b [8]byte
	if _, err := io.ReadFull(theirs, b[:]); err != nil {
		e.t.Fatalf("reading offset: %v", err)
	}
	offset := binary.LittleEndian.Uint64(b[:])
	end := len(data)
	if cut >= 0 {
		end = cut
	}
	theirs.Write(data[offset:end])
	theirs.Close()
	<-done
	return offset
}

const remote = `@@bob\Music\Artist\Album\01 - Song.flac`

func payload(n int) []byte {
	return bytes.Repeat([]byte("0123456789abcdef"), n/16)
}

func (e *env) enqueue(user, path string, size int) View {
	e.t.Helper()
	views, err := e.m.Enqueue(user, []FileRequest{{Path: path, Size: uint64(size)}}, `@@bob\Music\Artist\Album`)
	if err != nil || len(views) != 1 {
		e.t.Fatalf("Enqueue: %v %v", views, err)
	}
	return views[0]
}

func TestDownloadCompletes(t *testing.T) {
	e := newEnv(t)
	data := payload(100_000)
	v := e.enqueue("bob", remote, len(data))
	if v.Folder != "Album" || v.Name != "01 - Song.flac" {
		t.Errorf("dest %s / %s", v.Folder, v.Name)
	}
	e.expect("bob", protocol.QueueUpload{Filename: remote})
	e.waitState(v.ID, StateRemoteQueued)

	// Enqueueing the same file again returns the existing download.
	if again := e.enqueue("bob", remote, len(data)); again.ID != v.ID {
		t.Errorf("duplicate enqueue created %d", again.ID)
	}

	e.m.HandlePeer("bob", protocol.TransferRequest{Direction: protocol.TransferUpload, Token: 7, Filename: remote, Size: uint64(len(data))})
	e.expect("bob", protocol.TransferResponse{Token: 7, Allowed: true})
	e.waitState(v.ID, StateInitializing)
	if off := e.upload("bob", 7, data, -1); off != 0 {
		t.Errorf("offset %d, want 0", off)
	}
	done := e.waitState(v.ID, StateCompleted)
	got, err := os.ReadFile(filepath.Join(e.dir, "downloads", "Album", "01 - Song.flac"))
	if err != nil || !bytes.Equal(got, data) {
		t.Fatalf("downloaded file: %d bytes, %v", len(got), err)
	}
	if done.Bytes != uint64(len(data)) {
		t.Errorf("bytes %d", done.Bytes)
	}
	if left, _ := os.ReadDir(filepath.Join(e.dir, "incomplete")); len(left) != 0 {
		t.Errorf("partial files left: %v", left)
	}
	var state string
	e.db.QueryRow("SELECT state FROM downloads WHERE id = ?", v.ID).Scan(&state)
	if state != string(StateCompleted) {
		t.Errorf("saved state %q", state)
	}

	// A second copy of the same file gets a new name instead of
	// overwriting the first.
	v2 := e.enqueue("bob", remote, len(data))
	e.expect("bob", protocol.QueueUpload{Filename: remote})
	e.m.HandlePeer("bob", protocol.TransferRequest{Direction: protocol.TransferUpload, Token: 8, Filename: remote, Size: uint64(len(data))})
	e.expect("bob", protocol.TransferResponse{Token: 8, Allowed: true})
	e.waitState(v2.ID, StateInitializing)
	e.upload("bob", 8, data, -1)
	if v := e.waitState(v2.ID, StateCompleted); v.Name != "01 - Song (1).flac" {
		t.Errorf("second copy named %q", v.Name)
	}
}

func TestResumeAfterDrop(t *testing.T) {
	e := newEnv(t)
	data := payload(200_000)
	v := e.enqueue("bob", remote, len(data))
	e.expect("bob", protocol.QueueUpload{Filename: remote})
	e.m.HandlePeer("bob", protocol.TransferRequest{Direction: protocol.TransferUpload, Token: 1, Filename: remote, Size: uint64(len(data))})
	e.expect("bob", protocol.TransferResponse{Token: 1, Allowed: true})
	e.waitState(v.ID, StateInitializing)

	// The connection drops halfway: the attempt fails and is retried.
	e.upload("bob", 1, data, 80_000)
	e.expect("bob", protocol.QueueUpload{Filename: remote})
	if v, _ := e.m.Get(v.ID); v.Attempts != 1 || v.Bytes != 80_000 {
		t.Errorf("after drop: %+v", v)
	}
	e.m.HandlePeer("bob", protocol.TransferRequest{Direction: protocol.TransferUpload, Token: 2, Filename: remote, Size: uint64(len(data))})
	e.expect("bob", protocol.TransferResponse{Token: 2, Allowed: true})
	e.waitState(v.ID, StateInitializing)
	if off := e.upload("bob", 2, data, -1); off != 80_000 {
		t.Errorf("resumed from %d, want 80000", off)
	}
	e.waitState(v.ID, StateCompleted)
	got, _ := os.ReadFile(filepath.Join(e.dir, "downloads", "Album", "01 - Song.flac"))
	if !bytes.Equal(got, data) {
		t.Error("resumed file differs")
	}
}

func TestRestartRequeues(t *testing.T) {
	e := newEnv(t)
	v := e.enqueue("bob", remote, 1000)
	e.expect("bob", protocol.QueueUpload{Filename: remote})
	e.waitState(v.ID, StateRemoteQueued)

	e.start() // restart
	e.expect("bob", protocol.QueueUpload{Filename: remote})
	e.waitState(v.ID, StateRemoteQueued)
	if n := len(e.m.List()); n != 1 {
		t.Errorf("%d downloads after restart", n)
	}
}

func TestUnknownTransferDenied(t *testing.T) {
	e := newEnv(t)
	e.m.HandlePeer("mallory", protocol.TransferRequest{Direction: protocol.TransferUpload, Token: 3, Filename: `@@m\x.mp3`, Size: 10})
	e.expect("mallory", protocol.TransferResponse{Token: 3, Allowed: false, Reason: "Cancelled"})
	// File connections nobody asked for are closed.
	ours, theirs := net.Pipe()
	go e.m.HandleFileConn("mallory", 3, ours)
	theirs.SetReadDeadline(time.Now().Add(time.Second))
	if _, err := theirs.Read(make([]byte, 1)); err != io.EOF {
		t.Errorf("unexpected file connection not closed: %v", err)
	}
}

func TestDeniedSwitchesSource(t *testing.T) {
	e := newEnv(t)
	v := e.enqueue("bob", remote, 5000)
	e.expect("bob", protocol.QueueUpload{Filename: remote})
	e.waitState(v.ID, StateRemoteQueued)
	e.m.HandlePeer("bob", protocol.UploadDenied{Filename: remote, Reason: "File not shared."})

	var q string
	select {
	case q = <-e.snet.queries:
		if v, _ := e.m.Get(v.ID); !v.Searching {
			t.Error("view does not show the source search")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no search for another source")
	}
	if q != "01 song" {
		t.Errorf("search query %q", q)
	}
	token := e.snet.token.Load()
	other := `@@carol\Rips\Album\01 - Song.flac`
	e.search.HandleResponse("dave", protocol.FileSearchResponse{Token: token, Files: []protocol.File{{Name: `@@d\01 - Song.flac`, Size: 4999}}})
	e.search.HandleResponse("carol", protocol.FileSearchResponse{Token: token, Files: []protocol.File{{Name: other, Size: 5000}}, SlotFree: true})
	e.expect("carol", protocol.QueueUpload{Filename: other})
	got := e.waitState(v.ID, StateRemoteQueued)
	if got.Username != "carol" || got.Sources != 2 || got.Attempts != 0 {
		t.Errorf("after switch: %+v", got)
	}
	if len(e.search.List()) != 0 {
		t.Error("source search shows in the search list")
	}
}

func TestFailsWithoutOtherSource(t *testing.T) {
	e := newEnv(t)
	v := e.enqueue("bob", remote, 5000)
	for range 2 { // MaxAttempts
		e.expect("bob", protocol.QueueUpload{Filename: remote})
		e.waitState(v.ID, StateRemoteQueued)
		e.m.HandlePeer("bob", protocol.UploadFailed{Filename: remote})
	}
	<-e.snet.queries // nobody answers
	got := e.waitState(v.ID, StateFailed)
	if !strings.Contains(got.Error, "no other source") {
		t.Errorf("error %q", got.Error)
	}

	// Retry starts over with the same source.
	if err := e.m.Retry(v.ID); err != nil {
		t.Fatal(err)
	}
	e.expect("bob", protocol.QueueUpload{Filename: remote})
}

func TestOfflineUserIsWaitedFor(t *testing.T) {
	e := newEnv(t)
	e.net.setOffline("bob", true)
	v := e.enqueue("bob", remote, 100)
	deadline := time.Now().Add(5 * time.Second)
	for {
		got, _ := e.m.Get(v.ID)
		if got.State == StateQueued && got.Error == "user is offline" {
			if got.Attempts != 0 {
				t.Errorf("offline counted as a failed attempt: %+v", got)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("not waiting for user: %+v", got)
		}
		time.Sleep(5 * time.Millisecond)
	}
	e.net.setOffline("bob", false)
	e.m.HandleServer(protocol.UserStatusResponse{Username: "bob", Status: protocol.StatusOnline})
	e.expect("bob", protocol.QueueUpload{Filename: remote})
}

func TestCancelDuringTransfer(t *testing.T) {
	e := newEnv(t)
	v := e.enqueue("bob", remote, 1<<20)
	e.expect("bob", protocol.QueueUpload{Filename: remote})
	e.m.HandlePeer("bob", protocol.TransferRequest{Direction: protocol.TransferUpload, Token: 5, Filename: remote, Size: 1 << 20})
	e.expect("bob", protocol.TransferResponse{Token: 5, Allowed: true})
	e.waitState(v.ID, StateInitializing)

	ours, theirs := net.Pipe()
	done := make(chan struct{})
	go func() {
		e.m.HandleFileConn("bob", 5, ours)
		close(done)
	}()
	io.ReadFull(theirs, make([]byte, 8))
	theirs.Write(make([]byte, 1000))
	e.waitState(v.ID, StateTransferring)
	if err := e.m.Cancel(v.ID); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("transfer did not stop")
	}
	if got, _ := e.m.Get(v.ID); got.State != StateCancelled {
		t.Errorf("state %s", got.State)
	}
	if left, _ := os.ReadDir(filepath.Join(e.dir, "incomplete")); len(left) != 0 {
		t.Errorf("partial file kept after cancel: %v", left)
	}
	if n, _ := e.m.Clear(StateCancelled); n != 1 || len(e.m.List()) != 0 {
		t.Errorf("Clear removed %d, %d left", n, len(e.m.List()))
	}
}

func TestFolderFailoverReusesSource(t *testing.T) {
	e := newEnv(t)
	second := `@@bob\Music\Artist\Album\02 - Other.flac`
	views, _ := e.m.Enqueue("bob", []FileRequest{{Path: remote, Size: 5000}, {Path: second, Size: 7000}}, "")
	// Requests go out concurrently, in either order.
	got := map[string]bool{}
	for range 2 {
		select {
		case m := <-e.net.sent:
			got[m.msg.(protocol.QueueUpload).Filename] = true
		case <-time.After(5 * time.Second):
			t.Fatal("files not requested")
		}
	}
	if !got[remote] || !got[second] {
		t.Fatalf("requested %v", got)
	}
	e.waitState(views[0].ID, StateRemoteQueued)
	e.waitState(views[1].ID, StateRemoteQueued)

	// Both are refused at once: one search runs, and once it finds carol
	// the sibling, still waiting for its turn to search, follows.
	e.m.HandlePeer("bob", protocol.UploadDenied{Filename: remote, Reason: "Banned"})
	e.m.HandlePeer("bob", protocol.UploadDenied{Filename: second, Reason: "Banned"})
	q := <-e.snet.queries
	file := `@@carol\Album\01 - Song.flac`
	if q != "01 song" {
		file = `@@carol\Album\02 - Other.flac`
	}
	e.search.HandleResponse("carol", protocol.FileSearchResponse{Token: e.snet.token.Load(), Files: []protocol.File{{Name: file, Size: 5000}, {Name: file, Size: 7000}}})
	got = map[string]bool{}
	for range 2 {
		select {
		case m := <-e.net.sent:
			if m.user != "carol" {
				t.Fatalf("sent to %s", m.user)
			}
			got[m.msg.(protocol.QueueUpload).Filename] = true
		case <-time.After(5 * time.Second):
			t.Fatal("folder did not move to carol")
		}
	}
	if !got[`@@carol\Album\01 - Song.flac`] || !got[`@@carol\Album\02 - Other.flac`] {
		t.Errorf("requested from carol: %v", got)
	}
	select {
	case q := <-e.snet.queries:
		t.Errorf("searched again: %q", q)
	case <-time.After(500 * time.Millisecond):
	}

	// A file that only failed later still reuses the hint.
	third := `@@bob\Music\Artist\Album\03 - Late.flac`
	views, _ = e.m.Enqueue("bob", []FileRequest{{Path: third, Size: 3000}}, "")
	e.expect("bob", protocol.QueueUpload{Filename: third})
	e.waitState(views[0].ID, StateRemoteQueued)
	e.m.HandlePeer("bob", protocol.UploadDenied{Filename: third, Reason: "Banned"})
	e.expect("carol", protocol.QueueUpload{Filename: `@@carol\Album\03 - Late.flac`})
}
