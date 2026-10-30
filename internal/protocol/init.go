package protocol

import "fmt"

// Peer init message codes. Unlike every other channel these frames carry a
// uint8 code, and each connection starts with exactly one of them.
const (
	CodePierceFirewall uint8 = 0
	CodePeerInit       uint8 = 1
)

// InitMessage is the first message on a new peer connection.
type InitMessage interface {
	initCode() uint8
	encode(w *Writer)
}

// EncodeInit frames a peer init message for the wire.
func EncodeInit(m InitMessage) []byte {
	var w Writer
	w.Uint8(m.initCode())
	m.encode(&w)
	return Frame(w.Bytes())
}

// PierceFirewall answers a ConnectToPeer: it is sent on a connection we
// opened because the server told us the other side could not reach us.
type PierceFirewall struct{ Token uint32 }

func (PierceFirewall) initCode() uint8    { return CodePierceFirewall }
func (m PierceFirewall) encode(w *Writer) { w.Uint32(m.Token) }

// PeerInit opens a direct connection of the given type (ConnPeer, ConnFile
// or ConnDistributed).
type PeerInit struct {
	Username string
	Type     string
	Token    uint32
}

func (PeerInit) initCode() uint8 { return CodePeerInit }
func (m PeerInit) encode(w *Writer) {
	w.String(m.Username)
	w.String(m.Type)
	w.Uint32(m.Token)
}

// DecodeInit decodes a peer init frame body (uint8 code + payload).
func DecodeInit(body []byte) (InitMessage, error) {
	r := NewReader(body)
	code := r.Uint8()
	var m InitMessage
	switch code {
	case CodePierceFirewall:
		m = PierceFirewall{Token: r.Uint32()}
	case CodePeerInit:
		m = PeerInit{Username: r.String(), Type: r.String(), Token: r.Uint32()}
	default:
		if r.Err() == nil {
			return nil, fmt.Errorf("protocol: unknown peer init code %d", code)
		}
	}
	if err := r.Err(); err != nil {
		return nil, fmt.Errorf("decode peer init message %d: %w", code, err)
	}
	return m, nil
}
