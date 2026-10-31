package api

// slskd's /api/v0/logs: the most recent log records, and the log files.

import (
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// v0LevelName is slog's level as Serilog, and so slskd, names it.
func v0LevelName(l slog.Level) string {
	switch {
	case l < slog.LevelDebug:
		return "Verbose"
	case l < slog.LevelInfo:
		return "Debug"
	case l < slog.LevelWarn:
		return "Information"
	case l < slog.LevelError:
		return "Warning"
	case l < slog.LevelError+4:
		return "Error"
	}
	return "Fatal"
}

// v0LiveLogs returns the recent log records, oldest first. slskd keeps
// the same number, 100.
func (a *API) v0LiveLogs(w http.ResponseWriter, r *http.Request) {
	out := []map[string]any{}
	if a.logs != nil {
		for _, rec := range a.logs.Recent() {
			out = append(out, map[string]any{
				// slskd writes local time with its offset.
				"timestamp": rec.Time.Format("2006-01-02T15:04:05.0000000Z07:00"),
				"context":   rec.Context,
				"level":     v0LevelName(rec.Level),
				"message":   rec.Message,
			})
		}
	}
	v0JSON(w, r, http.StatusOK, out)
}

func (a *API) v0LogDir() string {
	if a.logs == nil {
		return ""
	}
	return a.logs.Dir()
}

// v0LogFiles lists the .log files, as slskd's FilesystemFile entries.
func (a *API) v0LogFiles(w http.ResponseWriter, r *http.Request) {
	dir := a.v0LogDir()
	entries, err := os.ReadDir(dir)
	if dir == "" || err != nil {
		writeError(w, http.StatusNotFound, "there are no log files")
		return
	}
	out := []map[string]any{}
	for _, e := range entries {
		name := e.Name()
		if strings.HasPrefix(name, ".") || !strings.HasSuffix(strings.ToLower(name), ".log") || !e.Type().IsRegular() {
			continue
		}
		if info, err := e.Info(); err == nil {
			out = append(out, v0FileEntry(name, name, info))
		}
	}
	v0JSON(w, r, http.StatusOK, out)
}

// v0LogFile serves one log file as text, with range requests, refusing
// anything but a plain .log file name.
func (a *API) v0LogFile(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("filename")
	switch {
	case strings.TrimSpace(name) == "":
		writeError(w, http.StatusBadRequest, "Filename is required")
		return
	case strings.ContainsAny(name, `/\`):
		writeError(w, http.StatusBadRequest, "Filename must not contain a path")
		return
	case name == "." || name == ".." || strings.ContainsFunc(name, func(r rune) bool { return r < 32 || r == 127 }):
		writeError(w, http.StatusBadRequest, "Filename contains one or more invalid characters")
		return
	case !strings.HasSuffix(strings.ToLower(name), ".log"):
		writeError(w, http.StatusForbidden, "Only .log files may be retrieved")
		return
	}
	dir := a.v0LogDir()
	if dir == "" {
		writeError(w, http.StatusNotFound, "there are no log files")
		return
	}
	f, err := os.Open(filepath.Join(dir, name))
	if err != nil {
		writeError(w, http.StatusNotFound, "no such log file")
		return
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		writeError(w, http.StatusNotFound, "no such log file")
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	http.ServeContent(w, r, name, info.ModTime(), f)
}
