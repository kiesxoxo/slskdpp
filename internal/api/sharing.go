package api

import (
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/kiesxoxo/slskdpp/internal/shares"
	"github.com/kiesxoxo/slskdpp/internal/transfers"
)

func (a *API) listUploads(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, r, http.StatusOK, struct {
		Status  transfers.UploadStatus `json:"status"`
		Uploads []transfers.UploadView `json:"uploads"`
	}{a.uploads.Status(), a.uploads.List()})
}

func (a *API) clearUploads(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, r, http.StatusOK, map[string]int{"removed": a.uploads.Clear()})
}

func (a *API) cancelUpload(w http.ResponseWriter, r *http.Request) {
	a.downloadAction(w, r, a.uploads.Cancel)
}

func (a *API) removeUpload(w http.ResponseWriter, r *http.Request) {
	a.downloadAction(w, r, a.uploads.Remove)
}

type shareView struct {
	shares.Root
	Folders int   `json:"folders"`
	Files   int   `json:"files"`
	Bytes   int64 `json:"bytes"`
}

func (a *API) listShares(w http.ResponseWriter, r *http.Request) {
	roots := []shareView{}
	for _, root := range a.shares.Roots() {
		st := a.shares.RootStats(root.Alias)
		roots = append(roots, shareView{root, st.Folders, st.Files, st.Bytes})
	}
	writeJSON(w, r, http.StatusOK, struct {
		Stats shares.Stats `json:"stats"`
		Roots []shareView  `json:"roots"`
	}{a.shares.Stats(), roots})
}

func (a *API) addShare(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Path  string `json:"path"`
		Alias string `json:"alias"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	if !filepath.IsAbs(req.Path) {
		writeError(w, http.StatusBadRequest, "path must be absolute")
		return
	}
	if strings.ContainsAny(req.Alias, `\/`) {
		writeError(w, http.StatusBadRequest, "alias cannot contain slashes")
		return
	}
	root, err := a.shares.AddRoot(req.Path, strings.TrimSpace(req.Alias))
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, r, http.StatusCreated, shareView{Root: root})
}

func (a *API) removeShare(w http.ResponseWriter, r *http.Request) {
	switch err := a.shares.RemoveRoot(r.PathValue("alias")); {
	case err == nil:
		w.WriteHeader(http.StatusNoContent)
	case errors.Is(err, shares.ErrNotFound):
		writeError(w, http.StatusNotFound, err.Error())
	default:
		writeError(w, http.StatusConflict, err.Error())
	}
}

func (a *API) rescanShares(w http.ResponseWriter, r *http.Request) {
	a.shares.Rescan()
	w.WriteHeader(http.StatusAccepted)
}

// localFolders lists the subfolders of a folder on this machine, for
// picking a folder to share. It starts at the home directory.
func (a *API) localFolders(w http.ResponseWriter, r *http.Request) {
	dir := r.URL.Query().Get("path")
	if dir == "" {
		dir, _ = os.UserHomeDir()
		if dir == "" {
			dir = string(filepath.Separator)
		}
	}
	if !filepath.IsAbs(dir) {
		writeError(w, http.StatusBadRequest, "path must be absolute")
		return
	}
	dir = filepath.Clean(dir)
	entries, err := os.ReadDir(dir)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	folders := []string{}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".") {
			continue
		}
		if e.IsDir() {
			folders = append(folders, e.Name())
		} else if e.Type()&os.ModeSymlink != 0 {
			if st, err := os.Stat(filepath.Join(dir, e.Name())); err == nil && st.IsDir() {
				folders = append(folders, e.Name())
			}
		}
	}
	slices.SortFunc(folders, func(a, b string) int { return strings.Compare(strings.ToLower(a), strings.ToLower(b)) })
	parent := filepath.Dir(dir)
	if parent == dir {
		parent = ""
	}
	writeJSON(w, r, http.StatusOK, map[string]any{"path": dir, "parent": parent, "folders": folders})
}
