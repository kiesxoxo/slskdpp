package shares

import (
	"context"
	"encoding/binary"
	"log/slog"
	"os"
	"path"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/kiesxoxo/slskdpp/internal/protocol"
	"github.com/kiesxoxo/slskdpp/internal/store"
)

func write(t *testing.T, path string, size int) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, make([]byte, size), 0o644); err != nil {
		t.Fatal(err)
	}
}

type env struct {
	t       *testing.T
	dir     string
	music   string
	dbPath  string
	x       *Index
	stop    context.CancelFunc
	changes chan struct{}
}

func newEnv(t *testing.T) *env {
	t.Helper()
	dir := t.TempDir()
	e := &env{t: t, dir: dir, music: filepath.Join(dir, "music"), dbPath: filepath.Join(dir, "db"), changes: make(chan struct{}, 100)}
	write(t, filepath.Join(e.music, "Björk", "Homogenic", "01 - Hunter.flac"), 1000)
	write(t, filepath.Join(e.music, "Björk", "Homogenic", "02 - Jóga.flac"), 2000)
	write(t, filepath.Join(e.music, "Björk", "Homogenic", "Scans", "front.jpg"), 300)
	write(t, filepath.Join(e.music, "Portishead", "Dummy", "Sour Times.mp3"), 400)
	write(t, filepath.Join(e.music, ".secret", "key.txt"), 10)
	write(t, filepath.Join(e.music, "Portishead", ".DS_Store"), 10)
	e.open([]Root{ParseRoot("Music=" + e.music)})
	e.waitFiles(4)
	return e
}

