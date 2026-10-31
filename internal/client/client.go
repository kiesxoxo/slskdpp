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
	// Search receives other users' searches, whether the server sent them
	// directly, embedded, or through our distributed parent. It must not
	// block.
	Search func(protocol.SearchRequest)
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
	distrib  *distrib
	h        Handlers
	username string
	tokens   atomic.Uint32
	shared   atomic.Uint64 // folders<<32 | files, as announced to the server

	mu      sync.Mutex
	waiters map[waitKey][]chan any
	browses map[string]BrowseProgress // share lists being received, by user
}

// BrowseProgress is how much of a user's share list has arrived.
type BrowseProgress struct {
	Username string
	Bytes    uint64 // received so far
	Size     uint64 // 0 until the response starts arriving
}

// browseKept is how long a finished browse's progress stays readable.
const browseKept = 5 * time.Second

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
	if h.State == nil {
		h.State = func(bool) {}
	}
	if h.Search == nil {
		h.Search = func(protocol.SearchRequest) {}
	}
	c := &Client{h: h, waiters: make(map[waitKey][]chan any), browses: make(map[string]BrowseProgress)}
	c.tokens.Store(rand.Uint32N(1 << 30))
	c.srv = NewServer(cfg, c.onServer)
	c.srv.onState = c.onState
	c.username = cfg.Username
	c.distrib = newDistrib(c, cfg.Logger)
	c.peers = newPeers(cfg.Username, cfg.ListenPort, cfg.Logger, c.srv, c.NewToken, c.onPeer, h.File)
	c.peers.wants = c.awaiting
	c.peers.browseProgress = c.onBrowseProgress
	return c
}

// Run listens for peers and keeps the server session alive until ctx is
// done or the server rejects us for good.
func (c *Client) Run(ctx context.Context) error {
	// Listen first so we are reachable by the time we announce the port.
	if err := c.peers.Listen(ctx); err != nil {
		return err
	}
	go c.distrib.stop(ctx)
	return c.srv.Run(ctx)
}

func (c *Client) Server() *Server { return c.srv }

// Username is the account this client logs in as.
func (c *Client) Username() string { return c.username }

// LoggedIn reports whether the server session is up.
func (c *Client) LoggedIn() bool { return c.srv.LoggedIn() }

// DistribParent is the user we receive distributed searches from, if any.
func (c *Client) DistribParent() string { return c.distrib.Parent() }

// SetShared tells the server how much we share, now and after each login.
func (c *Client) SetShared(folders, files uint32) {
	c.shared.Store(uint64(folders)<<32 | uint64(files))
	if c.srv.LoggedIn() {
		c.srv.Send(protocol.SharedFoldersFiles{Folders: folders, Files: files})
	}
}

func (c *Client) onState(loggedIn bool) {
	if loggedIn {
		v := c.shared.Load()
		c.srv.Send(protocol.SharedFoldersFiles{Folders: uint32(v >> 32), Files: uint32(v)})
		c.distrib.loggedIn()
	} else {
		c.distrib.drop()
	}
	c.h.State(loggedIn)
}

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

// WishlistSearch sends a search on the wishlist schedule. Responses
// arrive like those to Search.
func (c *Client) WishlistSearch(token uint32, query string) error {
	return c.srv.Send(protocol.WishlistSearch{Token: token, Query: query})
}

// SendPeer delivers a message to username, connecting if needed.
func (c *Client) SendPeer(ctx context.Context, username string, m protocol.PeerMessage) error {
	return c.peers.Send(ctx, username, m)
}

