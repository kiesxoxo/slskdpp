// Package protocol implements the Soulseek wire format: little-endian
// integers, length-prefixed strings, and length-prefixed message frames.
// It does no I/O policy; connection handling lives in internal/client.
package protocol

import (
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net/netip"
)

// ErrShort is returned when a payload ends before a field is fully read.
var ErrShort = errors.New("protocol: message too short")

// Writer builds a message payload.
type Writer struct {
	buf []byte
}

func (w *Writer) Uint8(v uint8) { w.buf = append(w.buf, v) }

func (w *Writer) Uint16(v uint16) { w.buf = binary.LittleEndian.AppendUint16(w.buf, v) }

func (w *Writer) Uint32(v uint32) { w.buf = binary.LittleEndian.AppendUint32(w.buf, v) }

func (w *Writer) Int32(v int32) { w.Uint32(uint32(v)) }

func (w *Writer) Uint64(v uint64) { w.buf = binary.LittleEndian.AppendUint64(w.buf, v) }

func (w *Writer) Bool(v bool) {
	if v {
		w.Uint8(1)
	} else {
		w.Uint8(0)
	}
}

func (w *Writer) String(s string) {
	w.Uint32(uint32(len(s)))
	w.buf = append(w.buf, s...)
}

// IP writes an IPv4 address the way Soulseek does: as a little-endian uint32
// whose most significant byte is the first octet.
func (w *Writer) IP(a netip.Addr) {
	b := a.As4()
	w.Uint32(binary.BigEndian.Uint32(b[:]))
}

func (w *Writer) Raw(b []byte) { w.buf = append(w.buf, b...) }

func (w *Writer) Bytes() []byte { return w.buf }

// Reset empties w, keeping its buffer for reuse.
func (w *Writer) Reset() { w.buf = w.buf[:0] }

func (w *Writer) Len() int { return len(w.buf) }

// Reader decodes a message payload. Errors are sticky: after the first
// failure every read returns a zero value and Err reports the failure.
type Reader struct {
	b   []byte
	off int
	err error
}

func NewReader(b []byte) *Reader { return &Reader{b: b} }

func (r *Reader) Err() error { return r.err }

// Remaining reports how many unread bytes are left.
func (r *Reader) Remaining() int { return len(r.b) - r.off }

func (r *Reader) take(n int) []byte {
	if r.err != nil {
		return nil
	}
	if n < 0 || r.Remaining() < n {
		r.err = ErrShort
		return nil
	}
	b := r.b[r.off : r.off+n]
	r.off += n
	return b
}

func (r *Reader) Uint8() uint8 {
	b := r.take(1)
	if b == nil {
		return 0
	}
	return b[0]
}

func (r *Reader) Uint16() uint16 {
	b := r.take(2)
	if b == nil {
		return 0
	}
	return binary.LittleEndian.Uint16(b)
}

func (r *Reader) Uint32() uint32 {
	b := r.take(4)
	if b == nil {
		return 0
	}
	return binary.LittleEndian.Uint32(b)
}

func (r *Reader) Int32() int32 { return int32(r.Uint32()) }

func (r *Reader) Uint64() uint64 {
	b := r.take(8)
	if b == nil {
		return 0
	}
	return binary.LittleEndian.Uint64(b)
}

func (r *Reader) Bool() bool { return r.Uint8() != 0 }

func (r *Reader) String() string {
	n := r.Uint32()
	return string(r.take(int(n)))
}

func (r *Reader) IP() netip.Addr {
	v := r.Uint32()
	var b [4]byte
	binary.BigEndian.PutUint32(b[:], v)
	return netip.AddrFrom4(b)
}

// Rest returns all unread bytes.
func (r *Reader) Rest() []byte { return r.take(r.Remaining()) }

// ReadFrame reads one length-prefixed frame and returns its body (the bytes
// after the length). Frames larger than max are rejected.
func ReadFrame(rd io.Reader, max uint32) ([]byte, error) {
	n, err := ReadFrameLen(rd, max)
	if err != nil {
		return nil, err
	}
	return ReadBody(rd, n)
}

// ReadFrameLen reads a frame's length prefix, rejecting lengths over max.
// The body follows; read it with ReadBody.
func ReadFrameLen(rd io.Reader, max uint32) (uint32, error) {
	var hdr [4]byte
	if _, err := io.ReadFull(rd, hdr[:]); err != nil {
		return 0, err
	}
	n := binary.LittleEndian.Uint32(hdr[:])
	if n > max {
		return 0, fmt.Errorf("protocol: frame of %d bytes exceeds limit %d", n, max)
	}
	return n, nil
}

// ReadBody reads n bytes of frame body.
func ReadBody(rd io.Reader, n uint32) ([]byte, error) {
	if n <= eagerFrame {
		body := make([]byte, n)
		if _, err := io.ReadFull(rd, body); err != nil {
			return nil, err
		}
		return body, nil
	}
	// Grow large frames as data arrives so a peer cannot make us allocate
	// a huge buffer just by announcing one.
	var buf bytes.Buffer
	buf.Grow(eagerFrame)
	if _, err := io.CopyN(&buf, rd, int64(n)); err != nil {
		if err == io.EOF {
			err = io.ErrUnexpectedEOF
		}
		return nil, err
	}
	return buf.Bytes(), nil
}

// eagerFrame is the largest frame ReadFrame allocates up front.
const eagerFrame = 1 << 20

// Frame prefixes body with its length.
func Frame(body []byte) []byte {
	out := make([]byte, 4, 4+len(body))
	binary.LittleEndian.PutUint32(out, uint32(len(body)))
	return append(out, body...)
}

// Compress zlib-compresses a payload, as used by browse and search responses.
func Compress(b []byte) []byte {
	var buf bytes.Buffer
	zw := zlib.NewWriter(&buf)
	zw.Write(b)
	zw.Close()
	return buf.Bytes()
}

// Decompress inflates a zlib payload, refusing output larger than max.
func Decompress(b []byte, max int64) ([]byte, error) {
	zr, err := zlib.NewReader(bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	defer zr.Close()
	out, err := io.ReadAll(io.LimitReader(zr, max+1))
	if err != nil {
		return nil, err
	}
	if int64(len(out)) > max {
		return nil, fmt.Errorf("protocol: decompressed payload exceeds %d bytes", max)
	}
	return out, nil
}
