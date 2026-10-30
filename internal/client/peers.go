package client

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/netip"
	"slices"
	"sync"
	"time"

	"github.com/kiesxoxo/slskdpp/internal/protocol"
)

const (
	maxInitFrame = 64 << 10
	maxPeerFrame = 128 << 20

	handshakeTimeout = 30 * time.Second
	addressTimeout   = 10 * time.Second
	directTimeout    = 5 * time.Second
	indirectTimeout  = 20 * time.Second
	peerWriteTimeout = 30 * time.Second
	// Peer connections with no traffic for this long are closed.
	peerIdleTimeout = 2 * time.Minute
)

var (
	ErrUserOffline = errors.New("client: user is offline")
	// ErrCantConnect means the peer reported, through the server, that it
	// could not answer our ConnectToPeer.
	ErrCantConnect = errors.New("client: peer could not connect to us")
)

// serverLink is the part of Server the peer layer needs.
type serverLink interface {
	Send(protocol.ServerRequest) error
}

// Peers manages connections to other users: it accepts incoming ones on
// the listen port and opens outgoing ones, first directly and then through
// the server (ConnectToPeer/PierceFirewall) when the peer is unreachable.
type Peers struct {
	username   string
	listenPort uint32
	log        *slog.Logger
	server     serverLink
	newToken   func() uint32
	// handler receives every message read from a ConnPeer connection on
	// that connection's read goroutine, so it must not block.
	handler func(username string, m any)
	// fileHandler takes over file connections once their transfer token
	// is read. It runs on the connection's own goroutine.
	fileHandler func(username string, token uint32, conn net.Conn)

	mu      sync.Mutex
	conns   map[string]*PeerConn // newest message connection per user
	dials   map[string]*dialCall
	addrs   map[string][]chan protocol.PeerAddress
	pierces map[uint32]chan pierceResult
}

type dialCall struct {
	done chan struct{}
	pc   *PeerConn
	err  error
}

type pierceResult struct {
	conn net.Conn
	err  error
}

func newPeers(username string, listenPort uint32, log *slog.Logger, server serverLink, newToken func() uint32, handler func(string, any), fileHandler func(string, uint32, net.Conn)) *Peers {
	return &Peers{
		username:    username,
		listenPort:  listenPort,
		log:         log.With("component", "peers"),
		server:      server,
		newToken:    newToken,
		handler:     handler,
		fileHandler: fileHandler,
		conns:       make(map[string]*PeerConn),
		dials:       make(map[string]*dialCall),
		addrs:       make(map[string][]chan protocol.PeerAddress),
		pierces:     make(map[uint32]chan pierceResult),
	}
}

// Listen binds the listen port and accepts peer connections until ctx is
// done.
func (p *Peers) Listen(ctx context.Context) error {
	var lc net.ListenConfig
	ln, err := lc.Listen(ctx, "tcp", fmt.Sprintf(":%d", p.listenPort))
	if err != nil {
		return fmt.Errorf("listen for peers: %w", err)
	}
	p.log.Info("listening for peers", "addr", ln.Addr())
	context.AfterFunc(ctx, func() { ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				if ctx.Err() == nil {
					p.log.Error("peer listener stopped", "err", err)
				}
				return
			}
			go p.accept(conn)
		}
	}()
	return nil
}

// handleServer consumes the server messages that belong to the peer layer.
func (p *Peers) handleServer(m any) {
	switch m := m.(type) {
	case protocol.PeerAddress:
		p.mu.Lock()
		waiters := p.addrs[m.Username]
		delete(p.addrs, m.Username)
		p.mu.Unlock()
		for _, ch := range waiters {
			ch <- m
		}
	case protocol.ConnectToPeerRequest:
		go p.answerConnectToPeer(m)
	case protocol.CantConnectToPeer:
		p.mu.Lock()
		ch := p.pierces[m.Token]
		delete(p.pierces, m.Token)
		p.mu.Unlock()
		if ch != nil {
			ch <- pierceResult{err: ErrCantConnect}
		}
	}
}

