// Package logs is the daemon's slog handler: text to stderr, optionally
// to daily log files, and the most recent records kept in memory for the
// API.
package logs

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
)

// Kept is how many recent records Recent returns.
const Kept = 100

// Record is one log entry as the API shows it.
type Record struct {
	Time    time.Time
	Context string // the "component" attribute, or "slskdpp"
	Level   slog.Level
	Message string // the message and its attributes, as key=value
}

type Config struct {
	Level slog.Leveler
	// Dir, if set, receives daily files named slskdpp-YYYYMMDD.log.
	Dir string
	// KeepDays is how many days of files to keep; 0 keeps them all.
	KeepDays int
	// Stderr is where text goes besides the files; os.Stderr if nil.
	Stderr io.Writer
}

// core is shared by a Handler and everything derived from it.
type core struct {
	mu     sync.Mutex
	recent []Record // ring of up to Kept records
	next   int
	files  *dailyFile // nil without a Dir
}

// Handler is a slog.Handler that also keeps recent records.
type Handler struct {
	c       *core
	text    slog.Handler
	attrs   []slog.Attr // for Record.Message and Context
	groups  []string
	context string
}

// New returns the handler. If Dir cannot be created, it logs to stderr
// only and returns the error alongside it.
func New(cfg Config) (*Handler, error) {
	out := cfg.Stderr
	if out == nil {
		out = os.Stderr
	}
	if cfg.Level == nil {
		cfg.Level = slog.LevelInfo
	}
	c := &core{}
	var err error
	if cfg.Dir != "" {
		if err = os.MkdirAll(cfg.Dir, 0o755); err == nil {
			c.files = &dailyFile{dir: cfg.Dir, keep: cfg.KeepDays}
			out = io.MultiWriter(out, lockedWriter{c})
		}
	}
	return &Handler{
		c:       c,
		text:    slog.NewTextHandler(out, &slog.HandlerOptions{Level: cfg.Level}),
		context: "slskdpp",
	}, err
}

func (h *Handler) Enabled(ctx context.Context, l slog.Level) bool { return h.text.Enabled(ctx, l) }

func (h *Handler) Handle(ctx context.Context, r slog.Record) error {
	err := h.text.Handle(ctx, r)
	var b strings.Builder
	b.WriteString(r.Message)
	prefix := ""
	if len(h.groups) > 0 {
		prefix = strings.Join(h.groups, ".") + "."
	}
	write := func(a slog.Attr) {
		if a.Equal(slog.Attr{}) {
			return
		}
		fmt.Fprintf(&b, " %s%s=%v", prefix, a.Key, a.Value)
	}
	for _, a := range h.attrs {
		if a.Key != "component" {
			write(a)
		}
	}
	r.Attrs(func(a slog.Attr) bool { write(a); return true })
	h.c.add(Record{Time: r.Time, Context: h.context, Level: r.Level, Message: b.String()})
	return err
}

func (h *Handler) WithAttrs(attrs []slog.Attr) slog.Handler {
	n := *h
	n.text = h.text.WithAttrs(attrs)
	n.attrs = append(slices.Clip(h.attrs), attrs...)
	for _, a := range attrs {
		if a.Key == "component" && len(h.groups) == 0 {
			n.context = a.Value.String()
		}
	}
	return &n
}

func (h *Handler) WithGroup(name string) slog.Handler {
	if name == "" {
		return h
	}
	n := *h
	n.text = h.text.WithGroup(name)
	n.groups = append(slices.Clip(h.groups), name)
	return &n
}

// Recent returns up to Kept recent records, oldest first.
func (h *Handler) Recent() []Record {
	h.c.mu.Lock()
	defer h.c.mu.Unlock()
	if len(h.c.recent) < Kept {
		return slices.Clone(h.c.recent)
	}
	return append(slices.Clone(h.c.recent[h.c.next:]), h.c.recent[:h.c.next]...)
}

// Dir is where log files go, or "" without files.
func (h *Handler) Dir() string {
	if h.c.files == nil {
		return ""
	}
	return h.c.files.dir
}

func (c *core) add(r Record) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.recent) < Kept {
		c.recent = append(c.recent, r)
		return
	}
	c.recent[c.next] = r
	c.next = (c.next + 1) % Kept
}

type lockedWriter struct{ c *core }

func (w lockedWriter) Write(p []byte) (int, error) {
	w.c.mu.Lock()
	defer w.c.mu.Unlock()
	return w.c.files.Write(p)
}

// dailyFile writes to a file per local day, removing old ones as days
// change. Callers serialize writes.
type dailyFile struct {
	dir  string
	keep int
	day  string
	f    *os.File
}

func (d *dailyFile) Write(p []byte) (int, error) {
	day := time.Now().Format("20060102")
	if d.f == nil || day != d.day {
		if d.f != nil {
			d.f.Close()
		}
		f, err := os.OpenFile(filepath.Join(d.dir, "slskdpp-"+day+".log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
		if err != nil {
			d.f = nil
			return len(p), nil // stderr still has it; do not fail the log call
		}
		d.f, d.day = f, day
		d.prune(day)
	}
	return d.f.Write(p)
}

// prune removes files older than keep days.
func (d *dailyFile) prune(today string) {
	if d.keep <= 0 {
		return
	}
	t, _ := time.ParseInLocation("20060102", today, time.Local)
	oldest := t.AddDate(0, 0, -(d.keep - 1)).Format("20060102")
	names, _ := filepath.Glob(filepath.Join(d.dir, "slskdpp-*.log"))
	for _, n := range names {
		day := strings.TrimSuffix(strings.TrimPrefix(filepath.Base(n), "slskdpp-"), ".log")
		if len(day) == 8 && day < oldest {
			os.Remove(n)
		}
	}
}
