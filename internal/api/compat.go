package api

// An slskd-compatible subset of /api/v0, enough for tools written for
// slskd (soularr, the slskd-api Python client, Lidarr plugins) to search,
// browse and download through slskdpp. Field names, ID formats and state
// strings follow slskd's.

import (
	"cmp"
	"context"
	"crypto/rand"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/kiesxoxo/slskdpp/internal/protocol"
	"github.com/kiesxoxo/slskdpp/internal/search"
	"github.com/kiesxoxo/slskdpp/internal/transfers"
)

// compatVersion is the slskd version we answer as. Clients check it to
// pick response formats (soularr expects directory listings as a list
// after 0.22.2).
const compatVersion = "0.24.0"

func (a *API) routesV0(mux *http.ServeMux) {
	h := func(pattern string, fn http.HandlerFunc) { mux.HandleFunc(pattern, a.auth(fn)) }
	mux.HandleFunc("POST /api/v0/session", a.v0Login)
	mux.HandleFunc("GET /api/v0/session/enabled", a.v0SessionEnabled)
	h("GET /api/v0/session", a.v0CheckSession)
	h("GET /api/v0/application", a.v0Application)
	h("PUT /api/v0/application", a.v0Restart)
	h("DELETE /api/v0/application", a.v0Shutdown)
	h("GET /api/v0/application/version", a.v0Version)
	h("GET /api/v0/application/version/latest", a.v0LatestVersion)
	h("POST /api/v0/application/gc", a.v0CollectGarbage)
	h("POST /api/v0/application/loopback", a.v0Loopback)
	a.routesFiles(h)
	h("GET /api/v0/options", a.v0Options)
	h("PATCH /api/v0/options", a.v0PatchOptions)
	h("GET /api/v0/options/startup", a.v0StartupOptions)
	h("GET /api/v0/options/debug", a.v0DebugOptions)
	h("GET /api/v0/options/yaml/location", a.v0YAMLLocation)
	h("GET /api/v0/options/yaml", a.v0GetYAML)
	h("PUT /api/v0/options/yaml", a.v0PutYAML)
	h("POST /api/v0/options/yaml/validate", a.v0ValidateYAML)
	h("GET /api/v0/logs/live", a.v0LiveLogs)
	h("GET /api/v0/logs/files", a.v0LogFiles)
	h("GET /api/v0/logs/files/{filename}", a.v0LogFile)
	h("GET /api/v0/events", a.v0ListEvents)
	h("POST /api/v0/events/{type}", a.v0RaiseEvent)

	h("GET /api/v0/searches", a.v0ListSearches)
	h("POST /api/v0/searches", a.v0StartSearch)
	h("GET /api/v0/searches/{id}", a.v0GetSearch)
	h("PUT /api/v0/searches/{id}", a.v0StopSearch)
	h("DELETE /api/v0/searches/{id}", a.v0DeleteSearch)
	h("GET /api/v0/searches/{id}/responses", a.v0SearchResponses)

	h("GET /api/v0/transfers/downloads", a.v0Downloads)
	h("DELETE /api/v0/transfers/downloads/all/completed", a.v0ClearDownloads)
	h("POST /api/v0/transfers/downloads/batches", a.v0EnqueueBatch)
	h("GET /api/v0/transfers/downloads/batches/{id}", a.v0GetBatch)
	h("GET /api/v0/transfers/downloads/{username}", a.v0UserDownloads)
	h("POST /api/v0/transfers/downloads/{username}", a.v0Enqueue)
	h("GET /api/v0/transfers/downloads/{username}/{id}", a.v0Download)
	h("DELETE /api/v0/transfers/downloads/{username}/{id}", a.v0CancelDownload)

	h("GET /api/v0/transfers/downloads/{username}/{id}/position", a.v0Position)
	h("GET /api/v0/transfers/uploads", a.v0Uploads)
	h("DELETE /api/v0/transfers/uploads/all/completed", a.v0ClearUploads)
	h("GET /api/v0/transfers/uploads/{username}", a.v0UserUploads)
	h("GET /api/v0/transfers/uploads/{username}/{id}", a.v0Upload)
	h("DELETE /api/v0/transfers/uploads/{username}/{id}", a.v0CancelUpload)

	h("GET /api/v0/users/{username}/browse", a.v0Browse)
	h("GET /api/v0/users/{username}/browse/status", a.v0BrowseStatus)
	h("POST /api/v0/users/{username}/directory", a.v0Directory)
	h("GET /api/v0/users/{username}/info", a.v0UserInfo)
	h("GET /api/v0/users/{username}/status", a.v0UserStatus)
	h("GET /api/v0/users/{username}/endpoint", a.v0UserEndpoint)

	h("GET /api/v0/server", a.v0GetServer)
	h("PUT /api/v0/server", a.v0Connect)
	h("DELETE /api/v0/server", a.v0Disconnect)

	h("GET /api/v0/shares", a.v0Shares)
	h("PUT /api/v0/shares", a.v0Rescan)
	h("DELETE /api/v0/shares", a.v0CancelScan)
	h("GET /api/v0/shares/contents", a.v0AllShareContents)
	h("GET /api/v0/shares/{id}", a.v0Share)
	h("GET /api/v0/shares/{id}/contents", a.v0ShareContents)
}

