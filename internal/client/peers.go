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
	"strconv"
	"sync"
	"sync/atomic"
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
	// wants reports whether a request is waiting for a response with
	// this code from username. Unsolicited browse, folder and user info
	// responses are dropped unread. Nil accepts everything.
	wants func(username string, code uint32) bool
	// browseProgress, if set, follows share lists as they arrive, with
	// bytes received and the total, header included.
	browseProgress func(username string, got, size uint64)

	inbound    *inboundLimiter
	bigDecodes chan struct{}
	inflates   chan struct{}

	mu        sync.Mutex
	listenCtx context.Context      // set by Listen
	listenIP  string               // "" for every address
	ln        net.Listener         // the current listener
	conns     map[string]*PeerConn // newest message connection per user
	dials     map[string]*dialCall
	addrs     map[string][]chan protocol.PeerAddress
	pierces   map[uint32]chan pierceResult
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
		inbound:     newInboundLimiter(),
		bigDecodes:  make(chan struct{}, maxBigDecodes),
		inflates:    make(chan struct{}, maxInflates),
		conns:       make(map[string]*PeerConn),
		dials:       make(map[string]*dialCall),
		addrs:       make(map[string][]chan protocol.PeerAddress),
		pierces:     make(map[uint32]chan pierceResult),
	}
}

// Listen binds the listen port and accepts peer connections until ctx is
// done.
func (p *Peers) Listen(ctx context.Context) error {
	p.mu.Lock()
	ip, port := p.listenIP, p.listenPort
	p.mu.Unlock()
	ln, err := p.listen(ctx, ip, port)
	if err != nil {
		return err
	}
	p.mu.Lock()
	p.listenCtx, p.ln = ctx, ln
	p.mu.Unlock()
	return nil
}

// Relisten moves the listener to another address and port. The old one
// is closed only once the new one is open; on error nothing changes.
func (p *Peers) Relisten(ip string, port uint32) error {
	p.mu.Lock()
	ctx, old := p.listenCtx, p.ln
	p.mu.Unlock()
	if ctx == nil {
		return errors.New("not listening yet")
	}
	ln, err := p.listen(ctx, ip, port)
	if err != nil {
		return err
	}
	p.mu.Lock()
	p.ln, p.listenIP, p.listenPort = ln, ip, port
	p.mu.Unlock()
	old.Close()
	return nil
}

// ListenAddr is the address ("" for all) and port we listen on.
func (p *Peers) ListenAddr() (string, uint32) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.listenIP, p.listenPort
}

// listen opens a listener and accepts peers on it until it is closed.
func (p *Peers) listen(ctx context.Context, ip string, port uint32) (net.Listener, error) {
	var lc net.ListenConfig
	ln, err := lc.Listen(ctx, "tcp", net.JoinHostPort(ip, strconv.Itoa(int(port))))
	if err != nil {
		return nil, fmt.Errorf("listen for peers: %w", err)
	}
	p.log.Info("listening for peers", "addr", ln.Addr())
	context.AfterFunc(ctx, func() { ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				p.mu.Lock()
				replaced := p.ln != nil && p.ln != ln
				p.mu.Unlock()
				if ctx.Err() == nil && !replaced {
					p.log.Error("peer listener stopped", "err", err)
				}
				return
			}
			lc, ok := p.inbound.admit(conn)
			if !ok {
				p.log.Debug("too many peer connections; refusing one", "remote", conn.RemoteAddr())
				conn.Close()
				continue
			}
			go p.accept(lc)
		}
	}()
	return ln, nil
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
		// Anyone can connect under any name, so this is not yet trusted
		// to carry what we send to m.Username; see Connect.
		p.adopt(m.Username, m.Type, conn, false)
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
	// The server gave the address, as it does for our own dials.
	p.adopt(m.Username, m.Type, conn, true)
}

