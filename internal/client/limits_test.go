package client

import (
	"bytes"
	"encoding/binary"
	"io"
	"log/slog"
	"net"
	"strings"
	"testing"

	"github.com/kiesxoxo/slskdpp/internal/protocol"
)

type addrConn struct {
	net.Conn
	addr net.Addr
}

func (c addrConn) RemoteAddr() net.Addr { return c.addr }
func (c addrConn) Close() error         { return nil }

func fakeConn(ip string) net.Conn {
	return addrConn{addr: &net.TCPAddr{IP: net.ParseIP(ip), Port: 1}}
}

func TestInboundLimiter(t *testing.T) {
	l := newInboundLimiter()
	var held []net.Conn
	for range maxInboundPerIP {
		c, ok := l.admit(fakeConn("10.0.0.1"))
		if !ok {
			t.Fatal("refused below the per-IP limit")
		}
		held = append(held, c)
	}
	if _, ok := l.admit(fakeConn("10.0.0.1")); ok {
		t.Fatal("admitted past the per-IP limit")
	}
	if _, ok := l.admit(fakeConn("10.0.0.2")); !ok {
		t.Fatal("another address was refused")
	}
	held[0].Close()
	held[0].Close() // a second close must not free a second place
	if _, ok := l.admit(fakeConn("10.0.0.1")); !ok {
		t.Fatal("closing a connection did not free its place")
	}
	if _, ok := l.admit(fakeConn("10.0.0.1")); ok {
		t.Fatal("double close freed two places")
	}

	l = newInboundLimiter()
	for i := range maxInbound {
		if _, ok := l.admit(fakeConn(net.IPv4(10, 1, byte(i>>8), byte(i)).String())); !ok {
			t.Fatalf("refused connection %d below the total limit", i)
		}
	}
	if _, ok := l.admit(fakeConn("10.9.9.9")); ok {
		t.Fatal("admitted past the total limit")
	}
}

func frame(code uint32, payload []byte) []byte {
	b := make([]byte, 8, 8+len(payload))
	binary.LittleEndian.PutUint32(b, uint32(4+len(payload)))
	binary.LittleEndian.PutUint32(b[4:], code)
	return append(b, payload...)
}

func TestReadOneLimits(t *testing.T) {
	var got []any
	p := newPeers("me", 0, slog.New(slog.DiscardHandler), nil, nil, func(_ string, m any) { got = append(got, m) }, nil)
	asked := false
	p.wants = func(string, uint32) bool { return asked }

	browse := protocol.EncodePeer(protocol.SharedFileListResponse{Dirs: []protocol.Directory{{Name: "x"}}})
	place := protocol.EncodePeer(protocol.PlaceInQueueRequest{Filename: `a\b.mp3`})
	r := bytes.NewReader(append(append([]byte{}, browse...), place...))
	for range 2 {
		if err := p.readOne(r, "peer"); err != nil {
			t.Fatal(err)
		}
	}
	if len(got) != 1 {
		t.Fatalf("handled %d messages, want only the queue request: %#v", len(got), got)
	}
	if _, ok := got[0].(protocol.PlaceInQueueRequest); !ok {
		t.Fatalf("handled %T", got[0])
	}

	asked = true
	if err := p.readOne(bytes.NewReader(browse), "peer"); err != nil || len(got) != 2 {
		t.Fatalf("requested browse response not handled: %v, %d messages", err, len(got))
	}

	// Small message types cannot be large, and the oversized body is not
	// read.
	big := frame(protocol.CodeQueueUpload, make([]byte, 2<<20))
	r = bytes.NewReader(big)
	err := p.readOne(r, "peer")
	if err == nil || !strings.Contains(err.Error(), "exceeds limit") {
		t.Fatalf("oversized queue request: err = %v", err)
	}
	if rest, _ := io.ReadAll(r); len(rest) < 2<<20 {
		t.Fatalf("read %d bytes of an oversized frame", len(big)-len(rest))
	}
}