// accept handles a connection that arrived on the listen port.
func (p *Peers) accept(conn net.Conn) {
	conn.SetReadDeadline(time.Now().Add(handshakeTimeout))
	body, err := protocol.ReadFrame(conn, maxInitFrame)
	if err != nil {
		conn.Close()
		return
	}
	m, err := protocol.DecodeInit(body)
	if err != nil {
		p.log.Debug("bad peer init", "remote", conn.RemoteAddr(), "err", err)
		conn.Close()
		return
	}
	conn.SetReadDeadline(time.Time{})
	switch m := m.(type) {
	case protocol.PierceFirewall:
		p.mu.Lock()
		ch := p.pierces[m.Token]
		delete(p.pierces, m.Token)
		p.mu.Unlock()
		if ch == nil {
			p.log.Debug("unexpected pierce firewall", "remote", conn.RemoteAddr(), "token", m.Token)
			conn.Close()
			return
		}
		ch <- pierceResult{conn: conn}
	case protocol.PeerInit:
		p.adopt(m.Username, m.Type, conn)
	}
}

// answerConnectToPeer connects to a peer that could not reach us.
func (p *Peers) answerConnectToPeer(m protocol.ConnectToPeerRequest) {
	addr := netip.AddrPortFrom(m.IP, uint16(m.Port))
	conn, err := net.DialTimeout("tcp", addr.String(), indirectTimeout)
	if err == nil {
		conn.SetWriteDeadline(time.Now().Add(peerWriteTimeout))
		_, err = conn.Write(protocol.EncodeInit(protocol.PierceFirewall{Token: m.Token}))
		conn.SetWriteDeadline(time.Time{})
		if err != nil {
			conn.Close()
		}
	}
	if err != nil {
		p.log.Debug("cannot answer connect to peer", "user", m.Username, "addr", addr, "err", err)
		p.server.Send(protocol.CantConnectToPeerRequest{Token: m.Token, Username: m.Username})
		return
	}
	p.adopt(m.Username, m.Type, conn)
}

// adopt takes over an established connection of the given type.
func (p *Peers) adopt(username, typ string, conn net.Conn) {
	switch typ {
	case protocol.ConnPeer:
		p.serve(p.register(username, conn))
	case protocol.ConnFile:
		// The uploader opens a file connection with the transfer token.
		var b [4]byte
		conn.SetReadDeadline(time.Now().Add(handshakeTimeout))
		if _, err := io.ReadFull(conn, b[:]); err != nil {
			p.log.Debug("file connection without token", "user", username, "err", err)
			conn.Close()
			return
		}
		conn.SetReadDeadline(time.Time{})
		if p.fileHandler == nil {
			conn.Close()
			return
		}
		p.fileHandler(username, binary.LittleEndian.Uint32(b[:]), conn)
	default:
		p.log.Debug("unsupported connection type", "user", username, "type", typ)
		conn.Close()
	}
}

