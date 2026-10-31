package api

// More of slskd's /api/v0: uploads, a download's place in the remote
// queue, user details, the server connection and our own shares.

import (
	"context"
	"crypto/sha1"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"net"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/kiesxoxo/slskdpp/internal/protocol"
	"github.com/kiesxoxo/slskdpp/internal/shares"
	"github.com/kiesxoxo/slskdpp/internal/transfers"
)

// Uploads.

func (a *API) v0UploadFiles(match func(transfers.UploadView) bool) []map[string]any {
	now := a.now()
	var out []map[string]any
	for _, u := range a.uploads.List() {
		if match(u) {
			out = append(out, v0UploadFile(u, now))
		}
	}
	return out
}

func (a *API) v0Uploads(w http.ResponseWriter, r *http.Request) {
	v0JSON(w, r, http.StatusOK, v0GroupFiles(a.v0UploadFiles(func(transfers.UploadView) bool { return true })))
}

// v0UserUploads answers 404 when the user has no uploads, as slskd does.
func (a *API) v0UserUploads(w http.ResponseWriter, r *http.Request) {
	user := r.PathValue("username")
	groups := v0GroupFiles(a.v0UploadFiles(func(u transfers.UploadView) bool { return u.Username == user }))
	if len(groups) == 0 {
		writeError(w, http.StatusNotFound, "no uploads to this user")
		return
	}
	v0JSON(w, r, http.StatusOK, groups[0])
}

func (a *API) v0FindUpload(w http.ResponseWriter, r *http.Request) (transfers.UploadView, bool) {
	id, ok := v0TransferIDFrom(w, r)
	if !ok {
		return transfers.UploadView{}, false
	}
	if u, ok := a.uploads.Get(id); ok {
		return u, true
	}
	writeError(w, http.StatusNotFound, "no such transfer")
	return transfers.UploadView{}, false
}

func (a *API) v0Upload(w http.ResponseWriter, r *http.Request) {
	if u, ok := a.v0FindUpload(w, r); ok {
		v0JSON(w, r, http.StatusOK, v0UploadFile(u, a.now()))
	}
}

func (a *API) v0CancelUpload(w http.ResponseWriter, r *http.Request) {
	u, ok := a.v0FindUpload(w, r)
	if !ok {
		return
	}
	if remove, _ := strconv.ParseBool(r.URL.Query().Get("remove")); remove {
		a.uploads.Remove(u.ID)
	} else if !u.State.Done() {
		a.uploads.Cancel(u.ID)
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) v0ClearUploads(w http.ResponseWriter, r *http.Request) {
	a.uploads.Clear()
	w.WriteHeader(http.StatusNoContent)
}

// v0Position asks the peer for a download's place in their queue and
// returns it as a bare number.
func (a *API) v0Position(w http.ResponseWriter, r *http.Request) {
	v, ok := a.v0Find(w, r)
	if !ok {
		return
	}
	place, err := a.downloads.PlaceInQueue(r.Context(), v.ID)
	switch {
	case errors.Is(err, transfers.ErrNotFound):
		writeError(w, http.StatusNotFound, "the download is not waiting in a queue")
	case err != nil:
		writeError(w, http.StatusInternalServerError, err.Error())
	default:
		v0JSON(w, r, http.StatusOK, place)
	}
}

// Users.

func (a *API) v0UserInfo(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), peerTimeout)
	defer cancel()
	info, err := a.net.UserInfo(ctx, r.PathValue("username"))
	if err != nil {
		peerError(w, err)
		return
	}
	out := map[string]any{
		"description":       info.Description,
		"hasFreeUploadSlot": info.SlotsFree,
		"hasPicture":        len(info.Picture) > 0,
		"queueLength":       info.QueueSize,
		"uploadSlots":       info.UploadSlots,
	}
	if len(info.Picture) > 0 {
		out["picture"] = base64.StdEncoding.EncodeToString(info.Picture)
	}
	v0JSON(w, r, http.StatusOK, out)
}

var v0Presence = map[protocol.UserStatus]string{
	protocol.StatusOffline: "Offline",
	protocol.StatusAway:    "Away",
	protocol.StatusOnline:  "Online",
}

func (a *API) v0UserStatus(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), peerTimeout)
	defer cancel()
	st, err := a.net.UserStatus(ctx, r.PathValue("username"))
	if err != nil {
		peerError(w, err)
		return
	}
	presence, ok := v0Presence[st.Status]
	if !ok {
		presence = "Offline"
	}
	v0JSON(w, r, http.StatusOK, map[string]any{"isPrivileged": st.Privileged, "presence": presence})
}

func (a *API) v0UserEndpoint(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), peerTimeout)
	defer cancel()
	addr, err := a.net.PeerAddress(ctx, r.PathValue("username"))
	if err != nil {
		peerError(w, err)
		return
	}
	v0JSON(w, r, http.StatusOK, v0Endpoint(addr.IP.String(), addr.IP.Is4() || addr.IP.Is4In6(), int(addr.Port)))
}

// v0Endpoint shows an address as .NET serializes an IPEndPoint.
func v0Endpoint(ip string, v4 bool, port int) map[string]any {
	family := "InterNetworkV6"
	if v4 {
		family = "InterNetwork"
		ip = strings.TrimPrefix(ip, "::ffff:")
	}
	return map[string]any{"addressFamily": family, "address": ip, "port": port}
}

