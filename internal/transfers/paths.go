package transfers

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

// remoteParts splits a remote path. Soulseek uses backslashes, but some
// clients send forward slashes.
func remoteParts(p string) []string {
	return strings.FieldsFunc(p, func(r rune) bool { return r == '\\' || r == '/' })
}

const maxNameBytes = 200

// cleanName makes one remote path component safe as a local file name.
// Remote names are chosen by other users, so this must never let a path
// escape the downloads directory.
func cleanName(s string) string {
	s = strings.Map(func(r rune) rune {
		if r < 32 || r == 127 || strings.ContainsRune(`<>:"/\|?*`, r) {
			return '_'
		}
		return r
	}, s)
	s = strings.TrimRight(strings.TrimSpace(s), ". ")
	if s == "" || s == "." || s == ".." {
		return "_"
	}
	if len(s) > maxNameBytes {
		ext := path.Ext(s)
		if len(ext) > 16 {
			ext = ""
		}
		stem := s[:maxNameBytes-len(ext)]
		for !utf8.ValidString(stem) {
			stem = stem[:len(stem)-1]
		}
		s = stem + ext
	}
	return s
}

// destFor picks where a remote file goes under the downloads directory, as
// a slash-separated relative path. Files downloaded as part of folder keep
// their path below it, starting with the folder's own name; single files
// go into a folder named after their remote parent.
func destFor(remote, folder string) string {
	parts := remoteParts(remote)
	if len(parts) == 0 {
		return "_"
	}
	var keep []string
	if fparts := remoteParts(folder); len(fparts) > 0 && len(parts) > len(fparts) && slicesEqual(parts[:len(fparts)], fparts) {
		keep = parts[len(fparts)-1:]
	} else {
		keep = parts[max(0, len(parts)-2):]
	}
	for i, p := range keep {
		keep[i] = cleanName(p)
	}
	return strings.Join(keep, "/")
}

func slicesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// uniquePath returns p, or p with " (n)" before the extension if p exists.
func uniquePath(p string) string {
	if _, err := os.Lstat(p); errors.Is(err, fs.ErrNotExist) {
		return p
	}
	ext := filepath.Ext(p)
	stem := strings.TrimSuffix(p, ext)
	for n := 1; ; n++ {
		c := fmt.Sprintf("%s (%d)%s", stem, n, ext)
		if _, err := os.Lstat(c); errors.Is(err, fs.ErrNotExist) {
			return c
		}
	}
}

// moveFile renames src to dst, copying when they are on different file
// systems.
func moveFile(src, dst string) error {
	if err := os.Rename(src, dst); err == nil {
		return nil
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		os.Remove(dst)
		return err
	}
	if err := out.Close(); err != nil {
		os.Remove(dst)
		return err
	}
	return os.Remove(src)
}

// altQuery builds a search for other copies of a remote file from the
// words in its name.
func altQuery(remote string) string {
	parts := remoteParts(remote)
	if len(parts) == 0 {
		return ""
	}
	name := parts[len(parts)-1]
	name = strings.TrimSuffix(name, path.Ext(name))
	words := strings.FieldsFunc(strings.ToLower(name), func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r > 127)
	})
	var keep []string
	for _, w := range words {
		if len(w) >= 2 {
			keep = append(keep, w)
		}
		if len(keep) == 6 {
			break
		}
	}
	return strings.Join(keep, " ")
}
