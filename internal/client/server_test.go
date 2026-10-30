package client

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"testing"
	"time"

	"github.com/kiesxoxo/slskdpp/internal/protocol"
)

// fakeServer accepts connections and runs fn for each one.
func fakeServer(t *testing.T, fn func(n int, c net.Conn)) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for n := 0; ; n++ {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				fn(n, c)
			}()
		}
	}()
	return ln.Addr().String()
}

func readCode(t *testing.T, c net.Conn) (uint32, *protocol.Reader) {
	t.Helper()
	c.SetReadDeadline(time.Now().Add(5 * time.Second))
	body, err := protocol.ReadFrame(c, 1<<20)
	if err != nil {
		t.Errorf("server read: %v", err)
		return 0, protocol.NewReader(nil)
	}
	r := protocol.NewReader(body)
	return r.Uint32(), r
}

func writeMsg(c net.Conn, code uint32, fill func(w *protocol.Writer)) {
	var w protocol.Writer
	w.Uint32(code)
	fill(&w)
	c.Write(protocol.Frame(w.Bytes()))
}

func acceptLogin(c net.Conn) {
	writeMsg(c, protocol.CodeLogin, func(w *protocol.Writer) {
		w.Bool(true)
		w.String("hi")
		w.IP(netip.MustParseAddr("127.0.0.1"))
		w.String("x")
		w.Bool(false)
	})
}

func TestServerLoginAndReconnect(t *testing.T) {
	logins := make(chan string, 4)
	announced := make(chan []uint32, 4)
	addr := fakeServer(t, func(n int, c net.Conn) {
		code, r := readCode(t, c)
		if code != protocol.CodeLogin {
			t.Errorf("first message code %d, want Login", code)
			return
		}
		logins <- r.String()
		acceptLogin(c)
		var codes []uint32
		for range 3 {
			code, _ := readCode(t, c)
			codes = append(codes, code)
		}
		announced <- codes
		// Drop the first connection to force a reconnect; keep the second.
		if n == 0 {
			return
		}
		time.Sleep(5 * time.Second)
	})

	srv := NewServer(ServerConfig{Addr: addr, Username: "tester", Password: "pw", ListenPort: 2234}, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	go srv.Run(ctx)

	for i := range 2 {
		select {
		case u := <-logins:
			if u != "tester" {
				t.Errorf("login %d username %q", i, u)
			}
		case <-ctx.Done():
			t.Fatalf("login %d never happened", i)
		}
		codes := <-announced
		want := []uint32{protocol.CodeSetWaitPort, protocol.CodeSharedFoldersFiles, protocol.CodeSetStatus}
		for j := range want {
			if codes[j] != want[j] {
				t.Errorf("login %d post-login codes %v, want %v", i, codes, want)
				break
			}
		}
	}
}

func TestServerLoginRejected(t *testing.T) {
	addr := fakeServer(t, func(n int, c net.Conn) {
		readCode(t, c)
		writeMsg(c, protocol.CodeLogin, func(w *protocol.Writer) {
			w.Bool(false)
			w.String("INVALIDPASS")
		})
	})
	srv := NewServer(ServerConfig{Addr: addr, Username: "u", Password: "bad"}, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err := srv.Run(ctx)
	var le *LoginError
	if !errors.As(err, &le) || le.Reason != "INVALIDPASS" {
		t.Fatalf("Run = %v, want LoginError INVALIDPASS", err)
	}
}

func TestServerRelogged(t *testing.T) {
	addr := fakeServer(t, func(n int, c net.Conn) {
		readCode(t, c)
		acceptLogin(c)
		writeMsg(c, protocol.CodeRelogged, func(*protocol.Writer) {})
		time.Sleep(time.Second)
	})
	srv := NewServer(ServerConfig{Addr: addr, Username: "u", Password: "p"}, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Run(ctx); !errors.Is(err, ErrRelogged) {
		t.Fatalf("Run = %v, want ErrRelogged", err)
	}
}
