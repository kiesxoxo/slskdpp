package search

import (
	"cmp"
	"fmt"
	"math"
	"path"
	"regexp"
	"slices"
	"strings"
	"unicode"

	"github.com/kiesxoxo/slskdpp/internal/protocol"
)

// File is one file in a search result.
type File struct {
	Path       string `json:"path"` // full remote path, as needed to download it
	Name       string `json:"name"`
	Size       uint64 `json:"size"`
	Ext        string `json:"ext"` // lowercase, without the dot
	BitRate    uint32 `json:"bitrate,omitempty"`
	Duration   uint32 `json:"duration,omitempty"`
	SampleRate uint32 `json:"sample_rate,omitempty"`
	BitDepth   uint32 `json:"bit_depth,omitempty"`
	VBR        bool   `json:"vbr,omitempty"`
}

// FolderHit is every file one user returned from one folder.
type FolderHit struct {
	User        string   `json:"user"`
	Folder      string   `json:"folder"`
	Name        string   `json:"name"` // last folder component
	Files       []File   `json:"files"`
	FileCount   int      `json:"file_count"`
	Formats     []string `json:"formats"` // most common first
	AvgBitRate  uint32   `json:"avg_bitrate,omitempty"`
	Lossless    bool     `json:"lossless"`
	TotalSize   uint64   `json:"total_size"`
	SlotFree    bool     `json:"slot_free"`
	QueueLength uint32   `json:"queue_length"`
	UploadSpeed uint32   `json:"upload_speed"` // bytes per second
	Score       float64  `json:"score"`
	// AlbumKey is shared by hits that may be the same release; Albums
	// further splits a key's hits by total size.
	AlbumKey string `json:"album_key"`
}

// Album is folder hits from different users that look like the same
// release: same normalized folder name, same file count and nearly the same
// total size.
type Album struct {
	Name      string      `json:"name"`
	FileCount int         `json:"file_count"`
	TotalSize uint64      `json:"total_size"`
	Sources   []FolderHit `json:"sources"` // best first
}

// Filter narrows folder hits. The zero value keeps everything.
type Filter struct {
	Formats      []string `json:"formats,omitempty"` // keep folders with at least one of these
	MinBitRate   uint32   `json:"min_bitrate,omitempty"`
	FreeSlotOnly bool     `json:"free_slot_only,omitempty"`
	MinFiles     int      `json:"min_files,omitempty"`
	ExcludeUsers []string `json:"exclude_users,omitempty"`
}

func (f Filter) match(h *FolderHit) bool {
	if f.FreeSlotOnly && !h.SlotFree {
		return false
	}
	if h.FileCount < f.MinFiles {
		return false
	}
	// Lossless files often carry no bitrate attribute; they always pass.
	if f.MinBitRate > 0 && !h.Lossless && h.AvgBitRate < f.MinBitRate {
		return false
	}
	if slices.Contains(f.ExcludeUsers, h.User) {
		return false
	}
	if len(f.Formats) > 0 && !slices.ContainsFunc(f.Formats, func(want string) bool {
		return slices.Contains(h.Formats, normExt(want))
	}) {
		return false
	}
	return true
}

var losslessExts = map[string]bool{
	"flac": true, "wav": true, "aif": true, "aiff": true, "ape": true,
	"wv": true, "alac": true, "dsf": true, "dff": true,
}

func normExt(s string) string { return strings.ToLower(strings.TrimPrefix(s, ".")) }

// splitPath splits a remote path into folder and file name. Soulseek
// paths use backslashes, but some clients send forward slashes.
func splitPath(p string) (folder, name string) {
	i := strings.LastIndexAny(p, `\/`)
	if i < 0 {
		return "", p
	}
	return p[:i], p[i+1:]
}

// ConvertFile turns a file from the wire into its API form.
func ConvertFile(f protocol.File) File {
	_, name := splitPath(f.Name)
	ext := normExt(path.Ext(name))
	if ext == "" {
		ext = normExt(f.Ext)
	}
	out := File{Path: f.Name, Name: name, Size: f.Size, Ext: ext}
	for _, a := range f.Attrs {
		switch a.Code {
		case protocol.AttrBitRate:
			out.BitRate = a.Value
		case protocol.AttrDuration:
			out.Duration = a.Value
		case protocol.AttrVBR:
			out.VBR = a.Value != 0
		case protocol.AttrSampleRate:
			out.SampleRate = a.Value
		case protocol.AttrBitDepth:
			out.BitDepth = a.Value
		}
	}
	// Derive a bitrate from size and duration when the peer sent none.
	if out.BitRate == 0 && out.Duration > 0 {
		out.BitRate = uint32(out.Size * 8 / uint64(out.Duration) / 1000)
	}
	return out
}

