// Package testserver is a minimal Soulseek server for tests: it logs
// anyone in, resolves peer addresses, relays ConnectToPeer and
// CantConnectToPeer, and passes searches on to every other user.
package testserver

import (
	"net"
	"net/netip"
	"sync"

	"github.com/kiesxoxo/slskdpp/internal/protocol"
)

type Server struct {
	ln net.Listener
	// WishlistInterval, in seconds, is sent after each login; 0 sends
	// nothing.
	WishlistInterval uint32

	mu    sync.Mutex
	users map[string]*user
}

type user struct {
	conn net.Conn
	port uint32
	wmu  sync.Mutex
}

func (u *user) send(code uint32, fill func(w *protocol.Writer)) {
	var w protocol.Writer
	w.Uint32(code)
	fill(&w)
	u.wmu.Lock()
	u.conn.Write(protocol.Frame(w.Bytes()))
	u.wmu.Unlock()
}

// Start listens on a free local port.
func Start() (*Server, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	s := &Server{ln: ln, users: map[string]*user{}}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go s.serve(c)
		}
	}()
	return s, nil
}

func (s *Server) Addr() string { return s.ln.Addr().String() }

func (s *Server) Close() error { return s.ln.Close() }

// Online reports whether username is logged in and listening for peers.
func (s *Server) Online(username string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	u := s.users[username]
	return u != nil && u.port != 0
}

func (s *Server) user(name string) *user {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.users[name]
}

var localhost = netip.MustParseAddr("127.0.0.1")

func (s *Server) serve(c net.Conn) {
	defer c.Close()
	body, err := protocol.ReadFrame(c, 1<<20)
	if err != nil {
		return
	}
	r := protocol.NewReader(body)
	if r.Uint32() != protocol.CodeLogin {
		return
	}
	name := r.String()
	me := &user{conn: c}
	s.mu.Lock()
	s.users[name] = me
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		if s.users[name] == me {
			delete(s.users, name)
		}
		s.mu.Unlock()
	}()
	me.send(protocol.CodeLogin, func(w *protocol.Writer) {
		w.Bool(true)
		w.String("testserver")
		w.IP(localhost)
		w.String("")
		w.Bool(false)
	})
	if s.WishlistInterval > 0 {
		me.send(protocol.CodeWishlistInterval, func(w *protocol.Writer) { w.Uint32(s.WishlistInterval) })
	}
	for {
		body, err := protocol.ReadFrame(c, 1<<20)
		if err != nil {
			return
		}
		r := protocol.NewReader(body)
		switch r.Uint32() {
		case protocol.CodeSetWaitPort:
			s.mu.Lock()
			me.port = r.Uint32()
			s.mu.Unlock()
		case protocol.CodeGetPeerAddress:
			target := r.String()
			s.mu.Lock()
			ip, port := netip.IPv4Unspecified(), uint32(0)
			if u := s.users[target]; u != nil && u.port != 0 {
				ip, port = localhost, u.port
			}
			s.mu.Unlock()
			me.send(protocol.CodeGetPeerAddress, func(w *protocol.Writer) {
				w.String(target)
				w.IP(ip)
				w.Uint32(port)
			})
		case protocol.CodeConnectToPeer:
			token, target, typ := r.Uint32(), r.String(), r.String()
			u := s.user(target)
			if u == nil {
				me.send(protocol.CodeCantConnectToPeer, func(w *protocol.Writer) { w.Uint32(token) })
				continue
			}
			s.mu.Lock()
			port := me.port
			s.mu.Unlock()
			u.send(protocol.CodeConnectToPeer, func(w *protocol.Writer) {
				w.String(name)
				w.String(typ)
				w.IP(localhost)
				w.Uint32(port)
				w.Uint32(token)
				w.Bool(false)
			})
		case protocol.CodeCantConnectToPeer:
			token, target := r.Uint32(), r.String()
			if u := s.user(target); u != nil {
				u.send(protocol.CodeCantConnectToPeer, func(w *protocol.Writer) { w.Uint32(token) })
			}
		case protocol.CodeFileSearch, protocol.CodeWishlistSearch:
			token, query := r.Uint32(), r.String()
			s.mu.Lock()
			var others []*user
			for n, u := range s.users {
				if n != name {
					others = append(others, u)
				}
			}
			s.mu.Unlock()
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
