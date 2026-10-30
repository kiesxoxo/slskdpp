package audio

import (
	"bytes"
	"encoding/binary"
	"math/rand/v2"
	"os"
	"path/filepath"
	"testing"
)

func save(t *testing.T, name string, b []byte) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, b, 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func id3v2(n int) []byte {
	// Tag of n bytes after the 10-byte header, size as a syncsafe int.
	h := []byte{'I', 'D', '3', 4, 0, 0, byte(n >> 21 & 0x7f), byte(n >> 14 & 0x7f), byte(n >> 7 & 0x7f), byte(n & 0x7f)}
	// Sync-like bytes inside the tag must not be taken for audio.
	body := bytes.Repeat([]byte{0xff, 0xfb, 0x90, 0x00}, n/4)
	return append(h, append(body, make([]byte, n-len(body))...)...)
}

func flac(sampleRate, depth uint32, samples uint64, audio int, tagged bool) []byte {
	var b []byte
	if tagged {
		b = id3v2(300)
	}
	b = append(b, "fLaC"...)
	b = append(b, 0, 0, 0, 34) // STREAMINFO, not last
	si := make([]byte, 34)
	v := uint64(sampleRate)<<44 | uint64(1)<<41 | uint64(depth-1)<<36 | samples
	binary.BigEndian.PutUint64(si[10:], v)
	b = append(b, si...)
	b = append(b, 0x81, 0, 0, 8) // PADDING, last
	b = append(b, make([]byte, 8)...)
	return append(b, make([]byte, audio)...)
}

func TestFLAC(t *testing.T) {
	for _, tagged := range []bool{false, true} {
		p := save(t, "a.flac", flac(44100, 16, 441000*3, 3_000_000, tagged))
		got, ok := Probe(p)
		want := Info{BitRate: 800, Duration: 30, SampleRate: 44100, BitDepth: 16, Lossless: true}
		if !ok || got != want {
			t.Errorf("tagged=%v: %+v %v, want %+v", tagged, got, ok, want)
		}
	}
	p := save(t, "hires.flac", flac(96000, 24, 96000*200, 50_000_000, false))
	if got, _ := Probe(p); got.SampleRate != 96000 || got.BitDepth != 24 || got.Duration != 200 || got.BitRate != 2000 {
		t.Errorf("hi-res: %+v", got)
	}
}

// mp3Frames builds n MPEG-1 Layer III frames at 128 kbps, 44.1 kHz.
func mp3Frames(n int) []byte {
	const length = 417 // 144 * 128000 / 44100
	var b []byte
	for range n {
		f := make([]byte, length)
		copy(f, []byte{0xff, 0xfb, 0x90, 0x00})
		b = append(b, f...)
	}
	return b
}

func TestMP3(t *testing.T) {
	// Constant bitrate: the length follows from the size. ID3 tags at
	// either end are not audio.
	cbr := append(id3v2(2000), mp3Frames(3828)...) // 100.0 s
	cbr = append(cbr, append([]byte("TAG"), make([]byte, 125)...)...)
	got, ok := Probe(save(t, "cbr.mp3", cbr))
	if want := (Info{BitRate: 128, Duration: 100, SampleRate: 44100}); !ok || got != want {
		t.Errorf("CBR: %+v %v, want %+v", got, ok, want)
	}

	// VBR with a Xing header: frame count and byte count give both.
	vbr := mp3Frames(20)
	x := 4 + 32
	copy(vbr[x:], "Xing")
	binary.BigEndian.PutUint32(vbr[x+4:], 3)          // frames and bytes present
	binary.BigEndian.PutUint32(vbr[x+8:], 9570)       // 250.0 s
	binary.BigEndian.PutUint32(vbr[x+12:], 7_812_500) // 250 kbps
	got, ok = Probe(save(t, "vbr.mp3", vbr))
	if want := (Info{BitRate: 250, Duration: 250, SampleRate: 44100, VBR: true}); !ok || got != want {
		t.Errorf("Xing: %+v %v, want %+v", got, ok, want)
	}

	// LAME writes "Info" for constant bitrate files.
	info := mp3Frames(20)
	copy(info[x:], "Info")
	binary.BigEndian.PutUint32(info[x+4:], 1)
	binary.BigEndian.PutUint32(info[x+8:], 3828)
	if got, _ := Probe(save(t, "info.mp3", info)); got.VBR || got.BitRate != 128 || got.Duration != 100 {
		t.Errorf("Info: %+v", got)
	}

	// Fraunhofer VBRI header.
	vbri := mp3Frames(20)
	copy(vbri[36:], "VBRI")
	binary.BigEndian.PutUint32(vbri[46:], 1_000_000)
	binary.BigEndian.PutUint32(vbri[50:], 383) // 10.0 s
	if got, _ := Probe(save(t, "vbri.mp3", vbri)); !got.VBR || got.Duration != 10 || got.BitRate != 800 {
		t.Errorf("VBRI: %+v", got)
	}
}

