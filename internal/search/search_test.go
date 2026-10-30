package search

import (
	"errors"
	"testing"
	"time"

	"github.com/kiesxoxo/slskdpp/internal/protocol"
)

type fakeNet struct {
	token   uint32
	queries []string
	err     error
}

func (n *fakeNet) NewToken() uint32 { n.token++; return n.token }

func (n *fakeNet) Search(token uint32, q string) error {
	n.queries = append(n.queries, q)
	return n.err
}

func pf(name string, size uint64, attrs ...protocol.FileAttribute) protocol.File {
	return protocol.File{Name: name, Size: size, Attrs: attrs}
}

func album(prefix string, n int, ext string, size uint64) []protocol.File {
	var files []protocol.File
	for i := range n {
		files = append(files, pf(prefix+`\0`+string(rune('1'+i))+" - Track."+ext, size,
			protocol.FileAttribute{Code: protocol.AttrBitRate, Value: 320}))
	}
	return files
}

func newTestManager(opts Options) (*Manager, *fakeNet, *time.Time) {
	net := &fakeNet{}
	m := NewManager(net, opts)
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	m.now = func() time.Time { return now }
	return m, net, &now
}

func TestAggregatesByUserAndFolder(t *testing.T) {
	var streamed []FolderHit
	m, net, _ := newTestManager(Options{OnHit: func(_ *Search, h FolderHit) { streamed = append(streamed, h) }})
	s, err := m.Start("  boards   of canada ")
	if err != nil {
		t.Fatal(err)
	}
	if net.queries[0] != "boards of canada" {
		t.Errorf("query sent as %q", net.queries[0])
	}
	files := append(album(`@@a\Music\Boards of Canada\Geogaddi`, 3, "mp3", 10_000_000),
		pf(`@@a\Music\Boards of Canada\Geogaddi\cover.jpg`, 100_000),
		pf(`@@a\Singles\Roygbiv.flac`, 30_000_000, protocol.FileAttribute{Code: protocol.AttrDuration, Value: 150}))
	m.HandleResponse("alice", protocol.FileSearchResponse{Username: "spoofed", Token: s.ID, Files: files, SlotFree: true, AvgSpeed: 500_000, QueueLength: 1})
	// A second response from the same user is ignored.
	m.HandleResponse("alice", protocol.FileSearchResponse{Token: s.ID, Files: files})
	// Responses for unknown tokens are dropped.
	m.HandleResponse("bob", protocol.FileSearchResponse{Token: 999, Files: files})

	if len(streamed) != 2 {
		t.Fatalf("streamed %d hits, want 2", len(streamed))
	}
	st := s.Stats()
	if st.Responses != 1 || st.Files != 5 || st.Folders != 2 || !st.Active {
		t.Errorf("stats %+v", st)
	}
	hits := s.Hits(Filter{})
	g := hits[0]
	if g.User != "alice" || g.Name != "Geogaddi" || g.FileCount != 4 {
		t.Fatalf("best hit %+v", g)
	}
	if g.Formats[0] != "mp3" || g.Formats[1] != "jpg" || g.AvgBitRate != 320 || g.Lossless {
		t.Errorf("formats %v bitrate %d lossless %v", g.Formats, g.AvgBitRate, g.Lossless)
	}
	if g.TotalSize != 30_100_000 || !g.SlotFree || g.QueueLength != 1 || g.UploadSpeed != 500_000 {
		t.Errorf("summary %+v", g)
	}
	single := hits[1]
	if !single.Lossless || single.AvgBitRate != 1600 {
		t.Errorf("derived bitrate %d lossless %v, want 1600 true", single.AvgBitRate, single.Lossless)
	}
}

