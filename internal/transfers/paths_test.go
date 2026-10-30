package transfers

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDestFor(t *testing.T) {
	for _, tc := range []struct{ remote, folder, want string }{
		{`@@a\Music\Artist\Album\01 - x.flac`, ``, `Album/01 - x.flac`},
		{`@@a\Music\Artist\Album\CD1\01.flac`, `@@a\Music\Artist\Album`, `Album/CD1/01.flac`},
		{`@@a\Music\Artist\Album\01.flac`, `@@a\Other`, `Album/01.flac`},
		{`song.mp3`, ``, `song.mp3`},
		{`@@a\..\..\etc\passwd`, ``, `etc/passwd`},
		{`@@a\x\..\..`, ``, `_/_`},
		{`@@a\x\..`, `@@a`, `@@a/x/_`},
		{`@@a\bad:name?\what*.mp3`, ``, `bad_name_/what_.mp3`},
		{`@@a/forward/slash.mp3`, ``, `forward/slash.mp3`},
		{`@@a\dots...\file. `, ``, `dots/file`},
	} {
		if got := destFor(tc.remote, tc.folder); got != tc.want {
			t.Errorf("destFor(%q, %q) = %q, want %q", tc.remote, tc.folder, got, tc.want)
		}
	}
	long := strings.Repeat("é", 150) + ".flac"
	if got := cleanName(long); len(got) > maxNameBytes || !strings.HasSuffix(got, ".flac") {
		t.Errorf("long name not shortened safely: %d bytes %q", len(got), got[len(got)-10:])
	}
}

func TestUniquePath(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "a.mp3")
	if uniquePath(p) != p {
		t.Error("free path changed")
	}
	os.WriteFile(p, nil, 0o644)
	os.WriteFile(filepath.Join(dir, "a (1).mp3"), nil, 0o644)
	if got := uniquePath(p); got != filepath.Join(dir, "a (2).mp3") {
		t.Errorf("uniquePath = %s", got)
	}
}

func TestAltQuery(t *testing.T) {
	if q := altQuery(`@@x\Portishead\Dummy\03 - Strangers (Remastered).flac`); q != "03 strangers remastered" {
		t.Errorf("altQuery = %q", q)
	}
	if q := altQuery(`@@x\a.mp3`); q != "" {
		t.Errorf("altQuery of a short name = %q", q)
	}
}

func TestBucket(t *testing.T) {
	if n, _ := (*bucket)(nil).take(context.Background(), 100); n != 100 {
		t.Errorf("nil bucket limited to %d", n)
	}
	b := newBucket(64 << 10) // burst 16 KiB
	start := time.Now()
	total := 0
	for total < 48<<10 {
		n, err := b.take(context.Background(), 8<<10)
		if err != nil {
			t.Fatal(err)
		}
		total += n
	}
	// 16 KiB burst plus 32 KiB at 64 KiB/s takes about half a second.
	if d := time.Since(start); d < 350*time.Millisecond || d > 2*time.Second {
		t.Errorf("48 KiB at 64 KiB/s took %v", d)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	b.tokens, b.last = 0, time.Now()
	if _, err := b.take(ctx, 1<<20); err == nil {
		t.Error("take ignored cancelled context")
	}
}
