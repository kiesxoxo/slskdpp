// Package shares indexes the folders we share and answers searches,
// browse and folder requests from it. The index lives in SQLite (FTS5 for
// searches), so memory use stays flat however much is shared.
package shares

import (
	"cmp"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/fsnotify/fsnotify"

	"github.com/kiesxoxo/slskdpp/internal/protocol"
)

// Root is a local folder shared under an alias, the first component of
// every path others see.
type Root struct {
	Alias      string `json:"alias"`
	Path       string `json:"path"`
	FromConfig bool   `json:"from_config"`
}

// ParseRoot reads a config entry: "/path" (alias is the folder name) or
// "alias=/path".
func ParseRoot(s string) Root {
	if alias, p, ok := strings.Cut(s, "="); ok && alias != "" && !strings.ContainsAny(alias, `/\`) {
		return Root{Alias: alias, Path: p, FromConfig: true}
	}
	return Root{Path: s, FromConfig: true}
}

type Config struct {
	Roots []Root // from the config file
	// MaxResults caps the files in one search response.
	MaxResults int
	// RescanInterval re-reads every share now and then, in case changes
	// were missed; 0 means every 6 hours.
	RescanInterval time.Duration
	// OnChange is called after the shared files change.
	OnChange func()
	Logger   *slog.Logger
}

// Index is the share index.
type Index struct {
	cfg Config
	log *slog.Logger
	db  *sql.DB

	scanMu sync.Mutex // one scan at a time

	mu       sync.Mutex
	roots    map[string]Root
	excluded []string
	browse   *protocol.Encoded // cached share list; nil when stale
	stats    Stats
	watcher  *fsnotify.Watcher
	watchErr bool
	dirty    map[string]bool // changed local folders waiting for a rescan
	kick     chan struct{}
}

// Stats sums up what is shared.
type Stats struct {
	Folders  int       `json:"folders"`
	Files    int       `json:"files"`
	Bytes    int64     `json:"bytes"`
	Scanning bool      `json:"scanning"`
	LastScan time.Time `json:"last_scan,omitzero"`
}

// Open loads share roots and the existing index.
func Open(cfg Config, db *sql.DB) (*Index, error) {
	if cfg.MaxResults <= 0 {
		cfg.MaxResults = 500
	}
	if cfg.RescanInterval <= 0 {
		cfg.RescanInterval = 6 * time.Hour
	}
	if cfg.OnChange == nil {
		cfg.OnChange = func() {}
	}
	log := cfg.Logger
	if log == nil {
		log = slog.Default()
	}
	x := &Index{
		cfg:   cfg,
		log:   log.With("component", "shares"),
		db:    db,
		roots: make(map[string]Root),
		dirty: make(map[string]bool),
		kick:  make(chan struct{}, 1),
	}
	for _, r := range cfg.Roots {
		if _, err := x.addRootLocked(r); err != nil {
			x.log.Error("ignoring share", "path", r.Path, "err", err)
		}
	}
	rows, err := db.Query(`SELECT alias, path FROM share_roots ORDER BY alias`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var r Root
		if err := rows.Scan(&r.Alias, &r.Path); err != nil {
			rows.Close()
			return nil, err
		}
		if _, err := x.addRootLocked(r); err != nil {
			x.log.Error("ignoring share", "path", r.Path, "err", err)
		}
	}
	rows.Close()
	// Forget files of roots no longer shared.
	aliases, err := x.queryStrings(`SELECT DISTINCT root FROM shared_files`)
	if err != nil {
		return nil, err
	}
	for _, a := range aliases {
		if _, ok := x.roots[a]; !ok {
			if _, err := db.Exec(`DELETE FROM shared_files WHERE root = ?`, a); err != nil {
				return nil, err
			}
		}
	}
	x.refreshStats()
	return x, nil
}

// addRootLocked validates r and adds it, picking an alias if needed, and
// returns it as stored.
func (x *Index) addRootLocked(r Root) (Root, error) {
	p, err := filepath.Abs(r.Path)
	if err != nil {
		return Root{}, err
	}
	r.Path = filepath.Clean(p)
	if r.Alias == "" {
		r.Alias = filepath.Base(r.Path)
	}
	r.Alias = strings.Map(func(c rune) rune {
		if c == '\\' || c == '/' || unicode.IsControl(c) {
			return '_'
		}
		return c
	}, r.Alias)
	for _, o := range x.roots {
		if o.Path == r.Path || within(o.Path, r.Path) || within(r.Path, o.Path) {
			return Root{}, fmt.Errorf("overlaps the share %q (%s)", o.Alias, o.Path)
		}
	}
	base := r.Alias
	for n := 2; x.roots[r.Alias].Path != ""; n++ {
		r.Alias = fmt.Sprintf("%s %d", base, n)
	}
	x.roots[r.Alias] = r
	return r, nil
}

// within reports whether p is inside dir.
func within(dir, p string) bool {
	rel, err := filepath.Rel(dir, p)
	return err == nil && rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// Roots lists the shared folders.
func (x *Index) Roots() []Root {
	x.mu.Lock()
	defer x.mu.Unlock()
	out := make([]Root, 0, len(x.roots))
	for _, r := range x.roots {
		out = append(out, r)
	}
	slices.SortFunc(out, func(a, b Root) int { return cmp.Compare(a.Alias, b.Alias) })
	return out
}

// RootStats counts what a root shares.
func (x *Index) RootStats(alias string) Stats {
	var s Stats
	x.db.QueryRow(`SELECT count(DISTINCT folder), count(*), coalesce(sum(size), 0) FROM shared_files WHERE root = ?`, alias).
		Scan(&s.Folders, &s.Files, &s.Bytes)
	return s
}

var ErrNotFound = errors.New("no such share")

// AddRoot shares another folder and scans it.
func (x *Index) AddRoot(dir, alias string) (Root, error) {
	st, err := os.Stat(dir)
	if err != nil {
		return Root{}, err
	}
	if !st.IsDir() {
		return Root{}, fmt.Errorf("%s is not a folder", dir)
	}
	x.mu.Lock()
	r, err := x.addRootLocked(Root{Alias: alias, Path: dir})
	if err != nil {
		x.mu.Unlock()
		return Root{}, err
	}
	_, err = x.db.Exec(`INSERT INTO share_roots (alias, path) VALUES (?, ?)`, r.Alias, r.Path)
	if err != nil {
		delete(x.roots, r.Alias)
		x.mu.Unlock()
		return Root{}, err
	}
	x.dirty[r.Path] = true
	x.mu.Unlock()
	x.wake()
	return r, nil
}

// RemoveRoot stops sharing a folder added from the UI.
func (x *Index) RemoveRoot(alias string) error {
	x.mu.Lock()
	r, ok := x.roots[alias]
	if !ok {
		x.mu.Unlock()
		return ErrNotFound
	}
	if r.FromConfig {
		x.mu.Unlock()
		return errors.New("this share comes from the config file; remove it there")
	}
	delete(x.roots, alias)
	x.mu.Unlock()
	x.scanMu.Lock()
	defer x.scanMu.Unlock()
	if _, err := x.db.Exec(`DELETE FROM share_roots WHERE alias = ?`, alias); err != nil {
		return err
	}
	if _, err := x.db.Exec(`DELETE FROM shared_files WHERE root = ?`, alias); err != nil {
		return err
	}
	x.changed()
	return nil
}

// Rescan re-reads every share.
func (x *Index) Rescan() {
	x.mu.Lock()
	for _, r := range x.roots {
		x.dirty[r.Path] = true
	}
	x.mu.Unlock()
	x.wake()
}

func (x *Index) wake() {
	select {
	case x.kick <- struct{}{}:
	default:
	}
}

// Stats sums up what is shared.
func (x *Index) Stats() Stats {
	x.mu.Lock()
	defer x.mu.Unlock()
	return x.stats
}

func (x *Index) refreshStats() {
	var s Stats
	x.db.QueryRow(`SELECT count(DISTINCT folder), count(*), coalesce(sum(size), 0) FROM shared_files`).
		Scan(&s.Folders, &s.Files, &s.Bytes)
	x.mu.Lock()
	s.Scanning, s.LastScan = x.stats.Scanning, x.stats.LastScan
	x.stats = s
	x.mu.Unlock()
}

// changed drops cached answers and tells the owner.
func (x *Index) changed() {
	x.mu.Lock()
	x.browse = nil
	x.mu.Unlock()
	x.refreshStats()
	x.cfg.OnChange()
}

// Run scans the shares, then keeps the index current until ctx is done.
func (x *Index) Run(ctx context.Context) {
	w, err := fsnotify.NewWatcher()
	if err != nil {
		x.log.Warn("cannot watch shares for changes; relying on periodic rescans", "err", err)
	} else {
		x.mu.Lock()
		x.watcher = w
		x.mu.Unlock()
		defer w.Close()
		go x.watch(ctx, w)
	}
	x.Rescan()
	rescan := time.NewTicker(x.cfg.RescanInterval)
	defer rescan.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-rescan.C:
			x.Rescan()
		case <-x.kick:
			x.scanDirty(ctx)
		}
	}
}

// watch turns file system events into rescans of the changed folders,
// waiting for activity to settle first.
func (x *Index) watch(ctx context.Context, w *fsnotify.Watcher) {
	const settle = 3 * time.Second
	timer := time.NewTimer(time.Hour)
	timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-w.Events:
			if !ok {
				return
			}
			if ev.Has(fsnotify.Chmod) && !ev.Has(fsnotify.Write) {
				continue
			}
			x.mu.Lock()
			x.dirty[filepath.Dir(ev.Name)] = true
			x.mu.Unlock()
			timer.Reset(settle)
		case err, ok := <-w.Errors:
			if !ok {
				return
			}
			x.log.Warn("share watcher", "err", err)
		case <-timer.C:
			x.wake()
		}
	}
}

// scanDirty rescans the folders marked as changed.
func (x *Index) scanDirty(ctx context.Context) {
	x.mu.Lock()
	dirs := make([]string, 0, len(x.dirty))
	for d := range x.dirty {
		dirs = append(dirs, d)
	}
	clear(x.dirty)
	x.stats.Scanning = true
	x.mu.Unlock()
	defer func() {
		x.mu.Lock()
		x.stats.Scanning = false
		x.stats.LastScan = time.Now()
		x.mu.Unlock()
		x.refreshStats()
	}()

	// Scanning a folder covers everything below it.
	slices.Sort(dirs)
	var todo []string
	for _, d := range dirs {
		if len(todo) == 0 || !(d == todo[len(todo)-1] || within(todo[len(todo)-1], d)) {
			todo = append(todo, d)
		}
	}
	changed := false
	for _, d := range todo {
		if ctx.Err() != nil {
			return
		}
		r, ok := x.rootOf(d)
		if !ok {
			continue
		}
		// A removed folder is handled by rescanning its nearest parent.
		for d != r.Path {
			if st, err := os.Stat(d); err == nil && st.IsDir() {
				break
			}
			d = filepath.Dir(d)
		}
		c, err := x.scan(ctx, r, d)
		if err != nil {
			x.log.Warn("scanning share", "share", r.Alias, "dir", d, "err", err)
		}
		changed = changed || c
	}
	if changed {
		x.changed()
	}
}

func (x *Index) rootOf(dir string) (Root, bool) {
	x.mu.Lock()
	defer x.mu.Unlock()
	for _, r := range x.roots {
		if dir == r.Path || within(r.Path, dir) {
			return r, true
		}
	}
	return Root{}, false
}

// virtualFolder maps a local folder inside r to the path others see.
func virtualFolder(r Root, dir string) string {
	rel, err := filepath.Rel(r.Path, dir)
	if err != nil || rel == "." {
		return r.Alias
	}
	return r.Alias + `\` + strings.ReplaceAll(filepath.ToSlash(rel), "/", `\`)
}

type entry struct {
	name  string
	size  int64
	mtime int64
}

// scan brings the index for dir (inside r) and everything below it in
// line with the disk, and reports whether anything changed. An
// unreadable root is left as it is, so a missing mount does not wipe it.
func (x *Index) scan(ctx context.Context, r Root, dir string) (bool, error) {
	x.scanMu.Lock()
	defer x.scanMu.Unlock()
	if _, err := os.ReadDir(dir); err != nil {
		return false, err
	}
	seen := map[string]bool{}
	changed := false
	var walk func(local string) error
	walk = func(local string) error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		entries, err := os.ReadDir(local)
		if err != nil {
			x.log.Debug("skipping unreadable folder", "dir", local, "err", err)
			return nil
		}
		x.addWatch(local)
		folder := virtualFolder(r, local)
		seen[folder] = true
		var files []entry
		for _, e := range entries {
			name := e.Name()
			if strings.HasPrefix(name, ".") || strings.ContainsRune(name, '\\') {
				continue
			}
			full := filepath.Join(local, name)
			if e.IsDir() {
				if err := walk(full); err != nil {
					return err
				}
				continue
			}
			info, err := os.Stat(full) // follows symlinks to files
			if err != nil || !info.Mode().IsRegular() {
				continue
			}
			files = append(files, entry{name, info.Size(), info.ModTime().UnixNano()})
		}
		c, err := x.syncFolder(r.Alias, folder, files)
		changed = changed || c
		return err
	}
	if err := walk(dir); err != nil {
		return changed, err
	}
	// Folders that were indexed below dir but are gone now.
	top := virtualFolder(r, dir)
	folders, err := x.queryStrings(`SELECT DISTINCT folder FROM shared_files WHERE folder = ? OR (folder >= ? AND folder < ?)`,
		top, top+`\`, top+`]`)
	if err != nil {
		return changed, err
	}
	for _, f := range folders {
		if !seen[f] {
			if _, err := x.db.Exec(`DELETE FROM shared_files WHERE folder = ?`, f); err != nil {
				return changed, err
			}
			changed = true
		}
	}
	return changed, nil
}

func (x *Index) addWatch(dir string) {
	x.mu.Lock()
	w, failed := x.watcher, x.watchErr
	x.mu.Unlock()
	if w == nil || failed {
		return
	}
	if err := w.Add(dir); err != nil {
		x.mu.Lock()
		x.watchErr = true
		x.mu.Unlock()
		x.log.Warn("cannot watch every shared folder; later changes are picked up by periodic rescans", "err", err)
	}
}

// syncFolder makes the index rows for one folder match files.
func (x *Index) syncFolder(root, folder string, files []entry) (bool, error) {
	type row struct {
		id          int64
		size, mtime int64
	}
	have := map[string]row{}
	rows, err := x.db.Query(`SELECT id, name, size, mtime FROM shared_files WHERE folder = ?`, folder)
	if err != nil {
		return false, err
	}
	for rows.Next() {
		var name string
		var r row
		if err := rows.Scan(&r.id, &name, &r.size, &r.mtime); err != nil {
			rows.Close()
			return false, err
		}
		have[name] = r
	}
	rows.Close()

	var add, upd []entry
	var del []int64
	for _, f := range files {
		r, ok := have[f.name]
		switch {
		case !ok:
			add = append(add, f)
		case r.size != f.size || r.mtime != f.mtime:
			upd = append(upd, f)
		}
		delete(have, f.name)
	}
	for _, r := range have {
		del = append(del, r.id)
	}
	if len(add)+len(upd)+len(del) == 0 {
		return false, nil
	}
	tx, err := x.db.Begin()
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	for _, f := range add {
		if _, err := tx.Exec(`INSERT INTO shared_files (root, folder, name, size, mtime) VALUES (?, ?, ?, ?, ?)`,
			root, folder, f.name, f.size, f.mtime); err != nil {
			return false, err
		}
	}
	for _, f := range upd {
		if _, err := tx.Exec(`UPDATE shared_files SET size = ?, mtime = ? WHERE folder = ? AND name = ?`,
			f.size, f.mtime, folder, f.name); err != nil {
			return false, err
		}
	}
	for _, id := range del {
		if _, err := tx.Exec(`DELETE FROM shared_files WHERE id = ?`, id); err != nil {
			return false, err
		}
	}
	return true, tx.Commit()
}

func (x *Index) queryStrings(q string, args ...any) ([]string, error) {
	rows, err := x.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// SetExcluded sets the phrases the server forbids in search results.
func (x *Index) SetExcluded(phrases []string) {
	lower := make([]string, 0, len(phrases))
	for _, p := range phrases {
		if p = strings.ToLower(strings.TrimSpace(p)); p != "" {
			lower = append(lower, p)
		}
	}
	x.mu.Lock()
	x.excluded = lower
	x.mu.Unlock()
}

// words splits text the way the FTS5 unicode61 tokenizer does.
func words(s string) []string {
	return strings.FieldsFunc(s, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsNumber(r) })
}

// matchQuery turns a Soulseek search into an FTS5 query: every word must
// appear in the path, and words of "-term" must not.
func matchQuery(q string) string {
	var inc, exc []string
	for _, term := range strings.Fields(strings.ToLower(q)) {
		neg := strings.HasPrefix(term, "-")
		for _, w := range words(strings.TrimLeft(term, "-*")) {
			quoted := `"` + w + `"`
			if neg {
				exc = append(exc, quoted)
			} else {
				inc = append(inc, quoted)
			}
		}
	}
	if len(inc) == 0 {
		return ""
	}
	m := strings.Join(inc, " AND ")
	for _, e := range exc {
		m += " NOT " + e
	}
	return m
}

// Search returns shared files matching a Soulseek query, with full paths.
func (x *Index) Search(query string) []protocol.File {
	match := matchQuery(query)
	if match == "" {
		return nil
	}
	x.mu.Lock()
	excluded := x.excluded
	x.mu.Unlock()
	// Fetch extra rows to make up for ones the excluded phrases drop.
	rows, err := x.db.Query(`SELECT f.folder, f.name, f.size FROM shared_fts JOIN shared_files f ON f.id = shared_fts.rowid
		WHERE shared_fts MATCH ? LIMIT ?`, match, x.cfg.MaxResults*2)
	if err != nil {
		x.log.Debug("search failed", "query", query, "err", err)
		return nil
	}
	defer rows.Close()
	var out []protocol.File
	for rows.Next() && len(out) < x.cfg.MaxResults {
		var folder, name string
		var size uint64
		if err := rows.Scan(&folder, &name, &size); err != nil {
			return out
		}
		full := folder + `\` + name
		if len(excluded) > 0 {
			lower := strings.ToLower(full)
			if slices.ContainsFunc(excluded, func(p string) bool { return strings.Contains(lower, p) }) {
				continue
			}
		}
		out = append(out, fileOf(full, name, size))
	}
	return out
}

func fileOf(full, name string, size uint64) protocol.File {
	return protocol.File{Name: full, Size: size, Ext: strings.ToLower(strings.TrimPrefix(path.Ext(name), "."))}
}

// listDirs reads folders with their files (bare names) for browse and
// folder responses.
func (x *Index) listDirs(where string, args ...any) ([]protocol.Directory, error) {
	rows, err := x.db.Query(`SELECT folder, name, size FROM shared_files `+where+` ORDER BY folder, name`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var dirs []protocol.Directory
	for rows.Next() {
		var folder, name string
		var size uint64
		if err := rows.Scan(&folder, &name, &size); err != nil {
			return nil, err
		}
		if len(dirs) == 0 || dirs[len(dirs)-1].Name != folder {
			dirs = append(dirs, protocol.Directory{Name: folder})
		}
		d := &dirs[len(dirs)-1]
		d.Files = append(d.Files, fileOf(name, name, size))
	}
	return dirs, rows.Err()
}

// BrowseResponse is our whole share list, compressed once and reused
// until the shares change.
func (x *Index) BrowseResponse() (protocol.Encoded, error) {
	x.mu.Lock()
	if b := x.browse; b != nil {
		x.mu.Unlock()
		return *b, nil
	}
	x.mu.Unlock()
	x.scanMu.Lock() // keep a scan from changing rows mid-read
	dirs, err := x.listDirs("")
	x.scanMu.Unlock()
	if err != nil {
		return protocol.Encoded{}, err
	}
	enc := protocol.EncodePayload(protocol.SharedFileListResponse{Dirs: dirs})
	x.mu.Lock()
	x.browse = &enc
	x.mu.Unlock()
	return enc, nil
}

// FolderContents lists a shared folder and its subfolders.
func (x *Index) FolderContents(folder string) ([]protocol.Directory, error) {
	return x.listDirs(`WHERE folder = ? OR (folder >= ? AND folder < ?)`, folder, folder+`\`, folder+`]`)
}

// Resolve maps a shared file's path, as others see it, to the local file.
// Only indexed files resolve, so requests cannot reach anything else.
func (x *Index) Resolve(virtual string) (local string, size uint64, ok bool) {
	folder, name, found := cutLast(virtual, `\`)
	if !found {
		return "", 0, false
	}
	var root string
	if err := x.db.QueryRow(`SELECT root FROM shared_files WHERE folder = ? AND name = ?`, folder, name).Scan(&root); err != nil {
		return "", 0, false
	}
	x.mu.Lock()
	r, ok := x.roots[root]
	x.mu.Unlock()
	if !ok {
		return "", 0, false
	}
	rel := strings.TrimPrefix(strings.TrimPrefix(virtual, r.Alias), `\`)
	local = filepath.Join(r.Path, filepath.FromSlash(strings.ReplaceAll(rel, `\`, "/")))
	if !within(r.Path, local) {
		return "", 0, false
	}
	st, err := os.Stat(local)
	if err != nil || !st.Mode().IsRegular() {
		return "", 0, false
	}
	return local, uint64(st.Size()), true
}

func cutLast(s, sep string) (before, after string, found bool) {
	i := strings.LastIndex(s, sep)
	if i < 0 {
		return "", s, false
	}
	return s[:i], s[i+len(sep):], true
}
