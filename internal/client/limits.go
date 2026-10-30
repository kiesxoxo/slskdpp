package client

import (
	"net"
	"sync"
	"time"

	"github.com/kiesxoxo/slskdpp/internal/protocol"
)

// Limits on what other users can make us hold. Everything a peer sends is
// untrusted, so these bound the memory and connections any number of
// misbehaving peers can tie up.
const (
	// maxInbound caps connections accepted on the listen port at once,
	// and maxInboundPerIP those from a single address. A popular search
	// brings a burst of short connections delivering results, so the
	// total is generous.
	maxInbound      = 1024
	maxInboundPerIP = 32

	// Compressed messages whose frame is at least gateFrame bytes, or any
	// frame larger than eagerBody, are read and decoded at most
	// maxBigDecodes at a time. Zlib inflates up to about 1000:1, so only
	// small compressed frames are cheap.
	gateFrame     = 16 << 10
	eagerBody     = 1 << 20
	maxBigDecodes = 3
	// bigDecodeWait is how long a connection waits for a turn before it
	// is dropped, so slow senders holding the turns cannot pile up the
	// rest.
	bigDecodeWait = 2 * time.Minute
)

// inboundLimiter counts open inbound connections, in total and per IP.
type inboundLimiter struct {
	mu    sync.Mutex
	total int
	perIP map[string]int
}

func newInboundLimiter() *inboundLimiter { return &inboundLimiter{perIP: make(map[string]int)} }

// admit wraps conn so closing it frees its place, or reports false when
// the limits are reached and conn should be refused.
func (l *inboundLimiter) admit(conn net.Conn) (net.Conn, bool) {
	ip := remoteIP(conn)
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.total >= maxInbound || l.perIP[ip] >= maxInboundPerIP {
		return nil, false
	}
	l.total++
	l.perIP[ip]++
	return &limitedConn{Conn: conn, release: func() { l.release(ip) }}, true
}

func (l *inboundLimiter) release(ip string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.total--
	if l.perIP[ip]--; l.perIP[ip] <= 0 {
		delete(l.perIP, ip)
	}
}

func remoteIP(conn net.Conn) string {
	if a, ok := conn.RemoteAddr().(*net.TCPAddr); ok {
		return a.IP.String()
	}
	host, _, err := net.SplitHostPort(conn.RemoteAddr().String())
	if err != nil {
		return conn.RemoteAddr().String()
	}
	return host
}

// limitedConn gives its limiter slot back on the first Close.
type limitedConn struct {
	net.Conn
	once    sync.Once
	release func()
}

func (c *limitedConn) Close() error {
	err := c.Conn.Close()
	c.once.Do(c.release)
	return err
}

// maxFrameFor caps a peer message's frame by its code. Only responses we
// asked for can be large; anything else a peer sends is small.
func maxFrameFor(code uint32) uint32 {
	switch code {
	case protocol.CodeSharedFileListResponse:
		return maxPeerFrame
	case protocol.CodeFolderContentsResponse:
		return 64 << 20
	case protocol.CodeFileSearchResponse, protocol.CodeUserInfoResponse:
		return 16 << 20
	}
	return 1 << 20
}
