package protocol

import "fmt"

// Distributed message codes, used on connections of type
// ConnDistributed. Like peer init messages they carry a uint8 code.
const (
	CodeDistribPing       uint8 = 0
	CodeDistribSearch     uint8 = 3
	CodeDistribBranchLvl  uint8 = 4
	CodeDistribBranchRoot uint8 = 5
	CodeDistribChildDepth uint8 = 7
	CodeDistribEmbedded   uint8 = 93
)

// DistribMessage is a message on a distributed connection.
type DistribMessage interface {
	distribCode() uint8
	encode(w *Writer)
}

// EncodeDistrib frames a distributed message for the wire.
func EncodeDistrib(m DistribMessage) []byte {
	var w Writer
	w.Uint8(m.distribCode())
	m.encode(&w)
	return Frame(w.Bytes())
}

type DistribPing struct{}

func (DistribPing) distribCode() uint8 { return CodeDistribPing }
func (DistribPing) encode(*Writer)     {}

// DistribSearch is another user's search, passed down the tree.
type DistribSearch struct {
	Unknown  uint32
	Username string
	Token    uint32
	Query    string
}

func (DistribSearch) distribCode() uint8 { return CodeDistribSearch }
func (m DistribSearch) encode(w *Writer) {
	w.Uint32(m.Unknown)
	w.String(m.Username)
	w.Uint32(m.Token)
	w.String(m.Query)
}

// DistribBranchLevel is the sender's depth in the tree.
type DistribBranchLevel struct{ Level int32 }

func (DistribBranchLevel) distribCode() uint8 { return CodeDistribBranchLvl }
func (m DistribBranchLevel) encode(w *Writer) { w.Int32(m.Level) }

// DistribBranchRoot names the root of the sender's branch.
type DistribBranchRoot struct{ Root string }

func (DistribBranchRoot) distribCode() uint8 { return CodeDistribBranchRoot }
func (m DistribBranchRoot) encode(w *Writer) { w.String(m.Root) }

type DistribChildDepth struct{ Depth uint32 }

func (DistribChildDepth) distribCode() uint8 { return CodeDistribChildDepth }
func (m DistribChildDepth) encode(w *Writer) { w.Uint32(m.Depth) }

// DistribEmbedded wraps a distributed message the branch root received
// from the server.
type DistribEmbedded struct {
	Code    uint8
	Payload []byte
}

func (DistribEmbedded) distribCode() uint8 { return CodeDistribEmbedded }
func (m DistribEmbedded) encode(w *Writer) {
	w.Uint8(m.Code)
	w.Raw(m.Payload)
}

// DecodeDistrib decodes a distributed frame body (uint8 code + payload).
// Unknown codes yield an UnknownMessage.
func DecodeDistrib(body []byte) (any, error) {
	r := NewReader(body)
	code := r.Uint8()
	if r.Err() != nil {
		return nil, r.Err()
	}
	var m any
	switch code {
	case CodeDistribPing:
		m = DistribPing{}
	case CodeDistribSearch:
		m = DistribSearch{Unknown: r.Uint32(), Username: r.String(), Token: r.Uint32(), Query: r.String()}
	case CodeDistribBranchLvl:
		m = DistribBranchLevel{Level: r.Int32()}
	case CodeDistribBranchRoot:
		m = DistribBranchRoot{Root: r.String()}
	case CodeDistribChildDepth:
		m = DistribChildDepth{Depth: r.Uint32()}
	case CodeDistribEmbedded:
		m = DistribEmbedded{Code: r.Uint8(), Payload: r.Rest()}
	default:
		return UnknownMessage{Code: uint32(code), Payload: r.Rest()}, nil
	}
	if err := r.Err(); err != nil {
		return nil, fmt.Errorf("decode distributed message %d: %w", code, err)
	}
	return m, nil
}

// DecodeEmbedded decodes the distributed message inside a server or
// distributed EmbeddedMessage.
func DecodeEmbedded(code uint8, payload []byte) (any, error) {
	return DecodeDistrib(append([]byte{code}, payload...))
}
