package client

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"sync"
	"testing"
	"time"

	"github.com/kiesxoxo/slskdpp/internal/protocol"
	"github.com/kiesxoxo/slskdpp/internal/search"
)

// hub is a tiny Soulseek server that routes between test clients: it
// resolves peer addresses, forwards ConnectToPeer and fans out searches.
type hub struct {
	t     *testing.T
	addr  string
	mu    sync.Mutex
	users map[string]*hubUser
	// addrOverride makes GetPeerAddress report a different port for a
	// user, e.g. a closed one to force an indirect connection.
	addrOverride map[string]uint32
	forwarded    int // ConnectToPeer requests passed on
}

type hubUser struct {
	conn net.Conn
	port uint32
	wmu  sync.Mutex
}

func (u *hubUser) send(code uint32, fill func(w *protocol.Writer)) {
	u.wmu.Lock()
	defer u.wmu.Unlock()
	writeMsg(u.conn, code, fill)
}

func newHub(t *testing.T) *hub {
	h := &hub{t: t, users: map[string]*hubUser{}, addrOverride: map[string]uint32{}}
	h.addr = fakeServer(t, func(_ int, c net.Conn) { h.serve(c) })
	return h
}

func (h *hub) user(name string) *hubUser {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.users[name]
}

func (h *hub) serve(c net.Conn) {
	body, err := protocol.ReadFrame(c, 1<<20)
	if err != nil {
		return
	}
	r := protocol.NewReader(body)
	r.Uint32()
	name := r.String()
	me := &hubUser{conn: c}
	h.mu.Lock()
	h.users[name] = me
	h.mu.Unlock()
	defer func() {
		h.mu.Lock()
		delete(h.users, name)
		h.mu.Unlock()
	}()
	me.wmu.Lock()
	acceptLogin(c)
	me.wmu.Unlock()

	for {
		body, err := protocol.ReadFrame(c, 1<<20)
		if err != nil {
			return
		}
		r := protocol.NewReader(body)
		switch r.Uint32() {
		case protocol.CodeSetWaitPort:
			h.mu.Lock()
			me.port = r.Uint32()
			h.mu.Unlock()
		case protocol.CodeGetPeerAddress:
			target := r.String()
			h.mu.Lock()
			port := uint32(0)
			if u := h.users[target]; u != nil {
				port = u.port
			}
			if p, ok := h.addrOverride[target]; ok {
				port = p
			}
			h.mu.Unlock()
			ip := netip.MustParseAddr("127.0.0.1")
			if port == 0 {
				ip = netip.IPv4Unspecified()
			}
			me.send(protocol.CodeGetPeerAddress, func(w *protocol.Writer) {
				w.String(target)
				w.IP(ip)
				w.Uint32(port)
			})
		case protocol.CodeConnectToPeer:
			token, target, typ := r.Uint32(), r.String(), r.String()
			u := h.user(target)
			if u == nil {
				me.send(protocol.CodeCantConnectToPeer, func(w *protocol.Writer) { w.Uint32(token) })
				continue
			}
			h.mu.Lock()
			port := me.port
			h.forwarded++
			h.mu.Unlock()
			u.send(protocol.CodeConnectToPeer, func(w *protocol.Writer) {
				w.String(name)
				w.String(typ)
				w.IP(netip.MustParseAddr("127.0.0.1"))
				w.Uint32(port)
				w.Uint32(token)
				w.Bool(false)
			})
		case protocol.CodeCantConnectToPeer:
			token, target := r.Uint32(), r.String()
			if u := h.user(target); u != nil {
				u.send(protocol.CodeCantConnectToPeer, func(w *protocol.Writer) { w.Uint32(token) })
			}
		case protocol.CodeFileSearch:
			token, query := r.Uint32(), r.String()
			h.mu.Lock()
			var others []*hubUser
			for n, u := range h.users {
				if n != name {
					others = append(others, u)
				}
			}
			h.mu.Unlock()
			for _, u := range others {
				u.send(protocol.CodeFileSearch, func(w *protocol.Writer) {
					w.String(name)
					w.Uint32(token)
					w.String(query)
				})
			}
		}
	}
}

func freePort(t *testing.T) uint32 {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return uint32(ln.Addr().(*net.TCPAddr).Port)
}

// startClient logs a client in to the hub and waits until the hub knows
// its listen port.
func startClient(t *testing.T, ctx context.Context, h *hub, name string, handlers Handlers) *Client {
	t.Helper()
	c := New(ServerConfig{Addr: h.addr, Username: name, Password: "pw", ListenPort: freePort(t)}, handlers)
	go c.Run(ctx)
	if err := c.Server().WaitLoggedIn(ctx); err != nil {
		t.Fatalf("%s login: %v", name, err)
	}
	for {
		h.mu.Lock()
		u := h.users[name]
		ready := u != nil && u.port != 0
		h.mu.Unlock()
		if ready {
			return c
		}
		select {
		case <-ctx.Done():
			t.Fatalf("%s never announced its port", name)
		case <-time.After(10 * time.Millisecond):
		}
	}
}

var share = []protocol.Directory{{
	Name: `@@bob\Music\Album`,
	Files: []protocol.File{
		{Name: `@@bob\Music\Album\01 - One.flac`, Size: 1000},
		{Name: `@@bob\Music\Album\02 - Two.flac`, Size: 2000},
	},
}}

