package protocol

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"reflect"
	"testing"
)

func TestPeerInitGolden(t *testing.T) {
	got := EncodeInit(PeerInit{Username: "bob", Type: ConnPeer, Token: 7})
	want := []byte{
		17, 0, 0, 0, // length
		1,                         // code
		3, 0, 0, 0, 'b', 'o', 'b', // username
		1, 0, 0, 0, 'P', // type
		7, 0, 0, 0, // token
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("PeerInit frame\n got %v\nwant %v", got, want)
	}

	got = EncodeInit(PierceFirewall{Token: 0x01020304})
	want = []byte{5, 0, 0, 0, 0, 4, 3, 2, 1}
	if !bytes.Equal(got, want) {
		t.Fatalf("PierceFirewall frame\n got %v\nwant %v", got, want)
	}
}

func TestInitRoundTrip(t *testing.T) {
	for _, m := range []InitMessage{
		PierceFirewall{Token: 42},
		PeerInit{Username: "alice", Type: ConnFile, Token: 9},
	} {
		frame := EncodeInit(m)
		got, err := DecodeInit(frame[4:])
		if err != nil {
			t.Fatalf("%T: %v", m, err)
		}
		if got != m {
			t.Errorf("got %+v, want %+v", got, m)
		}
	}
	if _, err := DecodeInit([]byte{9}); err == nil {
		t.Error("unknown init code accepted")
	}
	if _, err := DecodeInit([]byte{1, 3, 0, 0, 0, 'b'}); !errors.Is(err, ErrShort) {
		t.Errorf("truncated PeerInit: err = %v", err)
	}
}

var flac = File{
	Name:  `@@music\Artist\Album\01 - Song.flac`,
	Size:  31_000_000,
	Ext:   "flac",
	Attrs: []FileAttribute{{AttrDuration, 240}, {AttrSampleRate, 44100}, {AttrBitDepth, 16}},
}

func TestPeerRoundTrip(t *testing.T) {
	mp3 := File{Name: `@@music\Artist\Album\02 - Song.mp3`, Size: 9_000_000, Attrs: []FileAttribute{{AttrBitRate, 320}, {AttrVBR, 0}}}
	dirs := []Directory{{Name: `@@music\Artist\Album`, Files: []File{flac, mp3}}, {Name: `@@music\Empty`}}
	for _, m := range []PeerMessage{
		SharedFileListRequest{},
		SharedFileListResponse{Dirs: dirs, PrivateDirs: []Directory{{Name: `@@priv\x`, Files: []File{mp3}}}},
		FileSearchResponse{Username: "bob", Token: 5, Files: []File{flac, mp3}, SlotFree: true, AvgSpeed: 1 << 20, QueueLength: 3, PrivateFiles: []File{mp3}},
		UserInfoRequest{},
		UserInfoResponse{Description: "hi", Picture: []byte{1, 2, 3}, UploadSlots: 2, QueueSize: 4, SlotsFree: true, UploadPermitted: 1},
		UserInfoResponse{Description: "no picture"},
		FolderContentsRequest{Token: 11, Folder: `@@music\Artist`},
		FolderContentsResponse{Token: 11, Folder: `@@music\Artist`, Dirs: dirs},
		TransferRequest{Direction: TransferUpload, Token: 3, Filename: flac.Name, Size: flac.Size},
		TransferRequest{Direction: TransferDownload, Token: 4, Filename: flac.Name},
		TransferResponse{Token: 3, Allowed: true},
		TransferResponse{Token: 3, Allowed: true, Size: 100},
		TransferResponse{Token: 3, Reason: "Queued"},
		QueueUpload{Filename: flac.Name},
		PlaceInQueueResponse{Filename: flac.Name, Place: 8},
		UploadFailed{Filename: flac.Name},
		UploadDenied{Filename: flac.Name, Reason: "File not shared."},
		PlaceInQueueRequest{Filename: flac.Name},
	} {
		frame := EncodePeer(m)
		if n := binary.LittleEndian.Uint32(frame); int(n) != len(frame)-4 {
			t.Errorf("%T: length prefix %d, frame body %d", m, n, len(frame)-4)
		}
		got, err := DecodePeer(frame[4:])
		if err != nil {
			t.Errorf("%T: %v", m, err)
			continue
		}
		if !reflect.DeepEqual(got, m) {
			t.Errorf("%T round trip\n got %+v\nwant %+v", m, got, m)
		}
	}
}

