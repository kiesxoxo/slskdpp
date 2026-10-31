package logs

import (
	"bytes"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestHandler(t *testing.T) {
	dir := t.TempDir()
	var stderr bytes.Buffer
	h, err := New(Config{Level: slog.LevelInfo, Dir: dir, KeepDays: 3, Stderr: &stderr})
	if err != nil {
		t.Fatal(err)
	}
	log := slog.New(h)
	log.Debug("hidden")
	log.With("component", "server").Info("logged in", "user", "me")
	log.WithGroup("g").Warn("careful", "n", 2)

	recent := h.Recent()
	if len(recent) != 2 {
		t.Fatalf("recent %+v", recent)
	}
	if r := recent[0]; r.Context != "server" || r.Level != slog.LevelInfo || r.Message != "logged in user=me" || r.Time.IsZero() {
		t.Errorf("first %+v", r)
	}
	if r := recent[1]; r.Context != "slskdpp" || r.Message != "careful g.n=2" {
		t.Errorf("second %+v", r)
	}
	if !strings.Contains(stderr.String(), "logged in") || strings.Contains(stderr.String(), "hidden") {
		t.Errorf("stderr %q", stderr.String())
	}
	file := filepath.Join(dir, "slskdpp-"+time.Now().Format("20060102")+".log")
	if b, _ := os.ReadFile(file); !strings.Contains(string(b), "component=server") || !strings.Contains(string(b), "careful") {
		t.Errorf("file %q", b)
	}
	if h.Dir() != dir {
		t.Errorf("dir %q", h.Dir())
	}

	// Only the newest Kept records stay, oldest first.
	for i := range Kept + 5 {
		log.Info("n", "i", i)
	}
	recent = h.Recent()
	if len(recent) != Kept || recent[0].Message != "n i=5" || recent[Kept-1].Message != "n i=104" {
		t.Errorf("ring: %d, first %q, last %q", len(recent), recent[0].Message, recent[Kept-1].Message)
	}
}

func TestPrune(t *testing.T) {
	dir := t.TempDir()
	today := time.Now()
	for _, days := range []int{0, 1, 2, 3, 10} {
		os.WriteFile(filepath.Join(dir, "slskdpp-"+today.AddDate(0, 0, -days).Format("20060102")+".log"), nil, 0o644)
	}
	os.WriteFile(filepath.Join(dir, "other.log"), nil, 0o644)
	d := &dailyFile{dir: dir, keep: 3}
	d.prune(today.Format("20060102"))
	names, _ := filepath.Glob(filepath.Join(dir, "*.log"))
	if len(names) != 4 { // today, 1 and 2 days ago, and other.log
		t.Errorf("left %v", names)
	}
}

func TestNoDir(t *testing.T) {
	h, err := New(Config{Stderr: &bytes.Buffer{}})
	if err != nil || h.Dir() != "" {
		t.Fatalf("%v %q", err, h.Dir())
	}
	slog.New(h).Info("x")
	if len(h.Recent()) != 1 {
		t.Error("record not kept")
	}
}
