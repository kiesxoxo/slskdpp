// Package transfers downloads files from other users: a persistent queue
// with resume, retries and fallback to other sources.
package transfers

import (
	"context"
	"database/sql"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"net"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/kiesxoxo/slskdpp/internal/client"
	"github.com/kiesxoxo/slskdpp/internal/protocol"
	"github.com/kiesxoxo/slskdpp/internal/search"
)

// State is where a download is in its life cycle:
//
//	queued → requested → queued_remote → initializing → transferring → completed
//
// with failed and cancelled as the other end states. A failed attempt goes
// back to queued with a backoff.
type State string

const (
	StateQueued       State = "queued"        // waiting here, for its first try or a retry
	StateRequested    State = "requested"     // connecting and asking the peer to queue it
	StateRemoteQueued State = "queued_remote" // in the peer's upload queue
	StateInitializing State = "initializing"  // the peer is ready; waiting for its file connection
	StateTransferring State = "transferring"
	StateCompleted    State = "completed"
	StateFailed       State = "failed"
	StateCancelled    State = "cancelled"
)

// Done reports whether the state is final.
func (s State) Done() bool {
	return s == StateCompleted || s == StateFailed || s == StateCancelled
}

// Download is one file being fetched. Fields are guarded by the manager.
type Download struct {
	ID          int64
	Username    string
	Filename    string // remote path
	Size        uint64
	Dest        string // slash-separated path under the downloads directory
	State       State
	Bytes       uint64 // received so far
	Attempts    int    // failed attempts with the current source
	Error       string
	Tried       []string // earlier sources
	NextAttempt time.Time
	Created     time.Time
	Updated     time.Time
	Completed   time.Time

	// Not persisted.
	token        uint32
	speed        float64
	queuePlace   uint32
	initDeadline time.Time
	placeChecked time.Time
	inflight     bool // a request is being sent
	searching    bool // looking for another source
	cancel       context.CancelFunc
}

// View is a download as the API shows it.
type View struct {
	ID          int64     `json:"id"`
	Username    string    `json:"username"`
	Filename    string    `json:"filename"`
	Name        string    `json:"name"`
	Folder      string    `json:"folder"` // local folder, relative to the downloads directory
	Size        uint64    `json:"size"`
	Bytes       uint64    `json:"bytes"`
	State       State     `json:"state"`
	Error       string    `json:"error,omitempty"`
	Attempts    int       `json:"attempts"`
	Sources     int       `json:"sources"`             // sources tried, including the current one
	Searching   bool      `json:"searching,omitempty"` // looking for another source
	QueuePlace  uint32    `json:"queue_place,omitempty"`
	Speed       uint64    `json:"speed"` // bytes per second while transferring
	NextAttempt time.Time `json:"next_attempt,omitzero"`
	Created     time.Time `json:"created"`
	Updated     time.Time `json:"updated"`
	Completed   time.Time `json:"completed,omitzero"`
}

func (d *Download) view() View {
	v := View{
		ID: d.ID, Username: d.Username, Filename: d.Filename,
		Name: path.Base(d.Dest), Folder: path.Dir(d.Dest),
		Size: d.Size, Bytes: d.Bytes, State: d.State, Error: d.Error, Attempts: d.Attempts,
		Sources: len(d.Tried) + 1, Searching: d.searching,
		Created: d.Created, Updated: d.Updated, Completed: d.Completed,
	}
	if d.State == StateRemoteQueued {
		v.QueuePlace = d.queuePlace
	}
	if d.State == StateTransferring {
		v.Speed = uint64(d.speed)
	}
	if d.State == StateQueued {
		v.NextAttempt = d.NextAttempt
	}
	return v
}

// Network is the part of the client downloads need.
type Network interface {
	LoggedIn() bool
	SendPeer(ctx context.Context, username string, m protocol.PeerMessage) error
	WatchUser(username string) error
}

// Searcher finds other sources for a file.
type Searcher interface {
	StartBackground(query string) (*search.Search, error)
	Remove(id uint32)
}