// v0JSON writes v as slskd does: plain JSON, no envelope.
func v0JSON(w http.ResponseWriter, r *http.Request, status int, v any) { writeJSON(w, r, status, v) }

func (a *API) v0Version(w http.ResponseWriter, r *http.Request) {
	v0JSON(w, r, http.StatusOK, compatVersion)
}

// newUUID returns a random version 4 UUID.
func newUUID() string {
	b := make([]byte, 16)
	rand.Read(b)
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
}

// isUUID reports whether s looks like a UUID.
func isUUID(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i, c := range s {
		switch i {
		case 8, 13, 18, 23:
			if c != '-' {
				return false
			}
		default:
			if !strings.ContainsRune("0123456789abcdefABCDEF", c) {
				return false
			}
		}
	}
	return true
}

// Searches.

// v0Search is an slskd search: one of ours plus the options slskd applies
// to its responses.
type v0Search struct {
	id      string
	search  uint32
	timeout time.Duration
	started time.Time
	stopped bool

	filter        bool
	maxQueue      int
	minSpeed      int
	minFiles      int
	responseLimit int
	fileLimit     int
}

type v0Searches struct {
	mu   sync.Mutex
	byID map[string]*v0Search
}

func (a *API) v0Lookup(w http.ResponseWriter, r *http.Request) (*v0Search, *search.Search, bool) {
	a.v0.mu.Lock()
	vs := a.v0.byID[strings.ToLower(r.PathValue("id"))]
	a.v0.mu.Unlock()
	if vs != nil {
		if s, ok := a.searches.Get(vs.search); ok {
			return vs, s, true
		}
		a.v0.mu.Lock()
		delete(a.v0.byID, vs.id)
		a.v0.mu.Unlock()
	}
	writeError(w, http.StatusNotFound, "no such search")
	return nil, nil, false
}

