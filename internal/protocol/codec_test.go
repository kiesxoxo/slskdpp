package protocol

import (
	"bytes"
	"encoding/binary"
	"errors"
	"net/netip"
	"testing"
)

func TestRoundTrip(t *testing.T) {
	var w Writer
	w.Uint8(7)
	w.Uint16(0xBEEF)
	w.Uint32(0xDEADBEEF)
	w.Int32(-2)
	w.Uint64(1 << 40)
	w.Bool(true)
	w.String("héllo")
	w.IP(netip.MustParseAddr("192.168.1.20"))

	r := NewReader(w.Bytes())
	if got := r.Uint8(); got != 7 {
		t.Errorf("Uint8 = %d", got)
	}
	if got := r.Uint16(); got != 0xBEEF {
		t.Errorf("Uint16 = %x", got)
	}
	if got := r.Uint32(); got != 0xDEADBEEF {
		t.Errorf("Uint32 = %x", got)
	}
	if got := r.Int32(); got != -2 {
		t.Errorf("Int32 = %d", got)
	}
	if got := r.Uint64(); got != 1<<40 {
		t.Errorf("Uint64 = %d", got)
	}
	if got := r.Bool(); !got {
		t.Errorf("Bool = false")
	}
	if got := r.String(); got != "héllo" {
		t.Errorf("String = %q", got)
	}
	if got := r.IP(); got != netip.MustParseAddr("192.168.1.20") {
		t.Errorf("IP = %v", got)
	}
	if r.Err() != nil || r.Remaining() != 0 {
		t.Errorf("err=%v remaining=%d", r.Err(), r.Remaining())
	}
}

func TestIPByteOrder(t *testing.T) {
	// Soulseek sends 1.2.3.4 as the little-endian uint32 0x01020304.
	var w Writer
	w.IP(netip.MustParseAddr("1.2.3.4"))
	if want := []byte{4, 3, 2, 1}; !bytes.Equal(w.Bytes(), want) {
		t.Fatalf("IP bytes = %v, want %v", w.Bytes(), want)
	}
}

func TestShortReadIsSticky(t *testing.T) {
	r := NewReader([]byte{5, 0, 0, 0, 'a', 'b'})
	if s := r.String(); s != "" {
		t.Errorf("String = %q, want empty on short read", s)
	}
	if !errors.Is(r.Err(), ErrShort) {
		t.Fatalf("Err = %v, want ErrShort", r.Err())
	}
	if v := r.Uint32(); v != 0 {
		t.Errorf("read after error = %d, want 0", v)
	}
}

func TestLoginGolden(t *testing.T) {
	got := EncodeServer(Login{Username: "user", Password: "pass", Version: 160, MinorVersion: 1})

	var body []byte
	body = binary.LittleEndian.AppendUint32(body, 1) // code
	body = append(body, 4, 0, 0, 0, 'u', 's', 'e', 'r')
	body = append(body, 4, 0, 0, 0, 'p', 'a', 's', 's')
	body = binary.LittleEndian.AppendUint32(body, 160)
	body = append(body, 32, 0, 0, 0)
	body = append(body, "63e780c3f321d13109c71bf81805476e"...)
	body = binary.LittleEndian.AppendUint32(body, 1)
	want := binary.LittleEndian.AppendUint32(nil, uint32(len(body)))
	want = append(want, body...)

	if !bytes.Equal(got, want) {
		t.Fatalf("Login frame\n got %x\nwant %x", got, want)
	}
}

func TestReadFrame(t *testing.T) {
	frame := Frame([]byte{1, 2, 3})
	body, err := ReadFrame(bytes.NewReader(frame), 16)
	if err != nil || !bytes.Equal(body, []byte{1, 2, 3}) {
		t.Fatalf("ReadFrame = %v, %v", body, err)
	}
	if _, err := ReadFrame(bytes.NewReader(Frame(make([]byte, 32))), 16); err == nil {
		t.Fatal("oversized frame accepted")
	}
}

func TestCompressRoundTrip(t *testing.T) {
	in := bytes.Repeat([]byte("soulseek "), 1000)
	out, err := Decompress(Compress(in), int64(len(in)))
	if err != nil || !bytes.Equal(in, out) {
		t.Fatalf("round trip failed: %v", err)
	}
	if _, err := Decompress(Compress(in), 10); err == nil {
		t.Fatal("decompression limit not enforced")
	}
}

func serverFrame(code uint32, fill func(w *Writer)) []byte {
	var w Writer
	w.Uint32(code)
	fill(&w)
	return w.Bytes()
}

func TestDecodeLogin(t *testing.T) {
	ok := serverFrame(CodeLogin, func(w *Writer) {
		w.Bool(true)
		w.String("welcome")
		w.IP(netip.MustParseAddr("10.0.0.5"))
		w.String("hash")
		w.Bool(true)
	})
	m, err := DecodeServer(ok)
	if err != nil {
		t.Fatal(err)
	}
	lr := m.(LoginResponse)
	if !lr.Success || lr.Greeting != "welcome" || lr.OwnIP != netip.MustParseAddr("10.0.0.5") || !lr.Supporter {
		t.Errorf("unexpected %+v", lr)
	}

	bad := serverFrame(CodeLogin, func(w *Writer) {
		w.Bool(false)
		w.String("INVALIDPASS")
	})
	m, err = DecodeServer(bad)
	if err != nil {
		t.Fatal(err)
	}
	if lr := m.(LoginResponse); lr.Success || lr.Reason != "INVALIDPASS" {
		t.Errorf("unexpected %+v", lr)
	}
}

func TestDecodeConnectToPeer(t *testing.T) {
	body := serverFrame(CodeConnectToPeer, func(w *Writer) {
		w.String("alice")
		w.String(ConnPeer)
		w.IP(netip.MustParseAddr("8.8.4.4"))
		w.Uint32(2234)
		w.Uint32(99)
		w.Bool(false)
		w.Uint32(0) // obfuscation type
		w.Uint32(0) // obfuscated port
	})
	m, err := DecodeServer(body)
	if err != nil {
		t.Fatal(err)
	}
	want := ConnectToPeerRequest{Username: "alice", Type: "P", IP: netip.MustParseAddr("8.8.4.4"), Port: 2234, Token: 99}
	if m.(ConnectToPeerRequest) != want {
		t.Errorf("got %+v, want %+v", m, want)
	}
}

func TestDecodeUnknownAndTruncated(t *testing.T) {
	m, err := DecodeServer(serverFrame(9999, func(w *Writer) { w.Uint32(1) }))
	if err != nil {
		t.Fatal(err)
	}
	if u, ok := m.(UnknownMessage); !ok || u.Code != 9999 {
		t.Errorf("got %#v", m)
	}
	if _, err := DecodeServer(serverFrame(CodeFileSearch, func(w *Writer) { w.String("bob") })); err == nil {
		t.Error("truncated FileSearch decoded without error")
	}
}
