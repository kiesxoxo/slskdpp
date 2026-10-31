package api

// slskd's /api/v0/files: listing and deleting what is in the downloads
// and incomplete directories. Paths arrive base64-encoded, relative to
// the directory. Deleting needs web.remote_file_management, as slskd
// needs remoteFileManagement.

import (
	"encoding/base64"
	"errors"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

func (a *API) routesFiles(h func(string, http.HandlerFunc)) {
	for _, d := range []struct {
		name string
		root func() string
	}{
		{"downloads", func() string { return a.cfg.DownloadDir }},
		{"incomplete", func() string { return a.cfg.IncompleteDir }},
	} {
		base := "/api/v0/files/" + d.name
		h("GET "+base+"/directories", func(w http.ResponseWriter, r *http.Request) { a.v0ListFiles(w, r, d.root(), "") })
		h("GET "+base+"/directories/{path}", func(w http.ResponseWriter, r *http.Request) {
			a.v0ListFiles(w, r, d.root(), r.PathValue("path"))
		})
		h("DELETE "+base+"/directories/{path}", func(w http.ResponseWriter, r *http.Request) {
			a.v0DeleteFile(w, r, d.root(), r.PathValue("path"), true)
		})
		h("DELETE "+base+"/files/{path}", func(w http.ResponseWriter, r *http.Request) {
			a.v0DeleteFile(w, r, d.root(), r.PathValue("path"), false)
		})
	}
}

// errFileStatus carries the status a files request fails with.
type errFileStatus struct {
	status int
	msg    string
}

func (e *errFileStatus) Error() string { return e.msg }

func fileErr(status int, msg string) error { return &errFileStatus{status, msg} }

// v0Resolve decodes a base64 path and places it under root. It refuses
// "." and ".." segments outright, and any path that symlinks lead out of
// root: its parent always, and with follow the path itself.
func v0Resolve(root, b64 string, follow bool) (string, error) {
	if root == "" {
		return "", fileErr(http.StatusNotFound, "the directory is not configured")
	}
	root, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	rel := ""
	if b64 != "" {
		var raw []byte
		for _, enc := range []*base64.Encoding{base64.StdEncoding, base64.URLEncoding, base64.RawStdEncoding, base64.RawURLEncoding} {
			if raw, err = enc.DecodeString(b64); err == nil {
				break
			}
		}
		if err != nil {
			return "", fileErr(http.StatusBadRequest, "the path is not valid base64")
		}
		rel = strings.ReplaceAll(string(raw), `\`, "/")
	}
	rel = strings.TrimLeft(rel, "/")
	parts := strings.Split(rel, "/")
	for _, p := range parts {
		if p == "." || p == ".." {
			return "", fileErr(http.StatusBadRequest, "Paths containing traversal segments are not allowed")
		}
	}
	if len(parts) > 0 && strings.Contains(parts[0], ":") {
		return "", fileErr(http.StatusBadRequest, "Path must be relative")
	}
	full := filepath.Join(root, filepath.FromSlash(rel))
	// Symlinks may not lead out of root: check where the parent really is.
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", fileErr(http.StatusNotFound, "the directory does not exist")
	}
	inside := func(p string) error {
		real, err := filepath.EvalSymlinks(p)
		if errors.Is(err, fs.ErrNotExist) {
			return fileErr(http.StatusNotFound, "not found")
		}
		if err != nil {
			return err
		}
		if real != realRoot && !strings.HasPrefix(real, realRoot+string(filepath.Separator)) {
			return fileErr(http.StatusForbidden, "Only application-controlled directories can be accessed")
		}
		return nil
	}
	if full != root {
		if err := inside(filepath.Dir(full)); err != nil {
			return "", err
		}
		if follow {
			if err := inside(full); err != nil {
				return "", err
			}
		}
	}
	return full, nil
}

func writeFileErr(w http.ResponseWriter, err error) {
	var fe *errFileStatus
	switch {
	case errors.As(err, &fe):
		writeError(w, fe.status, fe.msg)
	case errors.Is(err, fs.ErrNotExist):
		writeError(w, http.StatusNotFound, "not found")
	case errors.Is(err, fs.ErrPermission):
		writeError(w, http.StatusForbidden, "access denied")
	default:
		writeError(w, http.StatusInternalServerError, err.Error())
	}
}

// v0Attrs renders file attributes as .NET's FileAttributes flags would
// be on Linux.
func v0Attrs(name string, info fs.FileInfo) string {
	var flags []string
	if info.Mode().Perm()&0o222 == 0 {
		flags = append(flags, "ReadOnly")
	}
	if strings.HasPrefix(name, ".") {
		flags = append(flags, "Hidden")
	}
	if info.IsDir() {
		flags = append(flags, "Directory")
	}
	if len(flags) == 0 {
		return "Normal"
	}
	return strings.Join(flags, ", ")
}

// v0FileTime formats a time as .NET writes a UTC DateTime.
func v0FileTime(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05.0000000Z") }

func v0FileEntry(name, fullName string, info fs.FileInfo) map[string]any {
	m := map[string]any{
		"name":       name,
		"fullName":   fullName,
		"attributes": v0Attrs(name, info),
		// Linux has no portable creation time; .NET falls back the same way.
		"createdAt":  v0FileTime(info.ModTime()),
		"modifiedAt": v0FileTime(info.ModTime()),
	}
	if !info.IsDir() {
		m["length"] = info.Size()
	}
	return m
}

// v0ListFiles lists a directory: its files and folders, or with
// ?recursive=true everything below it, flat, with paths relative to it.
func (a *API) v0ListFiles(w http.ResponseWriter, r *http.Request, root, b64 string) {
	recursive, _ := strconv.ParseBool(r.URL.Query().Get("recursive"))
	dir, err := v0Resolve(root, b64, true)
	if err != nil {
		writeFileErr(w, err)
		return
	}
	info, err := os.Stat(dir)
	if err != nil || !info.IsDir() {
		writeError(w, http.StatusNotFound, "the directory does not exist")
		return
	}
	files, dirs := []map[string]any{}, []map[string]any{}
	add := func(rel string, d fs.DirEntry) {
		fi, err := d.Info()
		if err != nil {
			return
		}
		if fi.Mode()&fs.ModeSymlink != 0 {
			if fi, err = os.Stat(filepath.Join(dir, rel)); err != nil {
				return // broken link
			}
		}
		e := v0FileEntry(d.Name(), filepath.ToSlash(rel), fi)
		if fi.IsDir() {
			dirs = append(dirs, e)
		} else if fi.Mode().IsRegular() {
			files = append(files, e)
		}
	}
	if recursive {
		err = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil // skip what cannot be read
			}
			if p == dir {
				return nil
			}
			rel, _ := filepath.Rel(dir, p)
			add(rel, d)
			return nil
		})
	} else {
		var entries []fs.DirEntry
		entries, err = os.ReadDir(dir)
		for _, d := range entries {
			add(d.Name(), d)
		}
	}
	if err != nil {
		writeFileErr(w, err)
		return
	}
	out := v0FileEntry(filepath.Base(dir), "", info)
	out["files"] = files
	out["directories"] = dirs
	v0JSON(w, r, http.StatusOK, out)
}

// v0DeleteFile deletes a file, or a folder and everything in it.
func (a *API) v0DeleteFile(w http.ResponseWriter, r *http.Request, root, b64 string, isDir bool) {
	if !a.cfg.RemoteFiles {
		writeError(w, http.StatusForbidden, "remote file management is off; set web.remote_file_management to allow it")
		return
	}
	target, err := v0Resolve(root, b64, false)
	if err != nil {
		writeFileErr(w, err)
		return
	}
	if abs, _ := filepath.Abs(root); target == abs {
		writeError(w, http.StatusBadRequest, "Deletion of application-controlled directory roots is not supported")
		return
	}
	info, err := os.Lstat(target)
	if err != nil {
		writeFileErr(w, err)
		return
	}
	// A symlink is removed itself, never what it points to.
	link := info.Mode()&fs.ModeSymlink != 0
	switch {
	case isDir && !info.IsDir() && !link:
		writeError(w, http.StatusBadRequest, "not a directory")
		return
	case !isDir && info.IsDir():
		writeError(w, http.StatusBadRequest, "not a file")
		return
	}
	if isDir && !link {
		err = os.RemoveAll(target)
	} else {
		err = os.Remove(target)
	}
	if err != nil {
		writeFileErr(w, err)
		return
	}
	a.log.Info("deleted through the slskd API", "path", target)
	w.WriteHeader(http.StatusNoContent)
}
