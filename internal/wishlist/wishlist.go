// Package wishlist runs saved searches on a schedule and can download the
// best match on its own.
package wishlist

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/kiesxoxo/slskdpp/internal/protocol"
	"github.com/kiesxoxo/slskdpp/internal/search"
	"github.com/kiesxoxo/slskdpp/internal/transfers"
)

// Entry is one saved search.
type Entry struct {
	ID           int64         `json:"id"`
	Query        string        `json:"query"`
	Filter       search.Filter `json:"filter"`
	AutoDownload bool          `json:"auto_download"`
	Enabled      bool          `json:"enabled"`
	Created      time.Time     `json:"created"`
	LastRun      time.Time     `json:"last_run,omitzero"`
	LastSearch   uint32        `json:"last_search,omitempty"` // search ID while its results are kept
	Matches      int           `json:"matches"`               // albums that passed the filter last time
	Running      bool          `json:"running,omitempty"`
	// What auto-download fetched; the entry is disabled then.
	GotUser   string    `json:"got_user,omitempty"`
	GotFolder string    `json:"got_folder,omitempty"`
	GotAt     time.Time `json:"got_at,omitzero"`
}

// Searches starts searches.
type Searches interface {
	Start(query string) (*search.Search, error)
	StartWishlist(query string) (*search.Search, error)
	Get(id uint32) (*search.Search, bool)
}

// Lister fetches a remote folder's full contents.
type Lister interface {
	FolderContents(ctx context.Context, username, folder string) (protocol.FolderContentsResponse, error)
}

// Downloader queues files.
type Downloader interface {
	Enqueue(username string, files []transfers.FileRequest, folder string) ([]transfers.View, error)
}

type Config struct {
	// Interval is the time between wishlist searches; the server's
	// interval (WishlistInterval) is the minimum. 0 means the server's.
	Interval time.Duration
	// Window is how long results are collected before judging them.
	Window time.Duration
	// OnUpdate is told about every change; it must not block.
	OnUpdate func(Entry)
	Logger   *slog.Logger
}

// Manager runs the wishlist.
type Manager struct {
	cfg  Config
	log  *slog.Logger
	db   *sql.DB
	srch Searches
	ls   Lister
	dl   Downloader
	kick chan struct{}

	mu             sync.Mutex
	ctx            context.Context
	serverInterval time.Duration
	running        map[int64]bool
}

// defaultServerInterval is the usual wishlist interval for accounts
// without privileges, used until the server says otherwise.
const defaultServerInterval = 12 * time.Minute

func New(cfg Config, db *sql.DB, s Searches, ls Lister, dl Downloader) *Manager {
	if cfg.Window <= 0 {
		cfg.Window = time.Minute
	}
	if cfg.OnUpdate == nil {
		cfg.OnUpdate = func(Entry) {}
	}
	log := cfg.Logger
	if log == nil {
		log = slog.Default()
	}
	return &Manager{
		cfg: cfg, log: log.With("component", "wishlist"), db: db, srch: s, ls: ls, dl: dl,
		kick: make(chan struct{}, 1), ctx: context.Background(),
		serverInterval: defaultServerInterval, running: map[int64]bool{},
	}
}

// SetServerInterval applies the server's WishlistInterval.
func (m *Manager) SetServerInterval(d time.Duration) {
	if d <= 0 {
		return
	}
	m.mu.Lock()
	m.serverInterval = d
	m.mu.Unlock()
}

// Interval is the time between scheduled wishlist searches.
func (m *Manager) Interval() time.Duration {
	m.mu.Lock()
	defer m.mu.Unlock()
	return max(m.cfg.Interval, m.serverInterval)
}

// Run searches one due entry per interval until ctx is done.
func (m *Manager) Run(ctx context.Context, ready func(context.Context) error) {
	m.mu.Lock()
	m.ctx = ctx
	m.mu.Unlock()
	var last time.Time // when the last scheduled search went out
	for {
		// Never search more often than the interval, whatever woke us.
		if wait := m.Interval() - time.Since(last); wait > 0 {
			select {
			case <-ctx.Done():
				return
			case <-time.After(wait):
			}
		}
		if ready != nil && ready(ctx) != nil {
			return
		}
		e, ok := m.next()
		if !ok {
			// Nothing to search: sleep until an entry is added.
			select {
			case <-ctx.Done():
				return
			case <-m.kick:
			case <-time.After(m.Interval()):
			}
			continue
		}
		last = time.Now()
		m.run(ctx, e, true)
	}
}

