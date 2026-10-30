package client

import (
	"context"
	"log/slog"
	"net"
	"net/netip"
	"sync"
	"time"

	"github.com/kiesxoxo/slskdpp/internal/protocol"
)

const (
	maxDistribFrame = 1 << 20
	// A parent passes on searches constantly; this long without any means
	// the link is dead.
	distribIdleTimeout = 10 * time.Minute
)

// distrib keeps us in the distributed search network as a leaf: we take
// searches from one parent and pass nothing on. Without a parent the
// server may also make us a branch root and send searches embedded in
// its own messages.
type distrib struct {
	c   *Client
	log *slog.Logger

	mu         sync.Mutex
	parent     net.Conn
	parentName string
	connecting bool
}

func newDistrib(c *Client, log *slog.Logger) *distrib {
	return &distrib{c: c, log: log.With("component", "distrib")}
}

// noParent tells the server we need a parent, which makes it send
// PossibleParents.
func (d *distrib) noParent() {
	srv := d.c.srv
	srv.Send(protocol.HaveNoParent{NoParent: true})
	srv.Send(protocol.SetBranchRoot{Root: d.c.username})
	srv.Send(protocol.SetBranchLevel{Level: 0})
}

// loggedIn starts over after each login.
func (d *distrib) loggedIn() {
	d.drop()
	d.c.srv.Send(protocol.AcceptChildren{Accept: false})
	d.noParent()
}

// drop disconnects from the parent.
func (d *distrib) drop() {
	d.mu.Lock()
	p := d.parent
	d.parent, d.parentName = nil, ""
	d.mu.Unlock()
	if p != nil {
		p.Close()
	}
}

// Parent returns the user we take searches from, if any.
func (d *distrib) Parent() string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.parentName
}

func (d *distrib) handleServer(m any) {
	switch m := m.(type) {
	case protocol.PossibleParents:
		d.mu.Lock()
		busy := d.parent != nil || d.connecting
		if !busy {
			d.connecting = true
		}
		d.mu.Unlock()
		if !busy {
			go d.connect(m.Parents)
		}
	case protocol.ResetDistributed:
		d.drop()
		d.noParent()
	case protocol.EmbeddedMessage:
		d.embedded(m.DistribCode, m.Payload)
	}
}

// connect tries the candidates in turn and keeps the first that answers.
func (d *distrib) connect(cands []protocol.PossibleParent) {
	defer func() {
		d.mu.Lock()
		d.connecting = false
		d.mu.Unlock()
	}()
	for _, p := range cands {
		if p.Username == d.c.username || p.Port == 0 {
			continue
		}
		addr := netip.AddrPortFrom(p.IP, uint16(p.Port)).String()
		conn, err := net.DialTimeout("tcp", addr, directTimeout)
		if err != nil {
			d.log.Debug("parent candidate unreachable", "user", p.Username, "err", err)
			continue
		}
		conn.SetWriteDeadline(time.Now().Add(peerWriteTimeout))
		init := protocol.PeerInit{Username: d.c.username, Type: protocol.ConnDistributed, Token: d.c.NewToken()}
		if _, err := conn.Write(protocol.EncodeInit(init)); err != nil {
			conn.Close()
			continue
		}
		conn.SetWriteDeadline(time.Time{})
		d.mu.Lock()
		d.parent, d.parentName = conn, p.Username
		d.mu.Unlock()
		d.log.Info("joined distributed network", "parent", p.Username)
		d.c.srv.Send(protocol.HaveNoParent{NoParent: false})
		go d.read(conn, p.Username)
		return
	}
}

// read handles messages from the parent until the link drops.
func (d *distrib) read(conn net.Conn, name string) {
	defer func() {
		conn.Close()
		d.mu.Lock()
		current := d.parent == conn
		if current {
			d.parent, d.parentName = nil, ""
		}
		d.mu.Unlock()
		if current {
			d.log.Info("lost distributed parent", "parent", name)
			d.noParent()
		}
	}()
	for {
		conn.SetReadDeadline(time.Now().Add(distribIdleTimeout))
		body, err := protocol.ReadFrame(conn, maxDistribFrame)
		if err != nil {
			return
		}
		m, err := protocol.DecodeDistrib(body)
		if err != nil {
			d.log.Debug("bad distributed message", "parent", name, "err", err)
			continue
		}
		switch m := m.(type) {
		case protocol.DistribSearch:
			d.search(m)
		case protocol.DistribBranchLevel:
			d.c.srv.Send(protocol.SetBranchLevel{Level: uint32(m.Level + 1)})
		case protocol.DistribBranchRoot:
			d.c.srv.Send(protocol.SetBranchRoot{Root: m.Root})
		case protocol.DistribEmbedded:
			d.embedded(m.Code, m.Payload)
		}
	}
}

func (d *distrib) embedded(code uint8, payload []byte) {
	m, err := protocol.DecodeEmbedded(code, payload)
	if err != nil {
		return
	}
	if s, ok := m.(protocol.DistribSearch); ok {
		d.search(s)
	}
}

func (d *distrib) search(s protocol.DistribSearch) {
	if s.Username == d.c.username {
		return
	}
	d.c.h.Search(protocol.SearchRequest{Username: s.Username, Token: s.Token, Query: s.Query})
}

// stop is used when the client shuts down.
func (d *distrib) stop(ctx context.Context) {
	<-ctx.Done()
	d.drop()
}
