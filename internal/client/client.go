package client

import (
	"context"
	"log/slog"
	"math/rand/v2"
	"net"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/kiesxoxo/slskdpp/internal/protocol"
)

// requestTimeout bounds a peer request when the caller sets no deadline.
const requestTimeout = 3 * time.Minute

// Handlers receive messages the client does not consume itself. Both run
// on network read goroutines and must not block.
type Handlers struct {
	// Server receives every decoded server message.
	Server func(m any)
	// Peer receives peer messages that do not answer one of our requests.
	Peer func(username string, m any)
	// State is told when the server session starts or ends.
	State func(loggedIn bool)
	// File takes over an incoming file connection for the transfer with
	// the given token, and must close it. It runs on its own goroutine.
	// Without it file connections are refused.
	File func(username string, token uint32, conn net.Conn)
}

// Client is a Soulseek session: the server connection plus the peer layer,
// with request/response matching for browse, folder and user info
// requests.
type Client struct {
	srv      *Server
	peers    *Peers
	h        Handlers
	username string
	tokens   atomic.Uint32

	mu      sync.Mutex
	waiters map[waitKey][]chan any
}

// waitKey identifies the response a request is waiting for.
type waitKey struct {
	user  string
	code  uint32
	token uint32
}

func New(cfg ServerConfig, h Handlers) *Client {
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	if h.Server == nil {
		h.Server = func(any) {}
	}
	if h.Peer == nil {
		h.Peer = func(string, any) {}
	}
	c := &Client{h: h, waiters: make(map[waitKey][]chan any)}
	c.tokens.Store(rand.Uint32N(1 << 30))
	c.srv = NewServer(cfg, c.onServer)
	c.srv.onState = h.State
	c.username = cfg.Username
	c.peers = newPeers(cfg.Username, cfg.ListenPort, cfg.Logger, c.srv, c.NewToken, c.onPeer, h.File)
	return c
}

// Run listens for peers and keeps the server session alive until ctx is
// done or the server rejects us for good.
func (c *Client) Run(ctx context.Context) error {
	// Listen first so we are reachable by the time we announce the port.
	if err := c.peers.Listen(ctx); err != nil {
		return err
	}
	return c.srv.Run(ctx)
}

func (c *Client) Server() *Server { return c.srv }

// Username is the account this client logs in as.
func (c *Client) Username() string { return c.username }

// LoggedIn reports whether the server session is up.
func (c *Client) LoggedIn() bool { return c.srv.LoggedIn() }

func (c *Client) Peers() *Peers { return c.peers }

// NewToken returns a fresh token for searches, connections and requests.
func (c *Client) NewToken() uint32 { return c.tokens.Add(1) }

// Search sends a network-wide search. Responses arrive as
// protocol.FileSearchResponse peer messages carrying token.
func (c *Client) Search(token uint32, query string) error {
	return c.srv.Send(protocol.FileSearch{Token: token, Query: query})
}

// WatchUser asks the server to report username's status changes, which
// arrive as protocol.UserStatusResponse server messages.
func (c *Client) WatchUser(username string) error {
	return c.srv.Send(protocol.WatchUser{Username: username})
}

// ConnectFile opens a file connection to username for the transfer with
// the given token, as the uploading side does.
func (c *Client) ConnectFile(ctx context.Context, username string, token uint32) (net.Conn, error) {
	return c.peers.ConnectFile(ctx, username, token)
}

// SendPeer delivers a message to username, connecting if needed.
func (c *Client) SendPeer(ctx context.Context, username string, m protocol.PeerMessage) error {
	return c.peers.Send(ctx, username, m)
}

// Browse fetches a user's complete share list.
func (c *Client) Browse(ctx context.Context, username string) (protocol.SharedFileListResponse, error) {
	key := waitKey{username, protocol.CodeSharedFileListResponse, 0}
	m, err := c.request(ctx, username, protocol.SharedFileListRequest{}, key)
	if err != nil {
		return protocol.SharedFileListResponse{}, err
	}
	return m.(protocol.SharedFileListResponse), nil
}

// FolderContents lists one folder (and its subfolders) of a user's share.
func (c *Client) FolderContents(ctx context.Context, username, folder string) (protocol.FolderContentsResponse, error) {
	token := c.NewToken()
	key := waitKey{username, protocol.CodeFolderContentsResponse, token}
	m, err := c.request(ctx, username, protocol.FolderContentsRequest{Token: token, Folder: folder}, key)
	if err != nil {
		return protocol.FolderContentsResponse{}, err
	}
	return m.(protocol.FolderContentsResponse), nil
}

// UserInfo fetches a user's description and upload slot details.
func (c *Client) UserInfo(ctx context.Context, username string) (protocol.UserInfoResponse, error) {
	key := waitKey{username, protocol.CodeUserInfoResponse, 0}
	m, err := c.request(ctx, username, protocol.UserInfoRequest{}, key)
	if err != nil {
		return protocol.UserInfoResponse{}, err
	}
	return m.(protocol.UserInfoResponse), nil
}

// request sends req to username and waits for the response matching key.
func (c *Client) request(ctx context.Context, username string, req protocol.PeerMessage, key waitKey) (any, error) {
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	ch := make(chan any, 1)
	c.mu.Lock()
	c.waiters[key] = append(c.waiters[key], ch)
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		if rest := slices.DeleteFunc(c.waiters[key], func(w chan any) bool { return w == ch }); len(rest) > 0 {
			c.waiters[key] = rest
		} else {
			delete(c.waiters, key)
		}
		c.mu.Unlock()
	}()
	if err := c.peers.Send(ctx, username, req); err != nil {
		return nil, err
	}
	select {
	case m := <-ch:
		return m, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (c *Client) onServer(m any) {
	c.peers.handleServer(m)
	c.h.Server(m)
}

func (c *Client) onPeer(username string, m any) {
	var key waitKey
	switch m := m.(type) {
	case protocol.SharedFileListResponse:
		key = waitKey{username, protocol.CodeSharedFileListResponse, 0}
	case protocol.FolderContentsResponse:
		key = waitKey{username, protocol.CodeFolderContentsResponse, m.Token}
	case protocol.UserInfoResponse:
		key = waitKey{username, protocol.CodeUserInfoResponse, 0}
	}
	if key.code != 0 {
		c.mu.Lock()
		waiters := c.waiters[key]
		delete(c.waiters, key)
		c.mu.Unlock()
		if len(waiters) > 0 {
			for _, ch := range waiters {
				ch <- m
			}
			return
		}
	}
	c.h.Peer(username, m)
}