// summarize fills the derived fields of h from its files and the
// responding peer's slot details.
func summarize(h *FolderHit, r protocol.FileSearchResponse, terms []string) {
	h.FileCount = len(h.Files)
	h.TotalSize = 0
	counts := map[string]int{}
	var brSum, brN uint64
	lossless := 0
	for _, f := range h.Files {
		h.TotalSize += f.Size
		if f.Ext != "" {
			counts[f.Ext]++
		}
		if losslessExts[f.Ext] {
			lossless++
		}
		if f.BitRate > 0 {
			brSum += uint64(f.BitRate)
			brN++
		}
	}
	h.Formats = h.Formats[:0]
	for ext := range counts {
		h.Formats = append(h.Formats, ext)
	}
	slices.SortFunc(h.Formats, func(a, b string) int {
		return cmp.Or(cmp.Compare(counts[b], counts[a]), cmp.Compare(a, b))
	})
	h.AvgBitRate = 0
	if brN > 0 {
		h.AvgBitRate = uint32(brSum / brN)
	}
	// Lossless when most files are (an album folder also has art and a cue).
	h.Lossless = lossless > 0 && lossless*2 >= h.FileCount
	h.SlotFree = r.SlotFree
	h.QueueLength = r.QueueLength
	h.UploadSpeed = r.AvgSpeed
	h.Score = score(h, terms)
	h.AlbumKey = fmt.Sprintf("%s|%d", albumName(h.Folder), h.FileCount)
}

// score ranks a folder hit; higher is better. A free slot matters most,
// then a short queue and a fast uplink, then how well the folder itself
// matches the query and how complete and high quality it looks.
func score(h *FolderHit, terms []string) float64 {
	s := 0.0
	if h.SlotFree {
		s += 3
	}
	s -= math.Min(float64(h.QueueLength), 50) / 25
	s += math.Min(math.Log10(float64(h.UploadSpeed)/1024+1), 4) / 2
	if len(terms) > 0 {
		folder := strings.ToLower(h.Folder)
		matched := 0
		for _, t := range terms {
			if strings.Contains(folder, t) {
				matched++
			}
		}
		s += 2 * float64(matched) / float64(len(terms))
	}
	s += math.Min(float64(h.FileCount), 20) / 20
	if h.Lossless {
		s += 0.5
	}
	return s
}

// queryTerms returns the lowercase words of a query that results should
// contain, leaving out excluded (-word) terms.
func queryTerms(q string) []string {
	var terms []string
	for _, t := range strings.Fields(strings.ToLower(q)) {
		if !strings.HasPrefix(t, "-") {
			terms = append(terms, strings.Trim(t, `"*`))
		}
	}
	return terms
}

// rank sorts hits best first, with a stable order for ties.
func rank(hits []FolderHit) {
	slices.SortFunc(hits, func(a, b FolderHit) int {
		return cmp.Or(
			cmp.Compare(b.Score, a.Score),
			cmp.Compare(b.UploadSpeed, a.UploadSpeed),
			cmp.Compare(a.User, b.User),
			cmp.Compare(a.Folder, b.Folder),
		)
	})
}

var discFolder = regexp.MustCompile(`(?i)^(cd|disc|disk|dvd)\s*\d+$`)

// albumName normalizes a folder for dedupe: the last component, with the
// parent prepended for generic names like "CD1", lowercased and stripped
// of everything but letters and digits.
func albumName(folder string) string {
	parent, base := splitPath(folder)
	if discFolder.MatchString(strings.TrimSpace(base)) {
		_, p := splitPath(parent)
		base = p + " " + base
	}
	return strings.Map(func(r rune) rune {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			return unicode.ToLower(r)
		}
		return -1
	}, base)
}

// sizeTolerance is how far apart two copies of an album may be in total
// size, as a fraction, and still count as the same release.
const sizeTolerance = 0.03

// groupAlbums dedupes ranked hits into albums, ordered by their best
// source.
func groupAlbums(hits []FolderHit) []Album {
	var albums []Album
	index := map[string][]int{}
	for _, h := range hits {
		k := h.AlbumKey
		found := -1
		if !strings.HasPrefix(k, "|") { // folders with no usable name stay alone
			for _, i := range index[k] {
				if closeSize(albums[i].TotalSize, h.TotalSize) {
					found = i
					break
				}
			}
		}
		if found < 0 {
			index[k] = append(index[k], len(albums))
			albums = append(albums, Album{Name: h.Name, FileCount: h.FileCount, TotalSize: h.TotalSize})
			found = len(albums) - 1
		}
		albums[found].Sources = append(albums[found].Sources, h)
	}
	return albums
}

func closeSize(a, b uint64) bool {
	if a == b {
		return true
	}
	hi, lo := float64(max(a, b)), float64(min(a, b))
	return (hi-lo)/hi <= sizeTolerance
}
