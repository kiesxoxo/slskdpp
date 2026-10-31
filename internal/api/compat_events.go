package api

// slskd's event log and download batches, as Lidarr's Tubifarry plugin
// uses them: it polls /api/v0/events to learn where finished files
// landed, and queues each album as a batch with a destination folder of
// its own choosing.

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/kiesxoxo/slskdpp/internal/events"
	"github.com/kiesxoxo/slskdpp/internal/transfers"
)

// slskd event types, in the order slskd lists them.
const (
	v0EventFileComplete       = "DownloadFileComplete"
	v0EventDirectoryComplete  = "DownloadDirectoryComplete"
	v0EventUploadFileComplete = "UploadFileComplete"
	v0EventPrivateMessage     = "PrivateMessageReceived"
	v0EventRoomMessage        = "RoomMessageReceived"
	v0EventClientConnected    = "SoulseekClientConnected"
	v0EventClientDisconnected = "SoulseekClientDisconnected"
	v0EventNoop               = "Noop"
)

const v0DisconnectedMessage = "Disconnected from the Soulseek server"

var v0EventTypes = []string{
	v0EventFileComplete, v0EventDirectoryComplete, v0EventUploadFileComplete, v0EventPrivateMessage,
	v0EventRoomMessage, v0EventClientConnected, v0EventClientDisconnected, v0EventNoop,
}

type v0Event struct {
	ID   string
	Type string
	At   time.Time
	Data string // the event's own fields as JSON, as slskd stores them
}

// v0Events is slskd's event log. It is kept in the database, or in
// memory when there is none.
type v0Events struct {
	mu   sync.Mutex
	db   *sql.DB
	list []v0Event // without a database, oldest first
	last time.Time
	adds int
	// keep caps the log; the oldest events are dropped beyond it, checked
	// every pruneEvery events. slskd keeps every event, but this bounds
	// the database.
	keep, pruneEvery int
}

func newV0Events(db *sql.DB) (*v0Events, error) {
	l := &v0Events{db: db, keep: 100_000, pruneEvery: 1000}
	if db == nil {
		l.keep, l.pruneEvery = 1000, 1
	}
	if db != nil {
		var last int64
		if err := db.QueryRow(`SELECT coalesce(max(timestamp), 0) FROM compat_events`).Scan(&last); err != nil {
			return nil, err
		}
		if last > 0 {
			l.last = time.UnixMicro(last).UTC()
		}
	}
	return l, nil
}

// add records an event and returns it whole, as slskd shows an Event.
// Timestamps strictly increase, since clients find new events by
// comparing them with the newest they have seen.
func (l *v0Events) add(typ string, at time.Time, fields map[string]any) (map[string]any, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	at = at.UTC().Truncate(time.Microsecond)
	if !at.After(l.last) {
		at = l.last.Add(time.Microsecond)
	}
	e := v0Event{ID: newUUID(), Type: typ, At: at}
	fields["version"] = 0
	b, err := json.Marshal(fields)
	if err != nil {
		return nil, err
	}
	e.Data = string(b)
	if l.db != nil {
		if _, err := l.db.Exec(`INSERT INTO compat_events (id, type, timestamp, data) VALUES (?, ?, ?, ?)`,
			e.ID, e.Type, at.UnixMicro(), e.Data); err != nil {
			return nil, err
		}
		if l.adds++; l.adds%l.pruneEvery == 0 {
			if _, err := l.db.Exec(`DELETE FROM compat_events WHERE timestamp <= (SELECT timestamp FROM compat_events ORDER BY timestamp DESC LIMIT 1 OFFSET ?)`, l.keep); err != nil {
				return nil, err
			}
		}
	} else {
		l.list = append(l.list, e)
		if n := len(l.list) - l.keep; n > 0 {
			l.list = append(l.list[:0:0], l.list[n:]...)
		}
	}
	l.last = at
	out := map[string]any{"id": e.ID, "timestamp": v0EventTime(at), "type": typ}
	for k, v := range fields {
		out[k] = v
	}
	return out, nil
}

// page returns up to limit events, newest first, skipping offset, and how
// many there are in all.
func (l *v0Events) page(offset, limit int) ([]v0Event, int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.db == nil {
		total := len(l.list)
		var out []v0Event
		for i := total - 1 - offset; i >= 0 && len(out) < limit; i-- {
			out = append(out, l.list[i])
		}
		return out, total, nil
	}
	var total int
	if err := l.db.QueryRow(`SELECT count(*) FROM compat_events`).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := l.db.Query(`SELECT id, type, timestamp, data FROM compat_events ORDER BY timestamp DESC LIMIT ? OFFSET ?`, limit, offset)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var out []v0Event
	for rows.Next() {
		var e v0Event
		var at int64
		if err := rows.Scan(&e.ID, &e.Type, &at, &e.Data); err != nil {
			return nil, 0, err
		}
		e.At = time.UnixMicro(at).UTC()
		out = append(out, e)
	}
	return out, total, rows.Err()
}