type Config struct {
	DownloadDir   string
	IncompleteDir string
	// MaxAttempts is how many times a source is tried before looking for
	// another one.
	MaxAttempts int
	// MaxSources caps how many users a file is tried from.
	MaxSources int
	// SpeedLimit and PerDownloadLimit cap download speed in bytes per
	// second, overall and per file; 0 means unlimited.
	SpeedLimit       int64
	PerDownloadLimit int64
	// OnUpdate is told about every change, with progress at most every
	// half second per download. It runs with the manager locked, so it
	// must not block or call back into the Manager.
	OnUpdate func(View)
	// OnRemove is told when a download is removed. Same rules as OnUpdate.
	OnRemove func(id int64)
	Logger   *slog.Logger
}

// timing holds the manager's delays, shortened in tests.
type timing struct {
	tick          time.Duration // how often the scheduler runs
	backoffBase   time.Duration
	backoffMax    time.Duration
	initTimeout   time.Duration // wait for the file connection after accepting
	idleTimeout   time.Duration // a transfer with no data this long fails
	placeEvery    time.Duration // how often to ask for our place in a queue
	offlineAlt    time.Duration // look for another source after the user is offline this long
	altWait       time.Duration // how long to collect search results for another source
	altGap        time.Duration // minimum time between those searches
	progressEvery time.Duration
	requestLimit  int // concurrent QueueUpload requests
}

var defaultTiming = timing{
	tick:          time.Second,
	backoffBase:   30 * time.Second,
	backoffMax:    30 * time.Minute,
	initTimeout:   time.Minute,
	idleTimeout:   time.Minute,
	placeEvery:    10 * time.Minute,
	offlineAlt:    30 * time.Minute,
	altWait:       20 * time.Second,
	altGap:        30 * time.Second,
	progressEvery: 500 * time.Millisecond,
	requestLimit:  8,
}

// Manager runs all downloads.
type Manager struct {
	cfg      Config
	log      *slog.Logger
	db       *sql.DB
	net      Network
	searcher Searcher
	t        timing
	limit    *bucket
	kick     chan struct{}

	mu        sync.Mutex
	ctx       context.Context
	downloads map[int64]*Download
	offline   map[string]time.Time // users seen offline, and since when
	requests  int                  // QueueUpload requests in flight
	nextAlt   time.Time            // earliest start for the next source search
	// hints remember where a file from a remote folder was found again,
	// keyed by old user and folder, so the folder's other files try the
	// same place before searching.
	hints map[string]sourceHint
}

type sourceHint struct{ user, folder string }

func hintKey(user, remotePath string) string {
	folder, _ := splitRemote(remotePath)
	return user + "\x00" + folder
}

// splitRemote splits a remote path into folder and file name.
func splitRemote(p string) (folder, name string) {
	i := strings.LastIndexAny(p, `\/`)
	if i < 0 {
		return "", p
	}
	return p[:i], p[i+1:]
}

// New loads saved downloads. Ones that were in progress go back to the
// queue; their partial files are kept and resumed.
func New(cfg Config, db *sql.DB, net Network, searcher Searcher) (*Manager, error) {
	if cfg.MaxAttempts <= 0 {
		cfg.MaxAttempts = 5
	}
	if cfg.MaxSources <= 0 {
		cfg.MaxSources = 3
	}
	if cfg.OnUpdate == nil {
		cfg.OnUpdate = func(View) {}
	}
	if cfg.OnRemove == nil {
		cfg.OnRemove = func(int64) {}
	}
	log := cfg.Logger
	if log == nil {
		log = slog.Default()
	}
	m := &Manager{
		cfg:       cfg,
		log:       log.With("component", "downloads"),
		db:        db,
		net:       net,
		searcher:  searcher,
		t:         defaultTiming,
		limit:     newBucket(cfg.SpeedLimit),
		kick:      make(chan struct{}, 1),
		ctx:       context.Background(),
		downloads: make(map[int64]*Download),
		offline:   make(map[string]time.Time),
		hints:     make(map[string]sourceHint),
	}
	saved, err := loadDownloads(db)
	if err != nil {
		return nil, fmt.Errorf("load downloads: %w", err)
	}
	now := time.Now()
	for _, d := range saved {
		if !d.State.Done() {
			if st, err := os.Stat(m.partPath(d)); err == nil {
				d.Bytes = uint64(st.Size())
			}
			if d.State != StateQueued {
				d.State = StateQueued
				d.NextAttempt = now
				m.saveLocked(d)
			}
		}
		m.downloads[d.ID] = d
	}
	return m, nil
}