func TestFilePath(t *testing.T) {
	if p := FilePath(`@@a\Music`, "x.mp3"); p != `@@a\Music\x.mp3` {
		t.Errorf("bare name: %s", p)
	}
	if p := FilePath(`@@a\Music`, `@@a\Music\x.mp3`); p != `@@a\Music\x.mp3` {
		t.Errorf("full path: %s", p)
	}
}

func TestDecodeSearchResponseWithoutOptionalTail(t *testing.T) {
	// Older clients stop after the queue length.
	var inner Writer
	inner.String("old")
	inner.Uint32(77)
	writeFiles(&inner, []File{flac})
	inner.Bool(false)
	inner.Uint32(500)
	inner.Uint32(2)
	var w Writer
	w.Uint32(CodeFileSearchResponse)
	w.Raw(Compress(inner.Bytes()))

	m, err := DecodePeer(w.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	r := m.(FileSearchResponse)
	if r.Username != "old" || r.Token != 77 || len(r.Files) != 1 || r.AvgSpeed != 500 || r.QueueLength != 2 || r.PrivateFiles != nil {
		t.Errorf("unexpected %+v", r)
	}
	if v, ok := r.Files[0].Attr(AttrDuration); !ok || v != 240 {
		t.Errorf("duration attr = %d, %v", v, ok)
	}
}

func TestDecodePeerRejectsBadPayloads(t *testing.T) {
	var w Writer
	w.Uint32(CodeFileSearchResponse)
	w.Raw([]byte("not zlib"))
	if _, err := DecodePeer(w.Bytes()); err == nil {
		t.Error("garbage compressed payload accepted")
	}

	// A file count far larger than the payload must fail, not allocate.
	var inner Writer
	inner.String("x")
	inner.Uint32(1)
	inner.Uint32(0xFFFFFFFF)
	w = Writer{}
	w.Uint32(CodeFileSearchResponse)
	w.Raw(Compress(inner.Bytes()))
	if _, err := DecodePeer(w.Bytes()); !errors.Is(err, ErrShort) {
		t.Errorf("huge count: err = %v, want ErrShort", err)
	}

	m, err := DecodePeer(binary.LittleEndian.AppendUint32(nil, 4242))
	if err != nil {
		t.Fatal(err)
	}
	if u, ok := m.(UnknownMessage); !ok || u.Code != 4242 {
		t.Errorf("got %#v", m)
	}
}

func TestReadFrameLarge(t *testing.T) {
	body := bytes.Repeat([]byte{7}, eagerFrame+10)
	got, err := ReadFrame(bytes.NewReader(Frame(body)), 4<<20)
	if err != nil || !bytes.Equal(got, body) {
		t.Fatalf("large frame: len %d, err %v", len(got), err)
	}
	// A large frame cut short reports an unexpected EOF.
	short := Frame(body)[:eagerFrame]
	if _, err := ReadFrame(bytes.NewReader(short), 4<<20); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Errorf("truncated large frame: err = %v", err)
	}
}

func TestStreamedShareList(t *testing.T) {
	dirs := []Directory{{Name: `@@a\b`, Files: []File{flac}}, {Name: `@@a\c`}}
	var w Writer
	w.Uint32(uint32(len(dirs)))
	for _, d := range dirs {
		WriteDirectory(&w, d)
	}
	w.Uint32(0)
	w.Uint32(0)
	streamed := Encoded{Code: CodeSharedFileListResponse, Payload: Compress(w.Bytes())}
	m, err := DecodePeer(EncodePeer(streamed)[4:])
	if err != nil || !reflect.DeepEqual(m, SharedFileListResponse{Dirs: dirs}) {
		t.Errorf("streamed list decodes to %+v, %v", m, err)
	}
}