// v0EventTime formats t with .NET's seven fractional digits.
func v0EventTime(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05.0000000Z") }

// v0Record adds an event, logging rather than failing: the event log is a
// side effect of whatever happened.
func (a *API) v0Record(typ string, at time.Time, fields map[string]any) {
	if _, err := a.v0Events.add(typ, at, fields); err != nil {
		a.log.Error("recording slskd event", "type", typ, "err", err)
	}
}

func (a *API) subscribeV0Events() *events.Sub {
	return a.bus.Subscribe(256, func(e events.Event) bool {
		switch e.Type {
		case events.TypeDownloadCompleted, events.TypeUploadCompleted, events.TypeServerState:
			return true
		}
		return false
	})
}

// watchV0Events turns what happens into slskd events for as long as the
// process runs.
func (a *API) watchV0Events(sub *events.Sub) {
	var connected *bool
	for {
		for e := range sub.C {
			switch d := e.Data.(type) {
			case events.DownloadCompleted:
				a.v0Completed(d)
			case events.UploadCompleted:
				at := d.Upload.Completed
				if at.IsZero() {
					at = a.now()
				}
				a.v0Record(v0EventUploadFileComplete, at, map[string]any{
					"localFilename":  d.Local,
					"remoteFilename": d.Upload.Filename,
					"transfer":       v0UploadFile(d.Upload, a.now()),
				})
			case events.ServerState:
				// Report changes only; the first state seen is a change
				// only if it is a connection.
				if connected != nil && *connected == d.Connected || connected == nil && !d.Connected {
					connected = &d.Connected
					continue
				}
				connected = &d.Connected
				if d.Connected {
					a.v0Record(v0EventClientConnected, a.now(), map[string]any{})
				} else {
					msg := v0DisconnectedMessage
					if m := a.v0DisconnectMsg.Swap(nil); m != nil {
						msg = *m
					}
					a.v0Record(v0EventClientDisconnected, a.now(), map[string]any{"message": msg, "exception": nil})
				}
			}
		}
		// Fell behind and was dropped: carry on with what comes next.
		a.log.Warn("slskd event log fell behind; some events were not recorded")
		sub = a.subscribeV0Events()
	}
}

// v0Completed records a DownloadFileComplete event, and a
// DownloadDirectoryComplete one if the file was the last of its remote
// directory from that user still to finish.
func (a *API) v0Completed(dc events.DownloadCompleted) {
	v := dc.Download
	at := v.Completed
	if at.IsZero() {
		at = a.now()
	}
	a.v0Record(v0EventFileComplete, at, map[string]any{
		"localFilename":  dc.Local,
		"remoteFilename": v.Filename,
		"transfer":       v0File(v, a.now()),
	})

	dir := remoteDir(v.Filename)
	group := []transfers.View{v}
	for _, o := range a.downloads.List() {
		if o.ID != v.ID && o.Username == v.Username && remoteDir(o.Filename) == dir {
			group = append(group, o)
		}
	}
	for _, o := range group {
		if !o.State.Done() {
			return
		}
		// Only the last file to complete reports the directory, so two
		// finishing together do not both do it.
		if o.State == transfers.StateCompleted && (o.Completed.After(v.Completed) || o.Completed.Equal(v.Completed) && o.ID > v.ID) {
			return
		}
	}
	a.v0Record(v0EventDirectoryComplete, at, map[string]any{
		"localDirectoryName":  filepath.Dir(dc.Local),
		"remoteDirectoryName": dir,
		"username":            v.Username,
	})
}

func (a *API) v0ListEvents(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	offset, limit := 0, 100
	var err error
	if s := q.Get("offset"); s != "" {
		if offset, err = strconv.Atoi(s); err != nil || offset < 0 {
			writeError(w, http.StatusBadRequest, "Offset must be greater than or equal to zero")
			return
		}
	}
	if s := q.Get("limit"); s != "" {
		if limit, err = strconv.Atoi(s); err != nil || limit <= 0 {
			writeError(w, http.StatusBadRequest, "Limit must be greater than zero")
			return
		}
	}
	list, total, err := a.v0Events.page(offset, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	out := make([]map[string]any, len(list))
	for i, e := range list {
		out[i] = map[string]any{"id": e.ID, "timestamp": v0EventTime(e.At), "type": e.Type, "data": e.Data}
	}
	w.Header().Set("X-Total-Count", strconv.Itoa(total))
	v0JSON(w, r, http.StatusOK, out)
}

// v0RaiseEvent raises a sample event of a type, as slskd does for testing
// integrations. The body is an optional JSON string prefixed to the
// sample's names.
func (a *API) v0RaiseEvent(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("type")
	typ := ""
	for _, t := range v0EventTypes {
		if strings.EqualFold(t, name) {
			typ = t
		}
	}
	if strings.EqualFold(name, "Any") || strings.EqualFold(name, "None") {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("Event type '%s' can not be raised", name))
		return
	}
	if typ == "" {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("Unknown event type '%s'; must be one of %s", name, strings.Join(v0EventTypes, ", ")))
		return
	}
	var d string
	if r.ContentLength != 0 {
		if !readJSON(w, r, &d) {
			return
		}
	}
	now := a.now()
	var fields map[string]any
	switch typ {
	case v0EventFileComplete:
		fields = map[string]any{"localFilename": d + "local.file", "remoteFilename": d + "remote.file", "transfer": v0File(transfers.View{}, now)}
	case v0EventDirectoryComplete:
		fields = map[string]any{"localDirectoryName": d + "local.directory", "remoteDirectoryName": d + "remote.directory", "username": d + "username"}
	case v0EventUploadFileComplete:
		fields = map[string]any{"localFilename": d + "local.file", "remoteFilename": d + "remote.file", "transfer": v0UploadFile(transfers.UploadView{}, now)}
	case v0EventPrivateMessage:
		fields = map[string]any{"message": map[string]any{
			"id": 42, "timestamp": v0Time(now), "username": d + "username", "message": d + "message", "direction": "In", "isAcknowledged": false,
		}}
	case v0EventRoomMessage:
		fields = map[string]any{"message": map[string]any{
			"timestamp": v0Time(now), "roomName": d + "room", "username": d + "username", "message": d + "message",
		}}
	case v0EventClientDisconnected:
		fields = map[string]any{"message": d + v0DisconnectedMessage, "exception": nil}
	default:
		fields = map[string]any{}
	}
	e, err := a.v0Events.add(typ, now, fields)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	v0JSON(w, r, http.StatusCreated, e)
}

