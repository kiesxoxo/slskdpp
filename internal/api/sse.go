package api

import (
	"encoding/json"
	"fmt"
	"github.com/kiesxoxo/slskdpp/internal/version"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/kiesxoxo/slskdpp/internal/events"
)

const (
	sseBuffer    = 1024
	sseKeepalive = 25 * time.Second
)

// events streams bus events as server-sent events. With ?search=ID only
// that search's events (and global ones such as server state) are sent.
// The stream ends if the client falls behind; EventSource reconnects and
// the UI refetches what it shows.
func (a *API) events(w http.ResponseWriter, r *http.Request) {
	var filter func(events.Event) bool
	if v := r.URL.Query().Get("search"); v != "" {
		id, err := strconv.ParseUint(v, 10, 32)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid search id")
			return
		}
		filter = func(e events.Event) bool { return e.Search == 0 || e.Search == uint32(id) }
	}
	sub := a.bus.Subscribe(sseBuffer, filter)
	defer a.bus.Unsubscribe(sub)

	rc := http.NewResponseController(w)
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-store")
	h.Set("X-Accel-Buffering", "no") // keep nginx from buffering the stream
	w.WriteHeader(http.StatusOK)

	// Start with the current server state so the UI needs no extra request.
	writeEvent(w, events.Event{Type: events.TypeServerState, Data: a.state()})
	if err := rc.Flush(); err != nil {
		return
	}
	ping := time.NewTicker(sseKeepalive)
	defer ping.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case e, ok := <-sub.C:
			if !ok {
				return
			}
			writeEvent(w, e)
		case <-ping.C:
			io.WriteString(w, ": ping\n\n")
		}
		if err := rc.Flush(); err != nil {
			return
		}
	}
}

func writeEvent(w io.Writer, e events.Event) {
	data, err := json.Marshal(e.Data)
	if err != nil {
		return
	}
	if e.ID != 0 {
		fmt.Fprintf(w, "id: %d\n", e.ID)
	}
	fmt.Fprintf(w, "event: %s\ndata: %s\n\n", e.Type, data)
}

func (a *API) state() events.ServerState {
	return events.ServerState{Connected: a.net.LoggedIn(), Username: a.net.Username(), Version: version.String()}
}

func (a *API) serverState(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, r, http.StatusOK, a.state())
}
