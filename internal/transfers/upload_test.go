package transfers

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kiesxoxo/slskdpp/internal/protocol"
)

type upNet struct {
	sent  chan sent
	conns chan net.Conn // the downloader's end of each file connection
	token atomic.Uint32
}

func (n *upNet) NewToken() uint32 { return n.token.Add(1) }
func (n *upNet) SendPeer(ctx context.Context, user string, m protocol.PeerMessage) error {
	n.sent <- sent{user, m}
	return nil
}
func (n *upNet) ConnectFile(ctx context.Context, user string, token uint32) (net.Conn, error) {
	ours, theirs := net.Pipe()
	n.conns <- theirs
	return ours, nil
}

type mapResolver map[string]string

func (r mapResolver) Resolve(v string) (string, uint64, bool) {
	p, ok := r[v]
	if !ok {
		return "", 0, false
	}
	st, err := os.Stat(p)
	if err != nil {
		return "", 0, false
	}
	return p, uint64(st.Size()), true
}

type upEnv struct {
	t     *testing.T
	net   *upNet
	u     *Uploads
	data  map[string][]byte
	speed chan uint32
}

func newUpEnv(t *testing.T, slots, perUser int) *upEnv {
	t.Helper()
	dir := t.TempDir()
	e := &upEnv{t: t, net: &upNet{sent: make(chan sent, 100), conns: make(chan net.Conn, 10)}, data: map[string][]byte{}, speed: make(chan uint32, 10)}
	files := mapResolver{}
	for i, name := range []string{"a.flac", "b.flac", "c.flac"} {
		data := bytes.Repeat([]byte{byte('a' + i)}, 300_000)
		p := filepath.Join(dir, name)
		os.WriteFile(p, data, 0o644)
		files[`Music\Album\`+name] = p
		e.data[`Music\Album\`+name] = data
	}
	e.u = NewUploads(UploadConfig{Slots: slots, MaxQueuedPerUser: perUser, Logger: slog.New(slog.DiscardHandler),
		OnSpeed: func(s uint32) { e.speed <- s }}, e.net, files)
	e.u.t = fast
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go e.u.Run(ctx)
	return e
}

func (e *upEnv) next() sent {
	e.t.Helper()
	select {
	case s := <-e.net.sent:
		return s
	case <-time.After(5 * time.Second):
		e.t.Fatal("nothing sent")
	}
	return sent{}
}

// accept plays the downloader: it allows the transfer, then receives the
// file from offset.
func (e *upEnv) accept(user, file string, offset uint64) {
	e.t.Helper()
	s := e.next()
	req, ok := s.msg.(protocol.TransferRequest)
	if !ok || s.user != user || req.Filename != file || req.Direction != protocol.TransferUpload || req.Size != uint64(len(e.data[file])) {
		e.t.Fatalf("sent %s %#v, want transfer request for %s to %s", s.user, s.msg, file, user)
	}
	e.u.HandlePeer(user, protocol.TransferResponse{Token: req.Token, Allowed: true})
	conn := <-e.net.conns
	defer conn.Close()
	conn.Write(binary.LittleEndian.AppendUint64(nil, offset))
	got, _ := io.ReadAll(conn)
	if !bytes.Equal(got, e.data[file][offset:]) {
		e.t.Fatalf("received %d bytes of %s, want %d from offset %d", len(got), file, len(e.data[file])-int(offset), offset)
	}
}

func (e *upEnv) waitState(id int64, want UploadState) UploadView {
	e.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		for _, v := range e.u.List() {
			if v.ID == id && v.State == want {
				return v
			}
		}
		if time.Now().After(deadline) {
			e.t.Fatalf("upload %d never reached %s: %+v", id, want, e.u.List())
		}
		time.Sleep(5 * time.Millisecond)
	}
}

const fileA, fileB, fileC = `Music\Album\a.flac`, `Music\Album\b.flac`, `Music\Album\c.flac`

func TestUploadFlow(t *testing.T) {
	e := newUpEnv(t, 2, 10)
	e.u.HandlePeer("bob", protocol.QueueUpload{Filename: fileA})
	e.accept("bob", fileA, 100_000) // bob resumes a partial download
	v := e.waitState(1, UploadCompleted)
	if v.Bytes != 300_000 || v.Name != "a.flac" || v.Folder != `Music\Album` {
		t.Errorf("view %+v", v)
	}
	select {
	case s := <-e.speed:
		if s == 0 {
			t.Error("zero speed reported")
		}
	case <-time.After(time.Second):
		t.Error("speed not reported")
	}
	if st := e.u.Status(); st.AvgSpeed == 0 || !st.SlotFree || st.Active != 0 {
		t.Errorf("status %+v", st)
	}
}

func TestUploadDenials(t *testing.T) {
	e := newUpEnv(t, 1, 2)
	e.u.HandlePeer("bob", protocol.QueueUpload{Filename: `Music\..\..\etc\passwd`})
	if s := e.next(); s.msg != (protocol.UploadDenied{Filename: `Music\..\..\etc\passwd`, Reason: "File not shared."}) {
		t.Errorf("sent %#v", s.msg)
	}
	e.u.HandlePeer("bob", protocol.QueueUpload{Filename: fileA})
	e.next() // a starts
	e.u.HandlePeer("bob", protocol.QueueUpload{Filename: fileB})
	e.u.HandlePeer("bob", protocol.QueueUpload{Filename: fileB}) // repeat is ignored
	e.u.HandlePeer("bob", protocol.QueueUpload{Filename: fileC})
	if s := e.next(); s.msg != (protocol.UploadDenied{Filename: fileC, Reason: "Too many files"}) {
		t.Errorf("sent %#v", s.msg)
	}
	if n := len(e.u.List()); n != 2 {
		t.Errorf("%d uploads listed", n)
	}
}

func TestUploadFairness(t *testing.T) {
	e := newUpEnv(t, 1, 10)
	e.u.HandlePeer("alice", protocol.QueueUpload{Filename: fileA})
	first := e.next().msg.(protocol.TransferRequest)
	e.u.HandlePeer("alice", protocol.QueueUpload{Filename: fileB})
	e.u.HandlePeer("bob", protocol.QueueUpload{Filename: fileC})

	// Bob was never served, so he goes ahead of alice's second file.
	e.u.HandlePeer("bob", protocol.PlaceInQueueRequest{Filename: fileC})
	if s := e.next(); s.msg != (protocol.PlaceInQueueResponse{Filename: fileC, Place: 1}) {
		t.Errorf("bob's place: %#v", s.msg)
	}
	if st := e.u.Status(); st.SlotFree || st.Queued != 2 {
		t.Errorf("status %+v", st)
	}

	e.u.HandlePeer("alice", protocol.TransferResponse{Token: first.Token, Allowed: true})
	conn := <-e.net.conns
	conn.Write(make([]byte, 8))
	io.ReadAll(conn)
	conn.Close()
	e.accept("bob", fileC, 0)
	e.accept("alice", fileB, 0)
}

func TestUploadRefusedAndUnanswered(t *testing.T) {
	e := newUpEnv(t, 1, 10)
	e.u.HandlePeer("bob", protocol.QueueUpload{Filename: fileA})
	req := e.next().msg.(protocol.TransferRequest)
	e.u.HandlePeer("bob", protocol.TransferResponse{Token: req.Token, Allowed: false, Reason: "Cancelled"})
	if v := e.waitState(1, UploadCancelled); v.Error == "" {
		t.Errorf("no reason recorded: %+v", v)
	}
	// A response nobody waits for is not ours to handle.
	if e.u.HandlePeer("bob", protocol.TransferResponse{Token: 999, Allowed: true}) {
		t.Error("stray transfer response consumed")
	}

	e.u.HandlePeer("carol", protocol.QueueUpload{Filename: fileB})
	e.next() // carol never answers
	e.waitState(2, UploadFailed)
	if s := e.next(); s.msg != (protocol.UploadFailed{Filename: fileB}) {
		t.Errorf("sent %#v", s.msg)
	}
}

func TestLegacyDownloadRequest(t *testing.T) {
	e := newUpEnv(t, 1, 10)
	e.u.HandlePeer("old", protocol.TransferRequest{Direction: protocol.TransferDownload, Token: 7, Filename: fileA})
	got := map[string]bool{}
	for range 2 {
		s := e.next()
		got[fmt.Sprintf("%T", s.msg)] = true
		if r, ok := s.msg.(protocol.TransferResponse); ok && (r.Allowed || r.Reason != "Queued" || r.Token != 7) {
			t.Errorf("response %#v", r)
		}
	}
	if !got["protocol.TransferResponse"] || !got["protocol.TransferRequest"] {
		t.Errorf("sent %v", got)
	}
}

func TestUploadCancelAndClear(t *testing.T) {
	e := newUpEnv(t, 1, 10)
	e.u.HandlePeer("bob", protocol.QueueUpload{Filename: fileA})
	e.next()
	e.u.HandlePeer("bob", protocol.QueueUpload{Filename: fileB})
	if err := e.u.Cancel(2); err != nil {
		t.Fatal(err)
	}
	if s := e.next(); s.msg != (protocol.UploadDenied{Filename: fileB, Reason: "Cancelled"}) {
		t.Errorf("sent %#v", s.msg)
	}
	if err := e.u.Cancel(1); err != nil { // waiting for bob's answer
		t.Fatal(err)
	}
	e.waitState(1, UploadCancelled)
	if n := e.u.Clear(); n != 2 || len(e.u.List()) != 0 {
		t.Errorf("Clear removed %d, %d left", n, len(e.u.List()))
	}
}