// Run schedules downloads until ctx is done.
func (m *Manager) Run(ctx context.Context) {
	m.mu.Lock()
	m.ctx = ctx
	m.mu.Unlock()
	m.watchUsers()
	tick := time.NewTicker(m.t.tick)
	defer tick.Stop()
	for {
		m.schedule()
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		case <-m.kick:
		}
	}
}

func (m *Manager) wake() {
	select {
	case m.kick <- struct{}{}:
	default:
	}
}

// FileRequest names a remote file to download.
type FileRequest struct {
	Path string `json:"path"`
	Size uint64 `json:"size"`
}

// Enqueue adds files from username. folder, if set, is the remote folder
// being downloaded: files keep their layout below it. Files already being
// downloaded from that user are returned as they are.
func (m *Manager) Enqueue(username string, files []FileRequest, folder string) ([]View, error) {
	if username == "" {
		return nil, errors.New("username is required")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []View
	now := time.Now()
	for _, f := range files {
		if f.Path == "" {
			continue
		}
		if d := m.findLocked(func(d *Download) bool {
			return !d.State.Done() && d.Username == username && d.Filename == f.Path
		}); d != nil {
			out = append(out, d.view())
			continue
		}
		d := &Download{
			Username: username, Filename: f.Path, Size: f.Size, Dest: destFor(f.Path, folder),
			State: StateQueued, NextAttempt: now, Created: now, Updated: now,
		}
		if err := insertDownload(m.db, d); err != nil {
			return out, err
		}
		m.downloads[d.ID] = d
		m.cfg.OnUpdate(d.view())
		out = append(out, d.view())
	}
	go m.net.WatchUser(username)
	m.wake()
	return out, nil
}

// List returns all downloads, oldest first.
func (m *Manager) List() []View {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]View, 0, len(m.downloads))
	for _, d := range m.downloads {
		out = append(out, d.view())
	}
	slices.SortFunc(out, func(a, b View) int { return int(a.ID - b.ID) })
	return out
}

func (m *Manager) Get(id int64) (View, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	d, ok := m.downloads[id]
	if !ok {
		return View{}, false
	}
	return d.view(), true
}

var (
	ErrNotFound = errors.New("no such download")
	ErrFinished = errors.New("download already finished")
)

// Cancel stops a download and deletes its partial file.
func (m *Manager) Cancel(id int64) error {
	m.mu.Lock()
	d, ok := m.downloads[id]
	if !ok {
		m.mu.Unlock()
		return ErrNotFound
	}
	if d.State.Done() {
		m.mu.Unlock()
		return ErrFinished
	}
	m.stopLocked(d)
	d.State = StateCancelled
	d.searching = false
	d.Error = ""
	m.saveLocked(d)
	part := m.partPath(d)
	m.mu.Unlock()
	os.Remove(part)
	return nil
}

// Retry restarts a failed or cancelled download, or makes a waiting one
// try again now.
func (m *Manager) Retry(id int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	d, ok := m.downloads[id]
	if !ok {
		return ErrNotFound
	}
	switch d.State {
	case StateFailed, StateCancelled:
		d.Attempts = 0
		d.Error = ""
		fallthrough
	case StateQueued:
		d.State = StateQueued
		d.NextAttempt = time.Now()
		delete(m.offline, d.Username)
		m.saveLocked(d)
		go m.net.WatchUser(d.Username)
		m.wake()
		return nil
	}
	return fmt.Errorf("download is %s", d.State)
}

