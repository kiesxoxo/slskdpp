// Package client manages the network side of a Soulseek session: the
// server connection, peer connections and the distributed network.
package client

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"net"
	"sync"
	"time"

	"github.com/kiesxoxo/slskdpp/internal/protocol"
)

const (
	clientVersion      = 160
	clientMinorVersion = 1

	maxServerFrame = 32 << 20
	dialTimeout    = 15 * time.Second
	loginTimeout   = 30 * time.Second
	pingInterval   = 2 * time.Minute
	sendQueueSize  = 256

	minBackoff = time.Second
	maxBackoff = 5 * time.Minute
	// A session that stayed up this long resets the reconnect backoff.
	stableSession = time.Minute
)

var (
	ErrNotConnected = errors.New("client: not logged in to server")
	ErrSendQueue    = errors.New("client: server send queue full")
	// ErrRelogged means the account was logged in from somewhere else.
	ErrRelogged = errors.New("client: logged in from another location")
)

// LoginError is a login the server rejected. Retrying will not help.
type LoginError struct {
	Reason string
	Detail string
}

func (e *LoginError) Error() string {
	if e.Detail != "" {
		return fmt.Sprintf("login rejected: %s (%s)", e.Reason, e.Detail)
	}
	return "login rejected: " + e.Reason
}

type ServerConfig struct {
	Addr       string
	Username   string
	Password   string
	ListenPort uint32
	Logger     *slog.Logger
}

// Server is a connection to the Soulseek server that logs in and stays
// connected, reconnecting with backoff when the link drops.
type Server struct {
	cfg     ServerConfig
	log     *slog.Logger
	handler func(any)
	// onState, when set, is told each time a session starts or ends.
	onState func(loggedIn bool)

	mu     sync.Mutex
	sendCh chan []byte   // nil while logged out
	ready  chan struct{} // closed while logged in
	login  protocol.LoginResponse
	// Disconnect pauses the session until Connect.
	paused     bool
	resume     chan struct{} // signalled by Connect while paused
	retryNow   chan struct{} // cuts a reconnect wait short
	endSession context.CancelFunc
	connecting bool
	remote     net.Addr // the server's address while connected
}

// ServerStatus describes the server connection.
type ServerStatus struct {
	Addr       string   // as configured
	Remote     net.Addr // resolved, while connected
	Connecting bool     // dialling or logging in
	LoggedIn   bool
	Paused     bool // disconnected on purpose
}

// NewServer creates a server session. handler receives every decoded
// server message (including the LoginResponse) on the read goroutine, so it
// must not block.
func NewServer(cfg ServerConfig, handler func(any)) *Server {
	log := cfg.Logger
	if log == nil {
		log = slog.Default()
	}
	if handler == nil {
		handler = func(any) {}
	}
	return &Server{
		cfg:      cfg,
		log:      log.With("component", "server"),
		handler:  handler,
		ready:    make(chan struct{}),
		resume:   make(chan struct{}, 1),
		retryNow: make(chan struct{}, 1),
	}
}

// Status describes the connection now.
func (s *Server) Status() ServerStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	return ServerStatus{Addr: s.cfg.Addr, Remote: s.remote, Connecting: s.connecting, LoggedIn: s.sendCh != nil, Paused: s.paused}
}

// Disconnect ends the session and stays disconnected until Connect.
func (s *Server) Disconnect() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.paused = true
	if s.endSession != nil {
		s.endSession()
	}
}

// Connect undoes Disconnect. While reconnecting after a dropped session it
// retries at once instead, and reports that it did.
func (s *Server) Connect() (retried bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch {
	case s.paused:
		s.paused = false
		signal(s.resume)
	case s.sendCh == nil:
		signal(s.retryNow)
		return true
	}
	return false
}

func signal(ch chan struct{}) {
	select {
	case ch <- struct{}{}:
	default:
	}
}

// waitWhilePaused blocks while the session is paused.
func (s *Server) waitWhilePaused(ctx context.Context) error {
	for {
		s.mu.Lock()
		paused := s.paused
		s.mu.Unlock()
		if !paused {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-s.resume:
		}
	}
}

// Run keeps a logged-in session alive until ctx is cancelled or the server
// rejects us permanently (a *LoginError or ErrRelogged).
func (s *Server) Run(ctx context.Context) error {
	backoff := minBackoff
	for {
		if err := s.waitWhilePaused(ctx); err != nil {
			return err
		}
		start := time.Now()
		err := s.session(ctx)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		s.mu.Lock()
		paused := s.paused
		s.mu.Unlock()
		if paused {
			s.log.Info("disconnected from the server on request")
			backoff = minBackoff
			continue
		}
		var le *LoginError
		if errors.As(err, &le) || errors.Is(err, ErrRelogged) {
			return err
		}
		if time.Since(start) > stableSession {
			backoff = minBackoff
		}
		wait := backoff/2 + rand.N(backoff/2+1)
		s.log.Warn("server connection lost", "err", err, "retry_in", wait.Round(time.Millisecond))
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(wait):
		case <-s.retryNow:
		}
		backoff = min(backoff*2, maxBackoff)
	}
}

// LoggedIn reports whether a session is currently established.
func (s *Server) LoggedIn() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sendCh != nil
}

