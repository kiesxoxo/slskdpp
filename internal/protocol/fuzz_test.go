package protocol

import (
	"bytes"
	"testing"
)

// Everything decoded here comes from other users or the network, so the
// decoders must never panic or hang on any input. Run longer with e.g.
// go test ./internal/protocol -run '^$' -fuzz FuzzDecodePeer -fuzztime 1m

func body(frame []byte) []byte { return frame[4:] }

var seedFiles = []File{
	{Name: `Music\Artist\Album\01 - Track.flac`, Size: 31_000_000, Ext: "flac", Attrs: []FileAttribute{{AttrDuration, 245}, {AttrSampleRate, 44100}, {AttrBitDepth, 16}}},
	{Name: `Music\Artist\Album\cover.jpg`, Size: 250_000},
}

func FuzzDecodePeer(f *testing.F) {
	for _, m := range []PeerMessage{
		SharedFileListRequest{},
		SharedFileListResponse{Dirs: []Directory{{Name: `Music\Artist\Album`, Files: seedFiles}}},
		FileSearchResponse{Username: "u", Token: 7, Files: seedFiles, SlotFree: true, AvgSpeed: 1000, QueueLength: 3},
		UserInfoRequest{},
		UserInfoResponse{Description: "hi", Picture: []byte{1, 2, 3}, UploadSlots: 2, QueueSize: 1, SlotsFree: true},
		FolderContentsRequest{Token: 1, Folder: `Music\Artist\Album`},
		FolderContentsResponse{Token: 1, Folder: `Music\Artist\Album`, Dirs: []Directory{{Name: `Music\Artist\Album`, Files: seedFiles}}},
		TransferRequest{Direction: 1, Token: 9, Filename: `a\b.mp3`, Size: 100},
		TransferResponse{Token: 9, Allowed: false, Reason: "Queued"},
		QueueUpload{Filename: `a\b.mp3`},
		PlaceInQueueResponse{Filename: `a\b.mp3`, Place: 4},
		UploadFailed{Filename: `a\b.mp3`},
		UploadDenied{Filename: `a\b.mp3`, Reason: "File not shared."},
		PlaceInQueueRequest{Filename: `a\b.mp3`},
	} {
		f.Add(body(EncodePeer(m)))
	}
	f.Fuzz(func(t *testing.T, b []byte) {
		DecodePeer(b)
	})
}

// FuzzDecodePeerPayload reaches the decoders behind compression, which
// random bytes rarely get past: it compresses the payload first.
func FuzzDecodePeerPayload(f *testing.F) {
	for _, m := range []PeerMessage{
		SharedFileListResponse{Dirs: []Directory{{Name: `Music\Artist\Album`, Files: seedFiles}}},
		FileSearchResponse{Username: "u", Token: 7, Files: seedFiles, SlotFree: true},
		FolderContentsResponse{Token: 1, Folder: `x`, Dirs: []Directory{{Name: `x`, Files: seedFiles}}},
	} {
		b := body(EncodePeer(m))
		raw, err := Decompress(b[4:], MaxInflated)
		if err != nil {
			f.Fatal(err)
		}
		f.Add(NewReader(b).Uint32(), raw)
	}
	f.Fuzz(func(t *testing.T, code uint32, payload []byte) {
		var w Writer
		w.Uint32(code)
		w.Raw(Compress(payload))
		DecodePeer(w.Bytes())
	})
}

func FuzzDecodeServer(f *testing.F) {
	seed := func(code uint32, fill func(w *Writer)) {
		var w Writer
		w.Uint32(code)
		fill(&w)
		f.Add(w.Bytes())
	}
	seed(CodeLogin, func(w *Writer) {
		w.Bool(true)
		w.String("Welcome")
		w.Uint32(0x0100007f)
		w.String("hash")
		w.Bool(false)
	})
	seed(CodeLogin, func(w *Writer) { w.Bool(false); w.String("INVALIDPASS") })
	seed(CodeGetPeerAddress, func(w *Writer) { w.String("u"); w.Uint32(0x0100007f); w.Uint32(2234); w.Uint32(0); w.Uint16(0) })
	seed(CodeConnectToPeer, func(w *Writer) {
		w.String("u")
		w.String("P")
		w.Uint32(0x0100007f)
		w.Uint32(2234)
		w.Uint32(5)
		w.Bool(false)
	})
	seed(CodeFileSearch, func(w *Writer) { w.String("u"); w.Uint32(5); w.String("query") })
	seed(CodeEmbeddedMessage, func(w *Writer) {
		w.Uint8(3)
		w.Raw(body(EncodeDistrib(DistribSearch{Unknown: 1, Username: "u", Token: 5, Query: "q"}))[1:])
	})
	seed(CodePossibleParents, func(w *Writer) { w.Uint32(1); w.String("p"); w.Uint32(0x0100007f); w.Uint32(2234) })
	seed(CodeExcludedSearchPhrases, func(w *Writer) { w.Uint32(2); w.String("a"); w.String("b") })
	f.Fuzz(func(t *testing.T, b []byte) {
		DecodeServer(b)
	})
}

func FuzzDecodeInit(f *testing.F) {
	f.Add(body(EncodeInit(PeerInit{Username: "u", Type: ConnPeer, Token: 1})))
	f.Add(body(EncodeInit(PierceFirewall{Token: 1})))
	f.Fuzz(func(t *testing.T, b []byte) {
		DecodeInit(b)
	})
}

func FuzzDecodeDistrib(f *testing.F) {
	for _, m := range []DistribMessage{
		DistribPing{},
		DistribSearch{Unknown: 1, Username: "u", Token: 5, Query: "q"},
		DistribBranchLevel{Level: 2},
		DistribBranchRoot{Root: "r"},
		DistribChildDepth{Depth: 1},
	} {
		f.Add(body(EncodeDistrib(m)))
	}
	f.Fuzz(func(t *testing.T, b []byte) {
		DecodeDistrib(b)
	})
}

func FuzzReadFrame(f *testing.F) {
	f.Add(Frame([]byte("hello")))
	f.Add([]byte{0xff, 0xff, 0xff, 0x7f, 1, 2, 3})
	f.Fuzz(func(t *testing.T, b []byte) {
		got, err := ReadFrame(bytes.NewReader(b), 1<<16)
		if err == nil && len(got)+4 > len(b) {
			t.Fatalf("frame of %d bytes from %d bytes of input", len(got), len(b))
		}
	})
}