func TestFilters(t *testing.T) {
	m, _, _ := newTestManager(Options{})
	s, _ := m.Start("x")
	m.HandleResponse("mp3user", protocol.FileSearchResponse{Token: s.ID, Files: album(`@@m\A`, 4, "mp3", 1e7), SlotFree: true})
	m.HandleResponse("flacuser", protocol.FileSearchResponse{Token: s.ID, Files: []protocol.File{
		pf(`@@f\B\1.flac`, 3e7), pf(`@@f\B\2.flac`, 3e7),
	}})
	m.HandleResponse("lowbr", protocol.FileSearchResponse{Token: s.ID, Files: []protocol.File{
		pf(`@@l\C\1.mp3`, 3e6, protocol.FileAttribute{Code: protocol.AttrBitRate, Value: 128}),
	}, SlotFree: true})

	users := func(f Filter) map[string]bool {
		out := map[string]bool{}
		for _, h := range s.Hits(f) {
			out[h.User] = true
		}
		return out
	}
	for name, tc := range map[string]struct {
		f    Filter
		want []string
	}{
		"none":       {Filter{}, []string{"mp3user", "flacuser", "lowbr"}},
		"format":     {Filter{Formats: []string{".FLAC"}}, []string{"flacuser"}},
		"bitrate":    {Filter{MinBitRate: 256}, []string{"mp3user", "flacuser"}},
		"free slot":  {Filter{FreeSlotOnly: true}, []string{"mp3user", "lowbr"}},
		"min files":  {Filter{MinFiles: 3}, []string{"mp3user"}},
		"exclude":    {Filter{ExcludeUsers: []string{"mp3user"}}, []string{"flacuser", "lowbr"}},
		"combined":   {Filter{FreeSlotOnly: true, MinBitRate: 256}, []string{"mp3user"}},
		"no matches": {Filter{Formats: []string{"ogg"}}, nil},
	} {
		got := users(tc.f)
		if len(got) != len(tc.want) {
			t.Errorf("%s: got %v, want %v", name, got, tc.want)
			continue
		}
		for _, u := range tc.want {
			if !got[u] {
				t.Errorf("%s: missing %s in %v", name, u, got)
			}
		}
	}
}

func TestRanking(t *testing.T) {
	m, _, _ := newTestManager(Options{})
	s, _ := m.Start("aphex twin drukqs")
	files := func(folder string) []protocol.File { return album(folder, 5, "mp3", 1e7) }
	m.HandleResponse("queued", protocol.FileSearchResponse{Token: s.ID, Files: files(`@@q\Aphex Twin\Drukqs`), AvgSpeed: 5e6, QueueLength: 40})
	m.HandleResponse("free", protocol.FileSearchResponse{Token: s.ID, Files: files(`@@f\Aphex Twin\Drukqs`), SlotFree: true, AvgSpeed: 1e5})
	m.HandleResponse("free-offtopic", protocol.FileSearchResponse{Token: s.ID, Files: files(`@@o\misc\stuff`), SlotFree: true, AvgSpeed: 1e5})
	m.HandleResponse("free-fast", protocol.FileSearchResponse{Token: s.ID, Files: files(`@@ff\Aphex Twin\Drukqs`), SlotFree: true, AvgSpeed: 5e6})

	var order []string
	for _, h := range s.Hits(Filter{}) {
		order = append(order, h.User)
	}
	want := []string{"free-fast", "free", "free-offtopic", "queued"}
	for i := range want {
		if order[i] != want[i] {
			t.Fatalf("order %v, want %v", order, want)
		}
	}
}

func TestAlbumDedupe(t *testing.T) {
	m, _, _ := newTestManager(Options{})
	s, _ := m.Start("dummy")
	resp := func(user, folder string, n int, size uint64, free bool) {
		m.HandleResponse(user, protocol.FileSearchResponse{Token: s.ID, Files: album(folder, n, "flac", size), SlotFree: free})
	}
	resp("a", `@@a\Portishead\Dummy`, 4, 30_000_000, true)
	resp("b", `@@b\music\portishead\DUMMY`, 4, 30_300_000, false) // 1% bigger: same release
	resp("c", `@@c\Portishead\Dummy`, 4, 40_000_000, false)       // much bigger: different rip
	resp("d", `@@d\Portishead\Dummy`, 3, 30_000_000, false)       // fewer files
	resp("e", `@@e\Portishead - Dummy\CD1`, 4, 30_000_000, false)
	resp("f", `@@f\Other Album\CD1`, 4, 30_000_000, false) // generic name, other parent

	albums := s.Albums(Filter{})
	if len(albums) != 5 {
		for _, a := range albums {
			t.Logf("%s %d files %d bytes: %d sources", a.Name, a.FileCount, a.TotalSize, len(a.Sources))
		}
		t.Fatalf("got %d albums, want 5", len(albums))
	}
	first := albums[0]
	if len(first.Sources) != 2 || first.Sources[0].User != "a" || first.Sources[1].User != "b" {
		t.Errorf("first album sources %+v", first.Sources)
	}
	if albumName(`@@e\Portishead - Dummy\CD1`) == albumName(`@@f\Other Album\CD1`) {
		t.Error("disc folders with different parents share a key")
	}
}

