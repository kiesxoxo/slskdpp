// Package search runs outgoing searches and turns the responses into
// ranked, filterable folder hits.
package search

import (
	"cmp"
	"errors"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/kiesxoxo/slskdpp/internal/protocol"
)

// Network is the part of the client a search needs.
type Network interface {
	NewToken() uint32
	Search(token uint32, query string) error
}

type Options struct {
	// Window is how long a search accepts responses.
	Window time.Duration
	// TTL is how long a search is kept after it starts.
	TTL time.Duration
	// MaxSearches caps how many searches are kept; the oldest go first.
	MaxSearches int
	// MaxResponses caps how many users' responses one search accepts.
	MaxResponses int
	// MaxFiles caps how many files one search keeps.
	MaxFiles int
	// OnHit is called, outside any lock, for each folder hit a response
	// adds. It must not block.
	OnHit func(s *Search, h FolderHit)
}

func (o *Options) defaults() {
	if o.Window <= 0 {
		o.Window = 2 * time.Minute
	}
	if o.TTL <= 0 {
		o.TTL = 30 * time.Minute
	}
	o.TTL = max(o.TTL, o.Window)
	if o.MaxSearches <= 0 {
		o.MaxSearches = 50
	}
	if o.MaxResponses <= 0 {
		o.MaxResponses = 2000
	}
	if o.MaxFiles <= 0 {
		o.MaxFiles = 50000
	}
	if o.OnHit == nil {
		o.OnHit = func(*Search, FolderHit) {}
	}
}

// Manager tracks searches by token and routes responses to them.
type Manager struct {
	net  Network
	opts Options
	now  func() time.Time

	mu       sync.Mutex
	searches map[uint32]*Search
}

func NewManager(net Network, opts Options) *Manager {
	opts.defaults()
	return &Manager{net: net, opts: opts, now: time.Now, searches: make(map[uint32]*Search)}
}

// Start sends a search to the network.
func (m *Manager) Start(query string) (*Search, error) {
	query = strings.Join(strings.Fields(query), " ")
	if query == "" {
		return nil, errors.New("search: empty query")
	}
	now := m.now()
	s := &Search{
		ID:       m.net.NewToken(),
		Query:    query,
		Started:  now,
		Ends:     now.Add(m.opts.Window),
		terms:    queryTerms(query),
		now:      m.now,
		hits:     make(map[hitKey]*FolderHit),
		users:    make(map[string]struct{}),
		maxResp:  m.opts.MaxResponses,
		maxFiles: m.opts.MaxFiles,
	}
	m.mu.Lock()
	m.expireLocked(now)
	for len(m.searches) >= m.opts.MaxSearches {
		oldest := slices.MinFunc(m.listLocked(), func(a, b *Search) int { return a.Started.Compare(b.Started) })
		delete(m.searches, oldest.ID)
	}
	m.searches[s.ID] = s
	m.mu.Unlock()
	if err := m.net.Search(s.ID, query); err != nil {
		m.Remove(s.ID)
		return nil, err
	}
	return s, nil
}

func (m *Manager) Get(id uint32) (*Search, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.expireLocked(m.now())
	s, ok := m.searches[id]
	return s, ok
}

// List returns the kept searches, newest first.
func (m *Manager) List() []*Search {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.expireLocked(m.now())
	out := m.listLocked()
	slices.SortFunc(out, func(a, b *Search) int {
		return cmp.Or(b.Started.Compare(a.Started), cmp.Compare(b.ID, a.ID))
	})
	return out
}

func (m *Manager) Remove(id uint32) {
	m.mu.Lock()
	delete(m.searches, id)
	m.mu.Unlock()
}

// HandleResponse adds a peer's search response to the search it answers.
// username is who sent it on the wire, which wins over the name inside.
func (m *Manager) HandleResponse(username string, r protocol.FileSearchResponse) {
	m.mu.Lock()
	s := m.searches[r.Token]
	m.mu.Unlock()
	if s == nil {
		return
	}
	for _, h := range s.add(username, r) {
		m.opts.OnHit(s, h)
	}
}

func (m *Manager) listLocked() []*Search {
	out := make([]*Search, 0, len(m.searches))
	for _, s := range m.searches {
		out = append(out, s)
	}
	return out
}

func (m *Manager) expireLocked(now time.Time) {
	for id, s := range m.searches {
		if now.Sub(s.Started) > m.opts.TTL {
			delete(m.searches, id)
		}
	}
}

// Search is one outgoing search and the folder hits collected so far.
type Search struct {
	ID      uint32
	Query   string
	Started time.Time
	Ends    time.Time // responses after this are dropped

	terms    []string
	now      func() time.Time
	maxResp  int
	maxFiles int

	mu      sync.Mutex
	stopped bool
	hits    map[hitKey]*FolderHit
	users   map[string]struct{}
	files   int
}

type hitKey struct{ user, folder string }

type Stats struct {
	Responses int  `json:"responses"`
	Files     int  `json:"files"`
	Folders   int  `json:"folders"`
	Active    bool `json:"active"`
}

func (s *Search) Stats() Stats {
	s.mu.Lock()
	defer s.mu.Unlock()
	return Stats{
		Responses: len(s.users),
		Files:     s.files,
		Folders:   len(s.hits),
		Active:    s.activeLocked(),
	}
}

// Stop makes the search ignore further responses.
func (s *Search) Stop() {
	s.mu.Lock()
	s.stopped = true
	s.mu.Unlock()
}

func (s *Search) activeLocked() bool { return !s.stopped && s.now().Before(s.Ends) }

// Hits returns the folder hits that pass f, best first.
func (s *Search) Hits(f Filter) []FolderHit {
	s.mu.Lock()
	out := make([]FolderHit, 0, len(s.hits))
	for _, h := range s.hits {
		if f.match(h) {
			out = append(out, *h)
		}
	}
	s.mu.Unlock()
	rank(out)
	return out
}

// Albums returns the hits that pass f, deduped across users, best first.
func (s *Search) Albums(f Filter) []Album { return groupAlbums(s.Hits(f)) }

// add records one user's response and returns the folder hits it created.
func (s *Search) add(username string, r protocol.FileSearchResponse) []FolderHit {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.activeLocked() || len(s.users) >= s.maxResp {
		return nil
	}
	// One response per user; repeats would only duplicate files.
	if _, dup := s.users[username]; dup {
		return nil
	}
	s.users[username] = struct{}{}

	var touched []*FolderHit
	for _, pf := range r.Files {
		if s.files >= s.maxFiles {
			break
		}
		f := newFile(pf)
		folder, _ := splitPath(f.Path)
		k := hitKey{username, folder}
		h := s.hits[k]
		if h == nil {
			_, name := splitPath(folder)
			h = &FolderHit{User: username, Folder: folder, Name: name}
			s.hits[k] = h
			touched = append(touched, h)
		}
		h.Files = append(h.Files, f)
		s.files++
	}
	out := make([]FolderHit, len(touched))
	for i, h := range touched {
		summarize(h, r, s.terms)
		out[i] = *h
	}
	return out
}