// adopt takes over an established connection of the given type. verified
// says whether conn is known to reach username; see PeerConn.verified.
func (p *Peers) adopt(username, typ string, conn net.Conn, verified bool) {
	switch typ {
	case protocol.ConnPeer:
		p.serve(p.register(username, conn, verified))
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

// Connect returns a message connection to username, reusing an open,
// verified one when there is one. Concurrent callers share a single dial.
func (p *Peers) Connect(ctx context.Context, username string) (*PeerConn, error) {
	p.mu.Lock()
	if pc := p.conns[username]; pc != nil && !pc.Closed() && pc.verified.Load() {
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

// runDial gets a verified connection to username for Connect: the one
// username opened to us, if it comes from the address the server has for
// them, or else a new one.
func (p *Peers) runDial(username string, call *dialCall) {
	// The dial is shared, so it must not depend on one caller's context.
	ctx, cancel := context.WithTimeout(context.Background(), addressTimeout+directTimeout+indirectTimeout)
	defer cancel()
	addr, err := p.lookup(ctx, username)
	if err == nil {
		p.mu.Lock()
		pc := p.conns[username]
		p.mu.Unlock()
		if pc != nil && !pc.Closed() && pc.from(addr.IP) {
			pc.verified.Store(true)
			call.pc = pc
		} else {
			var conn net.Conn
			if conn, err = p.dialAddr(ctx, username, addr, protocol.ConnPeer); err == nil {
				call.pc = p.register(username, conn, true)
				go p.serve(call.pc)
			}
		}
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
	frame := protocol.EncodePeerParts(m)
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
	addr, err := p.lookup(ctx, username)
	if err != nil {
		return nil, err
	}
	return p.dialAddr(ctx, username, addr, typ)
}

// lookup is peerAddress, failing with ErrUserOffline when the server does
// not know where username is.
func (p *Peers) lookup(ctx context.Context, username string) (protocol.PeerAddress, error) {
	addr, err := p.peerAddress(ctx, username)
	if err == nil && addr.Unknown() {
		err = fmt.Errorf("%w: %s", ErrUserOffline, username)
	}
	return addr, err
}

// dialAddr is dial with username's address already looked up.
func (p *Peers) dialAddr(ctx context.Context, username string, addr protocol.PeerAddress, typ string) (net.Conn, error) {
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

// register makes conn the current message connection for username,
// unless it is unverified and a verified one is open: otherwise anyone
// could take over what we send to username just by connecting under
// that name. A connection displaced or passed over is still read from,
// and left to finish what it is doing and idle out.
func (p *Peers) register(username string, conn net.Conn, verified bool) *PeerConn {
	pc := &PeerConn{Username: username, conn: conn, closed: make(chan struct{})}
	pc.verified.Store(verified)
	p.mu.Lock()
	if old := p.conns[username]; verified || old == nil || old.Closed() || !old.verified.Load() {
		p.conns[username] = pc
	}
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
		if err := p.readOne(r, pc.Username); err != nil {
			if !pc.Closed() {
				p.log.Debug("peer connection closed", "user", pc.Username, "err", err)
			}
			return
		}
	}
}

// readOne reads, decodes and handles one message. Responses nobody asked
// for are skipped without being read into memory, and large or
// compressed messages wait their turn so that many peers at once cannot
// make us hold more than a few of them.
func (p *Peers) readOne(r io.Reader, username string) error {
	n, err := protocol.ReadFrameLen(r, maxPeerFrame)
	if err != nil {
		return err
	}
	if n < 4 {
		_, err := io.CopyN(io.Discard, r, int64(n))
		return err
	}
	var hdr [4]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return err
	}
	code := binary.LittleEndian.Uint32(hdr[:])
	if max := maxFrameFor(code); n > max {
		return fmt.Errorf("peer message %d of %d bytes exceeds limit %d", code, n, max)
	}
	switch code {
	case protocol.CodeSharedFileListResponse, protocol.CodeFolderContentsResponse, protocol.CodeUserInfoResponse:
		if p.wants != nil && !p.wants(username, code) {
			p.log.Debug("dropping unrequested peer response", "user", username, "code", code, "len", n)
			_, err := io.CopyN(io.Discard, r, int64(n-4))
			return err
		}
	}
	// Every compressed message is inflated under one of two gates: big
	// frames take a turn before they are read, small ones only once they
	// are in memory, so slow senders cannot hold up the small ones.
	compressed := protocol.InflateLimit(code) > 0
	big := n > eagerBody || (n >= gateFrame && compressed)
	if big {
		if err := gate(p.bigDecodes, n); err != nil {
			return err
		}
		defer func() { <-p.bigDecodes }()
	}
	if code == protocol.CodeSharedFileListResponse && p.browseProgress != nil {
		size := uint64(n) + 4 // with the length prefix
		cr := &countingReader{r: r, got: 8, fn: func(got uint64) { p.browseProgress(username, got, size) }}
		cr.fn(cr.got)
		r = cr
	}
	payload, err := protocol.ReadBody(r, n-4)
	if err != nil {
		return err
	}
	if compressed && !big {
		if err := gate(p.inflates, n); err != nil {
			return err
		}
		defer func() { <-p.inflates }()
	}
	m, err := protocol.DecodePeerPayload(code, payload)
	if err != nil {
		p.log.Debug("bad peer message", "user", username, "err", err)
		return nil
	}
	if u, ok := m.(protocol.UnknownMessage); ok {
		p.log.Debug("unhandled peer message", "user", username, "code", u.Code, "len", len(u.Payload))
		return nil
	}
	p.handler(username, m)
	return nil
}

// gate waits for a turn in sem to decode an n byte message, giving up
// after bigDecodeWait.
func gate(sem chan struct{}, n uint32) error {
	t := time.NewTimer(bigDecodeWait)
	defer t.Stop()
	select {
	case sem <- struct{}{}:
		return nil
	case <-t.C:
		return fmt.Errorf("gave up waiting to decode a %d byte message", n)
	}
}

// PeerConn is a message connection (type ConnPeer) to one user.
type PeerConn struct {
	Username string
	conn     net.Conn
	// verified is set when the connection is known to reach Username: we
	// opened it to the address the server gave, or it was opened to us
	// from that address. Only verified connections carry what we send.
	verified atomic.Bool

	wmu       sync.Mutex
	closeOnce sync.Once
	closed    chan struct{}
}

// Send writes one message to the peer.
func (c *PeerConn) Send(m protocol.PeerMessage) error { return c.write(protocol.EncodePeerParts(m)) }

// write sends one frame, given in consecutive parts.
func (c *PeerConn) write(frame [][]byte) error {
	c.wmu.Lock()
	defer c.wmu.Unlock()
	if c.Closed() {
		return net.ErrClosed
	}
	c.conn.SetWriteDeadline(time.Now().Add(peerWriteTimeout))
	bufs := net.Buffers(slices.Clone(frame)) // WriteTo consumes it; Send may retry
	if _, err := bufs.WriteTo(c.conn); err != nil {
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

// from reports whether the connection comes from ip.
func (c *PeerConn) from(ip netip.Addr) bool {
	remote, err := netip.ParseAddrPort(c.conn.RemoteAddr().String())
	return err == nil && remote.Addr().Unmap() == ip.Unmap()
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

// countingReader reports the running total of bytes read.
type countingReader struct {
	r   io.Reader
	got uint64
	fn  func(got uint64)
}

func (c *countingReader) Read(b []byte) (int, error) {
	n, err := c.r.Read(b)
	if n > 0 {
		c.got += uint64(n)
		c.fn(c.got)
	}
	return n, err
}
