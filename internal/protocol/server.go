package protocol

import (
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"net/netip"
)

// Server message codes.
const (
	CodeLogin                 uint32 = 1
	CodeSetWaitPort           uint32 = 2
	CodeGetPeerAddress        uint32 = 3
	CodeWatchUser             uint32 = 5
	CodeUnwatchUser           uint32 = 6
	CodeGetUserStatus         uint32 = 7
	CodeConnectToPeer         uint32 = 18
	CodeFileSearch            uint32 = 26
	CodeSetStatus             uint32 = 28
	CodeServerPing            uint32 = 32
	CodeSharedFoldersFiles    uint32 = 35
	CodeRelogged              uint32 = 41
	CodeAdminMessage          uint32 = 66
	CodeHaveNoParent          uint32 = 71
	CodeParentMinSpeed        uint32 = 83
	CodeParentSpeedRatio      uint32 = 84
	CodeCheckPrivileges       uint32 = 92
	CodeEmbeddedMessage       uint32 = 93
	CodeAcceptChildren        uint32 = 100
	CodePossibleParents       uint32 = 102
	CodeWishlistInterval      uint32 = 104
	CodeBranchLevel           uint32 = 126
	CodeBranchRoot            uint32 = 127
	CodeResetDistributed      uint32 = 130
	CodeExcludedSearchPhrases uint32 = 160
	CodeCantConnectToPeer     uint32 = 1001
)

// User presence as reported by the server.
type UserStatus uint32

const (
	StatusOffline UserStatus = 0
	StatusAway    UserStatus = 1
	StatusOnline  UserStatus = 2
)

func (s UserStatus) String() string {
	switch s {
	case StatusOffline:
		return "offline"
	case StatusAway:
		return "away"
	case StatusOnline:
		return "online"
	}
	return fmt.Sprintf("status(%d)", uint32(s))
}

// Connection types used in PeerInit and ConnectToPeer.
const (
	ConnPeer        = "P"
	ConnFile        = "F"
	ConnDistributed = "D"
)

// ServerRequest is a message sent from the client to the server.
type ServerRequest interface {
	serverCode() uint32
	encode(w *Writer)
}

// EncodeServer frames a client-to-server message for the wire.
func EncodeServer(m ServerRequest) []byte {
	var w Writer
	w.Uint32(m.serverCode())
	m.encode(&w)
	return Frame(w.Bytes())
}

// Client-to-server messages.

type Login struct {
	Username     string
	Password     string
	Version      uint32
	MinorVersion uint32
}

func (Login) serverCode() uint32 { return CodeLogin }
func (m Login) encode(w *Writer) {
	sum := md5.Sum([]byte(m.Username + m.Password))
	w.String(m.Username)
	w.String(m.Password)
	w.Uint32(m.Version)
	w.String(hex.EncodeToString(sum[:]))
	w.Uint32(m.MinorVersion)
}

type SetWaitPort struct{ Port uint32 }

func (SetWaitPort) serverCode() uint32 { return CodeSetWaitPort }
func (m SetWaitPort) encode(w *Writer) { w.Uint32(m.Port) }

type GetPeerAddress struct{ Username string }

func (GetPeerAddress) serverCode() uint32 { return CodeGetPeerAddress }
func (m GetPeerAddress) encode(w *Writer) { w.String(m.Username) }

type WatchUser struct{ Username string }

func (WatchUser) serverCode() uint32 { return CodeWatchUser }
func (m WatchUser) encode(w *Writer) { w.String(m.Username) }

type UnwatchUser struct{ Username string }

func (UnwatchUser) serverCode() uint32 { return CodeUnwatchUser }
func (m UnwatchUser) encode(w *Writer) { w.String(m.Username) }

type GetUserStatus struct{ Username string }

func (GetUserStatus) serverCode() uint32 { return CodeGetUserStatus }
func (m GetUserStatus) encode(w *Writer) { w.String(m.Username) }

// ConnectToPeer asks the server to tell Username to connect to us.
type ConnectToPeer struct {
	Token    uint32
	Username string
	Type     string
}

func (ConnectToPeer) serverCode() uint32 { return CodeConnectToPeer }
func (m ConnectToPeer) encode(w *Writer) {
	w.Uint32(m.Token)
	w.String(m.Username)
	w.String(m.Type)
}