// Remove forgets a download, cancelling it first if needed. A completed
// file stays on disk.
func (m *Manager) Remove(id int64) error {
	m.mu.Lock()
	d, ok := m.downloads[id]
	if !ok {
		m.mu.Unlock()
		return ErrNotFound
	}
	m.stopLocked(d)
	done := d.State == StateCompleted
	if !d.State.Done() {
		d.State = StateCancelled // a running transfer sees this and stops
	}
	delete(m.downloads, id)
	err := deleteDownload(m.db, id)
	m.cfg.OnRemove(id)
	part := m.partPath(d)
	m.mu.Unlock()
	if !done {
		os.Remove(part)
	}
	return err
}

// Clear removes finished downloads in the given states (completed if
// none are given) and returns how many.
func (m *Manager) Clear(states ...State) (int, error) {
	if len(states) == 0 {
		states = []State{StateCompleted}
	}
	var ids []int64
	m.mu.Lock()
	for id, d := range m.downloads {
		if d.State.Done() && slices.Contains(states, d.State) {
			ids = append(ids, id)
		}
	}
	m.mu.Unlock()
	for _, id := range ids {
		if err := m.Remove(id); err != nil && !errors.Is(err, ErrNotFound) {
			return 0, err
		}
	}
	return len(ids), nil
}

