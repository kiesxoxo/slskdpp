package protocol

import (
	"bytes"
	"reflect"
	"testing"
)

func TestDistribGolden(t *testing.T) {
	got := EncodeDistrib(DistribSearch{Unknown: 0, Username: "al", Token: 9, Query: "x"})
	want := []byte{
		20, 0, 0, 0, // length
		3,          // code
		0, 0, 0, 0, // unknown
		2, 0, 0, 0, 'a', 'l',
		9, 0, 0, 0,
		1, 0, 0, 0, 'x',
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("DistribSearch\n got %v\nwant %v", got, want)
	}
}

func TestDistribRoundTrip(t *testing.T) {
	inner := EncodeDistrib(DistribSearch{Username: "u", Token: 1, Query: "q"})
	for _, m := range []DistribMessage{
		DistribPing{},
		DistribSearch{Unknown: 49, Username: "bob", Token: 5, Query: "aphex twin"},
		DistribBranchLevel{Level: -1},
		DistribBranchRoot{Root: "root"},
		DistribChildDepth{Depth: 3},
		DistribEmbedded{Code: CodeDistribSearch, Payload: inner[5:]},
	} {
		frame := EncodeDistrib(m)
		got, err := DecodeDistrib(frame[4:])
		if err != nil {
			t.Fatalf("%T: %v", m, err)
		}
		if !reflect.DeepEqual(got, m) {
			t.Errorf("%T round trip\n got %+v\nwant %+v", m, got, m)
		}
	}
	emb := DistribEmbedded{Code: CodeDistribSearch, Payload: inner[5:]}
	m, err := DecodeEmbedded(emb.Code, emb.Payload)
	if err != nil || m.(DistribSearch).Query != "q" {
		t.Errorf("embedded search: %+v %v", m, err)
	}
	if u, err := DecodeDistrib([]byte{200}); err != nil || u.(UnknownMessage).Code != 200 {
		t.Errorf("unknown code: %+v %v", u, err)
	}
	if _, err := DecodeDistrib([]byte{CodeDistribSearch, 1}); err == nil {
		t.Error("truncated search decoded")
	}
}

func TestEncodedMatchesDirect(t *testing.T) {
	m := SharedFileListResponse{Dirs: []Directory{{Name: `@@a\b`, Files: []File{{Name: "x.mp3", Size: 1}}}}}
	if !bytes.Equal(EncodePeer(EncodePayload(m)), EncodePeer(m)) {
		t.Error("pre-encoded frame differs")
	}
}