// Batches.

func (a *API) v0EnqueueBatch(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID       string `json:"id"`
		Username string `json:"username"`
		Files    []struct {
			Filename string `json:"filename"`
			Size     uint64 `json:"size"`
		} `json:"files"`
		Options struct {
			ExternalID  string `json:"externalId"`
			Destination string `json:"destination"`
		} `json:"options"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	if strings.TrimSpace(req.Username) == "" {
		writeError(w, http.StatusBadRequest, "username is required")
		return
	}
	id := strings.ToLower(req.ID)
	if id == "" {
		id = newUUID()
	} else if !isUUID(id) {
		writeError(w, http.StatusBadRequest, "id must be a GUID")
		return
	}
	dest, err := a.v0Destination(req.Options.Destination)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	var files []transfers.FileRequest
	failures := []map[string]any{}
	for _, f := range req.Files {
		if strings.TrimSpace(f.Filename) == "" {
			failures = append(failures, map[string]any{"filename": f.Filename, "message": "filename is required"})
			continue
		}
		files = append(files, transfers.FileRequest{Path: f.Filename, Size: f.Size})
	}
	if len(files) == 0 {
		writeError(w, http.StatusBadRequest, "no files to download")
		return
	}
	b := transfers.Batch{ID: id, ExternalID: req.Options.ExternalID, Destination: dest, Created: a.now()}
	views, err := a.downloads.EnqueueWith(req.Username, files, transfers.EnqueueOptions{Batch: &b})
	switch {
	case errors.Is(err, transfers.ErrBatchExists):
		writeError(w, http.StatusConflict, err.Error())
		return
	case err != nil:
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	b.Username = req.Username
	out := a.v0Batch(b)
	enqueued := make([]map[string]any, len(views))
	now := a.now()
	for i, v := range views {
		enqueued[i] = v0File(v, now)
	}
	out["enqueued"] = enqueued
	out["failures"] = failures
	status := http.StatusCreated
	if len(failures) > 0 {
		status = http.StatusMultiStatus
	}
	v0JSON(w, r, status, out)
}

// v0Destination makes a batch destination relative to the downloads
// directory. slskd takes absolute ones too, as long as they are inside it.
func (a *API) v0Destination(dest string) (string, error) {
	if strings.TrimSpace(dest) == "" {
		return "", nil
	}
	if filepath.IsAbs(dest) && a.cfg.DownloadDir != "" {
		root, err := filepath.Abs(a.cfg.DownloadDir)
		if err != nil {
			return "", err
		}
		rel, err := filepath.Rel(root, filepath.Clean(dest))
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return "", fmt.Errorf("destination %q is outside the downloads directory", dest)
		}
		if rel == "." {
			return "", nil
		}
		dest = filepath.ToSlash(rel)
	}
	return transfers.CleanDest(dest)
}

func (a *API) v0GetBatch(w http.ResponseWriter, r *http.Request) {
	b, ok := a.downloads.Batch(strings.ToLower(r.PathValue("id")))
	if !ok {
		writeError(w, http.StatusNotFound, "no such batch")
		return
	}
	v0JSON(w, r, http.StatusOK, a.v0Batch(b))
}

// v0Batch shows a batch with the downloads queued in it.
func (a *API) v0Batch(b transfers.Batch) map[string]any {
	options := map[string]any{"externalId": nil, "destination": nil}
	if b.ExternalID != "" {
		options["externalId"] = b.ExternalID
	}
	if b.Destination != "" {
		options["destination"] = b.Destination
	}
	files := []map[string]any{}
	now := a.now()
	for _, v := range a.downloads.List() {
		if v.Batch == b.ID {
			files = append(files, v0File(v, now))
		}
	}
	return map[string]any{
		"id":          b.ID,
		"username":    b.Username,
		"options":     options,
		"requestedAt": v0Time(b.Created),
		"fileCount":   len(files),
		"files":       files,
	}
}