func (a *API) v0StartSearch(w http.ResponseWriter, r *http.Request) {
	req := struct {
		ID                       string `json:"id"`
		SearchText               string `json:"searchText"`
		SearchTimeout            int    `json:"searchTimeout"`
		FilterResponses          *bool  `json:"filterResponses"`
		MaximumPeerQueueLength   int    `json:"maximumPeerQueueLength"`
		MinimumPeerUploadSpeed   int    `json:"minimumPeerUploadSpeed"`
		MinimumResponseFileCount int    `json:"minimumResponseFileCount"`
		ResponseLimit            int    `json:"responseLimit"`
		FileLimit                int    `json:"fileLimit"`
	}{SearchTimeout: 15000, MaximumPeerQueueLength: 1_000_000, MinimumResponseFileCount: 1, ResponseLimit: 100, FileLimit: 10_000}
	if !readJSON(w, r, &req) {
		return
	}
	if strings.TrimSpace(req.SearchText) == "" {
		writeError(w, http.StatusBadRequest, "searchText is required")
		return
	}
	id := strings.ToLower(req.ID)
	if !isUUID(id) {
		id = newUUID()
	}
	a.v0.mu.Lock()
	_, taken := a.v0.byID[id]
	a.v0.mu.Unlock()
	if taken {
		writeError(w, http.StatusConflict, "a search with this id exists")
		return
	}
	if !a.net.LoggedIn() {
		writeError(w, http.StatusServiceUnavailable, "not connected to the Soulseek server")
		return
	}
	s, err := a.searches.Start(req.SearchText)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	vs := &v0Search{
		id: id, search: s.ID, started: time.Now(),
		timeout:  time.Duration(min(max(req.SearchTimeout, 1000), 600_000)) * time.Millisecond,
		filter:   req.FilterResponses == nil || *req.FilterResponses,
		maxQueue: req.MaximumPeerQueueLength, minSpeed: req.MinimumPeerUploadSpeed,
		minFiles: req.MinimumResponseFileCount, responseLimit: req.ResponseLimit, fileLimit: req.FileLimit,
	}
	a.v0.mu.Lock()
	a.v0.byID[id] = vs
	a.v0.mu.Unlock()
	v0JSON(w, r, http.StatusOK, a.v0State(vs, s, false))
}

func (vs *v0Search) state() string {
	switch {
	case vs.stopped:
		return "Completed, Cancelled"
	case time.Since(vs.started) < vs.timeout:
		return "InProgress"
	}
	return "Completed, TimedOut"
}

func (a *API) v0State(vs *v0Search, s *search.Search, withResponses bool) map[string]any {
	responses := a.v0Responses(vs, s)
	files := 0
	for _, resp := range responses {
		files += resp["fileCount"].(int)
	}
	state := vs.state()
	out := map[string]any{
		"id":              vs.id,
		"searchText":      s.Query,
		"token":           s.ID,
		"state":           state,
		"isComplete":      state != "InProgress",
		"startedAt":       vs.started.UTC().Format(time.RFC3339Nano),
		"responseCount":   len(responses),
		"fileCount":       files,
		"lockedFileCount": 0,
		"responses":       []any{},
	}
	if state != "InProgress" {
		out["endedAt"] = vs.started.Add(vs.timeout).UTC().Format(time.RFC3339Nano)
	}
	if withResponses {
		out["responses"] = responses
	}
	return out
}

// v0Responses regroups our folder hits into slskd's per-user responses,
// applying the search's response filters.
func (a *API) v0Responses(vs *v0Search, s *search.Search) []map[string]any {
	byUser := map[string][]search.FolderHit{}
	for _, h := range s.Hits(search.Filter{}) {
		byUser[h.User] = append(byUser[h.User], h)
	}
	var users []string
	for u := range byUser {
		users = append(users, u)
	}
	// Best first, as the hits were ranked.
	slices.SortFunc(users, func(x, y string) int {
		return cmp.Or(cmp.Compare(byUser[y][0].Score, byUser[x][0].Score), cmp.Compare(x, y))
	})
	out := []map[string]any{}
	total := 0
	for _, u := range users {
		hits := byUser[u]
		first := hits[0]
		var files []map[string]any
		for _, h := range hits {
			for _, f := range h.Files {
				files = append(files, v0SearchFile(f))
			}
		}
		if vs.filter && (int(first.QueueLength) > vs.maxQueue || int(first.UploadSpeed) < vs.minSpeed || len(files) < vs.minFiles) {
			continue
		}
		if len(out) >= vs.responseLimit || total+len(files) > vs.fileLimit {
			break
		}
		total += len(files)
		out = append(out, map[string]any{
			"username":          u,
			"token":             s.ID,
			"fileCount":         len(files),
			"files":             files,
			"lockedFileCount":   0,
			"lockedFiles":       []any{},
			"hasFreeUploadSlot": first.SlotFree,
			"queueLength":       first.QueueLength,
			"uploadSpeed":       first.UploadSpeed,
		})
	}
	return out
}