// Connect returns a message connection to username, reusing an open one
// when there is one. Concurrent callers share a single dial.
func (p *Peers) Connect(ctx context.Context, username string) (*PeerConn, error) {
	p.mu.Lock()
	if pc := p.conns[username]; pc != nil && !pc.Closed() {
		p.mu.Unlock()
		return pc, nil
	}
	call := p.dials[username]
	if call == nil {
		call = &dialCall{done: make(chan struct{})}
		p.dials[username] = call
		go p.runDial(username, call)
	}
	p.mu.Unlock()
	select {
	case <-call.done:
		return call.pc, call.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (p *Peers) runDial(username string, call *dialCall) {
	// The dial is shared, so it must not depend on one caller's context.
	ctx, cancel := context.WithTimeout(context.Background(), addressTimeout+directTimeout+indirectTimeout)
	defer cancel()
	conn, err := p.dial(ctx, username, protocol.ConnPeer)
	if err == nil {
		call.pc = p.register(username, conn)
		go p.serve(call.pc)
	}
	call.err = err
	p.mu.Lock()
	delete(p.dials, username)
	p.mu.Unlock()
	close(call.done)
}

// Send delivers m to username, opening a connection if needed. A cached
// connection that turns out to be dead is replaced once.
func (p *Peers) Send(ctx context.Context, username string, m protocol.PeerMessage) error {
	frame := protocol.EncodePeer(m)
	for attempt := 0; ; attempt++ {
		pc, err := p.Connect(ctx, username)
		if err != nil {
			return err
		}
		err = pc.write(frame)
		if err == nil || attempt > 0 {
			return err
		}
		pc.Close()
	}
}

// ConnectFile opens a file connection to username for the transfer with
// the given token, as the uploading side does.
func (p *Peers) ConnectFile(ctx context.Context, username string, token uint32) (net.Conn, error) {
	conn, err := p.dial(ctx, username, protocol.ConnFile)
	if err != nil {
		return nil, err
	}
	conn.SetWriteDeadline(time.Now().Add(peerWriteTimeout))
	if _, err := conn.Write(binary.LittleEndian.AppendUint32(nil, token)); err != nil {
		conn.Close()
		return nil, err
	}
	conn.SetWriteDeadline(time.Time{})
	return conn, nil
}

// dial opens a connection of type typ to username and sends the init
// message that identifies it.
func (p *Peers) dial(ctx context.Context, username, typ string) (net.Conn, error) {
	addr, err := p.peerAddress(ctx, username)
	if err != nil {
		return nil, err
	}
	if addr.Unknown() {
		return nil, fmt.Errorf("%w: %s", ErrUserOffline, username)
	}
	token := p.newToken()
	conn, derr := p.dialDirect(ctx, addr, typ, token)
	if derr == nil {
		return conn, nil
	}
	p.log.Debug("direct connection failed, trying indirect", "user", username, "err", derr)
	conn, ierr := p.dialIndirect(ctx, username, typ, token)
	if ierr != nil {
		return nil, fmt.Errorf("connect to %s: direct: %v; indirect: %w", username, derr, ierr)
	}
	return conn, nil
}

func (p *Peers) dialDirect(ctx context.Context, addr protocol.PeerAddress, typ string, token uint32) (net.Conn, error) {
	d := net.Dialer{Timeout: directTimeout}
	conn, err := d.DialContext(ctx, "tcp", netip.AddrPortFrom(addr.IP, uint16(addr.Port)).String())
	if err != nil {
		return nil, err
	}
	conn.SetWriteDeadline(time.Now().Add(peerWriteTimeout))
	if _, err := conn.Write(protocol.EncodeInit(protocol.PeerInit{Username: p.username, Type: typ, Token: token})); err != nil {
		conn.Close()
		return nil, err
	}
	conn.SetWriteDeadline(time.Time{})
	return conn, nil
}

// dialIndirect asks the server to have username connect to us and waits
// for their PierceFirewall.
func (p *Peers) dialIndirect(ctx context.Context, username, typ string, token uint32) (net.Conn, error) {
	ch := make(chan pierceResult, 1)
	p.mu.Lock()
	p.pierces[token] = ch
	p.mu.Unlock()
	defer func() {
		p.mu.Lock()
		delete(p.pierces, token)
		p.mu.Unlock()
		// A pierce that raced with our return must not leak its conn.
		select {
		case r := <-ch:
			if r.conn != nil {
				r.conn.Close()
			}
		default:
		}
	}()
	if err := p.server.Send(protocol.ConnectToPeer{Token: token, Username: username, Type: typ}); err != nil {
		return nil, err
	}
	t := time.NewTimer(indirectTimeout)
	defer t.Stop()
	select {
	case r := <-ch:
		return r.conn, r.err
	case <-t.C:
		return nil, errors.New("timed out waiting for peer")
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// peerAddress asks the server where username listens.
func (p *Peers) peerAddress(ctx context.Context, username string) (protocol.PeerAddress, error) {
	ch := make(chan protocol.PeerAddress, 1)
	p.mu.Lock()
	p.addrs[username] = append(p.addrs[username], ch)
	p.mu.Unlock()
	defer func() {
		p.mu.Lock()
		if rest := slices.DeleteFunc(p.addrs[username], func(c chan protocol.PeerAddress) bool { return c == ch }); len(rest) > 0 {
			p.addrs[username] = rest
		} else {
			delete(p.addrs, username)
		}
		p.mu.Unlock()
	}()
	if err := p.server.Send(protocol.GetPeerAddress{Username: username}); err != nil {
		return protocol.PeerAddress{}, err
	}
	t := time.NewTimer(addressTimeout)
	defer t.Stop()
	select {
	case a := <-ch:
		return a, nil
	case <-t.C:
		return protocol.PeerAddress{}, fmt.Errorf("no address for %s from server", username)
	case <-ctx.Done():
		return protocol.PeerAddress{}, ctx.Err()
	}
}

// register makes conn the current message connection for username. An
// older connection is left to finish what it is doing and idle out.
func (p *Peers) register(username string, conn net.Conn) *PeerConn {
	pc := &PeerConn{Username: username, conn: conn, closed: make(chan struct{})}
	p.mu.Lock()
	p.conns[username] = pc
	p.mu.Unlock()
	return pc
}

// serve reads messages from pc until it fails or idles out.
func (p *Peers) serve(pc *PeerConn) {
	defer func() {
		pc.Close()
		p.mu.Lock()
		if p.conns[pc.Username] == pc {
			delete(p.conns, pc.Username)
		}
		p.mu.Unlock()
	}()
	r := idleReader{pc.conn}
	for {
		body, err := protocol.ReadFrame(r, maxPeerFrame)
		if err != nil {
			if !pc.Closed() {
				p.log.Debug("peer connection closed", "user", pc.Username, "err", err)
			}
			return
		}
		m, err := protocol.DecodePeer(body)
		if err != nil {
			p.log.Debug("bad peer message", "user", pc.Username, "err", err)
			continue
		}
		if u, ok := m.(protocol.UnknownMessage); ok {
			p.log.Debug("unhandled peer message", "user", pc.Username, "code", u.Code, "len", len(u.Payload))
			continue
		}
		p.handler(pc.Username, m)
	}
}

// PeerConn is a message connection (type ConnPeer) to one user.
type PeerConn struct {
	Username string
	conn     net.Conn

	wmu       sync.Mutex
	closeOnce sync.Once
	closed    chan struct{}
}

// Send writes one message to the peer.
func (c *PeerConn) Send(m protocol.PeerMessage) error { return c.write(protocol.EncodePeer(m)) }

func (c *PeerConn) write(frame []byte) error {
	c.wmu.Lock()
	defer c.wmu.Unlock()
	if c.Closed() {
		return net.ErrClosed
	}
	c.conn.SetWriteDeadline(time.Now().Add(peerWriteTimeout))
	if _, err := c.conn.Write(frame); err != nil {
		c.Close()
		return err
	}
	// Sending counts as activity: give the peer time to answer.
	c.conn.SetReadDeadline(time.Now().Add(peerIdleTimeout))
	return nil
}

func (c *PeerConn) Close() {
	c.closeOnce.Do(func() {
		close(c.closed)
		c.conn.Close()
	})
}

func (c *PeerConn) Closed() bool {
	select {
	case <-c.closed:
		return true
	default:
		return false
	}
}

// idleReader pushes the read deadline forward on every read, so a
// connection is closed only after peerIdleTimeout without any data, however
// long a single large message takes to arrive.
type idleReader struct{ conn net.Conn }

func (r idleReader) Read(b []byte) (int, error) {
	r.conn.SetReadDeadline(time.Now().Add(peerIdleTimeout))
	return r.conn.Read(b)
}
