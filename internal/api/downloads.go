package api

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/kiesxoxo/slskdpp/internal/transfers"
)

// folderTimeout bounds asking a peer for a folder's full listing before
// falling back to the files the client sent.
const folderTimeout = 45 * time.Second

func (a *API) listDownloads(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, r, http.StatusOK, a.downloads.List())
}

type enqueueRequest struct {
	Username string                  `json:"username"`
	Folder   string                  `json:"folder"`
	Files    []transfers.FileRequest `json:"files"`
}

func (a *API) enqueueFiles(w http.ResponseWriter, r *http.Request) {
	var req enqueueRequest
	if !readJSON(w, r, &req) {
		return
	}
	if req.Username == "" || len(req.Files) == 0 {
		writeError(w, http.StatusBadRequest, "username and files are required")
		return
	}
	a.enqueue(w, r, req)
}

// enqueueFolder downloads a whole remote folder. It asks the peer for the
// folder's complete contents, since a search usually returns only some of
// its files, and falls back to the files in the request.
func (a *API) enqueueFolder(w http.ResponseWriter, r *http.Request) {
	var req enqueueRequest
	if !readJSON(w, r, &req) {
		return
	}
	if req.Username == "" || req.Folder == "" {
		writeError(w, http.StatusBadRequest, "username and folder are required")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), folderTimeout)
	defer cancel()
	resp, err := a.net.FolderContents(ctx, req.Username, req.Folder)
	if files := transfers.FilesInFolder(req.Folder, resp.Dirs); err == nil && len(files) > 0 {
		req.Files = files
	} else if len(req.Files) == 0 {
		if err == nil {
			err = errors.New("folder is empty")
		}
		peerError(w, err)
		return
	} else {
		a.log.Info("using search results for folder download", "user", req.Username, "folder", req.Folder, "err", err)
	}
	a.enqueue(w, r, req)
}

func (a *API) enqueue(w http.ResponseWriter, r *http.Request, req enqueueRequest) {
	views, err := a.downloads.Enqueue(req.Username, req.Files, req.Folder)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, r, http.StatusCreated, views)
}

func (a *API) clearDownloads(w http.ResponseWriter, r *http.Request) {
	var req struct {
		States []transfers.State `json:"states"`
	}
	if r.ContentLength != 0 && !readJSON(w, r, &req) {
		return
	}
	for _, s := range req.States {
		if !s.Done() {
			writeError(w, http.StatusBadRequest, "only completed, failed and cancelled downloads can be cleared")
			return
		}
	}
	n, err := a.downloads.Clear(req.States...)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, r, http.StatusOK, map[string]int{"removed": n})
}

func (a *API) cancelDownload(w http.ResponseWriter, r *http.Request) {
	a.downloadAction(w, r, a.downloads.Cancel)
}

func (a *API) retryDownload(w http.ResponseWriter, r *http.Request) {
	a.downloadAction(w, r, a.downloads.Retry)
}

func (a *API) removeDownload(w http.ResponseWriter, r *http.Request) {
	a.downloadAction(w, r, a.downloads.Remove)
}

func (a *API) downloadAction(w http.ResponseWriter, r *http.Request, act func(int64) error) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusNotFound, "no such download")
		return
	}
	switch err := act(id); {
	case err == nil:
		w.WriteHeader(http.StatusNoContent)
	case errors.Is(err, transfers.ErrNotFound):
		writeError(w, http.StatusNotFound, err.Error())
	default:
		writeError(w, http.StatusConflict, err.Error())
	}
}
