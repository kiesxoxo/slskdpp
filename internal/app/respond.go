package app

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/kiesxoxo/slskdpp/internal/client"
	"github.com/kiesxoxo/slskdpp/internal/protocol"
	"github.com/kiesxoxo/slskdpp/internal/shares"
	"github.com/kiesxoxo/slskdpp/internal/transfers"
)

const (
	// maxSearchReplies bounds search replies in flight; searches arriving
	// beyond it are dropped rather than queued.
	maxSearchReplies = 32
	// Browse, folder and user info replies in flight are bounded per user
	// and in all; requests beyond them are dropped, so repeating them
	// cannot pile up work. Peers name themselves, so the total is what
	// really bounds it.
	maxRepliesPerUser = 4
	maxReplies        = 32
	// maxFolderListings bounds folder listings being read from the index
	// at once: a request for a share's top folder lists all of it.
	maxFolderListings = 4
)

// Responder answers what other users ask of us: searches, browsing,
// folder listings and user info. Without shares (the debug commands) it
// only answers user info.
type Responder struct {
	c       *client.Client
	shares  *shares.Index
	uploads *transfers.Uploads
	sem     chan struct{}
	folders chan struct{}

	mu      sync.Mutex
	total   int
	perUser map[string]int // replies in flight
}

// NewResponder answers for c from idx and up, either of which may be nil.
func NewResponder(c *client.Client, idx *shares.Index, up *transfers.Uploads) *Responder {
	return &Responder{
		c: c, shares: idx, uploads: up,
		sem:     make(chan struct{}, maxSearchReplies),
		folders: make(chan struct{}, maxFolderListings),
		perUser: map[string]int{},
	}
}

// begin takes a place for a reply to user, reporting false when there is
// none.
func (r *Responder) begin(user string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.total >= maxReplies || r.perUser[user] >= maxRepliesPerUser {
		return false
	}
	r.total++
	r.perUser[user]++
	return true
}

func (r *Responder) end(user string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.total--
	if r.perUser[user]--; r.perUser[user] <= 0 {
		delete(r.perUser, user)
	}
}

func (r *Responder) status() transfers.UploadStatus {
	if r.uploads == nil {
		return transfers.UploadStatus{}
	}
	return r.uploads.Status()
}

// Search answers another user's search if anything we share matches.
func (r *Responder) Search(s protocol.SearchRequest) {
	if r.shares == nil {
		return
	}
	select {
	case r.sem <- struct{}{}:
	default:
		slog.Debug("dropping search, too many replies in flight", "user", s.Username)
		return
	}
	go func() {
		defer func() { <-r.sem }()
		files := r.shares.Search(s.Query)
		if len(files) == 0 {
			return
		}
		st := r.status()
		r.reply(s.Username, protocol.FileSearchResponse{
			Username:    r.c.Username(),
			Token:       s.Token,
			Files:       files,
			SlotFree:    st.SlotFree,
			AvgSpeed:    st.AvgSpeed,
			QueueLength: uint32(st.Queued),
		})
	}()
}

// Peer answers requests that need no transfer.
func (r *Responder) Peer(user string, m any) {
	var answer func() protocol.PeerMessage // nil: nothing to send
	switch m := m.(type) {
	case protocol.UserInfoRequest:
		answer = func() protocol.PeerMessage {
			st := r.status()
			return protocol.UserInfoResponse{
				Description: "slskdpp",
				UploadSlots: uint32(st.Slots),
				QueueSize:   uint32(st.Queued),
				SlotsFree:   st.SlotFree,
			}
		}
	case protocol.SharedFileListRequest:
		answer = func() protocol.PeerMessage {
			if r.shares == nil {
				return protocol.SharedFileListResponse{}
			}
			enc, err := r.shares.BrowseResponse()
			if err != nil {
				slog.Error("building share list", "err", err)
				return nil
			}
			return enc
		}
	case protocol.FolderContentsRequest:
		answer = func() protocol.PeerMessage {
			var dirs []protocol.Directory
			if r.shares != nil {
				r.folders <- struct{}{}
				dirs, _ = r.shares.FolderContents(m.Folder)
				<-r.folders
			}
			return protocol.FolderContentsResponse{Token: m.Token, Folder: m.Folder, Dirs: dirs}
		}
	default:
		slog.Debug("peer message", "user", user, "type", fmt.Sprintf("%T", m))
		return
	}
	if !r.begin(user) {
		slog.Debug("dropping request, too many replies in flight", "user", user, "type", fmt.Sprintf("%T", m))
		return
	}
	go func() {
		defer r.end(user)
		if reply := answer(); reply != nil {
			r.reply(user, reply)
		}
	}()
}

func (r *Responder) reply(user string, m protocol.PeerMessage) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if err := r.c.SendPeer(ctx, user, m); err != nil {
		slog.Debug("reply failed", "user", user, "type", fmt.Sprintf("%T", m), "err", err)
	}
}