func v0SearchFile(f search.File) map[string]any {
	m := map[string]any{
		"filename":  f.Path,
		"size":      f.Size,
		"code":      1,
		"isLocked":  false,
		"extension": f.Ext,
	}
	if f.BitDepth > 0 {
		m["bitDepth"] = f.BitDepth
	} else if f.BitRate > 0 {
		m["bitRate"] = f.BitRate
		m["isVariableBitRate"] = f.VBR
	}
	if f.Duration > 0 {
		m["length"] = f.Duration
	}
	if f.SampleRate > 0 {
		m["sampleRate"] = f.SampleRate
	}
	return m
}

func (a *API) v0ListSearches(w http.ResponseWriter, r *http.Request) {
	a.v0.mu.Lock()
	all := make([]*v0Search, 0, len(a.v0.byID))
	for _, vs := range a.v0.byID {
		all = append(all, vs)
	}
	a.v0.mu.Unlock()
	slices.SortFunc(all, func(x, y *v0Search) int { return x.started.Compare(y.started) })
	out := []map[string]any{}
	for _, vs := range all {
		if s, ok := a.searches.Get(vs.search); ok {
			out = append(out, a.v0State(vs, s, false))
		}
	}
	v0JSON(w, r, http.StatusOK, out)
}

func (a *API) v0GetSearch(w http.ResponseWriter, r *http.Request) {
	if vs, s, ok := a.v0Lookup(w, r); ok {
		with, _ := strconv.ParseBool(r.URL.Query().Get("includeResponses"))
		v0JSON(w, r, http.StatusOK, a.v0State(vs, s, with))
	}
}

func (a *API) v0SearchResponses(w http.ResponseWriter, r *http.Request) {
	if vs, s, ok := a.v0Lookup(w, r); ok {
		v0JSON(w, r, http.StatusOK, a.v0Responses(vs, s))
	}
}

func (a *API) v0StopSearch(w http.ResponseWriter, r *http.Request) {
	if vs, s, ok := a.v0Lookup(w, r); ok {
		a.v0.mu.Lock()
		vs.stopped = true
		a.v0.mu.Unlock()
		s.Stop()
		w.WriteHeader(http.StatusOK)
	}
}

func (a *API) v0DeleteSearch(w http.ResponseWriter, r *http.Request) {
	if vs, s, ok := a.v0Lookup(w, r); ok {
		a.searches.Remove(s.ID)
		a.v0.mu.Lock()
		delete(a.v0.byID, vs.id)
		a.v0.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	}
}

// Transfers.

// v0TransferID shows a download ID in slskd's GUID format.
func v0TransferID(id int64) string { return fmt.Sprintf("00000000-0000-4000-8000-%012x", id) }

func v0ParseTransferID(s string) (int64, bool) {
	if !isUUID(s) || !strings.HasPrefix(s, "00000000-0000-4000-8000-") {
		return 0, false
	}
	id, err := strconv.ParseInt(s[24:], 16, 64)
	return id, err == nil
}

var v0States = map[transfers.State]string{
	transfers.StateQueued:       "Queued, Locally",
	transfers.StateRequested:    "Requested",
	transfers.StateRemoteQueued: "Queued, Remotely",
	transfers.StateInitializing: "Initializing",
	transfers.StateTransferring: "InProgress",
	transfers.StateCompleted:    "Completed, Succeeded",
	transfers.StateFailed:       "Completed, Errored",
	transfers.StateCancelled:    "Completed, Cancelled",
}

func v0Time(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t.UTC().Format(time.RFC3339Nano)
}

