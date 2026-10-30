package api

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/kiesxoxo/slskdpp/internal/search"
	"github.com/kiesxoxo/slskdpp/internal/wishlist"
)

func (a *API) listWishlist(w http.ResponseWriter, r *http.Request) {
	entries, err := a.wishlist.List()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, r, http.StatusOK, struct {
		Interval int              `json:"interval_seconds"`
		Entries  []wishlist.Entry `json:"entries"`
	}{int(a.wishlist.Interval().Seconds()), entries})
}

func (a *API) addWishlist(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Query        string        `json:"query"`
		Filter       search.Filter `json:"filter"`
		AutoDownload bool          `json:"auto_download"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	e, err := a.wishlist.Add(req.Query, req.Filter, req.AutoDownload)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, r, http.StatusCreated, e)
}

// wishlistID parses {id}, writing a 404 if it is not a number.
func wishlistID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusNotFound, wishlist.ErrNotFound.Error())
	}
	return id, err == nil
}

func wishlistError(w http.ResponseWriter, err error) {
	if errors.Is(err, wishlist.ErrNotFound) {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	writeError(w, http.StatusInternalServerError, err.Error())
}

func (a *API) updateWishlist(w http.ResponseWriter, r *http.Request) {
	id, ok := wishlistID(w, r)
	if !ok {
		return
	}
	var c wishlist.Change
	if !readJSON(w, r, &c) {
		return
	}
	e, err := a.wishlist.Update(id, c)
	if err != nil {
		wishlistError(w, err)
		return
	}
	writeJSON(w, r, http.StatusOK, e)
}

func (a *API) removeWishlist(w http.ResponseWriter, r *http.Request) {
	if id, ok := wishlistID(w, r); ok {
		if err := a.wishlist.Remove(id); err != nil {
			wishlistError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

func (a *API) searchWishlist(w http.ResponseWriter, r *http.Request) {
	if id, ok := wishlistID(w, r); ok {
		if !a.net.LoggedIn() {
			writeError(w, http.StatusServiceUnavailable, "not connected to the Soulseek server")
			return
		}
		if err := a.wishlist.RunNow(id); err != nil {
			wishlistError(w, err)
			return
		}
		w.WriteHeader(http.StatusAccepted)
	}
}