type FileSearch struct {
	Token uint32
	Query string
}

func (FileSearch) serverCode() uint32 { return CodeFileSearch }
func (m FileSearch) encode(w *Writer) {
	w.Uint32(m.Token)
	w.String(m.Query)
}

type SetStatus struct{ Status UserStatus }

func (SetStatus) serverCode() uint32 { return CodeSetStatus }
func (m SetStatus) encode(w *Writer) { w.Int32(int32(m.Status)) }

type ServerPing struct{}

func (ServerPing) serverCode() uint32 { return CodeServerPing }
func (ServerPing) encode(*Writer)     {}

type SharedFoldersFiles struct{ Folders, Files uint32 }

func (SharedFoldersFiles) serverCode() uint32 { return CodeSharedFoldersFiles }
func (m SharedFoldersFiles) encode(w *Writer) {
	w.Uint32(m.Folders)
	w.Uint32(m.Files)
}

type HaveNoParent struct{ NoParent bool }

func (HaveNoParent) serverCode() uint32 { return CodeHaveNoParent }
func (m HaveNoParent) encode(w *Writer) { w.Bool(m.NoParent) }

type AcceptChildren struct{ Accept bool }

func (AcceptChildren) serverCode() uint32 { return CodeAcceptChildren }
func (m AcceptChildren) encode(w *Writer) { w.Bool(m.Accept) }

type SetBranchLevel struct{ Level uint32 }

func (SetBranchLevel) serverCode() uint32 { return CodeBranchLevel }
func (m SetBranchLevel) encode(w *Writer) { w.Uint32(m.Level) }

type SetBranchRoot struct{ Root string }

func (SetBranchRoot) serverCode() uint32 { return CodeBranchRoot }
func (m SetBranchRoot) encode(w *Writer) { w.String(m.Root) }

// CantConnectToPeerRequest tells the server we failed to answer a
// ConnectToPeer so it can notify the other side.
type CantConnectToPeerRequest struct {
	Token    uint32
	Username string
}

func (CantConnectToPeerRequest) serverCode() uint32 { return CodeCantConnectToPeer }
func (m CantConnectToPeerRequest) encode(w *Writer) {
	w.Uint32(m.Token)
	w.String(m.Username)
}

// Server-to-client messages.

type LoginResponse struct {
	Success   bool
	Greeting  string
	OwnIP     netip.Addr
	Supporter bool
	Reason    string // set on failure, e.g. INVALIDPASS
	Detail    string
}

type PeerAddress struct {
	Username string
	IP       netip.Addr
	Port     uint32
}

// Unknown reports whether the server could not resolve the user; it then
// returns 0.0.0.0 and port 0.
func (p PeerAddress) Unknown() bool { return p.Port == 0 || !p.IP.IsValid() || p.IP.IsUnspecified() }

type WatchUserResponse struct {
	Username string
	Exists   bool
	Status   UserStatus
	AvgSpeed uint32
	Uploads  uint32
	Files    uint32
	Folders  uint32
	Country  string
}

type UserStatusResponse struct {
	Username   string
	Status     UserStatus
	Privileged bool
}

// ConnectToPeerRequest means Username wants us to connect to them.
type ConnectToPeerRequest struct {
	Username   string
	Type       string
	IP         netip.Addr
	Port       uint32
	Token      uint32
	Privileged bool
}

// SearchRequest is a search forwarded to us by the server.
type SearchRequest struct {
	Username string
	Token    uint32
	Query    string
}

type Ping struct{}

// Relogged means another client logged in with our account.
type Relogged struct{}

type AdminMessage struct{ Text string }

type ParentMinSpeed struct{ Speed uint32 }

type ParentSpeedRatio struct{ Ratio uint32 }

type CheckPrivileges struct{ Seconds uint32 }

// EmbeddedMessage carries a distributed message from the server when we
// are a branch root.
type EmbeddedMessage struct {
	DistribCode uint8
	Payload     []byte
}

type PossibleParent struct {
	Username string
	IP       netip.Addr
	Port     uint32
}

type PossibleParents struct{ Parents []PossibleParent }

