// Package hooks tells other programs about finished downloads: it runs
// shell commands with the details in SLSKDPP_* environment variables and
// POSTs them as JSON to a webhook.
package hooks

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// Event types.
const (
	DownloadCompleted = "download.completed"
	FolderCompleted   = "folder.completed"
	Test              = "test"
)

// Event is what a hook is told.
type Event struct {
	Event    string    `json:"event"`
	Path     string    `json:"path"`   // local file, or folder for folder events
	Folder   string    `json:"folder"` // local folder
	Username string    `json:"username,omitempty"`
	Filename string    `json:"filename,omitempty"` // remote path
	Size     uint64    `json:"size,omitempty"`
	Files    int       `json:"files,omitempty"`  // folder events: completed files
	Failed   int       `json:"failed,omitempty"` // folder events: failed or cancelled files
	Time     time.Time `json:"time"`
}

type Config struct {
	// OnDownload runs (with sh -c) after each downloaded file.
	OnDownload string
	// OnFolder runs after a folder's downloads have all finished.
	OnFolder string
	// Webhook receives every event as a JSON POST.
	Webhook string
	// CommandTimeout bounds a command; 0 means 10 minutes.
	CommandTimeout time.Duration
	Logger         *slog.Logger
}

// Runner fires hooks in the background, a few at a time.
type Runner struct {
	cfg     Config
	log     *slog.Logger
	sem     chan struct{}
	client  *http.Client
	retries []time.Duration
}

func New(cfg Config) *Runner {
	if cfg.CommandTimeout <= 0 {
		cfg.CommandTimeout = 10 * time.Minute
	}
	log := cfg.Logger
	if log == nil {
		log = slog.Default()
	}
	return &Runner{
		cfg:     cfg,
		log:     log.With("component", "hooks"),
		sem:     make(chan struct{}, 4),
		client:  &http.Client{Timeout: 15 * time.Second},
		retries: []time.Duration{2 * time.Second, 10 * time.Second, 30 * time.Second},
	}
}

// Config returns what is set up, for display.
func (r *Runner) Config() Config { return r.cfg }

func (r *Runner) command(event string) string {
	switch event {
	case DownloadCompleted:
		return r.cfg.OnDownload
	case FolderCompleted:
		return r.cfg.OnFolder
	}
	return ""
}

// Fire runs the hooks for e without waiting for them.
func (r *Runner) Fire(e Event) {
	if e.Time.IsZero() {
		e.Time = time.Now()
	}
	cmd := r.command(e.Event)
	if cmd == "" && r.cfg.Webhook == "" {
		return
	}
	go func() {
		r.sem <- struct{}{}
		defer func() { <-r.sem }()
		if cmd != "" {
			if out, err := r.run(context.Background(), cmd, e); err != nil {
				r.log.Warn("hook command failed", "event", e.Event, "path", e.Path, "err", err, "output", out)
			} else {
				r.log.Info("hook command ran", "event", e.Event, "path", e.Path)
			}
		}
		if r.cfg.Webhook != "" {
			if err := r.post(context.Background(), e, r.retries); err != nil {
				r.log.Warn("webhook failed", "event", e.Event, "err", err)
			}
		}
	}()
}

// Result is the outcome of one hook in a test.
type Result struct {
	Hook   string `json:"hook"`
	OK     bool   `json:"ok"`
	Error  string `json:"error,omitempty"`
	Output string `json:"output,omitempty"`
}

// TestAll runs every configured hook once with a test event and reports
// how each went.
func (r *Runner) TestAll(ctx context.Context) []Result {
	e := Event{Event: Test, Path: os.TempDir(), Folder: os.TempDir(), Username: "slskdpp", Filename: `test\file.flac`, Time: time.Now()}
	var out []Result
	for _, h := range []struct{ name, cmd string }{{"on_download", r.cfg.OnDownload}, {"on_folder", r.cfg.OnFolder}} {
		if h.cmd == "" {
			continue
		}
		output, err := r.run(ctx, h.cmd, e)
		out = append(out, result(h.name, output, err))
	}
	if r.cfg.Webhook != "" {
		out = append(out, result("webhook", "", r.post(ctx, e, nil)))
	}
	return out
}

func result(name, output string, err error) Result {
	res := Result{Hook: name, OK: err == nil, Output: output}
	if err != nil {
		res.Error = err.Error()
	}
	return res
}

const maxOutput = 4 << 10

// run executes cmd with the event in its environment and returns the tail
// of its output.
func (r *Runner) run(ctx context.Context, cmd string, e Event) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, r.cfg.CommandTimeout)
	defer cancel()
	c := exec.CommandContext(ctx, "/bin/sh", "-c", cmd)
	c.Env = append(os.Environ(),
		"SLSKDPP_EVENT="+e.Event,
		"SLSKDPP_PATH="+e.Path,
		"SLSKDPP_FOLDER="+e.Folder,
		"SLSKDPP_USERNAME="+e.Username,
		"SLSKDPP_REMOTE_PATH="+e.Filename,
		"SLSKDPP_SIZE="+strconv.FormatUint(e.Size, 10),
		"SLSKDPP_FILES="+strconv.Itoa(e.Files),
		"SLSKDPP_FAILED="+strconv.Itoa(e.Failed),
	)
	ownGroup(c)
	c.WaitDelay = time.Second // stop waiting for output after a kill
	var buf bytes.Buffer
	c.Stdout, c.Stderr = &buf, &buf
	err := c.Run()
	out := buf.String()
	if len(out) > maxOutput {
		out = "…" + out[len(out)-maxOutput:]
	}
	if ctx.Err() == context.DeadlineExceeded {
		err = fmt.Errorf("timed out after %s", r.cfg.CommandTimeout)
	}
	return strings.TrimSpace(out), err
}

// post sends e to the webhook, waiting out each delay in retries before
// trying again.
func (r *Runner) post(ctx context.Context, e Event, retries []time.Duration) error {
	body, _ := json.Marshal(e)
	var err error
	for attempt := 0; ; attempt++ {
		err = r.postOnce(ctx, body)
		if err == nil || attempt >= len(retries) {
			return err
		}
		select {
		case <-ctx.Done():
			return errors.Join(err, ctx.Err())
		case <-time.After(retries[attempt]):
		}
	}
}

func (r *Runner) postOnce(ctx context.Context, body []byte) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, r.cfg.Webhook, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "slskdpp")
	resp, err := r.client.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("webhook answered %s", resp.Status)
	}
	return nil
}