// Browse fetches a user's complete share list.
func (c *Client) Browse(ctx context.Context, username string) (protocol.SharedFileListResponse, error) {
	key := waitKey{username, protocol.CodeSharedFileListResponse, 0}
	c.mu.Lock()
	c.browses[username] = BrowseProgress{Username: username}
	c.mu.Unlock()
	defer time.AfterFunc(browseKept, func() {
		c.mu.Lock()
		delete(c.browses, username)
		c.mu.Unlock()
	})
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

// BrowseStatus reports the progress of a browse of username that is under
// way or finished in the last few seconds.
func (c *Client) BrowseStatus(username string) (BrowseProgress, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	p, ok := c.browses[username]
	return p, ok
}

func (c *Client) onBrowseProgress(username string, got, size uint64) {
	c.mu.Lock()
	if _, ok := c.browses[username]; ok {
		c.browses[username] = BrowseProgress{Username: username, Bytes: got, Size: size}
	}
	c.mu.Unlock()
}

// UserStatus asks the server whether username is online.
func (c *Client) UserStatus(ctx context.Context, username string) (protocol.UserStatusResponse, error) {
	key := waitKey{username, protocol.CodeGetUserStatus, 0}
	m, err := c.await(ctx, key, func(context.Context) error { return c.srv.Send(protocol.GetUserStatus{Username: username}) })
	if err != nil {
		return protocol.UserStatusResponse{}, err
	}
	return m.(protocol.UserStatusResponse), nil
}

// PeerAddress asks the server where username listens. It fails with
// ErrUserOffline when the server does not know.
func (c *Client) PeerAddress(ctx context.Context, username string) (protocol.PeerAddress, error) {
	a, err := c.peers.peerAddress(ctx, username)
	if err == nil && a.Unknown() {
		err = ErrUserOffline
	}
	return a, err
}

// ServerStatus describes the server connection.
func (c *Client) ServerStatus() ServerStatus { return c.srv.Status() }

// SetListen moves the peer listener and tells the server the new port.
// ip "" listens on every address.
func (c *Client) SetListen(ip string, port uint32) error {
	if err := c.peers.Relisten(ip, port); err != nil {
		return err
	}
	c.srv.SetListenPort(port)
	return nil
}

// ListenAddr is the address ("" for all) and port peers can reach us on.
func (c *Client) ListenAddr() (string, uint32) { return c.peers.ListenAddr() }

// Disconnect leaves the server until Connect.
func (c *Client) Disconnect() { c.srv.Disconnect() }

// Connect undoes Disconnect, or retries at once if reconnecting, which it
// then reports.
func (c *Client) Connect() bool { return c.srv.Connect() }

// request sends req to username and waits for the response matching key.
func (c *Client) request(ctx context.Context, username string, req protocol.PeerMessage, key waitKey) (any, error) {
	return c.await(ctx, key, func(ctx context.Context) error { return c.peers.Send(ctx, username, req) })
}

// await registers for the response matching key, sends the request and
// waits.
func (c *Client) await(ctx context.Context, key waitKey, send func(context.Context) error) (any, error) {
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
	if err := send(ctx); err != nil {
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
	if s, ok := m.(protocol.SearchRequest); ok {
		if s.Username != c.username {
			c.h.Search(s)
		}
		return
	}
	if st, ok := m.(protocol.UserStatusResponse); ok {
		c.deliver(waitKey{st.Username, protocol.CodeGetUserStatus, 0}, m)
	}
	c.peers.handleServer(m)
	c.distrib.handleServer(m)
	c.h.Server(m)
}

// awaiting reports whether a request to username waits for a response
// with code.
func (c *Client) awaiting(username string, code uint32) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	for k := range c.waiters {
		if k.user == username && k.code == code {
			return true
		}
	}
	return false
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
	if key.code != 0 && c.deliver(key, m) {
		return
	}
	c.h.Peer(username, m)
}

// deliver hands m to the requests waiting for key, reporting whether
// there were any.
func (c *Client) deliver(key waitKey, m any) bool {
	c.mu.Lock()
	waiters := c.waiters[key]
	delete(c.waiters, key)
	c.mu.Unlock()
	for _, ch := range waiters {
		ch <- m
	}
	return len(waiters) > 0
}