type WishlistInterval struct{ Seconds uint32 }

type ResetDistributed struct{}

type ExcludedSearchPhrases struct{ Phrases []string }

// CantConnectToPeer means a peer could not answer our ConnectToPeer.
type CantConnectToPeer struct{ Token uint32 }

// UnknownMessage is any message this package does not decode.
type UnknownMessage struct {
	Code    uint32
	Payload []byte
}

// DecodeServer decodes a server-to-client frame body (code + payload).
func DecodeServer(body []byte) (any, error) {
	r := NewReader(body)
	code := r.Uint32()
	if r.Err() != nil {
		return nil, r.Err()
	}
	var m any
	switch code {
	case CodeLogin:
		v := LoginResponse{Success: r.Bool()}
		if v.Success {
			v.Greeting = r.String()
			v.OwnIP = r.IP()
			if r.Remaining() > 0 {
				_ = r.String() // md5 of our password, unused
			}
			if r.Remaining() > 0 {
				v.Supporter = r.Bool()
			}
		} else {
			v.Reason = r.String()
			if r.Remaining() > 0 {
				v.Detail = r.String()
			}
		}
		m = v
	case CodeGetPeerAddress:
		m = PeerAddress{Username: r.String(), IP: r.IP(), Port: r.Uint32()}
		r.Rest() // obfuscation fields
	case CodeWatchUser:
		v := WatchUserResponse{Username: r.String(), Exists: r.Bool()}
		if v.Exists {
			v.Status = UserStatus(r.Uint32())
			v.AvgSpeed = r.Uint32()
			v.Uploads = r.Uint32()
			r.Uint32()
			v.Files = r.Uint32()
			v.Folders = r.Uint32()
			if r.Remaining() > 0 {
				v.Country = r.String()
			}
		}
		m = v
	case CodeGetUserStatus:
		v := UserStatusResponse{Username: r.String(), Status: UserStatus(r.Uint32())}
		if r.Remaining() > 0 {
			v.Privileged = r.Bool()
		}
		m = v
	case CodeConnectToPeer:
		v := ConnectToPeerRequest{
			Username: r.String(),
			Type:     r.String(),
			IP:       r.IP(),
			Port:     r.Uint32(),
			Token:    r.Uint32(),
		}
		if r.Remaining() > 0 {
			v.Privileged = r.Bool()
		}
		r.Rest()
		m = v
	case CodeFileSearch:
		m = SearchRequest{Username: r.String(), Token: r.Uint32(), Query: r.String()}
	case CodeServerPing:
		m = Ping{}
	case CodeRelogged:
		m = Relogged{}
	case CodeAdminMessage:
		m = AdminMessage{Text: r.String()}
	case CodeParentMinSpeed:
		m = ParentMinSpeed{Speed: r.Uint32()}
	case CodeParentSpeedRatio:
		m = ParentSpeedRatio{Ratio: r.Uint32()}
	case CodeCheckPrivileges:
		m = CheckPrivileges{Seconds: r.Uint32()}
	case CodeEmbeddedMessage:
		m = EmbeddedMessage{DistribCode: r.Uint8(), Payload: r.Rest()}
	case CodePossibleParents:
		n := r.Uint32()
		v := PossibleParents{}
		for i := uint32(0); i < n && r.Err() == nil; i++ {
			v.Parents = append(v.Parents, PossibleParent{Username: r.String(), IP: r.IP(), Port: r.Uint32()})
		}
		m = v
	case CodeWishlistInterval:
		m = WishlistInterval{Seconds: r.Uint32()}
	case CodeResetDistributed:
		m = ResetDistributed{}
	case CodeExcludedSearchPhrases:
		n := r.Uint32()
		v := ExcludedSearchPhrases{}
		for i := uint32(0); i < n && r.Err() == nil; i++ {
			v.Phrases = append(v.Phrases, r.String())
		}
		m = v
	case CodeCantConnectToPeer:
		m = CantConnectToPeer{Token: r.Uint32()}
	default:
		return UnknownMessage{Code: code, Payload: r.Rest()}, nil
	}
	if err := r.Err(); err != nil {
		return nil, fmt.Errorf("decode server message %d: %w", code, err)
	}
	return m, nil
}