func (e *env) open(roots []Root) {
	e.t.Helper()
	if e.stop != nil {
		e.stop()
	}
	db, err := store.Open(e.dbPath)
	if err != nil {
		e.t.Fatal(err)
	}
	e.t.Cleanup(func() { db.Close() })
	x, err := Open(Config{Roots: roots, Logger: slog.New(slog.DiscardHandler), OnChange: func() {
		select {
		case e.changes <- struct{}{}:
		default:
		}
	}}, db)
	if err != nil {
		e.t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	e.t.Cleanup(cancel)
	e.x, e.stop = x, cancel
	go x.Run(ctx)
}

func (e *env) waitFiles(n int) {
	e.t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for e.x.Stats().Files != n {
		if time.Now().After(deadline) {
			e.t.Fatalf("index has %d files, want %d", e.x.Stats().Files, n)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func names(files []protocol.File) []string {
	var out []string
	for _, f := range files {
		out = append(out, f.Name)
	}
	sort.Strings(out)
	return out
}

func TestSearch(t *testing.T) {
	e := newEnv(t)
	for q, want := range map[string][]string{
		"hunter":             {`Music\Björk\Homogenic\01 - Hunter.flac`},
		"BJORK joga":         {`Music\Björk\Homogenic\02 - Jóga.flac`},
		"homogenic -hunter":  {`Music\Björk\Homogenic\02 - Jóga.flac`, `Music\Björk\Homogenic\Scans\front.jpg`},
		"homogenic -scans":   {`Music\Björk\Homogenic\01 - Hunter.flac`, `Music\Björk\Homogenic\02 - Jóga.flac`},
		"sour times":         {`Music\Portishead\Dummy\Sour Times.mp3`},
		"secret":             nil, // hidden folders are not shared
		"-hunter":            nil, // nothing to include
		"hunt":               nil, // whole words only
		`"; DROP TABLE x --`: nil,
	} {
		if got := names(e.x.Search(q)); strings.Join(got, "|") != strings.Join(want, "|") {
			t.Errorf("Search(%q) = %v, want %v", q, got, want)
		}
	}
	if f := e.x.Search("hunter")[0]; f.Size != 1000 || f.Ext != "flac" {
		t.Errorf("result %+v", f)
	}
	e.x.SetExcluded([]string{"Sour Times"})
	if got := e.x.Search("dummy"); len(got) != 0 {
		t.Errorf("excluded phrase returned %v", names(got))
	}
}

func TestBrowseAndFolder(t *testing.T) {
	e := newEnv(t)
	enc, err := e.x.BrowseResponse()
	if err != nil {
		t.Fatal(err)
	}
	m, err := protocol.DecodePeer(protocol.EncodePeer(enc)[4:])
	if err != nil {
		t.Fatal(err)
	}
	dirs := m.(protocol.SharedFileListResponse).Dirs
	var got []string
	for _, d := range dirs {
		for _, f := range d.Files {
			got = append(got, d.Name+" | "+f.Name)
		}
	}
	want := []string{
		`Music\Björk\Homogenic | 01 - Hunter.flac`,
		`Music\Björk\Homogenic | 02 - Jóga.flac`,
		`Music\Björk\Homogenic\Scans | front.jpg`,
		`Music\Portishead\Dummy | Sour Times.mp3`,
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("browse:\n%s", strings.Join(got, "\n"))
	}
	again, _ := e.x.BrowseResponse()
	if &again.Payload[0] != &enc.Payload[0] {
		t.Error("browse response rebuilt without changes")
	}

	fc, _ := e.x.FolderContents(`Music\Björk\Homogenic`)
	if len(fc) != 2 || fc[1].Name != `Music\Björk\Homogenic\Scans` {
		t.Errorf("folder contents %+v", fc)
	}
	if fc, _ := e.x.FolderContents(`Music\Björk\Homo`); len(fc) != 0 {
		t.Errorf("prefix of a folder name matched: %+v", fc)
	}
}

func TestResolve(t *testing.T) {
	e := newEnv(t)
	local, size, ok := e.x.Resolve(`Music\Björk\Homogenic\01 - Hunter.flac`)
	if !ok || size != 1000 || local != filepath.Join(e.music, "Björk", "Homogenic", "01 - Hunter.flac") {
		t.Errorf("Resolve = %s %d %v", local, size, ok)
	}
	for _, bad := range []string{
		`Music\.secret\key.txt`,
		`Music\Portishead\.DS_Store`,
		`Music\..\..\etc\passwd`,
		`Other\Björk\Homogenic\01 - Hunter.flac`,
		`Music`,
	} {
		if _, _, ok := e.x.Resolve(bad); ok {
			t.Errorf("Resolve(%q) succeeded", bad)
		}
	}
}

func TestWatchAndRescan(t *testing.T) {
	e := newEnv(t)
	<-e.changes // initial scan
	write(t, filepath.Join(e.music, "Portishead", "Third", "Machine Gun.flac"), 500)
	e.waitFiles(5)
	if got := e.x.Search("machine gun"); len(got) != 1 {
		t.Errorf("new file not found: %v", got)
	}
	os.RemoveAll(filepath.Join(e.music, "Björk"))
	e.waitFiles(2)
	if st := e.x.Stats(); st.Folders != 2 || st.Bytes != 900 {
		t.Errorf("stats after delete %+v", st)
	}
	if enc, _ := e.x.BrowseResponse(); strings.Contains(string(enc.Payload), "Hunter") {
		t.Error("stale browse response")
	}

	// A rescan shows as scanning straight away, and stops when done.
	e.x.Rescan()
	if !e.x.Stats().Scanning {
		t.Error("not scanning right after Rescan")
	}
	deadline := time.Now().Add(5 * time.Second)
	for e.x.Stats().Scanning {
		if time.Now().After(deadline) {
			t.Fatal("still scanning")
		}
		time.Sleep(10 * time.Millisecond)
	}

	// A share that disappears (an unmounted disk) keeps its index.
	os.Rename(e.music, e.music+".gone")
	e.x.Rescan()
	time.Sleep(300 * time.Millisecond)
	if n := e.x.Stats().Files; n != 2 {
		t.Errorf("missing share wiped the index: %d files", n)
	}
}

func TestRoots(t *testing.T) {
	e := newEnv(t)
	vinyl := filepath.Join(e.dir, "vinyl")
	write(t, filepath.Join(vinyl, "rip.flac"), 70)
	if _, err := e.x.AddRoot(filepath.Join(e.music, "Björk"), ""); err == nil {
		t.Error("folder inside a share accepted")
	}
	if _, err := e.x.AddRoot(filepath.Join(e.dir, "nope"), ""); err == nil {
		t.Error("missing folder accepted")
	}
	r, err := e.x.AddRoot(vinyl, "Music") // alias taken
	if err != nil {
		t.Fatal(err)
	}
	if r.Alias != "Music 2" || r.FromConfig {
		t.Errorf("root %+v", r)
	}
	e.waitFiles(5)
	if got := names(e.x.Search("rip")); len(got) != 1 || got[0] != `Music 2\rip.flac` {
		t.Errorf("search in added root: %v", got)
	}
	if st := e.x.RootStats("Music 2"); st.Files != 1 || st.Bytes != 70 {
		t.Errorf("root stats %+v", st)
	}
	if err := e.x.RemoveRoot("Music"); err == nil {
		t.Error("removed a share from the config file")
	}

	// Roots added in the UI survive a restart; ones dropped from the
	// config lose their files.
	e.open(nil)
	if roots := e.x.Roots(); len(roots) != 1 || roots[0].Alias != "Music 2" {
		t.Fatalf("roots after restart %+v", roots)
	}
	if n := e.x.Stats().Files; n != 1 {
		t.Errorf("%d files after dropping the config share", n)
	}
	if err := e.x.RemoveRoot("Music 2"); err != nil {
		t.Fatal(err)
	}
	if n := e.x.Stats().Files; n != 0 || len(e.x.Roots()) != 0 {
		t.Errorf("after remove: %d files, roots %v", n, e.x.Roots())
	}
}

func TestParseRoot(t *testing.T) {
	if r := ParseRoot("Vinyl=/mnt/v"); r.Alias != "Vinyl" || r.Path != "/mnt/v" {
		t.Errorf("%+v", r)
	}
	if r := ParseRoot("/mnt/a=b"); r.Alias != "" || r.Path != "/mnt/a=b" {
		t.Errorf("path with = parsed as alias: %+v", r)
	}
}

// flacFile is a minimal FLAC: STREAMINFO for 44.1 kHz/16-bit, 30 s.
func flacFile() []byte {
	b := append([]byte("fLaC"), 0x80, 0, 0, 34)
	si := make([]byte, 34)
	binary.BigEndian.PutUint64(si[10:], uint64(44100)<<44|uint64(1)<<41|uint64(15)<<36|44100*30)
	return append(append(b, si...), make([]byte, 3_000_000)...)
}

// mp3File is 10 s of MPEG-1 Layer III frames at 128 kbps.
func mp3File() []byte {
	var b []byte
	for range 383 {
		f := make([]byte, 417)
		copy(f, []byte{0xff, 0xfb, 0x90, 0x00})
		b = append(b, f...)
	}
	return b
}

func TestAudioAttributes(t *testing.T) {
	e := newEnv(t)
	album := filepath.Join(e.music, "Artist", "Lossless Album")
	os.MkdirAll(album, 0o755)
	os.WriteFile(filepath.Join(album, "01 - Track.flac"), flacFile(), 0o644)
	os.WriteFile(filepath.Join(album, "02 - Track.mp3"), mp3File(), 0o644)
	e.waitFiles(6)

	attrs := map[string][]protocol.FileAttribute{}
	for _, f := range e.x.Search("lossless album") {
		attrs[path.Base(strings.ReplaceAll(f.Name, `\`, "/"))] = f.Attrs
	}
	want := map[string][]protocol.FileAttribute{
		"01 - Track.flac": {{Code: protocol.AttrDuration, Value: 30}, {Code: protocol.AttrSampleRate, Value: 44100}, {Code: protocol.AttrBitDepth, Value: 16}},
		"02 - Track.mp3":  {{Code: protocol.AttrBitRate, Value: 128}, {Code: protocol.AttrDuration, Value: 10}, {Code: protocol.AttrVBR, Value: 0}},
	}
	if !reflect.DeepEqual(attrs, want) {
		t.Errorf("search attributes\n got %v\nwant %v", attrs, want)
	}

	// Browse replies carry them too, and the streamed list decodes.
	enc, _ := e.x.BrowseResponse()
	m, err := protocol.DecodePeer(protocol.EncodePeer(enc)[4:])
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, d := range m.(protocol.SharedFileListResponse).Dirs {
		for _, f := range d.Files {
			if f.Name == "01 - Track.flac" {
				found = reflect.DeepEqual(f.Attrs, want["01 - Track.flac"])
			}
		}
	}
	if !found {
		t.Error("browse reply lacks the FLAC's attributes")
	}
}
