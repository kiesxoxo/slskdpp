package transfers

import (
	"cmp"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"slices"
	"sync"
	"time"

	"github.com/kiesxoxo/slskdpp/internal/protocol"
)

// UploadState is where an upload is: queued, initializing (we asked the
// peer to take it), transferring, then completed, failed or cancelled.
type UploadState string

const (
	UploadQueued       UploadState = "queued"
	UploadInitializing UploadState = "initializing"
	UploadTransferring UploadState = "transferring"
	UploadCompleted    UploadState = "completed"
	UploadFailed       UploadState = "failed"
	UploadCancelled    UploadState = "cancelled"
)

func (s UploadState) Done() bool {
	return s == UploadCompleted || s == UploadFailed || s == UploadCancelled
}

// Upload is a file a peer asked us for. Fields are guarded by Uploads.
type Upload struct {
	ID        int64
	Username  string
	Filename  string // as the peer sees it
	local     string
	Size      uint64
	Bytes     uint64
	State     UploadState
	Error     string
	Created   time.Time
	Started   time.Time
	Completed time.Time

	speed  float64
	cancel context.CancelFunc
}

// UploadView is an upload as the API shows it.
type UploadView struct {
	ID        int64       `json:"id"`
	Username  string      `json:"username"`
	Filename  string      `json:"filename"`
	Name      string      `json:"name"`
	Folder    string      `json:"folder"`
	Size      uint64      `json:"size"`
	Bytes     uint64      `json:"bytes"`
	State     UploadState `json:"state"`
	Error     string      `json:"error,omitempty"`
	Place     int         `json:"place,omitempty"` // in our queue, while queued
	Speed     uint64      `json:"speed"`
	Created   time.Time   `json:"created"`
	Started   time.Time   `json:"started,omitzero"`
	Completed time.Time   `json:"completed,omitzero"`
}

// Resolver finds the local file behind a shared path.
type Resolver interface {
	Resolve(virtual string) (local string, size uint64, ok bool)
}

// UploadNetwork is the part of the client uploads need.
type UploadNetwork interface {
	SendPeer(ctx context.Context, username string, m protocol.PeerMessage) error
	ConnectFile(ctx context.Context, username string, token uint32) (net.Conn, error)
	NewToken() uint32
}

type UploadConfig struct {
	// Slots is how many uploads run at once; each user gets one at a time.
	Slots int
	// MaxQueuedPerUser caps how many files one user may queue.
	MaxQueuedPerUser int
	// SpeedLimit caps total upload speed in bytes per second; 0 is none.
	SpeedLimit int64
	// KeepFinished is how many finished uploads stay listed.
	KeepFinished int
	// OnUpdate and OnRemove follow the same rules as in Config.
	OnUpdate func(UploadView)
	OnRemove func(id int64)
	// OnComplete is told when an upload has been sent, with the local
	// file's path. Same rules as OnUpdate.
	OnComplete func(v UploadView, local string)
	// OnSpeed reports our average upload speed after each upload, for the
	// server (bytes per second).
	OnSpeed func(uint32)
	Logger  *slog.Logger
}

// Uploads serves files to peers that queued them.
type Uploads struct {
	cfg   UploadConfig
	log   *slog.Logger
	net   UploadNetwork
	files Resolver
	t     timing
	limit *bucket
	kick  chan struct{}

	mu         sync.Mutex
	ctx        context.Context
	list       []*Upload // oldest first
	nextID     int64
	active     int
	lastServed map[string]time.Time
	responses  map[uint32]chan protocol.TransferResponse
	avgSpeed   float64
}

func NewUploads(cfg UploadConfig, net UploadNetwork, files Resolver) *Uploads {
	if cfg.Slots <= 0 {
		cfg.Slots = 2
	}
	if cfg.MaxQueuedPerUser <= 0 {
		cfg.MaxQueuedPerUser = 500
	}
	if cfg.KeepFinished <= 0 {
		cfg.KeepFinished = 200
	}
	if cfg.OnUpdate == nil {
		cfg.OnUpdate = func(UploadView) {}
	}
	if cfg.OnComplete == nil {
		cfg.OnComplete = func(UploadView, string) {}
	}
	if cfg.OnRemove == nil {
		cfg.OnRemove = func(int64) {}
	}
	if cfg.OnSpeed == nil {
		cfg.OnSpeed = func(uint32) {}
	}
	log := cfg.Logger
	if log == nil {
		log = slog.Default()
	}
	return &Uploads{
		cfg:        cfg,
		log:        log.With("component", "uploads"),
		net:        net,
		files:      files,
		t:          defaultTiming,
		limit:      newBucket(cfg.SpeedLimit),
		kick:       make(chan struct{}, 1),
		ctx:        context.Background(),
		lastServed: make(map[string]time.Time),
		responses:  make(map[uint32]chan protocol.TransferResponse),
	}
}