func wav(rate uint32, channels, depth uint16, dataLen int) []byte {
	byteRate := rate * uint32(channels) * uint32(depth) / 8
	var b bytes.Buffer
	b.WriteString("RIFF")
	binary.Write(&b, binary.LittleEndian, uint32(36+dataLen))
	b.WriteString("WAVEfmt ")
	for _, v := range []any{uint32(16), uint16(1), channels, rate, byteRate, channels * depth / 8, depth} {
		binary.Write(&b, binary.LittleEndian, v)
	}
	b.WriteString("LIST")
	binary.Write(&b, binary.LittleEndian, uint32(3)) // odd chunk, padded
	b.Write([]byte{1, 2, 3, 0})
	b.WriteString("data")
	binary.Write(&b, binary.LittleEndian, uint32(dataLen))
	b.Write(make([]byte, dataLen))
	return b.Bytes()
}

func TestWAV(t *testing.T) {
	got, ok := Probe(save(t, "a.wav", wav(44100, 2, 16, 176400*5)))
	if want := (Info{BitRate: 1411, Duration: 5, SampleRate: 44100, BitDepth: 16, Lossless: true}); !ok || got != want {
		t.Errorf("WAV: %+v %v, want %+v", got, ok, want)
	}
}

func TestAIFF(t *testing.T) {
	var b bytes.Buffer
	b.WriteString("FORM\x00\x00\x00\x00AIFFCOMM")
	binary.Write(&b, binary.BigEndian, uint32(18))
	binary.Write(&b, binary.BigEndian, uint16(2))
	binary.Write(&b, binary.BigEndian, uint32(48000*7))
	binary.Write(&b, binary.BigEndian, uint16(24))
	// 48000 as an 80-bit extended float.
	binary.Write(&b, binary.BigEndian, uint16(16383+15))
	binary.Write(&b, binary.BigEndian, uint64(48000)<<48)
	got, ok := Probe(save(t, "a.aiff", b.Bytes()))
	if want := (Info{BitRate: 2304, Duration: 7, SampleRate: 48000, BitDepth: 24, Lossless: true}); !ok || got != want {
		t.Errorf("AIFF: %+v %v, want %+v", got, ok, want)
	}
}

func oggPage(granule uint64, packet []byte) []byte {
	b := []byte("OggS")
	b = append(b, 0, 2)
	b = binary.LittleEndian.AppendUint64(b, granule)
	b = append(b, make([]byte, 12)...) // serial, sequence, checksum
	b = append(b, 1, byte(len(packet)))
	return append(b, packet...)
}

func TestOgg(t *testing.T) {
	id := append([]byte("\x01vorbis"), 0, 0, 0, 0, 2)
	id = binary.LittleEndian.AppendUint32(id, 44100)
	id = append(id, make([]byte, 16)...)
	vorbis := oggPage(0, id)
	vorbis = append(vorbis, make([]byte, 200_000)...)
	vorbis = append(vorbis, oggPage(44100*10, []byte{0})...)
	got, ok := Probe(save(t, "a.ogg", vorbis))
	if !ok || got.Duration != 10 || got.SampleRate != 44100 || !got.VBR || got.BitRate != 160 {
		t.Errorf("Vorbis: %+v %v", got, ok)
	}

	head := append([]byte("OpusHead"), 1, 2)
	head = binary.LittleEndian.AppendUint16(head, 312)
	head = binary.LittleEndian.AppendUint32(head, 44100)
	head = append(head, 0, 0, 0)
	opus := oggPage(0, head)
	opus = append(opus, make([]byte, 120_000)...)
	opus = append(opus, oggPage(48000*12+312, []byte{0})...)
	if got, ok := Probe(save(t, "a.opus", opus)); !ok || got.Duration != 12 || got.SampleRate != 44100 || got.BitRate != 80 {
		t.Errorf("Opus: %+v %v", got, ok)
	}
}