// sharingPeer answers browse, folder and user info requests like a real
// client would.
func sharingPeer(c **Client) func(string, any) {
	return func(user string, m any) {
		var reply protocol.PeerMessage
		switch m := m.(type) {
		case protocol.SharedFileListRequest:
			reply = protocol.SharedFileListResponse{Dirs: share}
		case protocol.FolderContentsRequest:
			reply = protocol.FolderContentsResponse{Token: m.Token, Folder: m.Folder, Dirs: share}
		case protocol.UserInfoRequest:
			reply = protocol.UserInfoResponse{Description: "bob here", UploadSlots: 2, SlotsFree: true}
		default:
			return
		}
		go (*c).SendPeer(context.Background(), user, reply)
	}
}

func testRequests(t *testing.T, ctx context.Context, alice *Client) {
	t.Helper()
	list, err := alice.Browse(ctx, "bob")
	if err != nil {
		t.Fatalf("Browse: %v", err)
	}
	if len(list.Dirs) != 1 || len(list.Dirs[0].Files) != 2 {
		t.Errorf("Browse = %+v", list)
	}
	fc, err := alice.FolderContents(ctx, "bob", `@@bob\Music\Album`)
	if err != nil {
		t.Fatalf("FolderContents: %v", err)
	}
	if fc.Folder != `@@bob\Music\Album` || len(fc.Dirs) != 1 {
		t.Errorf("FolderContents = %+v", fc)
	}
	info, err := alice.UserInfo(ctx, "bob")
	if err != nil {
		t.Fatalf("UserInfo: %v", err)
	}
	if info.Description != "bob here" || info.UploadSlots != 2 {
		t.Errorf("UserInfo = %+v", info)
	}
}

func TestPeerDirectConnection(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	h := newHub(t)
	var bob *Client
	bob = startClient(t, ctx, h, "bob", Handlers{Peer: sharingPeer(&bob)})
	states := make(chan bool, 4)
	alice := startClient(t, ctx, h, "alice", Handlers{State: func(up bool) { states <- up }})
	if up := <-states; !up || !alice.LoggedIn() {
		t.Errorf("state %v, LoggedIn %v after login", up, alice.LoggedIn())
	}
	testRequests(t, ctx, alice)
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.forwarded != 0 {
		t.Error("direct connection fell back to ConnectToPeer")
	}
}

func TestPeerIndirectConnection(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	h := newHub(t)
	var bob *Client
	bob = startClient(t, ctx, h, "bob", Handlers{Peer: sharingPeer(&bob)})
	alice := startClient(t, ctx, h, "alice", Handlers{})
	// Bob looks firewalled: his advertised port refuses connections, so
	// alice must ask the server to have bob connect to her.
	h.mu.Lock()
	h.addrOverride["bob"] = freePort(t)
	h.mu.Unlock()
	testRequests(t, ctx, alice)
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.forwarded == 0 {
		t.Error("connection was not made through ConnectToPeer")
	}
}

func TestPeerUnreachable(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	h := newHub(t)
	alice := startClient(t, ctx, h, "alice", Handlers{})

	if _, err := alice.Browse(ctx, "ghost"); !errors.Is(err, ErrUserOffline) {
		t.Errorf("offline user: err = %v, want ErrUserOffline", err)
	}

	// Carol has an address but is not connected, so the server answers our
	// ConnectToPeer with CantConnectToPeer.
	h.mu.Lock()
	h.addrOverride["carol"] = freePort(t)
	h.mu.Unlock()
	start := time.Now()
	if _, err := alice.Browse(ctx, "carol"); !errors.Is(err, ErrCantConnect) {
		t.Errorf("unreachable user: err = %v, want ErrCantConnect", err)
	}
	if d := time.Since(start); d > indirectTimeout/2 {
		t.Errorf("CantConnectToPeer did not cut the wait short (%v)", d)
	}
}

func TestSearchRoundTrip(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	h := newHub(t)

	var bob *Client
	bob = startClient(t, ctx, h, "bob", Handlers{Server: func(m any) {
		req, ok := m.(protocol.SearchRequest)
		if !ok {
			return
		}
		resp := protocol.FileSearchResponse{Username: "bob", Token: req.Token, Files: share[0].Files, SlotFree: true, AvgSpeed: 1 << 20}
		go bob.SendPeer(context.Background(), req.Username, resp)
	}})

	hits := make(chan search.FolderHit, 1)
	var mgr *search.Manager
	alice := startClient(t, ctx, h, "alice", Handlers{Peer: func(user string, m any) {
		if r, ok := m.(protocol.FileSearchResponse); ok {
			mgr.HandleResponse(user, r)
		}
	}})
	mgr = search.NewManager(alice, search.Options{OnHit: func(_ *search.Search, hit search.FolderHit) { hits <- hit }})

	s, err := mgr.Start("album")
	if err != nil {
		t.Fatal(err)
	}
	select {
	case hit := <-hits:
		if hit.User != "bob" || hit.Folder != `@@bob\Music\Album` || hit.FileCount != 2 || !hit.SlotFree {
			t.Errorf("hit %+v", hit)
		}
	case <-ctx.Done():
		t.Fatal("no search results")
	}
	if st := s.Stats(); st.Responses != 1 || st.Files != 2 {
		t.Errorf("stats %+v", st)
	}
}