// Run starts queued uploads as slots free up, until ctx is done.
func (u *Uploads) Run(ctx context.Context) {
	u.mu.Lock()
	u.ctx = ctx
	u.mu.Unlock()
	tick := time.NewTicker(u.t.tick)
	defer tick.Stop()
	for {
		u.schedule()
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		case <-u.kick:
		}
	}
}

func (u *Uploads) wake() {
	select {
	case u.kick <- struct{}{}:
	default:
	}
}

// HandlePeer handles the upload messages a peer sends and reports
// whether msg was one of them.
func (u *Uploads) HandlePeer(username string, msg any) bool {
	switch m := msg.(type) {
	case protocol.QueueUpload:
		if reason := u.enqueue(username, m.Filename); reason != "" {
			go u.send(username, protocol.UploadDenied{Filename: m.Filename, Reason: reason})
		}
	case protocol.TransferRequest:
		if m.Direction != protocol.TransferDownload {
			return false
		}
		// Older clients ask for the file this way; queue it and say so.
		reason := u.enqueue(username, m.Filename)
		if reason == "" {
			reason = "Queued"
		}
		go u.send(username, protocol.TransferResponse{Token: m.Token, Allowed: false, Reason: reason})
	case protocol.PlaceInQueueRequest:
		u.mu.Lock()
		place := 0
		for i, up := range u.queueOrderLocked() {
			if up.Username == username && up.Filename == m.Filename {
				place = i + 1
				break
			}
		}
		u.mu.Unlock()
		if place > 0 {
			go u.send(username, protocol.PlaceInQueueResponse{Filename: m.Filename, Place: uint32(place)})
		}
	case protocol.TransferResponse:
		u.mu.Lock()
		ch, ok := u.responses[m.Token]
		delete(u.responses, m.Token)
		u.mu.Unlock()
		if !ok {
			return false
		}
		ch <- m
	default:
		return false
	}
	return true
}

