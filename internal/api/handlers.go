package api

import (
	"bufio"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/kiesxoxo/slskdpp/internal/client"
	"github.com/kiesxoxo/slskdpp/internal/protocol"
	"github.com/kiesxoxo/slskdpp/internal/search"
)

// peerTimeout bounds browse, folder and user info requests.
const peerTimeout = 2 * time.Minute

type searchView struct {
	ID      uint32    `json:"id"`
	Query   string    `json:"query"`
	Started time.Time `json:"started"`
	Ends    time.Time `json:"ends"`
	search.Stats
}

func viewSearch(s *search.Search) searchView {
	return searchView{ID: s.ID, Query: s.Query, Started: s.Started, Ends: s.Ends, Stats: s.Stats()}
}

func (a *API) listSearches(w http.ResponseWriter, r *http.Request) {
	out := []searchView{}
	for _, s := range a.searches.List() {
		out = append(out, viewSearch(s))
	}
	writeJSON(w, r, http.StatusOK, out)
}

func (a *API) startSearch(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Query string `json:"query"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	if strings.TrimSpace(req.Query) == "" {
		writeError(w, http.StatusBadRequest, "query is required")
		return
	}
	if !a.net.LoggedIn() {
		writeError(w, http.StatusServiceUnavailable, "not connected to the Soulseek server")
		return
	}
	s, err := a.searches.Start(req.Query)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	writeJSON(w, r, http.StatusCreated, viewSearch(s))
}

// lookupSearch resolves the {id} path value, writing a 404 if needed.
func (a *API) lookupSearch(w http.ResponseWriter, r *http.Request) (*search.Search, bool) {
	id, err := strconv.ParseUint(r.PathValue("id"), 10, 32)
	if err == nil {
		if s, ok := a.searches.Get(uint32(id)); ok {
			return s, true
		}
	}
	writeError(w, http.StatusNotFound, "no such search")
	return nil, false
}

func (a *API) getSearch(w http.ResponseWriter, r *http.Request) {
	if s, ok := a.lookupSearch(w, r); ok {
		writeJSON(w, r, http.StatusOK, viewSearch(s))
	}
}

func (a *API) deleteSearch(w http.ResponseWriter, r *http.Request) {
	if s, ok := a.lookupSearch(w, r); ok {
		a.searches.Remove(s.ID)
		w.WriteHeader(http.StatusNoContent)
	}
}

func (a *API) searchHits(w http.ResponseWriter, r *http.Request) {
	s, ok := a.lookupSearch(w, r)
	if !ok {
		return
	}
	f, err := parseFilter(r.URL.Query())
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, r, http.StatusOK, s.Hits(f))
}

func (a *API) searchAlbums(w http.ResponseWriter, r *http.Request) {
	s, ok := a.lookupSearch(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	f, err := parseFilter(q)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	albums := s.Albums(f)
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			writeError(w, http.StatusBadRequest, "invalid limit")
			return
		}
		albums = albums[:min(n, len(albums))]
	}
	if albums == nil {
		albums = []search.Album{}
	}
	writeJSON(w, r, http.StatusOK, albums)
}

// parseFilter reads a search.Filter from query parameters: formats and
// exclude_users (comma-separated), min_bitrate, min_files and
// free_slot_only.
func parseFilter(q url.Values) (search.Filter, error) {
	var f search.Filter
	list := func(key string) []string {
		var out []string
		for _, v := range strings.Split(q.Get(key), ",") {
			if v = strings.TrimSpace(v); v != "" {
				out = append(out, v)
			}
		}
		return out
	}
	f.Formats = list("formats")
	f.ExcludeUsers = list("exclude_users")
	if v := q.Get("min_bitrate"); v != "" {
		n, err := strconv.ParseUint(v, 10, 32)
		if err != nil {
			return f, fmt.Errorf("invalid min_bitrate %q", v)
		}
		f.MinBitRate = uint32(n)
	}
	if v := q.Get("min_files"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			return f, fmt.Errorf("invalid min_files %q", v)
		}
		f.MinFiles = n
	}
	if v := q.Get("free_slot_only"); v != "" {
		b, err := strconv.ParseBool(v)
		if err != nil {
			return f, fmt.Errorf("invalid free_slot_only %q", v)
		}
		f.FreeSlotOnly = b
	}
	return f, nil
}

type dirView struct {
	Name  string        `json:"name"`
	Files []search.File `json:"files"`
}

func viewDir(d protocol.Directory) dirView {
	files := make([]search.File, len(d.Files))
	for j, f := range d.Files {
		f.Name = protocol.FilePath(d.Name, f.Name)
		files[j] = search.ConvertFile(f)
	}
	return dirView{Name: d.Name, Files: files}
}

func viewDirs(dirs []protocol.Directory) []dirView {
	out := make([]dirView, len(dirs))
	for i, d := range dirs {
		out[i] = viewDir(d)
	}
	return out
}

// writeDirList writes dirs as a JSON array one folder at a time, so a
// share of 100,000 files never exists as one big document in memory.
func writeDirList(w io.Writer, dirs []protocol.Directory) error {
	if _, err := io.WriteString(w, "["); err != nil {
		return err
	}
	for i, d := range dirs {
		if i > 0 {
			io.WriteString(w, ",")
		}
		b, err := json.Marshal(viewDir(d))
		if err != nil {
			return err
		}
		if _, err := w.Write(b); err != nil {
			return err
		}
	}
	_, err := io.WriteString(w, "]")
	return err
}

// peerError maps a failed peer request to an HTTP status.
func peerError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, client.ErrUserOffline):
		writeError(w, http.StatusNotFound, "user is offline")
	case errors.Is(err, client.ErrNotConnected):
		writeError(w, http.StatusServiceUnavailable, "not connected to the Soulseek server")
	case errors.Is(err, context.DeadlineExceeded):
		writeError(w, http.StatusGatewayTimeout, "user did not answer in time")
	default:
		writeError(w, http.StatusBadGateway, err.Error())
	}
}

func (a *API) browse(w http.ResponseWriter, r *http.Request) {
	user := r.PathValue("user")
	ctx, cancel := context.WithTimeout(r.Context(), peerTimeout)
	defer cancel()
	list, err := a.net.Browse(ctx, user)
	if err != nil {
		peerError(w, err)
		return
	}
	h := w.Header()
	h.Set("Content-Type", "application/json")
	h.Set("Cache-Control", "no-store")
	h.Add("Vary", "Accept-Encoding")
	var out io.Writer = w
	if acceptsGzip(r) {
		h.Set("Content-Encoding", "gzip")
		gz, _ := gzip.NewWriterLevel(w, gzip.BestSpeed)
		defer gz.Close()
		out = gz
	}
	bw := bufio.NewWriterSize(out, 32<<10)
	defer bw.Flush() // before the gzip writer closes
	name, _ := json.Marshal(user)
	fmt.Fprintf(bw, `{"user":%s,"dirs":`, name)
	if err := writeDirList(bw, list.Dirs); err != nil {
		return // the response has started; the client sees a broken body
	}
	io.WriteString(bw, `,"private_dirs":`)
	writeDirList(bw, list.PrivateDirs)
	io.WriteString(bw, "}")
}

func (a *API) folder(w http.ResponseWriter, r *http.Request) {
	user := r.PathValue("user")
	folder := r.URL.Query().Get("path")
	if folder == "" {
		writeError(w, http.StatusBadRequest, "path is required")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), peerTimeout)
	defer cancel()
	resp, err := a.net.FolderContents(ctx, user, folder)
	if err != nil {
		peerError(w, err)
		return
	}
	writeJSON(w, r, http.StatusOK, struct {
		User   string    `json:"user"`
		Folder string    `json:"folder"`
		Dirs   []dirView `json:"dirs"`
	}{user, resp.Folder, viewDirs(resp.Dirs)})
}

func (a *API) userInfo(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), peerTimeout)
	defer cancel()
	info, err := a.net.UserInfo(ctx, r.PathValue("user"))
	if err != nil {
		peerError(w, err)
		return
	}
	writeJSON(w, r, http.StatusOK, struct {
		Description string `json:"description"`
		Picture     []byte `json:"picture,omitempty"` // base64
		UploadSlots uint32 `json:"upload_slots"`
		QueueSize   uint32 `json:"queue_size"`
		SlotsFree   bool   `json:"slots_free"`
	}{info.Description, info.Picture, info.UploadSlots, info.QueueSize, info.SlotsFree})
}
