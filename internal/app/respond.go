package app

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/kiesxoxo/slskdpp/internal/client"
	"github.com/kiesxoxo/slskdpp/internal/protocol"
	"github.com/kiesxoxo/slskdpp/internal/shares"
	"github.com/kiesxoxo/slskdpp/internal/transfers"
)

// maxSearchReplies bounds search replies in flight; searches arriving
// beyond it are dropped rather than queued.
const maxSearchReplies = 32

// Responder answers what other users ask of us: searches, browsing,
// folder listings and user info. Without shares (the debug commands) it
// only answers user info.
type Responder struct {
	c       *client.Client
	shares  *shares.Index
	uploads *transfers.Uploads
	sem     chan struct{}
}

// NewResponder answers for c from idx and up, either of which may be nil.
func NewResponder(c *client.Client, idx *shares.Index, up *transfers.Uploads) *Responder {
	return &Responder{c: c, shares: idx, uploads: up, sem: make(chan struct{}, maxSearchReplies)}
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
	switch m := m.(type) {
	case protocol.UserInfoRequest:
		st := r.status()
		go r.reply(user, protocol.UserInfoResponse{
			Description: "slskdpp",
			UploadSlots: uint32(st.Slots),
			QueueSize:   uint32(st.Queued),
			SlotsFree:   st.SlotFree,
		})
	case protocol.SharedFileListRequest:
		if r.shares == nil {
			go r.reply(user, protocol.SharedFileListResponse{})
			return
		}
		go func() {
			enc, err := r.shares.BrowseResponse()
			if err != nil {
				slog.Error("building share list", "err", err)
				return
			}
			r.reply(user, enc)
		}()
	case protocol.FolderContentsRequest:
		go func() {
			var dirs []protocol.Directory
			if r.shares != nil {
				dirs, _ = r.shares.FolderContents(m.Folder)
			}
			r.reply(user, protocol.FolderContentsResponse{Token: m.Token, Folder: m.Folder, Dirs: dirs})
		}()
	default:
		slog.Debug("peer message", "user", user, "type", fmt.Sprintf("%T", m))
	}
}

func (r *Responder) reply(user string, m protocol.PeerMessage) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if err := r.c.SendPeer(ctx, user, m); err != nil {
		slog.Debug("reply failed", "user", user, "type", fmt.Sprintf("%T", m), "err", err)
	}
}