func box(typ string, body ...[]byte) []byte {
	n := 8
	for _, b := range body {
		n += len(b)
	}
	out := binary.BigEndian.AppendUint32(nil, uint32(n))
	out = append(out, typ...)
	for _, b := range body {
		out = append(out, b...)
	}
	return out
}

func m4a(format string, depth uint16, rate uint32, seconds uint32, mdat int, moovFirst bool) []byte {
	mvhd := make([]byte, 100)
	binary.BigEndian.PutUint32(mvhd[12:], 600)
	binary.BigEndian.PutUint32(mvhd[16:], 600*seconds)
	entry := make([]byte, 36)
	copy(entry[4:], format)
	binary.BigEndian.PutUint16(entry[16+8:], 2) // channels
	binary.BigEndian.PutUint16(entry[16+10:], depth)
	binary.BigEndian.PutUint32(entry[16+16:], rate<<16)
	if format == "alac" {
		cfg := make([]byte, 24)
		cfg[5] = byte(depth)
		binary.BigEndian.PutUint32(cfg[20:], rate)
		entry = append(entry, box("alac", make([]byte, 4), cfg)...)
		binary.BigEndian.PutUint32(entry[16+16:], 0) // too big for 16.16
	}
	binary.BigEndian.PutUint32(entry, uint32(len(entry)))
	stsd := append([]byte{0, 0, 0, 0, 0, 0, 0, 1}, entry...)
	moov := box("moov", box("mvhd", mvhd), box("trak", box("mdia", box("minf", box("stbl", box("stsd", stsd))))))
	data := box("mdat", make([]byte, mdat))
	ftyp := box("ftyp", []byte("M4A \x00\x00\x00\x00"))
	if moovFirst {
		return append(append(ftyp, moov...), data...)
	}
	return append(append(ftyp, data...), moov...)
}

func TestMP4(t *testing.T) {
	got, ok := Probe(save(t, "a.m4a", m4a("mp4a", 16, 44100, 20, 640_000, false)))
	if want := (Info{BitRate: 256, Duration: 20, SampleRate: 44100}); !ok || got != want {
		t.Errorf("AAC: %+v %v, want %+v", got, ok, want)
	}
	got, ok = Probe(save(t, "b.m4a", m4a("alac", 24, 96000, 10, 5_000_000, true)))
	if want := (Info{BitRate: 4000, Duration: 10, SampleRate: 96000, BitDepth: 24, Lossless: true}); !ok || got != want {
		t.Errorf("ALAC: %+v %v, want %+v", got, ok, want)
	}
}

func TestGarbage(t *testing.T) {
	if Supported("cover.jpg") || !Supported("x.FLAC") {
		t.Error("Supported")
	}
	if _, ok := Probe(save(t, "cover.jpg", []byte("jpeg"))); ok {
		t.Error("probed a jpg")
	}
	rng := rand.New(rand.NewPCG(1, 2))
	for ext := range probers {
		for i := range 200 {
			b := make([]byte, rng.IntN(5000))
			for j := range b {
				b[j] = byte(rng.Uint32())
			}
			// Plant valid magic now and then so parsers go deeper.
			if i%2 == 0 && len(b) > 12 {
				magic := map[string]string{".flac": "fLaC", ".ogg": "OggS", ".wav": "RIFF", ".aiff": "FORM", ".mp3": "ID3"}[ext]
				copy(b, magic)
			}
			Probe(save(t, "x"+ext, b)) // must not panic or hang
		}
	}
	for _, trunc := range [][]byte{flac(44100, 16, 1000, 10, false)[:20], mp3Frames(1)[:3], wav(44100, 2, 16, 10)[:30]} {
		if info, ok := Probe(save(t, "t.flac", trunc)); ok {
			t.Errorf("truncated file probed: %+v", info)
		}
	}
}

func TestMP3AfterLongJunk(t *testing.T) {
	// Some files carry padding or a broken tag before the first frame;
	// the probe looks further when the start shows no audio.
	b := append(make([]byte, 20_000), mp3Frames(383)...)
	if got, ok := Probe(save(t, "junk.mp3", b)); !ok || got.BitRate != 128 || got.Duration != 10 {
		t.Errorf("%+v %v", got, ok)
	}
}
