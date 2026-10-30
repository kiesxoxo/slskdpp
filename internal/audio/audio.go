// Package audio reads the technical details of audio files (bitrate,
// duration, sample rate, bit depth) from their headers, reading as little
// of each file as it can. It never decodes audio.
package audio

import (
	"bytes"
	"encoding/binary"
	"io"
	"math"
	"os"
	"path/filepath"
	"strings"
)

// Info describes an audio file. Zero fields are unknown.
type Info struct {
	BitRate    uint32 // kbps, average for VBR
	Duration   uint32 // seconds
	SampleRate uint32 // Hz
	BitDepth   uint32 // bits per sample, lossless formats only
	VBR        bool
	Lossless   bool
}

// Supported reports whether Probe understands files with this name.
func Supported(name string) bool {
	_, ok := probers[strings.ToLower(filepath.Ext(name))]
	return ok
}

type prober func(r io.ReaderAt, size int64) (Info, bool)

var probers = map[string]prober{
	".flac": probeFLAC,
	".mp3":  probeMP3,
	".ogg":  probeOgg,
	".oga":  probeOgg,
	".opus": probeOgg,
	".wav":  probeWAV,
	".aif":  probeAIFF,
	".aiff": probeAIFF,
	".aifc": probeAIFF,
	".m4a":  probeMP4,
	".m4b":  probeMP4,
	".mp4":  probeMP4,
	".alac": probeMP4,
}