// HandleServer follows the status of users we download from.
func (m *Manager) HandleServer(msg any) {
	var user string
	var online bool
	switch v := msg.(type) {
	case protocol.UserStatusResponse:
		user, online = v.Username, v.Status != protocol.StatusOffline
	case protocol.WatchUserResponse:
		user, online = v.Username, v.Exists && v.Status != protocol.StatusOffline
	default:
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.hasActiveLocked(user) {
		return
	}
	if !online {
		if _, ok := m.offline[user]; !ok {
			m.offline[user] = time.Now()
		}
		return
	}
	if _, was := m.offline[user]; !was {
		return
	}
	delete(m.offline, user)
	// Back online: retry now, and queue again with a peer that may have
	// restarted and forgotten its queue.
	for _, d := range m.downloads {
		if d.Username == user && (d.State == StateQueued || d.State == StateRemoteQueued) && !d.searching {
			d.State = StateQueued
			d.NextAttempt = time.Now()
			m.saveLocked(d)
		}
	}
	m.wake()
}

// LoggedIn re-subscribes to the status of users we download from; call it
// after each login.
func (m *Manager) LoggedIn() {
	m.watchUsers()
	m.wake()
}

func (m *Manager) watchUsers() {
	m.mu.Lock()
	users := map[string]bool{}
	for _, d := range m.downloads {
		if !d.State.Done() {
			users[d.Username] = true
		}
	}
	m.mu.Unlock()
	for u := range users {
		m.net.WatchUser(u)
	}
}

// HandlePeer handles the transfer messages a peer sends about our
// downloads and reports whether msg was one of them.
func (m *Manager) HandlePeer(username string, msg any) bool {
	switch v := msg.(type) {
	case protocol.TransferRequest:
		if v.Direction != protocol.TransferUpload {
			return false
		}
		m.transferRequest(username, v)
	case protocol.PlaceInQueueResponse:
		m.mu.Lock()
		if d := m.activeLocked(username, v.Filename); d != nil && d.State == StateRemoteQueued {
			d.queuePlace = v.Place
			m.cfg.OnUpdate(d.view())
		}
		m.mu.Unlock()
	case protocol.UploadDenied:
		m.mu.Lock()
		if d := m.activeLocked(username, v.Filename); d != nil && d.State != StateTransferring {
			m.deniedLocked(d, v.Reason)
		}
		m.mu.Unlock()
	case protocol.UploadFailed:
		m.mu.Lock()
		if d := m.activeLocked(username, v.Filename); d != nil && d.State != StateTransferring {
			m.failLocked(d, "upload failed on the peer's side")
		}
		m.mu.Unlock()
	default:
		return false
	}
	return true
}

// transferRequest answers a peer that is ready to send us a file.
func (m *Manager) transferRequest(username string, r protocol.TransferRequest) {
	resp := protocol.TransferResponse{Token: r.Token, Allowed: false, Reason: "Cancelled"}
	m.mu.Lock()
	d := m.activeLocked(username, r.Filename)
	if d != nil && d.State != StateTransferring && !d.searching {
		d.token = r.Token
		if r.Size > 0 {
			d.Size = r.Size
		}
		d.State = StateInitializing
		d.initDeadline = time.Now().Add(m.t.initTimeout)
		d.Error = ""
		delete(m.offline, username)
		m.saveLocked(d)
		resp = protocol.TransferResponse{Token: r.Token, Allowed: true}
	}
	ctx := m.ctx
	m.mu.Unlock()
	go func() {
		ctx, cancel := context.WithTimeout(ctx, time.Minute)
		defer cancel()
		if err := m.net.SendPeer(ctx, username, resp); err != nil {
			m.log.Debug("cannot answer transfer request", "user", username, "err", err)
		}
	}()
}

// HandleFileConn receives a file on a connection the uploader opened.
// It blocks until the transfer ends and closes conn.
func (m *Manager) HandleFileConn(username string, token uint32, conn net.Conn) {
	m.mu.Lock()
	d := m.findLocked(func(d *Download) bool {
		return d.State == StateInitializing && d.Username == username && d.token == token
	})
	if d == nil {
		m.mu.Unlock()
		m.log.Debug("unexpected file connection", "user", username, "token", token)
		conn.Close()
		return
	}
	ctx, cancel := context.WithCancel(m.ctx)
	defer cancel()
	d.cancel = cancel
	d.State = StateTransferring
	d.speed = 0
	m.saveLocked(d)
	part, size := m.partPath(d), d.Size
	m.mu.Unlock()

	err := m.receive(ctx, d, conn, part, size)

	m.mu.Lock()
	defer m.mu.Unlock()
	d.cancel = nil
	if d.State != StateTransferring || m.downloads[d.ID] != d {
		return // cancelled or removed meanwhile
	}
	if err == nil {
		err = m.finishLocked(d, part)
	}
	if err != nil {
		// What is on disk is what the next attempt resumes from.
		d.Bytes = 0
		if st, serr := os.Stat(part); serr == nil {
			d.Bytes = uint64(st.Size())
		}
		m.log.Info("download attempt failed", "user", username, "file", d.Filename, "err", err)
		m.failLocked(d, err.Error())
		return
	}
	m.log.Info("download completed", "user", username, "file", d.Dest)
}

// receive writes the file from conn into part, resuming from its size.
func (m *Manager) receive(ctx context.Context, d *Download, conn net.Conn, part string, size uint64) error {
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()
	if err := os.MkdirAll(filepath.Dir(part), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(part, os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return err
	}
	offset := uint64(st.Size())
	if offset > size {
		offset = 0
	}
	if err := f.Truncate(int64(offset)); err != nil {
		return err
	}
	if _, err := f.Seek(int64(offset), io.SeekStart); err != nil {
		return err
	}
	conn.SetWriteDeadline(time.Now().Add(m.t.idleTimeout))
	if _, err := conn.Write(binary.LittleEndian.AppendUint64(nil, offset)); err != nil {
		return fmt.Errorf("sending offset: %w", err)
	}

	perFile := newBucket(m.cfg.PerDownloadLimit)
	buf := make([]byte, 64<<10)
	got := offset
	lastAt, lastBytes := time.Now(), got
	m.progress(d, got, 0)
	for got < size {
		n := int(min(uint64(len(buf)), size-got))
		if n, err = perFile.take(ctx, n); err != nil {
			return err
		}
		if n, err = m.limit.take(ctx, n); err != nil {
			return err
		}
		conn.SetReadDeadline(time.Now().Add(m.t.idleTimeout))
		n, rerr := conn.Read(buf[:n])
		if n > 0 {
			if _, err := f.Write(buf[:n]); err != nil {
				return err
			}
			got += uint64(n)
		}
		if now := time.Now(); now.Sub(lastAt) >= m.t.progressEvery || got == size {
			speed := float64(got-lastBytes) / max(now.Sub(lastAt).Seconds(), 0.001)
			m.progress(d, got, speed)
			lastAt, lastBytes = now, got
		}
		if rerr != nil && got < size {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if errors.Is(rerr, io.EOF) {
				return fmt.Errorf("connection closed after %d of %d bytes", got, size)
			}
			return rerr
		}
	}
	return f.Close()
}

func (m *Manager) progress(d *Download, bytes uint64, speed float64) {
	m.mu.Lock()
	d.Bytes = bytes
	d.speed = speed
	if d.State == StateTransferring {
		m.cfg.OnUpdate(d.view())
	}
	m.mu.Unlock()
}

// finishLocked checks the received file and moves it into place.
func (m *Manager) finishLocked(d *Download, part string) error {
	st, err := os.Stat(part)
	if err != nil {
		return err
	}
	if uint64(st.Size()) != d.Size {
		os.Remove(part)
		return fmt.Errorf("received %d bytes, expected %d", st.Size(), d.Size)
	}
	final := uniquePath(filepath.Join(m.cfg.DownloadDir, filepath.FromSlash(d.Dest)))
	if err := os.MkdirAll(filepath.Dir(final), 0o755); err != nil {
		return err
	}
	if err := moveFile(part, final); err != nil {
		return err
	}
	if rel, err := filepath.Rel(m.cfg.DownloadDir, final); err == nil {
		d.Dest = filepath.ToSlash(rel)
	}
	d.State = StateCompleted
	d.Bytes = d.Size
	d.Error = ""
	d.Completed = time.Now()
	m.saveLocked(d)
	return nil
}

// schedule starts what is due: requests to peers, queue place checks,
// timeouts and source searches.
func (m *Manager) schedule() {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := time.Now()
	loggedIn := m.net.LoggedIn()
	ids := make([]int64, 0, len(m.downloads))
	for id := range m.downloads {
		ids = append(ids, id)
	}
	slices.Sort(ids) // oldest first
	for _, id := range ids {
		d := m.downloads[id]
		if d.searching {
			continue
		}
		offSince, offline := m.offline[d.Username]
		switch d.State {
		case StateQueued:
			if offline {
				if now.Sub(offSince) >= m.t.offlineAlt {
					d.Error = "user offline for a long time"
					m.switchSourceLocked(d)
				}
				continue
			}
			if now.Before(d.NextAttempt) || !loggedIn || m.requests >= m.t.requestLimit {
				continue
			}
			d.State = StateRequested
			d.inflight = true
			m.requests++
			m.saveLocked(d)
			go m.request(d.ID, d.Username, d.Filename)
		case StateRemoteQueued:
			if offline && now.Sub(offSince) >= m.t.offlineAlt {
				d.Error = "user offline for a long time"
				m.switchSourceLocked(d)
				continue
			}
			if loggedIn && !offline && now.Sub(d.placeChecked) >= m.t.placeEvery {
				d.placeChecked = now
				go m.send(d.Username, protocol.PlaceInQueueRequest{Filename: d.Filename})
			}
		case StateInitializing:
			if now.After(d.initDeadline) {
				m.failLocked(d, "the peer did not start sending")
			}
		}
	}
}

func (m *Manager) send(username string, msg protocol.PeerMessage) {
	m.mu.Lock()
	ctx := m.ctx
	m.mu.Unlock()
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	if err := m.net.SendPeer(ctx, username, msg); err != nil {
		m.log.Debug("send to peer failed", "user", username, "err", err)
	}
}

// request asks the peer to queue the file for us.
func (m *Manager) request(id int64, username, filename string) {
	m.mu.Lock()
	ctx := m.ctx
	m.mu.Unlock()
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	err := m.net.SendPeer(ctx, username, protocol.QueueUpload{Filename: filename})

	m.mu.Lock()
	defer m.mu.Unlock()
	m.requests--
	d := m.downloads[id]
	if d == nil {
		return
	}
	d.inflight = false
	if d.State != StateRequested || d.Username != username {
		return // cancelled, or the peer already answered
	}
	switch {
	case err == nil:
		d.State = StateRemoteQueued
		d.Error = ""
		d.queuePlace = 0
		// Ask for our place soon, then every placeEvery.
		d.placeChecked = time.Now().Add(-m.t.placeEvery + min(m.t.placeEvery, 30*time.Second))
		delete(m.offline, username)
		m.saveLocked(d)
	case errors.Is(err, client.ErrUserOffline):
		// Not the source's fault; wait for it to come back.
		if _, ok := m.offline[username]; !ok {
			m.offline[username] = time.Now()
		}
		d.State = StateQueued
		d.Error = "user is offline"
		m.saveLocked(d)
	default:
		m.failLocked(d, err.Error())
	}
	m.wake()
}

// failLocked records a failed attempt: retry later, or look for another
// source once the attempts are used up.
func (m *Manager) failLocked(d *Download, reason string) {
	m.stopLocked(d)
	d.Attempts++
	d.Error = reason
	d.speed = 0
	if d.Attempts >= m.cfg.MaxAttempts {
		m.switchSourceLocked(d)
		return
	}
	d.State = StateQueued
	d.NextAttempt = time.Now().Add(m.backoff(d.Attempts))
	m.saveLocked(d)
}

// deniedLocked handles an UploadDenied. Queue limits are worth waiting
// out; anything else (not shared, banned) means trying another source.
func (m *Manager) deniedLocked(d *Download, reason string) {
	if strings.HasPrefix(strings.ToLower(reason), "too many") {
		m.failLocked(d, "denied: "+reason)
		return
	}
	m.stopLocked(d)
	d.Error = "denied: " + reason
	m.switchSourceLocked(d)
}

func (m *Manager) backoff(attempt int) time.Duration {
	d := m.t.backoffBase << min(attempt-1, 16)
	d = min(d, m.t.backoffMax)
	// ±25% jitter so retries to one peer spread out.
	return d*3/4 + rand.N(d/2+1)
}

// switchSourceLocked looks for another user sharing the same file, or
// gives up when too many have been tried.
func (m *Manager) switchSourceLocked(d *Download) {
	if len(d.Tried)+1 >= m.cfg.MaxSources || m.searcher == nil || altQuery(d.Filename) == "" {
		d.State = StateFailed
		m.saveLocked(d)
		return
	}
	tried := append(slices.Clone(d.Tried), d.Username)
	if h, ok := m.hints[hintKey(d.Username, d.Filename)]; ok && !slices.Contains(tried, h.user) {
		// A sibling was found at h: assume the rest of the folder is there.
		_, name := splitRemote(d.Filename)
		m.useSourceLocked(d, tried, h.user, h.folder+`\`+name, d.Size)
		return
	}
	d.State = StateQueued
	d.searching = true
	m.saveLocked(d)
	go m.findSource(m.ctx, d.ID, d.Filename, d.Size, tried)
}

// findSource searches the network for the same file name and size from a
// user not tried yet, and switches the download to them. Searches are
// spaced altGap apart; while one waits, a sibling's search may already
// have moved this file along.
func (m *Manager) findSource(ctx context.Context, id int64, filename string, size uint64, tried []string) {
	stillNeeded := func() bool {
		d := m.downloads[id]
		return d != nil && d.searching && d.State == StateQueued && d.Filename == filename
	}
	for {
		m.mu.Lock()
		if !stillNeeded() {
			m.mu.Unlock()
			return
		}
		now := time.Now()
		if !now.Before(m.nextAlt) {
			m.nextAlt = now.Add(m.t.altGap)
			m.mu.Unlock()
			break
		}
		wait := min(m.nextAlt.Sub(now), time.Second)
		m.mu.Unlock()
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
	}

	var pick *search.File
	var pickUser string
	_, name := splitRemote(filename)
	s, err := m.searcher.StartBackground(altQuery(filename))
	if err == nil {
		select {
		case <-ctx.Done():
		case <-time.After(m.t.altWait):
		}
		for _, h := range s.Hits(search.Filter{}) {
			if slices.Contains(tried, h.User) || pick != nil {
				continue
			}
			for _, f := range h.Files {
				if strings.EqualFold(f.Name, name) && (size == 0 || f.Size == size) {
					pick, pickUser = &f, h.User
					break
				}
			}
		}
		m.searcher.Remove(s.ID)
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if !stillNeeded() {
		return
	}
	d := m.downloads[id]
	d.searching = false
	if pick == nil {
		if err != nil {
			d.Error += "; searching for another source failed: " + err.Error()
		}
		d.Error = strings.TrimPrefix(d.Error+"; no other source found", "; ")
		d.State = StateFailed
		m.saveLocked(d)
		return
	}
	key := hintKey(d.Username, d.Filename)
	newFolder, _ := splitRemote(pick.Path)
	m.hints[key] = sourceHint{pickUser, newFolder}
	if pick.Size > 0 {
		size = pick.Size
	}
	m.useSourceLocked(d, tried, pickUser, pick.Path, size)
	// Move the rest of the folder along without more searches.
	for _, o := range m.downloads {
		if o.searching && hintKey(o.Username, o.Filename) == key && !slices.Contains(o.Tried, pickUser) {
			o.searching = false
			_, oname := splitRemote(o.Filename)
			m.useSourceLocked(o, append(slices.Clone(o.Tried), o.Username), pickUser, newFolder+`\`+oname, o.Size)
		}
	}
}

// useSourceLocked switches d to another user's copy and starts over.
func (m *Manager) useSourceLocked(d *Download, tried []string, user, filename string, size uint64) {
	m.log.Info("switching download source", "file", path.Base(d.Dest), "from", d.Username, "to", user)
	// Another user's copy may differ byte for byte, so start over.
	os.Remove(m.partPath(d))
	d.Tried = tried
	d.Username = user
	d.Filename = filename
	d.Size = size
	d.State = StateQueued
	d.Bytes = 0
	d.Attempts = 0
	d.Error = ""
	d.NextAttempt = time.Now()
	m.saveLocked(d)
	go m.net.WatchUser(user)
	m.wake()
}

// stopLocked aborts a running transfer.
func (m *Manager) stopLocked(d *Download) {
	if d.cancel != nil {
		d.cancel()
		d.cancel = nil
	}
}

func (m *Manager) saveLocked(d *Download) {
	d.Updated = time.Now()
	if err := updateDownload(m.db, d); err != nil {
		m.log.Error("saving download", "id", d.ID, "err", err)
	}
	m.cfg.OnUpdate(d.view())
}

func (m *Manager) findLocked(match func(*Download) bool) *Download {
	for _, d := range m.downloads {
		if match(d) {
			return d
		}
	}
	return nil
}

// activeLocked finds the unfinished download of filename from username.
func (m *Manager) activeLocked(username, filename string) *Download {
	return m.findLocked(func(d *Download) bool {
		return !d.State.Done() && d.Username == username && d.Filename == filename
	})
}

func (m *Manager) hasActiveLocked(username string) bool {
	return m.findLocked(func(d *Download) bool { return !d.State.Done() && d.Username == username }) != nil
}

// partPath is where a download's data collects until it completes.
func (m *Manager) partPath(d *Download) string {
	return filepath.Join(m.cfg.IncompleteDir, fmt.Sprintf("%d-%s.part", d.ID, cleanName(path.Base(d.Dest))))
}