func TestWindowTTLAndCaps(t *testing.T) {
	m, _, now := newTestManager(Options{Window: time.Minute, TTL: 10 * time.Minute, MaxSearches: 2, MaxResponses: 1, MaxFiles: 2})
	s, _ := m.Start("one")
	m.HandleResponse("a", protocol.FileSearchResponse{Token: s.ID, Files: album(`@@a\X`, 5, "mp3", 1)})
	m.HandleResponse("b", protocol.FileSearchResponse{Token: s.ID, Files: album(`@@b\X`, 5, "mp3", 1)})
	if st := s.Stats(); st.Responses != 1 || st.Files != 2 {
		t.Errorf("caps not applied: %+v", st)
	}

	*now = now.Add(2 * time.Minute)
	s2, _ := m.Start("two")
	m.HandleResponse("c", protocol.FileSearchResponse{Token: s2.ID, Files: album(`@@c\X`, 1, "mp3", 1)})
	if s.Stats().Active || s2.Stats().Files != 1 {
		t.Error("window not applied")
	}
	s2.Stop()
	m.HandleResponse("d", protocol.FileSearchResponse{Token: s2.ID, Files: album(`@@d\X`, 1, "mp3", 1)})
	if s2.Stats().Responses != 1 {
		t.Error("stopped search accepted a response")
	}

	s3, _ := m.Start("three") // evicts "one", the oldest
	if _, ok := m.Get(s.ID); ok {
		t.Error("oldest search not evicted")
	}
	if l := m.List(); len(l) != 2 || l[0] != s3 {
		t.Errorf("List = %v", l)
	}

	*now = now.Add(11 * time.Minute)
	if len(m.List()) != 0 {
		t.Error("searches not expired after TTL")
	}
}

func TestStartErrors(t *testing.T) {
	m, net, _ := newTestManager(Options{})
	if _, err := m.Start("   "); err == nil {
		t.Error("empty query accepted")
	}
	net.err = errors.New("offline")
	if _, err := m.Start("x"); err == nil {
		t.Error("send failure not reported")
	}
	if len(m.List()) != 0 {
		t.Error("failed search kept")
	}
}

func TestOnDone(t *testing.T) {
	done := make(chan uint32, 4)
	m, _, _ := newTestManager(Options{Window: 50 * time.Millisecond, OnDone: func(s *Search) { done <- s.ID }})
	s1, _ := m.Start("window")
	s2, _ := m.Start("removed")
	m.Remove(s2.ID)
	got := map[uint32]bool{}
	for range 2 {
		select {
		case id := <-done:
			got[id] = true
		case <-time.After(5 * time.Second):
			t.Fatal("OnDone not called")
		}
	}
	if !got[s1.ID] || !got[s2.ID] {
		t.Errorf("OnDone for %v", got)
	}
	s1.Stop()
	select {
	case id := <-done:
		t.Errorf("OnDone called twice for %d", id)
	case <-time.After(100 * time.Millisecond):
	}
}

func TestBackgroundSearchesAreNotListed(t *testing.T) {
	m, net, _ := newTestManager(Options{})
	fg, _ := m.Start("visible")
	bg, err := m.StartBackground("hidden")
	if err != nil || len(net.queries) != 2 {
		t.Fatalf("background search not sent: %v %v", err, net.queries)
	}
	if l := m.List(); len(l) != 1 || l[0] != fg {
		t.Errorf("List = %v", l)
	}
	if got, ok := m.Get(bg.ID); !ok || got != bg {
		t.Error("background search not found by ID")
	}
}
