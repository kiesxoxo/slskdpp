package api

// slskd's event log and download batches, as Lidarr's Tubifarry plugin
// uses them: it polls /api/v0/events to learn where finished files
// landed, and queues each album as a batch with a destination folder of
// its own choosing.

import (
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

// v0EventsKept is how many events the log holds; older ones are dropped.
const v0EventsKept = 1000

// slskd event types.
const (
	v0EventFileComplete      = "DownloadFileComplete"
	v0EventDirectoryComplete = "DownloadDirectoryComplete"
)

type v0Event struct {
	ID   string
	Type string
	At   time.Time
	Data string // the event itself as JSON, as slskd stores it
}

// v0Events is an in-memory log of slskd events, oldest first.
type v0Events struct {
	mu   sync.Mutex
	list []v0Event
	last time.Time
}

// add appends an event. Timestamps strictly increase, since clients find
// new events by comparing them with the newest they have seen.
func (l *v0Events) add(typ string, at time.Time, fields map[string]any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	at = at.UTC().Truncate(time.Microsecond)
	if !at.After(l.last) {
		at = l.last.Add(time.Microsecond)
	}
	l.last = at
	e := v0Event{ID: newUUID(), Type: typ, At: at}
	fields["id"] = e.ID
	fields["timestamp"] = v0EventTime(at)
	fields["type"] = typ
	fields["version"] = 0
	b, _ := json.Marshal(fields)
	e.Data = string(b)
	l.list = append(l.list, e)
	if n := len(l.list) - v0EventsKept; n > 0 {
		l.list = append(l.list[:0:0], l.list[n:]...)
	}
}

// page returns up to limit events, newest first, skipping offset, and how
// many there are in all.
func (l *v0Events) page(offset, limit int) ([]v0Event, int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	total := len(l.list)
	var out []v0Event
	for i := total - 1 - offset; i >= 0 && len(out) < limit; i-- {
		out = append(out, l.list[i])
	}
	return out, total
}

// v0EventTime formats t with .NET's seven fractional digits.
func v0EventTime(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05.0000000Z") }

func (a *API) subscribeCompletions() *events.Sub {
	return a.bus.Subscribe(256, func(e events.Event) bool { return e.Type == events.TypeDownloadCompleted })
}

// watchCompletions turns finished downloads into slskd events for as long
// as the process runs.
func (a *API) watchCompletions(sub *events.Sub) {
	for {
		for e := range sub.C {
			if dc, ok := e.Data.(events.DownloadCompleted); ok {
				a.v0Completed(dc)
			}
		}
		// Fell behind and was dropped: carry on with what comes next.
		a.log.Warn("slskd event log fell behind; some completions were not recorded")
		sub = a.subscribeCompletions()
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
	a.v0Events.add(v0EventFileComplete, at, map[string]any{
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
	a.v0Events.add(v0EventDirectoryComplete, at, map[string]any{
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
			writeError(w, http.StatusBadRequest, "offset must be greater than or equal to zero")
			return
		}
	}
	if s := q.Get("limit"); s != "" {
		if limit, err = strconv.Atoi(s); err != nil || limit <= 0 {
			writeError(w, http.StatusBadRequest, "limit must be greater than zero")
			return
		}
	}
	list, total := a.v0Events.page(offset, limit)
	out := make([]map[string]any, len(list))
	for i, e := range list {
		out[i] = map[string]any{"id": e.ID, "timestamp": v0EventTime(e.At), "type": e.Type, "data": e.Data}
	}
	w.Header().Set("X-Total-Count", strconv.Itoa(total))
	v0JSON(w, r, http.StatusOK, out)
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