// v0BrowseStatus reports a browse under way or just finished.
func (a *API) v0BrowseStatus(w http.ResponseWriter, r *http.Request) {
	p, ok := a.net.BrowseStatus(r.PathValue("username"))
	if !ok {
		writeError(w, http.StatusNotFound, "no browse of this user")
		return
	}
	pct := 0.0
	if p.Size > 0 {
		pct = float64(p.Bytes) / float64(p.Size) * 100
	}
	v0JSON(w, r, http.StatusOK, map[string]any{
		"username": p.Username, "bytesTransferred": p.Bytes, "size": p.Size, "percentComplete": pct,
	})
}

// The server connection.

func (a *API) v0Server() map[string]any {
	st := a.net.ServerStatus()
	state := "Disconnected"
	switch {
	case st.LoggedIn:
		state = "Connected, LoggedIn"
	case st.Connecting:
		state = "Connecting"
	}
	out := map[string]any{
		"address":         st.Addr,
		"state":           state,
		"username":        a.net.Username(),
		"isConnected":     st.LoggedIn,
		"isConnecting":    st.Connecting,
		"isLoggedIn":      st.LoggedIn,
		"isLoggingIn":     false,
		"isTransitioning": st.Connecting,
	}
	if tcp, ok := st.Remote.(*net.TCPAddr); ok {
		out["ipEndPoint"] = v0Endpoint(tcp.IP.String(), tcp.IP.To4() != nil, tcp.Port)
	}
	return out
}

func (a *API) v0GetServer(w http.ResponseWriter, r *http.Request) {
	v0JSON(w, r, http.StatusOK, a.v0Server())
}

// v0Connect answers 205 when it only cut a reconnect wait short.
func (a *API) v0Connect(w http.ResponseWriter, r *http.Request) {
	if a.net.Connect() {
		w.WriteHeader(http.StatusResetContent)
		return
	}
	w.WriteHeader(http.StatusOK)
}

// v0Disconnect takes an optional JSON string, the reason recorded with
// the SoulseekClientDisconnected event.
func (a *API) v0Disconnect(w http.ResponseWriter, r *http.Request) {
	var msg string
	if r.ContentLength != 0 {
		if !readJSON(w, r, &msg) {
			return
		}
	}
	if msg != "" {
		a.v0DisconnectMsg.Store(&msg)
	}
	a.net.Disconnect()
	w.WriteHeader(http.StatusNoContent)
}

// Shares.

// v0ShareID is slskd's share ID: the SHA-1 of its remote path, in
// upper-case hex.
func v0ShareID(alias string) string {
	sum := sha1.Sum([]byte(alias))
	return strings.ToUpper(hex.EncodeToString(sum[:]))
}

func (a *API) v0ShareView(root shares.Root) map[string]any {
	st := a.shares.RootStats(root.Alias)
	return map[string]any{
		"id":          v0ShareID(root.Alias),
		"alias":       root.Alias,
		"isExcluded":  false,
		"localPath":   root.Path,
		"raw":         "[" + root.Alias + "]" + root.Path,
		"remotePath":  root.Alias,
		"directories": st.Folders,
		"files":       st.Files,
	}
}

// v0Shares lists shares by host, as slskd does; ours are all on "local".
func (a *API) v0Shares(w http.ResponseWriter, r *http.Request) {
	list := []map[string]any{}
	for _, root := range a.shares.Roots() {
		list = append(list, a.v0ShareView(root))
	}
	v0JSON(w, r, http.StatusOK, map[string]any{"local": list})
}

func (a *API) v0FindShare(w http.ResponseWriter, r *http.Request) (shares.Root, bool) {
	id := r.PathValue("id")
	for _, root := range a.shares.Roots() {
		if strings.EqualFold(v0ShareID(root.Alias), id) {
			return root, true
		}
	}
	writeError(w, http.StatusNotFound, "no such share")
	return shares.Root{}, false
}

func (a *API) v0Share(w http.ResponseWriter, r *http.Request) {
	if root, ok := a.v0FindShare(w, r); ok {
		v0JSON(w, r, http.StatusOK, a.v0ShareView(root))
	}
}

func (a *API) v0AllShareContents(w http.ResponseWriter, r *http.Request) {
	a.v0WriteContents(w, r, "")
}

func (a *API) v0ShareContents(w http.ResponseWriter, r *http.Request) {
	if root, ok := a.v0FindShare(w, r); ok {
		a.v0WriteContents(w, r, root.Alias)
	}
}

// v0WriteContents lists shared folders with full file paths. Like slskd,
// it includes folders that hold only folders, empty, so the tree is whole.
func (a *API) v0WriteContents(w http.ResponseWriter, r *http.Request, alias string) {
	dirs, err := a.shares.Contents(alias)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	have := map[string]bool{}
	for _, d := range dirs {
		have[d.Name] = true
	}
	for _, d := range slices.Clone(dirs) {
		for p := remoteDir(d.Name); p != ""; p = remoteDir(p) {
			if have[p] || alias != "" && p != alias && !strings.HasPrefix(p, alias+`\`) {
				break
			}
			have[p] = true
			dirs = append(dirs, protocol.Directory{Name: p})
		}
	}
	slices.SortFunc(dirs, func(x, y protocol.Directory) int { return strings.Compare(x.Name, y.Name) })
	out := make([]map[string]any, len(dirs))
	for i, d := range dirs {
		out[i] = v0DirNamed(d, true)
	}
	v0JSON(w, r, http.StatusOK, out)
}

func (a *API) v0Rescan(w http.ResponseWriter, r *http.Request) {
	if a.shares.Stats().Scanning {
		writeError(w, http.StatusConflict, "A share scan is already in progress.")
		return
	}
	a.shares.Rescan()
	w.WriteHeader(http.StatusOK)
}

func (a *API) v0CancelScan(w http.ResponseWriter, r *http.Request) {
	if a.shares.CancelScan() {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	writeError(w, http.StatusNotFound, "no share scan is in progress")
}
