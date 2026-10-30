package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/kiesxoxo/slskdpp/internal/client"
	"github.com/kiesxoxo/slskdpp/internal/protocol"
	"github.com/kiesxoxo/slskdpp/internal/search"
)

// Debug subcommands: each logs in, does one thing and exits.
var commands = map[string]func(ctx context.Context, cfg client.ServerConfig, args []string) error{
	"search": cmdSearch,
	"browse": cmdBrowse,
	"folder": cmdFolder,
}

// start runs c until ctx is done and waits for it to log in. Build the
// client and everything its handlers use before calling it.
func start(ctx context.Context, c *client.Client) error {
	runErr := make(chan error, 1)
	go func() { runErr <- c.Run(ctx) }()
	loginCtx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	ready := make(chan error, 1)
	go func() { ready <- c.Server().WaitLoggedIn(loginCtx) }()
	select {
	case err := <-runErr:
		return err
	case err := <-ready:
		if err != nil {
			return fmt.Errorf("waiting for login: %w", err)
		}
		return nil
	}
}

// newDebugClient returns a client that offers each peer message to onPeer
// first and answers basic requests itself when onPeer returns false.
func newDebugClient(cfg client.ServerConfig, onPeer func(user string, m any) bool) *client.Client {
	var c *client.Client
	c = client.New(cfg, client.Handlers{Peer: func(user string, m any) {
		if onPeer == nil || !onPeer(user, m) {
			newResponder(c, nil, nil).peer(user, m)
		}
	}})
	return c
}

func cmdSearch(ctx context.Context, cfg client.ServerConfig, args []string) error {
	fs := flag.NewFlagSet("search", flag.ExitOnError)
	wait := fs.Duration("wait", 20*time.Second, "how long to collect results")
	limit := fs.Int("n", 15, "number of albums to print")
	formats := fs.String("format", "", "comma-separated formats to keep, e.g. flac,mp3")
	minBitRate := fs.Uint("min-bitrate", 0, "minimum average bitrate in kbps (lossless always passes)")
	freeOnly := fs.Bool("free", false, "only peers with a free upload slot")
	minFiles := fs.Int("min-files", 0, "minimum files in a folder")
	quiet := fs.Bool("q", false, "do not print folder hits as they arrive")
	fs.Parse(args)
	query := strings.Join(fs.Args(), " ")
	if query == "" {
		return errors.New("search: missing query")
	}
	filter := search.Filter{MinBitRate: uint32(*minBitRate), FreeSlotOnly: *freeOnly, MinFiles: *minFiles}
	if *formats != "" {
		filter.Formats = strings.Split(*formats, ",")
	}

	var mgr *search.Manager
	c := newDebugClient(cfg, func(user string, m any) bool {
		r, ok := m.(protocol.FileSearchResponse)
		if ok {
			mgr.HandleResponse(user, r)
		}
		return ok
	})
	mgr = search.NewManager(c, search.Options{
		Window: *wait,
		OnHit: func(_ *search.Search, h search.FolderHit) {
			if !*quiet {
				fmt.Fprintf(os.Stderr, "+ %-20s %3d files  %-10s %s\n", h.User, h.FileCount, strings.Join(h.Formats, ","), h.Folder)
			}
		},
	})
	if err := start(ctx, c); err != nil {
		return err
	}
	s, err := mgr.Start(query)
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "searching for %q for %s...\n", query, *wait)
	select {
	case <-ctx.Done():
	case <-time.After(*wait):
	}

	st := s.Stats()
	albums := s.Albums(filter)
	fmt.Printf("\n%d responses, %d files in %d folders; %d albums after filters\n\n", st.Responses, st.Files, st.Folders, len(albums))
	for i, a := range albums[:min(*limit, len(albums))] {
		best := a.Sources[0]
		fmt.Printf("%2d. %s  [score %.2f]\n", i+1, a.Name, best.Score)
		fmt.Printf("    %d files, %s, %s, %s, %d source(s)\n",
			a.FileCount, strings.Join(best.Formats, "/"), bitrate(best), humanSize(a.TotalSize), len(a.Sources))
		for _, src := range a.Sources[:min(3, len(a.Sources))] {
			slot := "queue " + fmt.Sprint(src.QueueLength)
			if src.SlotFree {
				slot = "free slot"
			}
			fmt.Printf("    - %s (%s, %s/s): %s\n", src.User, slot, humanSize(uint64(src.UploadSpeed)), src.Folder)
		}
	}
	return nil
}

func cmdBrowse(ctx context.Context, cfg client.ServerConfig, args []string) error {
	fs := flag.NewFlagSet("browse", flag.ExitOnError)
	files := fs.Bool("files", false, "list files, not just folders")
	match := fs.String("match", "", "only folders whose path contains this text (case-insensitive)")
	fs.Parse(args)
	if fs.NArg() != 1 {
		return errors.New("browse: want exactly one user")
	}
	user := fs.Arg(0)
	c := newDebugClient(cfg, nil)
	if err := start(ctx, c); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "browsing %s...\n", user)
	list, err := c.Browse(ctx, user)
	if err != nil {
		return err
	}
	var nfiles int
	var size uint64
	needle := strings.ToLower(*match)
	for _, d := range list.Dirs {
		nfiles += len(d.Files)
		for _, f := range d.Files {
			size += f.Size
		}
		if !strings.Contains(strings.ToLower(d.Name), needle) {
			continue
		}
		printDir(d, *files)
	}
	fmt.Printf("\n%s shares %d folders, %d files, %s", user, len(list.Dirs), nfiles, humanSize(size))
	if n := len(list.PrivateDirs); n > 0 {
		fmt.Printf(" (+%d private folders)", n)
	}
	fmt.Println()
	return nil
}

func cmdFolder(ctx context.Context, cfg client.ServerConfig, args []string) error {
	if len(args) != 2 {
		return errors.New(`folder: want <user> <folder>, e.g. folder bob '@@abcd\Music\Album'`)
	}
	c := newDebugClient(cfg, nil)
	if err := start(ctx, c); err != nil {
		return err
	}
	resp, err := c.FolderContents(ctx, args[0], args[1])
	if err != nil {
		return err
	}
	for _, d := range resp.Dirs {
		printDir(d, true)
	}
	return nil
}

func printDir(d protocol.Directory, files bool) {
	fmt.Printf("%s  (%d files)\n", d.Name, len(d.Files))
	if !files {
		return
	}
	for _, f := range d.Files {
		name := f.Name
		if i := strings.LastIndexAny(name, `\/`); i >= 0 {
			name = name[i+1:]
		}
		fmt.Printf("    %-60s %10s", name, humanSize(f.Size))
		if br, ok := f.Attr(protocol.AttrBitRate); ok {
			fmt.Printf("  %d kbps", br)
		}
		if d, ok := f.Attr(protocol.AttrDuration); ok {
			fmt.Printf("  %d:%02d", d/60, d%60)
		}
		fmt.Println()
	}
}

func bitrate(h search.FolderHit) string {
	switch {
	case h.Lossless:
		return "lossless"
	case h.AvgBitRate > 0:
		return fmt.Sprintf("%d kbps", h.AvgBitRate)
	}
	return "? kbps"
}

func humanSize(n uint64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := uint64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}
