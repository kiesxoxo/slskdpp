package client

import (
	"context"
	"net"
	"net/netip"
	"testing"
	"time"

	"github.com/kiesxoxo/slskdpp/internal/protocol"
)

func TestDistributedLeaf(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	h := newHub(t)
	h.seen = make(chan hubMsg, 100)

	// A parent that hands down its branch details and two searches, then
	// waits to be told to hang up.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	hangUp := make(chan struct{})
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		body, err := protocol.ReadFrame(c, 1<<16)
		if err != nil {
			return
		}
		if m, _ := protocol.DecodeInit(body); m != (protocol.PeerInit{Username: "alice", Type: protocol.ConnDistributed, Token: m.(protocol.PeerInit).Token}) {
			t.Errorf("parent got init %+v", m)
		}
		inner := protocol.EncodeDistrib(protocol.DistribSearch{Username: "carol", Token: 2, Query: "embedded"})
		for _, m := range []protocol.DistribMessage{
			protocol.DistribBranchLevel{Level: 2},
			protocol.DistribBranchRoot{Root: "root"},
			protocol.DistribPing{},
			protocol.DistribSearch{Username: "bob", Token: 1, Query: "aphex twin"},
			protocol.DistribSearch{Username: "alice", Token: 9, Query: "our own"},
			protocol.DistribEmbedded{Code: protocol.CodeDistribSearch, Payload: inner[5:]},
		} {
			c.Write(protocol.EncodeDistrib(m))
		}
		<-hangUp
	}()

	searches := make(chan protocol.SearchRequest, 10)
	alice := startClient(t, ctx, h, "alice", Handlers{Search: func(s protocol.SearchRequest) { searches <- s }})

	// waitFor drains what alice sent the server until want shows up.
	waitFor := func(code uint32, check func(r *protocol.Reader) bool) {
		t.Helper()
		for {
			select {
			case m := <-h.seen:
				if m.user == "alice" && m.code == code && check(protocol.NewReader(m.body)) {
					return
				}
			case <-ctx.Done():
				t.Fatalf("alice never sent server message %d", code)
			}
		}
	}
	waitFor(protocol.CodeAcceptChildren, func(r *protocol.Reader) bool { return !r.Bool() })
	waitFor(protocol.CodeHaveNoParent, func(r *protocol.Reader) bool { return r.Bool() })

	port := uint32(ln.Addr().(*net.TCPAddr).Port)
	h.user("alice").send(protocol.CodePossibleParents, func(w *protocol.Writer) {
		w.Uint32(2)
		w.String("deadparent") // refuses connections
		w.IP(netip.MustParseAddr("127.0.0.1"))
		w.Uint32(freePort(t))
		w.String("parent")
		w.IP(netip.MustParseAddr("127.0.0.1"))
		w.Uint32(port)
	})
	waitFor(protocol.CodeHaveNoParent, func(r *protocol.Reader) bool { return !r.Bool() })
	waitFor(protocol.CodeBranchLevel, func(r *protocol.Reader) bool { return r.Uint32() == 3 })
	waitFor(protocol.CodeBranchRoot, func(r *protocol.Reader) bool { return r.String() == "root" })
	if p := alice.DistribParent(); p != "parent" {
		t.Errorf("parent %q", p)
	}

	for _, want := range []protocol.SearchRequest{
		{Username: "bob", Token: 1, Query: "aphex twin"},
		{Username: "carol", Token: 2, Query: "embedded"},
	} {
		select {
		case got := <-searches:
			if got != want {
				t.Errorf("search %+v, want %+v", got, want)
			}
		case <-ctx.Done():
			t.Fatalf("search %q not delivered", want.Query)
		}
	}

	// Searches the server embeds itself (we are a branch root) and plain
	// server searches arrive the same way; our own are ignored.
	inner := protocol.EncodeDistrib(protocol.DistribSearch{Username: "dave", Token: 3, Query: "from server"})
	h.user("alice").send(protocol.CodeEmbeddedMessage, func(w *protocol.Writer) { w.Raw(inner[4:]) })
	h.user("alice").send(protocol.CodeFileSearch, func(w *protocol.Writer) {
		w.String("alice")
		w.Uint32(4)
		w.String("ours")
	})
	h.user("alice").send(protocol.CodeFileSearch, func(w *protocol.Writer) {
		w.String("erin")
		w.Uint32(5)
		w.String("direct")
	})
	for _, q := range []string{"from server", "direct"} {
		select {
		case got := <-searches:
			if got.Query != q {
				t.Errorf("search %+v, want query %q", got, q)
			}
		case <-ctx.Done():
			t.Fatalf("search %q not delivered", q)
		}
	}

	// Losing the parent puts us back in the market for one.
	close(hangUp)
	waitFor(protocol.CodeHaveNoParent, func(r *protocol.Reader) bool { return r.Bool() })
	if p := alice.DistribParent(); p != "" {
		t.Errorf("parent after hang-up %q", p)
	}
}