// v0Duration formats d as a .NET TimeSpan: [d.]hh:mm:ss.fffffff.
func v0Duration(d time.Duration) string {
	d = max(d, 0)
	ticks := int64(d / 100)
	frac := ticks % 10_000_000
	secs := ticks / 10_000_000
	days, h, m, sec := secs/86400, secs/3600%24, secs/60%60, secs%60
	out := fmt.Sprintf("%02d:%02d:%02d.%07d", h, m, sec, frac)
	if days > 0 {
		out = fmt.Sprintf("%d.%s", days, out)
	}
	return out
}

func v0File(v transfers.View, now time.Time) map[string]any {
	state := v0States[v.State]
	if v.State == transfers.StateFailed && strings.HasPrefix(v.Error, "denied") {
		state = "Completed, Rejected"
	}
	pct := 0.0
	if v.Size > 0 {
		pct = float64(v.Bytes) / float64(v.Size) * 100
	}
	m := map[string]any{
		"id":               v0TransferID(v.ID),
		"username":         v.Username,
		"direction":        "Download",
		"filename":         v.Filename,
		"size":             v.Size,
		"startOffset":      0,
		"state":            state,
		"requestedAt":      v0Time(v.Created),
		"enqueuedAt":       v0Time(v.Created),
		"endedAt":          v0Time(v.Completed),
		"bytesTransferred": v.Bytes,
		"averageSpeed":     float64(v.Speed),
		"bytesRemaining":   v.Size - min(v.Bytes, v.Size),
		"percentComplete":  pct,
	}
	if v.QueuePlace > 0 {
		m["placeInQueue"] = v.QueuePlace
	}
	if v.Batch != "" {
		m["batchId"] = v.Batch
	}
	if !v.Started.IsZero() && (v.State == transfers.StateTransferring || v.State == transfers.StateCompleted) {
		m["startedAt"] = v0Time(v.Started)
		end := now
		if v.State == transfers.StateCompleted && v.Completed.After(v.Started) {
			end = v.Completed
		}
		m["elapsedTime"] = v0Duration(end.Sub(v.Started))
		if v.State == transfers.StateTransferring && v.Speed > 0 {
			left := v.Size - min(v.Bytes, v.Size)
			m["remainingTime"] = v0Duration(time.Duration(float64(left) / float64(v.Speed) * float64(time.Second)))
		}
	}
	if v.Error != "" {
		m["exception"] = v.Error
	}
	return m
}

var v0UploadStates = map[transfers.UploadState]transfers.State{
	transfers.UploadQueued:       transfers.StateQueued,
	transfers.UploadInitializing: transfers.StateInitializing,
	transfers.UploadTransferring: transfers.StateTransferring,
	transfers.UploadCompleted:    transfers.StateCompleted,
	transfers.UploadFailed:       transfers.StateFailed,
	transfers.UploadCancelled:    transfers.StateCancelled,
}

// v0UploadFile shows an upload as an slskd transfer.
func v0UploadFile(u transfers.UploadView, now time.Time) map[string]any {
	m := v0File(transfers.View{
		ID: u.ID, Username: u.Username, Filename: u.Filename, Size: u.Size, Bytes: u.Bytes,
		State: v0UploadStates[u.State], Error: u.Error, Speed: u.Speed,
		Created: u.Created, Started: u.Started, Completed: u.Completed,
	}, now)
	m["direction"] = "Upload"
	if u.Place > 0 {
		m["placeInQueue"] = u.Place
	}
	return m
}

// v0Group groups downloads into slskd's per-user transfers, each with
// its remote directories.
func v0Group(views []transfers.View, now time.Time) []map[string]any {
	files := make([]map[string]any, len(views))
	for i, v := range views {
		files[i] = v0File(v, now)
	}
	return v0GroupFiles(files)
}

