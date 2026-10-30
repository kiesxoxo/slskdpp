// Package events is an in-process publish/subscribe bus that feeds live
// updates to the web UI.
package events

import (
	"sync"

	"github.com/kiesxoxo/slskdpp/internal/search"
)

// Event types.
const (
	TypeServerState = "server.state"
	TypeSearchHit   = "search.hit"
	TypeSearchDone  = "search.done"
	// Data is a transfers.View.
	TypeDownload = "download"
	// Data is DownloadRemoved.
	TypeDownloadRemoved = "download.removed"
	// Data is a transfers.UploadView.
	TypeUpload = "upload"
	// Data is DownloadRemoved (an upload ID).
	TypeUploadRemoved = "upload.removed"
	// Data is shares.Stats.
	TypeShares = "shares"
	// Data is a wishlist.Entry.
	TypeWishlist = "wishlist"
)

// Event is one update. Data is marshalled to JSON for subscribers.
type Event struct {
	ID     uint64 // assigned by the bus, increasing
	Type   string
	Search uint32 // the search this event belongs to, or 0
	Data   any
}

// Payloads.

type ServerState struct {
	Connected bool   `json:"connected"`
	Username  string `json:"username"`
}

type SearchHit struct {
	SearchID uint32           `json:"search_id"`
	Hit      search.FolderHit `json:"hit"`
}

type SearchDone struct {
	SearchID uint32       `json:"search_id"`
	Stats    search.Stats `json:"stats"`
}

type DownloadRemoved struct {
	ID int64 `json:"id"`
}

// Bus delivers events to subscribers without ever blocking a publisher.
type Bus struct {
	mu   sync.Mutex
	seq  uint64
	subs map[*Sub]struct{}
}

func NewBus() *Bus { return &Bus{subs: make(map[*Sub]struct{})} }

// Sub is a subscription. C is closed when the subscription ends, either
// through Unsubscribe or because the subscriber fell too far behind; a
// subscriber that sees C close should resynchronize its state.
type Sub struct {
	C      <-chan Event
	ch     chan Event
	filter func(Event) bool
}

// Subscribe returns a subscription buffering up to buffer events. filter,
// if not nil, selects which events it receives.
func (b *Bus) Subscribe(buffer int, filter func(Event) bool) *Sub {
	ch := make(chan Event, buffer)
	s := &Sub{C: ch, ch: ch, filter: filter}
	b.mu.Lock()
	b.subs[s] = struct{}{}
	b.mu.Unlock()
	return s
}

func (b *Bus) Unsubscribe(s *Sub) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if _, ok := b.subs[s]; ok {
		delete(b.subs, s)
		close(s.ch)
	}
}

// Publish sends e to every matching subscriber. A subscriber whose buffer
// is full is dropped rather than waited for.
func (b *Bus) Publish(e Event) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.seq++
	e.ID = b.seq
	for s := range b.subs {
		if s.filter != nil && !s.filter(e) {
			continue
		}
		select {
		case s.ch <- e:
		default:
			delete(b.subs, s)
			close(s.ch)
		}
	}
}