// WaitLoggedIn blocks until a session is established or ctx is done.
func (s *Server) WaitLoggedIn(ctx context.Context) error {
	s.mu.Lock()
	ready := s.ready
	s.mu.Unlock()
	select {
	case <-ready:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Login returns the response from the most recent successful login.
func (s *Server) Login() protocol.LoginResponse {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.login
}

// Send queues a message for the server without blocking.
func (s *Server) Send(m protocol.ServerRequest) error {
	s.mu.Lock()
	ch := s.sendCh
	s.mu.Unlock()
	if ch == nil {
		return ErrNotConnected
	}
	select {
	case ch <- protocol.EncodeServer(m):
		return nil
	default:
		return ErrSendQueue
	}
}

// session runs one connection from dial to disconnect.
// SetListenPort changes the port announced to the server, now if logged
// in and on every later login.
func (s *Server) SetListenPort(port uint32) {
	s.mu.Lock()
	s.cfg.ListenPort = port
	s.mu.Unlock()
	s.Send(protocol.SetWaitPort{Port: port})
}

func (s *Server) session(ctx context.Context) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	s.mu.Lock()
	if s.paused {
		s.mu.Unlock()
		return nil
	}
	s.endSession, s.connecting = cancel, true
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.endSession, s.connecting, s.remote = nil, false, nil
		s.mu.Unlock()
	}()
	conn, resp, err := dialAndLogin(ctx, s.cfg, s.handler)
	s.mu.Lock()
	s.connecting = false
	s.mu.Unlock()
	if err != nil {
		return err
	}
	defer conn.Close()
	s.log.Info("logged in", "user", s.cfg.Username, "ip", resp.OwnIP, "greeting", resp.Greeting)

	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()

	sendCh := make(chan []byte, sendQueueSize)
	s.mu.Lock()
	port := s.cfg.ListenPort
	s.mu.Unlock()
	// Announce ourselves before anything else can be queued.
	for _, m := range []protocol.ServerRequest{
		protocol.SetWaitPort{Port: port},
		protocol.SharedFoldersFiles{},
		protocol.SetStatus{Status: protocol.StatusOnline},
	} {
		sendCh <- protocol.EncodeServer(m)
	}
	s.mu.Lock()
	s.sendCh = sendCh
	s.remote = conn.RemoteAddr()
	s.login = resp
	close(s.ready)
	s.mu.Unlock()
	s.notifyState(true)
	defer func() {
		s.mu.Lock()
		s.sendCh = nil
		s.ready = make(chan struct{})
		s.mu.Unlock()
		s.notifyState(false)
	}()

	writeErr := make(chan error, 1)
	go func() { writeErr <- s.writeLoop(ctx, conn, sendCh) }()

	readErr := s.readLoop(conn)
	cancel()
	if werr := <-writeErr; readErr == nil {
		readErr = werr
	}
	return readErr
}

func (s *Server) notifyState(loggedIn bool) {
	if s.onState != nil {
		s.onState(loggedIn)
	}
}

func (s *Server) writeLoop(ctx context.Context, conn net.Conn, sendCh <-chan []byte) error {
	ping := time.NewTicker(pingInterval)
	defer ping.Stop()
	for {
		var frame []byte
		select {
		case <-ctx.Done():
			return nil
		case frame = <-sendCh:
		case <-ping.C:
			frame = protocol.EncodeServer(protocol.ServerPing{})
		}
		conn.SetWriteDeadline(time.Now().Add(30 * time.Second))
		if _, err := conn.Write(frame); err != nil {
			conn.Close()
			return err
		}
	}
}

func (s *Server) readLoop(conn net.Conn) error {
	for {
		body, err := protocol.ReadFrame(conn, maxServerFrame)
		if err != nil {
			return err
		}
		m, err := protocol.DecodeServer(body)
		if err != nil {
			s.log.Debug("bad server message", "err", err)
			continue
		}
		switch m := m.(type) {
		case protocol.Ping:
			continue
		case protocol.Relogged:
			s.handler(m)
			return ErrRelogged
		case protocol.UnknownMessage:
			s.log.Debug("unhandled server message", "code", m.Code, "len", len(m.Payload))
			continue
		}
		s.handler(m)
	}
}

// dialAndLogin connects and completes the login handshake. Messages that
// arrive before the login response are passed to handler.
func dialAndLogin(ctx context.Context, cfg ServerConfig, handler func(any)) (net.Conn, protocol.LoginResponse, error) {
	d := net.Dialer{Timeout: dialTimeout, KeepAlive: 30 * time.Second}
	conn, err := d.DialContext(ctx, "tcp", cfg.Addr)
	if err != nil {
		return nil, protocol.LoginResponse{}, err
	}
	conn.SetDeadline(time.Now().Add(loginTimeout))
	login := protocol.Login{
		Username:     cfg.Username,
		Password:     cfg.Password,
		Version:      clientVersion,
		MinorVersion: clientMinorVersion,
	}
	if _, err := conn.Write(protocol.EncodeServer(login)); err != nil {
		conn.Close()
		return nil, protocol.LoginResponse{}, err
	}
	for {
		body, err := protocol.ReadFrame(conn, maxServerFrame)
		if err != nil {
			conn.Close()
			return nil, protocol.LoginResponse{}, fmt.Errorf("waiting for login response: %w", err)
		}
		m, err := protocol.DecodeServer(body)
		if err != nil {
			continue
		}
		resp, ok := m.(protocol.LoginResponse)
		if !ok {
			if handler != nil {
				handler(m)
			}
			continue
		}
		if !resp.Success {
			conn.Close()
			return nil, resp, &LoginError{Reason: resp.Reason, Detail: resp.Detail}
		}
		if handler != nil {
			handler(resp)
		}
		conn.SetDeadline(time.Time{})
		return conn, resp, nil
	}
}

// CheckLogin logs in once and disconnects, reporting the server's answer.
func CheckLogin(ctx context.Context, cfg ServerConfig) (protocol.LoginResponse, error) {
	conn, resp, err := dialAndLogin(ctx, cfg, nil)
	if err != nil {
		return resp, err
	}
	conn.Close()
	return resp, nil
}