// v0GroupFiles groups slskd transfers by user and remote directory.
func v0GroupFiles(files []map[string]any) []map[string]any {
	type dirKey struct{ user, dir string }
	dirs := map[dirKey][]map[string]any{}
	var order []dirKey
	for _, f := range files {
		k := dirKey{f["username"].(string), remoteDir(f["filename"].(string))}
		if _, ok := dirs[k]; !ok {
			order = append(order, k)
		}
		dirs[k] = append(dirs[k], f)
	}
	var users []string
	byUser := map[string][]map[string]any{}
	for _, k := range order {
		if _, ok := byUser[k.user]; !ok {
			users = append(users, k.user)
		}
		byUser[k.user] = append(byUser[k.user], map[string]any{
			"directory": k.dir, "fileCount": len(dirs[k]), "files": dirs[k],
		})
	}
	out := []map[string]any{}
	for _, u := range users {
		out = append(out, map[string]any{"username": u, "directories": byUser[u]})
	}
	return out
}

func remoteDir(p string) string {
	if i := strings.LastIndexAny(p, `\/`); i >= 0 {
		return p[:i]
	}
	return ""
}

func (a *API) v0Downloads(w http.ResponseWriter, r *http.Request) {
	v0JSON(w, r, http.StatusOK, v0Group(a.downloads.List(), a.now()))
}

func (a *API) v0UserDownloads(w http.ResponseWriter, r *http.Request) {
	user := r.PathValue("username")
	var mine []transfers.View
	for _, v := range a.downloads.List() {
		if v.Username == user {
			mine = append(mine, v)
		}
	}
	groups := v0Group(mine, a.now())
	if len(groups) == 0 {
		v0JSON(w, r, http.StatusOK, map[string]any{"username": user, "directories": []any{}})
		return
	}
	v0JSON(w, r, http.StatusOK, groups[0])
}

func (a *API) v0Enqueue(w http.ResponseWriter, r *http.Request) {
	var files []transfers.FileRequest
	var raw []struct {
		Filename string `json:"filename"`
		Size     uint64 `json:"size"`
	}
	if !readJSON(w, r, &raw) {
		return
	}
	for _, f := range raw {
		if f.Filename != "" {
			files = append(files, transfers.FileRequest{Path: f.Filename, Size: f.Size})
		}
	}
	if len(files) == 0 {
		writeError(w, http.StatusBadRequest, "no files to download")
		return
	}
	views, err := a.downloads.Enqueue(r.PathValue("username"), files, "")
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	enqueued := make([]map[string]any, len(views))
	for i, v := range views {
		enqueued[i] = v0File(v, a.now())
	}
	v0JSON(w, r, http.StatusCreated, map[string]any{"enqueued": enqueued, "failed": []any{}})
}

// v0Find looks a download up by ID alone: after a source switch its user
// is no longer the one the client queued it from.
func (a *API) v0Find(w http.ResponseWriter, r *http.Request) (transfers.View, bool) {
	id, ok := v0TransferIDFrom(w, r)
	if !ok {
		return transfers.View{}, false
	}
	if v, ok := a.downloads.Get(id); ok {
		return v, true
	}
	writeError(w, http.StatusNotFound, "no such transfer")
	return transfers.View{}, false
}

// v0TransferIDFrom reads a transfer ID from the path. Like slskd, it
// answers 400 when the ID is not a GUID at all; a GUID that is not one of
// ours has id -1, which matches nothing.
func v0TransferIDFrom(w http.ResponseWriter, r *http.Request) (int64, bool) {
	s := strings.ToLower(r.PathValue("id"))
	if !isUUID(s) {
		writeError(w, http.StatusBadRequest, "id must be a GUID")
		return 0, false
	}
	if id, ok := v0ParseTransferID(s); ok {
		return id, true
	}
	return -1, true
}

func (a *API) v0Download(w http.ResponseWriter, r *http.Request) {
	if v, ok := a.v0Find(w, r); ok {
		v0JSON(w, r, http.StatusOK, v0File(v, a.now()))
	}
}