// enqueue adds a requested file and returns a denial reason, or "".
func (u *Uploads) enqueue(username, filename string) string {
	local, size, ok := u.files.Resolve(filename)
	if !ok {
		return "File not shared."
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	queued := 0
	for _, up := range u.list {
		if up.Username != username || up.State.Done() {
			continue
		}
		if up.Filename == filename {
			return "" // already queued
		}
		queued++
	}
	if queued >= u.cfg.MaxQueuedPerUser {
		return "Too many files"
	}
	u.nextID++
	up := &Upload{ID: u.nextID, Username: username, Filename: filename, local: local, Size: size, State: UploadQueued, Created: time.Now()}
	u.list = append(u.list, up)
	u.cfg.OnUpdate(u.viewLocked(up))
	u.wake()
	return ""
}

// queueOrderLocked lists queued uploads in the order they will start:
// users served least recently first, then oldest first.
func (u *Uploads) queueOrderLocked() []*Upload {
	var q []*Upload
	for _, up := range u.list {
		if up.State == UploadQueued {
			q = append(q, up)
		}
	}
	slices.SortStableFunc(q, func(a, b *Upload) int {
		return cmp.Or(u.lastServed[a.Username].Compare(u.lastServed[b.Username]), cmp.Compare(a.ID, b.ID))
	})
	return q
}

func (u *Uploads) schedule() {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.active >= u.cfg.Slots {
		return
	}
	busy := map[string]bool{}
	for _, up := range u.list {
		if up.State == UploadInitializing || up.State == UploadTransferring {
			busy[up.Username] = true
		}
	}
	for _, up := range u.queueOrderLocked() {
		if u.active >= u.cfg.Slots {
			return
		}
		if busy[up.Username] {
			continue
		}
		busy[up.Username] = true
		u.active++
		u.lastServed[up.Username] = time.Now()
		ctx, cancel := context.WithCancel(u.ctx)
		up.cancel = cancel
		u.setLocked(up, UploadInitializing, "")
		go u.run(ctx, up)
	}
}

// run offers one file to its peer and sends it.
func (u *Uploads) run(ctx context.Context, up *Upload) {
	err := u.transfer(ctx, up)
	u.mu.Lock()
	defer u.mu.Unlock()
	u.active--
	up.cancel = nil
	up.speed = 0
	switch {
	case up.State == UploadCancelled:
	case err == nil:
		up.Completed = time.Now()
		u.setLocked(up, UploadCompleted, "")
		u.cfg.OnComplete(u.viewLocked(up), up.local)
		u.log.Info("upload completed", "user", up.Username, "file", up.Filename)
	case errors.Is(err, errRefused):
		u.setLocked(up, UploadCancelled, err.Error())
	default:
		u.log.Info("upload failed", "user", up.Username, "file", up.Filename, "err", err)
		u.setLocked(up, UploadFailed, err.Error())
		go u.send(up.Username, protocol.UploadFailed{Filename: up.Filename})
	}
	u.trimLocked()
	u.wake()
}

var errRefused = errors.New("the peer declined")

func (u *Uploads) transfer(ctx context.Context, up *Upload) error {
	token := u.net.NewToken()
	ch := make(chan protocol.TransferResponse, 1)
	u.mu.Lock()
	u.responses[token] = ch
	u.mu.Unlock()
	defer func() {
		u.mu.Lock()
		delete(u.responses, token)
		u.mu.Unlock()
	}()

	f, err := os.Open(up.local)
	if err != nil {
		return err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return err
	}
	size := uint64(st.Size())

	sendCtx, cancel := context.WithTimeout(ctx, u.t.initTimeout)
	err = u.net.SendPeer(sendCtx, up.Username, protocol.TransferRequest{Direction: protocol.TransferUpload, Token: token, Filename: up.Filename, Size: size})
	cancel()
	if err != nil {
		return fmt.Errorf("cannot reach the peer: %w", err)
	}
	var resp protocol.TransferResponse
	select {
	case resp = <-ch:
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(u.t.initTimeout):
		return errors.New("the peer did not answer")
	}
	if !resp.Allowed {
		reason := resp.Reason
		if reason == "" {
			reason = "no reason given"
		}
		return fmt.Errorf("%w: %s", errRefused, reason)
	}

	connCtx, cancel := context.WithTimeout(ctx, u.t.initTimeout)
	conn, err := u.net.ConnectFile(connCtx, up.Username, token)
	cancel()
	if err != nil {
		return fmt.Errorf("file connection: %w", err)
	}
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()

	var b [8]byte
	conn.SetReadDeadline(time.Now().Add(u.t.idleTimeout))
	if _, err := io.ReadFull(conn, b[:]); err != nil {
		return fmt.Errorf("reading offset: %w", err)
	}
	offset := binary.LittleEndian.Uint64(b[:])
	if offset > size {
		return fmt.Errorf("peer asked to resume at %d of %d bytes", offset, size)
	}
	if _, err := f.Seek(int64(offset), io.SeekStart); err != nil {
		return err
	}
	u.mu.Lock()
	up.Size, up.Bytes, up.Started = size, offset, time.Now()
	u.setLocked(up, UploadTransferring, "")
	u.mu.Unlock()

	buf := make([]byte, 64<<10)
	sent := offset
	start := time.Now()
	lastAt, lastBytes := start, sent
	for sent < size {
		n, err := u.limit.take(ctx, int(min(uint64(len(buf)), size-sent)))
		if err != nil {
			return err
		}
		n, err = io.ReadFull(f, buf[:n])
		if err != nil {
			return fmt.Errorf("reading file: %w", err)
		}
		conn.SetWriteDeadline(time.Now().Add(u.t.idleTimeout))
		if _, err := conn.Write(buf[:n]); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return fmt.Errorf("sending: %w", err)
		}
		sent += uint64(n)
		if now := time.Now(); now.Sub(lastAt) >= u.t.progressEvery || sent == size {
			speed := float64(sent-lastBytes) / max(now.Sub(lastAt).Seconds(), 0.001)
			u.mu.Lock()
			up.Bytes, up.speed = sent, speed
			u.cfg.OnUpdate(u.viewLocked(up))
			u.mu.Unlock()
			lastAt, lastBytes = now, sent
		}
	}
	if elapsed := time.Since(start).Seconds(); sent > offset && elapsed > 0 {
		u.mu.Lock()
		speed := float64(sent-offset) / elapsed
		if u.avgSpeed == 0 {
			u.avgSpeed = speed
		} else {
			u.avgSpeed = 0.7*u.avgSpeed + 0.3*speed
		}
		avg := uint32(min(u.avgSpeed, float64(^uint32(0))))
		u.mu.Unlock()
		u.cfg.OnSpeed(avg)
	}
	return nil
}

func (u *Uploads) send(username string, m protocol.PeerMessage) {
	u.mu.Lock()
	ctx := u.ctx
	u.mu.Unlock()
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	if err := u.net.SendPeer(ctx, username, m); err != nil {
		u.log.Debug("send to peer failed", "user", username, "err", err)
	}
}

