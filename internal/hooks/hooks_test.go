package hooks

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestCommandGetsEvent(t *testing.T) {
	out := filepath.Join(t.TempDir(), "env.txt")
	r := New(Config{
		OnDownload: `env | grep ^SLSKDPP_ | sort > "` + out + `"`,
		Logger:     slog.New(slog.DiscardHandler),
	})
	r.Fire(Event{Event: DownloadCompleted, Path: "/dl/Album/01 it's.flac", Folder: "/dl/Album", Username: "bob", Filename: `@@b\Album\01 it's.flac`, Size: 42})
	var got []byte
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		if got, _ = os.ReadFile(out); len(got) > 0 {
			break
		}
	}
	for _, want := range []string{
		"SLSKDPP_EVENT=download.completed",
		"SLSKDPP_PATH=/dl/Album/01 it's.flac",
		"SLSKDPP_REMOTE_PATH=@@b\\Album\\01 it's.flac",
		"SLSKDPP_SIZE=42",
		"SLSKDPP_USERNAME=bob",
	} {
		if !strings.Contains(string(got), want+"\n") {
			t.Errorf("environment lacks %q:\n%s", want, got)
		}
	}
}

func TestHooksRunInOrder(t *testing.T) {
	out := filepath.Join(t.TempDir(), "order.txt")
	r := New(Config{
		// The first hook is slow; the rest must still wait their turn.
		OnDownload: `case "$SLSKDPP_PATH" in 1) sleep 0.3;; esac; echo "$SLSKDPP_PATH" >> "` + out + `"`,
		OnFolder:   `echo folder >> "` + out + `"`,
		Logger:     slog.New(slog.DiscardHandler),
	})
	for _, p := range []string{"1", "2", "3"} {
		r.Fire(Event{Event: DownloadCompleted, Path: p})
	}
	r.Fire(Event{Event: FolderCompleted, Path: "f"})
	var got string
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		b, _ := os.ReadFile(out)
		if got = string(b); strings.Count(got, "\n") == 4 {
			break
		}
	}
	if got != "1\n2\n3\nfolder\n" {
		t.Errorf("hooks ran as %q", got)
	}
}

func TestWebhookRetries(t *testing.T) {
	var calls atomic.Int32
	got := make(chan Event, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if calls.Add(1) < 3 {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		var e Event
		b, _ := io.ReadAll(req.Body)
		json.Unmarshal(b, &e)
		got <- e
	}))
	defer srv.Close()
	r := New(Config{Webhook: srv.URL, Logger: slog.New(slog.DiscardHandler)})
	r.retries = []time.Duration{10 * time.Millisecond, 10 * time.Millisecond, 10 * time.Millisecond}
	r.Fire(Event{Event: FolderCompleted, Path: "/dl/Album", Files: 12, Failed: 1})
	select {
	case e := <-got:
		if e.Event != FolderCompleted || e.Files != 12 || e.Failed != 1 || e.Time.IsZero() {
			t.Errorf("webhook got %+v", e)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("webhook never succeeded")
	}
	if n := calls.Load(); n != 3 {
		t.Errorf("%d calls, want 3", n)
	}
}

func TestTestAll(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()
	r := New(Config{OnDownload: "echo hello $SLSKDPP_EVENT", OnFolder: "echo oops >&2; exit 3", Webhook: srv.URL, CommandTimeout: time.Second})
	res := r.TestAll(context.Background())
	if len(res) != 3 {
		t.Fatalf("results %+v", res)
	}
	if !res[0].OK || res[0].Output != "hello test" {
		t.Errorf("on_download %+v", res[0])
	}
	if res[1].OK || res[1].Output != "oops" || !strings.Contains(res[1].Error, "exit status 3") {
		t.Errorf("on_folder %+v", res[1])
	}
	if res[2].OK || !strings.Contains(res[2].Error, "404") {
		t.Errorf("webhook %+v", res[2])
	}
	slow := New(Config{OnDownload: "sleep 5", CommandTimeout: 100 * time.Millisecond})
	if res := slow.TestAll(context.Background()); res[0].OK || !strings.Contains(res[0].Error, "timed out") {
		t.Errorf("slow command %+v", res)
	}
}