func (a *API) v0CancelDownload(w http.ResponseWriter, r *http.Request) {
	v, ok := a.v0Find(w, r)
	if !ok {
		return
	}
	if remove, _ := strconv.ParseBool(r.URL.Query().Get("remove")); remove {
		a.downloads.Remove(v.ID)
	} else if !v.State.Done() {
		a.downloads.Cancel(v.ID)
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) v0ClearDownloads(w http.ResponseWriter, r *http.Request) {
	if _, err := a.downloads.Clear(transfers.StateCompleted, transfers.StateFailed, transfers.StateCancelled); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// Users.

var v0AttrNames = map[uint32]string{
	protocol.AttrBitRate:    "BitRate",
	protocol.AttrDuration:   "Length",
	protocol.AttrVBR:        "VariableBitRate",
	protocol.AttrSampleRate: "SampleRate",
	protocol.AttrBitDepth:   "BitDepth",
}

var v0AttrKeys = map[uint32]string{
	protocol.AttrBitRate:    "bitRate",
	protocol.AttrDuration:   "length",
	protocol.AttrSampleRate: "sampleRate",
	protocol.AttrBitDepth:   "bitDepth",
}

// v0Dir lists a directory as slskd does: bare file names, attributes both
// as a list and as keys.
func v0Dir(d protocol.Directory) map[string]any { return v0DirNamed(d, false) }

// v0DirNamed is v0Dir with, if full is set, file names as full paths, as
// slskd lists its own shares.
func v0DirNamed(d protocol.Directory, full bool) map[string]any {
	files := make([]map[string]any, 0, len(d.Files))
	for _, f := range d.Files {
		name := f.Name
		if i := strings.LastIndexAny(name, `\/`); i >= 0 {
			name = name[i+1:]
		}
		if full {
			name = protocol.FilePath(d.Name, name)
		}
		attrs := make([]map[string]any, 0, len(f.Attrs))
		file := map[string]any{"filename": name, "size": f.Size, "code": 1, "extension": search.ConvertFile(f).Ext}
		for _, at := range f.Attrs {
			if n, ok := v0AttrNames[at.Code]; ok {
				attrs = append(attrs, map[string]any{"type": n, "value": at.Value})
			}
			if k, ok := v0AttrKeys[at.Code]; ok {
				file[k] = at.Value
			}
			if at.Code == protocol.AttrVBR {
				file["isVariableBitRate"] = at.Value != 0
			}
		}
		file["attributeCount"] = len(attrs)
		file["attributes"] = attrs
		files = append(files, file)
	}
	return map[string]any{"name": d.Name, "fileCount": len(files), "files": files}
}

func v0Dirs(dirs []protocol.Directory) []map[string]any {
	out := make([]map[string]any, len(dirs))
	for i, d := range dirs {
		out[i] = v0Dir(d)
	}
	return out
}

func (a *API) v0Browse(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), peerTimeout)
	defer cancel()
	list, err := a.net.Browse(ctx, r.PathValue("username"))
	if err != nil {
		peerError(w, err)
		return
	}
	v0JSON(w, r, http.StatusOK, map[string]any{
		"directoryCount":       len(list.Dirs),
		"directories":          v0Dirs(list.Dirs),
		"lockedDirectoryCount": len(list.PrivateDirs),
		"lockedDirectories":    v0Dirs(list.PrivateDirs),
	})
}

// v0Directory lists one remote directory. slskd returns a list holding
// just that directory, its subdirectories not included.
func (a *API) v0Directory(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Directory string `json:"directory"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	if req.Directory == "" {
		writeError(w, http.StatusBadRequest, "directory is required")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), peerTimeout)
	defer cancel()
	resp, err := a.net.FolderContents(ctx, r.PathValue("username"), req.Directory)
	if err != nil {
		peerError(w, err)
		return
	}
	out := []map[string]any{}
	for _, d := range resp.Dirs {
		if d.Name == req.Directory {
			out = append(out, v0Dir(d))
		}
	}
	if len(out) == 0 {
		out = append(out, map[string]any{"name": req.Directory, "fileCount": 0, "files": []any{}})
	}
	v0JSON(w, r, http.StatusOK, out)
}