// next picks the enabled entry searched least recently.
func (m *Manager) next() (Entry, bool) {
	entries, err := m.List()
	if err != nil {
		m.log.Error("reading wishlist", "err", err)
		return Entry{}, false
	}
	var due []Entry
	for _, e := range entries {
		if e.Enabled && !e.Running {
			due = append(due, e)
		}
	}
	if len(due) == 0 {
		return Entry{}, false
	}
	return slices.MinFunc(due, func(a, b Entry) int {
		if c := a.LastRun.Compare(b.LastRun); c != 0 {
			return c
		}
		return int(a.ID - b.ID)
	}), true
}

// RunNow searches for an entry right away, as a normal search.
func (m *Manager) RunNow(id int64) error {
	e, err := m.Get(id)
	if err != nil {
		return err
	}
	m.mu.Lock()
	ctx := m.ctx
	m.mu.Unlock()
	go m.run(ctx, e, false)
	return nil
}

// run searches for e, waits for results and acts on them.
func (m *Manager) run(ctx context.Context, e Entry, scheduled bool) {
	m.mu.Lock()
	if m.running[e.ID] {
		m.mu.Unlock()
		return
	}
	m.running[e.ID] = true
	m.mu.Unlock()
	defer func() {
		m.mu.Lock()
		delete(m.running, e.ID)
		m.mu.Unlock()
		if cur, err := m.Get(e.ID); err == nil {
			m.cfg.OnUpdate(cur)
		}
	}()
	if cur, err := m.Get(e.ID); err == nil {
		m.cfg.OnUpdate(cur)
	}

	start := m.srch.Start
	if scheduled {
		start = m.srch.StartWishlist
	}
	s, err := start(e.Query)
	if err != nil {
		m.log.Warn("wishlist search failed", "query", e.Query, "err", err)
		return
	}
	select {
	case <-ctx.Done():
		return
	case <-time.After(m.cfg.Window):
	}
	albums := s.Albums(e.Filter)
	now := time.Now()
	if _, err := m.db.Exec(`UPDATE wishlist SET last_run = ?, last_search = ?, matches = ? WHERE id = ?`,
		now.UnixMilli(), s.ID, len(albums), e.ID); err != nil {
		m.log.Error("saving wishlist run", "err", err)
		return
	}
	m.log.Info("wishlist searched", "query", e.Query, "albums", len(albums))
	if !e.AutoDownload || len(albums) == 0 {
		return
	}
	// Still wanted? It may have been edited or removed meanwhile.
	if cur, err := m.Get(e.ID); err != nil || !cur.AutoDownload || cur.GotFolder != "" {
		return
	}
	best := albums[0].Sources[0]
	files := m.folderFiles(ctx, best)
	if _, err := m.dl.Enqueue(best.User, files, best.Folder); err != nil {
		m.log.Error("wishlist download", "err", err)
		return
	}
	m.log.Info("wishlist downloading", "query", e.Query, "user", best.User, "folder", best.Folder, "files", len(files))
	m.db.Exec(`UPDATE wishlist SET got_user = ?, got_folder = ?, got_at = ?, enabled = 0 WHERE id = ?`,
		best.User, best.Folder, now.UnixMilli(), e.ID)
}

// folderFiles asks the peer for the whole folder, falling back to the
// files the search returned.
func (m *Manager) folderFiles(ctx context.Context, h search.FolderHit) []transfers.FileRequest {
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	if resp, err := m.ls.FolderContents(ctx, h.User, h.Folder); err == nil {
		if files := transfers.FilesInFolder(h.Folder, resp.Dirs); len(files) > 0 {
			return files
		}
	}
	files := make([]transfers.FileRequest, len(h.Files))
	for i, f := range h.Files {
		files[i] = transfers.FileRequest{Path: f.Path, Size: f.Size}
	}
	return files
}

var ErrNotFound = errors.New("no such wishlist entry")

const entryCols = `id, query, formats, min_bitrate, free_slot_only, min_files, auto_download, enabled,
	created, last_run, last_search, matches, got_user, got_folder, got_at`