func (u *Uploads) setLocked(up *Upload, s UploadState, errText string) {
	up.State, up.Error = s, errText
	u.cfg.OnUpdate(u.viewLocked(up))
}

// trimLocked forgets the oldest finished uploads beyond KeepFinished.
func (u *Uploads) trimLocked() {
	finished := 0
	for _, up := range u.list {
		if up.State.Done() {
			finished++
		}
	}
	for i := 0; finished > u.cfg.KeepFinished && i < len(u.list); {
		if up := u.list[i]; up.State.Done() {
			u.list = slices.Delete(u.list, i, i+1)
			finished--
			u.cfg.OnRemove(up.ID)
			continue
		}
		i++
	}
}

func (u *Uploads) viewLocked(up *Upload) UploadView {
	folder, name := splitRemote(up.Filename)
	v := UploadView{
		ID: up.ID, Username: up.Username, Filename: up.Filename, Name: name, Folder: folder,
		Size: up.Size, Bytes: up.Bytes, State: up.State, Error: up.Error,
		Created: up.Created, Started: up.Started, Completed: up.Completed,
	}
	if up.State == UploadTransferring {
		v.Speed = uint64(up.speed)
	}
	return v
}

// List returns all uploads, oldest first, with queue places.
func (u *Uploads) List() []UploadView {
	u.mu.Lock()
	defer u.mu.Unlock()
	place := map[*Upload]int{}
	for i, up := range u.queueOrderLocked() {
		place[up] = i + 1
	}
	out := make([]UploadView, 0, len(u.list))
	for _, up := range u.list {
		v := u.viewLocked(up)
		v.Place = place[up]
		out = append(out, v)
	}
	return out
}

func (u *Uploads) find(id int64) (*Upload, int) {
	for i, up := range u.list {
		if up.ID == id {
			return up, i
		}
	}
	return nil, -1
}

// Get returns one upload.
func (u *Uploads) Get(id int64) (UploadView, bool) {
	for _, v := range u.List() {
		if v.ID == id {
			return v, true
		}
	}
	return UploadView{}, false
}

// Cancel stops an upload and tells the peer.
func (u *Uploads) Cancel(id int64) error {
	u.mu.Lock()
	up, _ := u.find(id)
	if up == nil {
		u.mu.Unlock()
		return ErrNotFound
	}
	if up.State.Done() {
		u.mu.Unlock()
		return ErrFinished
	}
	if up.cancel != nil {
		up.cancel()
	}
	u.setLocked(up, UploadCancelled, "")
	user, file := up.Username, up.Filename
	u.mu.Unlock()
	go u.send(user, protocol.UploadDenied{Filename: file, Reason: "Cancelled"})
	return nil
}

// Remove forgets a finished upload, cancelling it first if needed.
func (u *Uploads) Remove(id int64) error {
	u.mu.Lock()
	up, _ := u.find(id)
	active := up != nil && !up.State.Done()
	u.mu.Unlock()
	if up == nil {
		return ErrNotFound
	}
	if active {
		if err := u.Cancel(id); err != nil && !errors.Is(err, ErrFinished) {
			return err
		}
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	if _, i := u.find(id); i >= 0 {
		u.list = slices.Delete(u.list, i, i+1)
		u.cfg.OnRemove(id)
	}
	return nil
}

// Clear forgets all finished uploads and returns how many.
func (u *Uploads) Clear() int {
	u.mu.Lock()
	defer u.mu.Unlock()
	n := 0
	u.list = slices.DeleteFunc(u.list, func(up *Upload) bool {
		if up.State.Done() {
			u.cfg.OnRemove(up.ID)
			n++
			return true
		}
		return false
	})
	return n
}

// Status sums up our upload side for search and user info replies.
type UploadStatus struct {
	Slots    int    `json:"slots"`
	Active   int    `json:"active"`
	Queued   int    `json:"queued"`
	SlotFree bool   `json:"slot_free"`
	AvgSpeed uint32 `json:"avg_speed"`
}

func (u *Uploads) Status() UploadStatus {
	u.mu.Lock()
	defer u.mu.Unlock()
	queued := 0
	for _, up := range u.list {
		if up.State == UploadQueued {
			queued++
		}
	}
	return UploadStatus{
		Slots:    u.cfg.Slots,
		Active:   u.active,
		Queued:   queued,
		SlotFree: u.active+queued < u.cfg.Slots,
		AvgSpeed: uint32(min(u.avgSpeed, float64(^uint32(0)))),
	}
}