// Probe reads path's audio details. ok is false for unsupported or
// unreadable files.
func Probe(path string) (info Info, ok bool) {
	p, found := probers[strings.ToLower(filepath.Ext(path))]
	if !found {
		return Info{}, false
	}
	f, err := os.Open(path)
	if err != nil {
		return Info{}, false
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil || st.Size() == 0 {
		return Info{}, false
	}
	// Parsers index into header bytes; a malformed file must not take the
	// scanner down with it.
	defer func() {
		if recover() != nil {
			info, ok = Info{}, false
		}
	}()
	info, ok = p(f, st.Size())
	if !ok || info.Duration == 0 && info.BitRate == 0 {
		return Info{}, false
	}
	return info, true
}

// readAt reads up to n bytes at off; short reads at the end of the file
// return what there is.
func readAt(r io.ReaderAt, off int64, n int) []byte {
	if off < 0 || n <= 0 {
		return nil
	}
	b := make([]byte, n)
	m, _ := r.ReadAt(b, off)
	return b[:m]
}

// seconds rounds a duration, rejecting nonsense.
func seconds(d float64) uint32 {
	if d <= 0 || math.IsNaN(d) || d > 1e6 {
		return 0
	}
	return uint32(math.Round(d))
}

// kbps is the average bitrate of bytes played over d seconds.
func kbps(bytes int64, d float64) uint32 {
	if bytes <= 0 || d <= 0 {
		return 0
	}
	v := float64(bytes) * 8 / d / 1000
	if v > 1e6 {
		return 0
	}
	return uint32(math.Round(v))
}

// skipID3v2 returns the offset after any ID3v2 tags at off.
func skipID3v2(r io.ReaderAt, off int64) int64 {
	for range 4 {
		h := readAt(r, off, 10)
		if len(h) < 10 || string(h[:3]) != "ID3" {
			return off
		}
		size := int64(h[6]&0x7f)<<21 | int64(h[7]&0x7f)<<14 | int64(h[8]&0x7f)<<7 | int64(h[9]&0x7f)
		off += 10 + size
		if h[5]&0x10 != 0 { // footer
			off += 10
		}
	}
	return off
}

func probeFLAC(r io.ReaderAt, size int64) (Info, bool) {
	off := skipID3v2(r, 0)
	if string(readAt(r, off, 4)) != "fLaC" {
		return Info{}, false
	}
	off += 4
	var info Info
	var total uint64
	for range 1000 {
		h := readAt(r, off, 4)
		if len(h) < 4 {
			return Info{}, false
		}
		last, typ := h[0]&0x80 != 0, h[0]&0x7f
		length := int64(h[1])<<16 | int64(h[2])<<8 | int64(h[3])
		if typ == 0 && length >= 34 {
			si := readAt(r, off+4, 34)
			if len(si) < 34 {
				return Info{}, false
			}
			v := binary.BigEndian.Uint64(si[10:18])
			info.SampleRate = uint32(v >> 44)
			info.BitDepth = uint32(v>>36&0x1f) + 1
			total = v & 0xfffffffff
		}
		off += 4 + length
		if last {
			break
		}
	}
	if info.SampleRate == 0 {
		return Info{}, false
	}
	d := float64(total) / float64(info.SampleRate)
	info.Duration = seconds(d)
	info.BitRate = kbps(size-off, d)
	info.Lossless = true
	return info, true
}

// MPEG audio tables, indexed [version][layer][index] where version is
// 0 for MPEG-1 and 1 for MPEG-2/2.5, and layer is 0 for Layer I.
var mpegBitrates = [2][3][16]uint32{
	{
		{0, 32, 64, 96, 128, 160, 192, 224, 256, 288, 320, 352, 384, 416, 448, 0},
		{0, 32, 48, 56, 64, 80, 96, 112, 128, 160, 192, 224, 256, 320, 384, 0},
		{0, 32, 40, 48, 56, 64, 80, 96, 112, 128, 160, 192, 224, 256, 320, 0},
	},
	{
		{0, 32, 48, 56, 64, 80, 96, 112, 128, 144, 160, 176, 192, 224, 256, 0},
		{0, 8, 16, 24, 32, 40, 48, 56, 64, 80, 96, 112, 128, 144, 160, 0},
		{0, 8, 16, 24, 32, 40, 48, 56, 64, 80, 96, 112, 128, 144, 160, 0},
	},
}

var mpegRates = map[uint32][3]uint32{
	3: {44100, 48000, 32000}, // MPEG-1
	2: {22050, 24000, 16000}, // MPEG-2
	0: {11025, 12000, 8000},  // MPEG-2.5
}

type mpegFrame struct {
	version    uint32 // header bits: 3 MPEG-1, 2 MPEG-2, 0 MPEG-2.5
	layer      uint32 // 1, 2 or 3
	bitrate    uint32 // kbps
	sampleRate uint32
	length     int64
	samples    uint32 // per frame
	mono       bool
}

func parseMPEGHeader(b []byte) (mpegFrame, bool) {
	if len(b) < 4 {
		return mpegFrame{}, false
	}
	h := binary.BigEndian.Uint32(b)
	if h>>21 != 0x7ff {
		return mpegFrame{}, false
	}
	version, layerBits := h>>19&3, h>>17&3
	brIndex, srIndex := h>>12&0xf, h>>10&3
	if version == 1 || layerBits == 0 || brIndex == 0 || brIndex == 15 || srIndex == 3 {
		return mpegFrame{}, false
	}
	f := mpegFrame{version: version, layer: 4 - layerBits, mono: h>>6&3 == 3}
	v := 0
	if version != 3 {
		v = 1
	}
	f.bitrate = mpegBitrates[v][f.layer-1][brIndex]
	f.sampleRate = mpegRates[version][srIndex]
	pad := int64(h >> 9 & 1)
	br := int64(f.bitrate) * 1000
	sr := int64(f.sampleRate)
	switch {
	case f.layer == 1:
		f.samples = 384
		f.length = (12*br/sr + pad) * 4
	case f.layer == 3 && version != 3:
		f.samples = 576
		f.length = 72*br/sr + pad
	default:
		f.samples = 1152
		f.length = 144*br/sr + pad
	}
	return f, f.length > 4
}

func probeMP3(r io.ReaderAt, size int64) (Info, bool) {
	start := skipID3v2(r, 0)
	buf := readAt(r, start, 256<<10)
	// Find a frame whose successor is where its length says, so stray
	// sync bytes in leftover tag data do not fool us.
	pos := -1
	var fr mpegFrame
	for i := 0; i+4 <= len(buf); i++ {
		if buf[i] != 0xff {
			continue
		}
		f, ok := parseMPEGHeader(buf[i:])
		if !ok {
			continue
		}
		next := i + int(f.length)
		if next+4 <= len(buf) {
			g, ok := parseMPEGHeader(buf[next:])
			if !ok || g.version != f.version || g.layer != f.layer || g.sampleRate != f.sampleRate {
				continue
			}
		}
		pos, fr = i, f
		break
	}
	if pos < 0 {
		return Info{}, false
	}
	audioStart := start + int64(pos)
	audioEnd := size
	if string(readAt(r, size-128, 3)) == "TAG" {
		audioEnd -= 128
	}
	info := Info{SampleRate: fr.sampleRate}

	// A Xing/Info header (LAME and most encoders) or a VBRI header
	// (Fraunhofer) in the first frame gives the frame count.
	side := 32
	switch {
	case fr.version == 3 && fr.mono:
		side = 17
	case fr.version != 3 && !fr.mono:
		side = 17
	case fr.version != 3 && fr.mono:
		side = 9
	}
	frame := buf[pos:min(len(buf), pos+int(fr.length))]
	var frames, streamBytes uint32
	if x := 4 + side; len(frame) >= x+16 && (string(frame[x:x+4]) == "Xing" || string(frame[x:x+4]) == "Info") {
		info.VBR = string(frame[x:x+4]) == "Xing"
		flags := binary.BigEndian.Uint32(frame[x+4:])
		p := x + 8
		if flags&1 != 0 && len(frame) >= p+4 {
			frames = binary.BigEndian.Uint32(frame[p:])
			p += 4
		}
		if flags&2 != 0 && len(frame) >= p+4 {
			streamBytes = binary.BigEndian.Uint32(frame[p:])
		}
	} else if len(frame) >= 4+32+18 && string(frame[36:40]) == "VBRI" {
		info.VBR = true
		streamBytes = binary.BigEndian.Uint32(frame[46:])
		frames = binary.BigEndian.Uint32(frame[50:])
	}
	if frames > 0 {
		d := float64(frames) * float64(fr.samples) / float64(fr.sampleRate)
		info.Duration = seconds(d)
		if streamBytes == 0 {
			streamBytes = uint32(min(audioEnd-audioStart, math.MaxUint32))
		}
		info.BitRate = kbps(int64(streamBytes), d)
		if !info.VBR {
			info.BitRate = fr.bitrate // "Info" marks a constant bitrate
		}
		return info, true
	}
	// Constant bitrate: the length follows from the size.
	info.BitRate = fr.bitrate
	info.Duration = seconds(float64(audioEnd-audioStart) * 8 / float64(fr.bitrate*1000))
	return info, true
}

func probeOgg(r io.ReaderAt, size int64) (Info, bool) {
	h := readAt(r, 0, 27+255+64)
	if len(h) < 28 || string(h[:4]) != "OggS" {
		return Info{}, false
	}
	nseg := int(h[26])
	p := 27 + nseg
	if len(h) < p+19 {
		return Info{}, false
	}
	pkt := h[p:]
	var info Info
	var divisor float64
	var preskip uint64
	switch {
	case len(pkt) >= 16 && pkt[0] == 1 && string(pkt[1:7]) == "vorbis":
		info.SampleRate = binary.LittleEndian.Uint32(pkt[12:])
		divisor = float64(info.SampleRate)
	case len(pkt) >= 16 && string(pkt[:8]) == "OpusHead":
		preskip = uint64(binary.LittleEndian.Uint16(pkt[10:]))
		info.SampleRate = binary.LittleEndian.Uint32(pkt[12:])
		if info.SampleRate == 0 {
			info.SampleRate = 48000
		}
		divisor = 48000 // Opus granules always count 48 kHz samples
	default:
		return Info{}, false
	}
	if divisor == 0 {
		return Info{}, false
	}
	// The last page's granule position is the stream length in samples.
	tailLen := int64(64 << 10)
	tailOff := max(0, size-tailLen)
	tail := readAt(r, tailOff, int(size-tailOff))
	i := bytes.LastIndex(tail, []byte("OggS"))
	if i < 0 || len(tail) < i+14 {
		return Info{}, false
	}
	granule := binary.LittleEndian.Uint64(tail[i+6:])
	if granule == ^uint64(0) || granule < preskip {
		return Info{}, false
	}
	d := float64(granule-preskip) / divisor
	info.Duration = seconds(d)
	info.BitRate = kbps(size, d)
	info.VBR = true
	return info, true
}

func probeWAV(r io.ReaderAt, size int64) (Info, bool) {
	h := readAt(r, 0, 12)
	if len(h) < 12 || string(h[:4]) != "RIFF" || string(h[8:12]) != "WAVE" {
		return Info{}, false
	}
	var info Info
	var byteRate uint32
	off := int64(12)
	for range 1000 {
		c := readAt(r, off, 8)
		if len(c) < 8 {
			break
		}
		id, n := string(c[:4]), int64(binary.LittleEndian.Uint32(c[4:]))
		switch id {
		case "fmt ":
			f := readAt(r, off+8, 16)
			if len(f) < 16 {
				return Info{}, false
			}
			info.SampleRate = binary.LittleEndian.Uint32(f[4:])
			byteRate = binary.LittleEndian.Uint32(f[8:])
			info.BitDepth = uint32(binary.LittleEndian.Uint16(f[14:]))
		case "data":
			if byteRate == 0 {
				return Info{}, false
			}
			if n == 0 || n > size-off-8 { // streamed files leave it unset
				n = size - off - 8
			}
			info.Duration = seconds(float64(n) / float64(byteRate))
			info.BitRate = byteRate * 8 / 1000
			info.Lossless = true
			return info, true
		}
		off += 8 + n + n&1
	}
	return Info{}, false
}

// ext80 decodes the 80-bit extended float AIFF uses for its sample rate.
func ext80(b []byte) float64 {
	exp := int(binary.BigEndian.Uint16(b) & 0x7fff)
	mant := binary.BigEndian.Uint64(b[2:])
	if exp == 0 && mant == 0 {
		return 0
	}
	return math.Ldexp(float64(mant), exp-16383-63)
}

func probeAIFF(r io.ReaderAt, size int64) (Info, bool) {
	h := readAt(r, 0, 12)
	if len(h) < 12 || string(h[:4]) != "FORM" || (string(h[8:12]) != "AIFF" && string(h[8:12]) != "AIFC") {
		return Info{}, false
	}
	off := int64(12)
	for range 1000 {
		c := readAt(r, off, 8)
		if len(c) < 8 {
			break
		}
		n := int64(binary.BigEndian.Uint32(c[4:]))
		if string(c[:4]) == "COMM" {
			f := readAt(r, off+8, 18)
			if len(f) < 18 {
				return Info{}, false
			}
			channels := uint32(binary.BigEndian.Uint16(f))
			frames := binary.BigEndian.Uint32(f[2:])
			depth := uint32(binary.BigEndian.Uint16(f[6:]))
			rate := ext80(f[8:18])
			if rate <= 0 || rate > 1e7 {
				return Info{}, false
			}
			return Info{
				SampleRate: uint32(math.Round(rate)),
				BitDepth:   depth,
				Duration:   seconds(float64(frames) / rate),
				BitRate:    uint32(math.Round(rate * float64(channels*depth) / 1000)),
				Lossless:   true,
			}, true
		}
		off += 8 + n + n&1
	}
	return Info{}, false
}

// MP4 boxes we descend into to reach the audio sample description.
var mp4Containers = map[string]bool{"moov": true, "trak": true, "mdia": true, "minf": true, "stbl": true}

func probeMP4(r io.ReaderAt, size int64) (Info, bool) {
	var timescale, duration uint64
	var info Info
	var format string
	var mdat int64
	var walk func(off, end int64, depth int)
	walk = func(off, end int64, depth int) {
		for n := 0; off+8 <= end && n < 1000; n++ {
			h := readAt(r, off, 16)
			if len(h) < 8 {
				return
			}
			boxSize := int64(binary.BigEndian.Uint32(h))
			typ := string(h[4:8])
			hdr := int64(8)
			switch boxSize {
			case 0:
				boxSize = end - off
			case 1:
				if len(h) < 16 {
					return
				}
				boxSize, hdr = int64(binary.BigEndian.Uint64(h[8:])), 16
			}
			if boxSize < hdr || off+boxSize > end {
				return
			}
			body := off + hdr
			switch {
			case typ == "mdat":
				mdat += boxSize - hdr
			case typ == "mvhd":
				b := readAt(r, body, 32)
				if len(b) >= 20 && b[0] == 0 {
					timescale = uint64(binary.BigEndian.Uint32(b[12:]))
					duration = uint64(binary.BigEndian.Uint32(b[16:]))
				} else if len(b) >= 32 && b[0] == 1 {
					timescale = uint64(binary.BigEndian.Uint32(b[20:]))
					duration = binary.BigEndian.Uint64(b[24:])
				}
			case typ == "stsd" && format == "":
				// version/flags, entry count, then the first entry:
				// size, format, 6 reserved, data ref index, then the audio
				// sample entry (version, revision, vendor, channels,
				// sample size, compression id, packet size, rate 16.16).
				b := readAt(r, body, 8+36+36)
				if len(b) >= 44 {
					f := string(b[12:16])
					if f == "mp4a" || f == "alac" {
						format = f
						info.BitDepth = uint32(binary.BigEndian.Uint16(b[34:]))
						info.SampleRate = binary.BigEndian.Uint32(b[40:]) >> 16
					}
					// The 16.16 rate field tops out at 65535 Hz; ALAC keeps
					// the real rate and depth in its decoder config box:
					// size, "alac", version/flags, then frame length,
					// version, bit depth, ..., sample rate at byte 20.
					if c := b[44:]; f == "alac" && len(c) >= 36 && string(c[4:8]) == "alac" {
						info.BitDepth = uint32(c[12+5])
						info.SampleRate = binary.BigEndian.Uint32(c[12+20:])
					}
				}
			case mp4Containers[typ] && depth < 8:
				walk(body, off+boxSize, depth+1)
			}
			off += boxSize
		}
	}
	walk(0, size, 0)
	if timescale == 0 || format == "" {
		return Info{}, false
	}
	d := float64(duration) / float64(timescale)
	info.Duration = seconds(d)
	if mdat == 0 {
		mdat = size
	}
	info.BitRate = kbps(mdat, d)
	if format == "alac" {
		info.Lossless = true
	} else {
		info.BitDepth = 0 // AAC has no meaningful bit depth
	}
	return info, true
}