func scanEntry(row interface{ Scan(...any) error }) (Entry, error) {
	var e Entry
	var formats string
	var created, lastRun, gotAt int64
	err := row.Scan(&e.ID, &e.Query, &formats, &e.Filter.MinBitRate, &e.Filter.FreeSlotOnly, &e.Filter.MinFiles,
		&e.AutoDownload, &e.Enabled, &created, &lastRun, &e.LastSearch, &e.Matches, &e.GotUser, &e.GotFolder, &gotAt)
	if err != nil {
		return e, err
	}
	if formats != "" {
		e.Filter.Formats = strings.Split(formats, ",")
	}
	e.Created = time.UnixMilli(created)
	if lastRun > 0 {
		e.LastRun = time.UnixMilli(lastRun)
	}
	if gotAt > 0 {
		e.GotAt = time.UnixMilli(gotAt)
	}
	return e, nil
}

func (m *Manager) annotate(e Entry) Entry {
	m.mu.Lock()
	e.Running = m.running[e.ID]
	m.mu.Unlock()
	if e.LastSearch != 0 {
		if _, ok := m.srch.Get(e.LastSearch); !ok {
			e.LastSearch = 0 // results expired
		}
	}
	return e
}

// List returns all entries, oldest first.
func (m *Manager) List() ([]Entry, error) {
	rows, err := m.db.Query(`SELECT ` + entryCols + ` FROM wishlist ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Entry{}
	for rows.Next() {
		e, err := scanEntry(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m.annotate(e))
	}
	return out, rows.Err()
}

func (m *Manager) Get(id int64) (Entry, error) {
	e, err := scanEntry(m.db.QueryRow(`SELECT `+entryCols+` FROM wishlist WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return e, ErrNotFound
	}
	if err != nil {
		return e, err
	}
	return m.annotate(e), nil
}

// Add saves a new entry; it is searched at the next free slot.
func (m *Manager) Add(query string, f search.Filter, auto bool) (Entry, error) {
	query = strings.Join(strings.Fields(query), " ")
	if query == "" {
		return Entry{}, errors.New("query is required")
	}
	res, err := m.db.Exec(`INSERT INTO wishlist (query, formats, min_bitrate, free_slot_only, min_files, auto_download, created)
		VALUES (?, ?, ?, ?, ?, ?, ?)`, query, strings.Join(f.Formats, ","), f.MinBitRate, f.FreeSlotOnly, f.MinFiles, auto, time.Now().UnixMilli())
	if err != nil {
		return Entry{}, err
	}
	id, _ := res.LastInsertId()
	e, err := m.Get(id)
	if err == nil {
		m.cfg.OnUpdate(e)
		select {
		case m.kick <- struct{}{}:
		default:
		}
	}
	return e, err
}

// Change is a partial update; nil fields stay as they are.
type Change struct {
	Query        *string        `json:"query"`
	Filter       *search.Filter `json:"filter"`
	AutoDownload *bool          `json:"auto_download"`
	Enabled      *bool          `json:"enabled"`
}

func (m *Manager) Update(id int64, c Change) (Entry, error) {
	e, err := m.Get(id)
	if err != nil {
		return e, err
	}
	if c.Query != nil {
		if q := strings.Join(strings.Fields(*c.Query), " "); q != "" {
			e.Query = q
		}
	}
	if c.Filter != nil {
		e.Filter = *c.Filter
	}
	if c.AutoDownload != nil {
		e.AutoDownload = *c.AutoDownload
	}
	gotUser, gotFolder := e.GotUser, e.GotFolder
	if c.Enabled != nil {
		e.Enabled = *c.Enabled
		if e.Enabled {
			gotUser, gotFolder = "", "" // re-enabling wants it again
		}
	}
	_, err = m.db.Exec(`UPDATE wishlist SET query = ?, formats = ?, min_bitrate = ?, free_slot_only = ?, min_files = ?,
		auto_download = ?, enabled = ?, got_user = ?, got_folder = ? WHERE id = ?`,
		e.Query, strings.Join(e.Filter.Formats, ","), e.Filter.MinBitRate, e.Filter.FreeSlotOnly, e.Filter.MinFiles,
		e.AutoDownload, e.Enabled, gotUser, gotFolder, id)
	if err != nil {
		return e, err
	}
	e, err = m.Get(id)
	if err == nil {
		m.cfg.OnUpdate(e)
	}
	return e, err
}

func (m *Manager) Remove(id int64) error {
	res, err := m.db.Exec(`DELETE FROM wishlist WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}
