package api

// slskd's /api/v0/application: the application state, version, restart
// and shutdown.

import (
	"encoding/json"
	"io"
	"net/http"
	"runtime"
	"runtime/debug"
	"time"
)

// v0StopDelay lets the response to a restart or shutdown go out first.
const v0StopDelay = 200 * time.Millisecond

func v0VersionState() map[string]any {
	return map[string]any{
		"full":              compatVersion,
		"current":           compatVersion,
		"isUpdateAvailable": false, // slskdpp does not check for updates
		"isCanary":          false,
		"isDevelopment":     false,
	}
}

// v0Application is slskd's State, filled in with what slskdpp has. Parts
// slskdpp lacks (VPN, relay, rooms) show as slskd shows them unused.
func (a *API) v0Application(w http.ResponseWriter, r *http.Request) {
	st := a.net.ServerStatus()
	shares := a.shares.Stats()
	up := a.uploads.Status()
	parent := a.net.DistribParent()
	progress := 1.0
	if shares.Scanning {
		progress = 0
	}
	dist := map[string]any{
		"branchLevel": 0, "canAcceptChildren": false, "childLimit": 0, "children": []string{},
		"hasParent": parent != "", "isBranchRoot": false,
	}
	if parent != "" {
		dist["parent"] = parent
	}
	v0JSON(w, r, http.StatusOK, map[string]any{
		"version":          v0VersionState(),
		"pendingReconnect": false,
		"pendingRestart":   false,
		"server":           a.v0Server(),
		"connectionWatchdog": map[string]any{
			"isEnabled":              !st.LoggedIn && !st.Paused,
			"isAttemptingConnection": st.Connecting,
			"isAwaitingVpn":          false,
		},
		"vpn":   map[string]any{"isReady": false, "isConnected": false},
		"relay": map[string]any{"mode": "None", "controller": map[string]any{"state": "Disconnected"}, "agents": []any{}},
		"health": map[string]any{"search": map[string]any{"incoming": map[string]any{
			"latency": 0, "queueDepth": 0, "dropRate": 0,
		}}},
		"user": map[string]any{
			"username":   a.net.Username(),
			"privileges": map[string]any{"isPrivileged": false, "privilegesRemaining": 0},
			"statistics": map[string]any{
				"averageSpeed": up.AvgSpeed, "directoryCount": shares.Folders, "fileCount": shares.Files, "uploadCount": 0,
			},
		},
		"distributedNetwork": dist,
		"shares": map[string]any{
			"scanPending": false, "scanning": shares.Scanning, "ready": !shares.Scanning, "faulted": false,
			"cancelled": false, "scanProgress": progress, "hosts": []string{"local"},
			"directories": shares.Folders, "files": shares.Files,
		},
		"rooms": []string{},
		"users": []any{},
	})
}

// v0LatestVersion returns the version state. slskdpp does not check for
// newer releases, so forceCheck changes nothing.
func (a *API) v0LatestVersion(w http.ResponseWriter, r *http.Request) {
	v0JSON(w, r, http.StatusOK, v0VersionState())
}

// v0Stop restarts or shuts down. Like slskd, it takes a login (session
// or token), never an API key.
func (a *API) v0Stop(w http.ResponseWriter, r *http.Request, restart bool) {
	if viaKey(r) {
		writeError(w, http.StatusForbidden, "API keys cannot restart or shut down slskdpp; log in instead")
		return
	}
	if a.stop == nil {
		writeError(w, http.StatusNotImplemented, "restarting and shutting down are not available")
		return
	}
	what := "shutting down"
	if restart {
		what = "restarting"
	}
	a.log.Warn(what+" on request through the API", "ip", a.clientIP(r))
	time.AfterFunc(v0StopDelay, func() { a.stop(restart) })
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) v0Restart(w http.ResponseWriter, r *http.Request)  { a.v0Stop(w, r, true) }
func (a *API) v0Shutdown(w http.ResponseWriter, r *http.Request) { a.v0Stop(w, r, false) }

func (a *API) v0CollectGarbage(w http.ResponseWriter, r *http.Request) {
	runtime.GC()
	debug.FreeOSMemory()
	w.WriteHeader(http.StatusOK)
}

// v0Loopback logs what it is sent, for testing a client.
func (a *API) v0Loopback(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(io.LimitReader(r.Body, 64<<10))
	if !json.Valid(body) {
		writeError(w, http.StatusBadRequest, "body must be JSON")
		return
	}
	a.log.Info("loopback POST", "body", string(body))
	w.WriteHeader(http.StatusOK)
}
