package protocol

import (
	"fmt"
	"strings"
)

// Peer message codes, used on connections of type ConnPeer.
const (
	CodeSharedFileListRequest  uint32 = 4
	CodeSharedFileListResponse uint32 = 5
	CodeFileSearchResponse     uint32 = 9
	CodeUserInfoRequest        uint32 = 15
	CodeUserInfoResponse       uint32 = 16
	CodeFolderContentsRequest  uint32 = 36
	CodeFolderContentsResponse uint32 = 37
	CodeTransferRequest        uint32 = 40
	CodeTransferResponse       uint32 = 41
	CodeQueueUpload            uint32 = 43
	CodePlaceInQueueResponse   uint32 = 44
	CodeUploadFailed           uint32 = 46
	CodeUploadDenied           uint32 = 50
	CodePlaceInQueueRequest    uint32 = 51
)

// MaxInflated caps the decompressed size of a browse, search or folder
// response.
const MaxInflated = 256 << 20

// File attribute codes.
const (
	AttrBitRate    uint32 = 0 // kbps
	AttrDuration   uint32 = 1 // seconds
	AttrVBR        uint32 = 2 // 0 or 1
	AttrSampleRate uint32 = 4 // Hz
	AttrBitDepth   uint32 = 5 // bits
)

// Transfer directions in TransferRequest.
const (
	TransferDownload uint32 = 0
	TransferUpload   uint32 = 1
)

type FileAttribute struct {
	Code  uint32
	Value uint32
}

// File is a shared file as listed in search, browse and folder responses.
type File struct {
	Name  string // full path, backslash separated
	Size  uint64
	Ext   string // often empty; derive the format from Name
	Attrs []FileAttribute
}

// Attr returns the value of the attribute with the given code.
func (f File) Attr(code uint32) (uint32, bool) {
	for _, a := range f.Attrs {
		if a.Code == code {
			return a.Value, true
		}
	}
	return 0, false
}

// FilePath returns the full remote path of a file listed in dir. Browse
// and folder responses list bare file names; search responses list full
// paths.
func FilePath(dir, name string) string {
	if strings.ContainsAny(name, `\/`) || dir == "" {
		return name
	}
	return dir + `\` + name
}

type Directory struct {
	Name  string
	Files []File
}

// minFileSize is the smallest encoding of a File, used to bound
// preallocation from counts read off the wire.
const minFileSize = 1 + 4 + 8 + 4 + 4

func writeFiles(w *Writer, files []File) {
	w.Uint32(uint32(len(files)))
	for _, f := range files {
		w.Uint8(1)
		w.String(f.Name)
		w.Uint64(f.Size)
		w.String(f.Ext)
		w.Uint32(uint32(len(f.Attrs)))
		for _, a := range f.Attrs {
			w.Uint32(a.Code)
			w.Uint32(a.Value)
		}
	}
}

func readFiles(r *Reader) []File {
	n := r.Uint32()
	if n == 0 {
		return nil
	}
	files := make([]File, 0, min(int(n), r.Remaining()/minFileSize))
	for i := uint32(0); i < n && r.Err() == nil; i++ {
		r.Uint8()
		f := File{Name: r.String(), Size: r.Uint64(), Ext: r.String()}
		na := r.Uint32()
		if na > 0 {
			f.Attrs = make([]FileAttribute, 0, min(int(na), r.Remaining()/8))
		}
		for j := uint32(0); j < na && r.Err() == nil; j++ {
			f.Attrs = append(f.Attrs, FileAttribute{Code: r.Uint32(), Value: r.Uint32()})
		}
		files = append(files, f)
	}
	return files
}

func writeDirs(w *Writer, dirs []Directory) {
	w.Uint32(uint32(len(dirs)))
	for _, d := range dirs {
		w.String(d.Name)
		writeFiles(w, d.Files)
	}
}

func readDirs(r *Reader) []Directory {
	n := r.Uint32()
	if n == 0 {
		return nil
	}
	dirs := make([]Directory, 0, min(int(n), r.Remaining()/8))
	for i := uint32(0); i < n && r.Err() == nil; i++ {
		dirs = append(dirs, Directory{Name: r.String(), Files: readFiles(r)})
	}
	return dirs
}

// PeerMessage is a message on a peer connection, in either direction.
type PeerMessage interface {
	peerCode() uint32
	encode(w *Writer)
}

// EncodePeer frames a peer message for the wire, compressing it if the
// message type is sent compressed.
func EncodePeer(m PeerMessage) []byte {
	var w Writer
	w.Uint32(m.peerCode())
	m.encode(&w)
	return Frame(w.Bytes())
}

// compressed encodes fill into a zlib payload appended to w.
func compressed(w *Writer, fill func(w *Writer)) {
	var inner Writer
	fill(&inner)
	w.Raw(Compress(inner.Bytes()))
}

type SharedFileListRequest struct{}

func (SharedFileListRequest) peerCode() uint32 { return CodeSharedFileListRequest }
func (SharedFileListRequest) encode(*Writer)   {}

// SharedFileListResponse is a user's whole share, sent compressed.
type SharedFileListResponse struct {
	Dirs        []Directory
	PrivateDirs []Directory
}

func (SharedFileListResponse) peerCode() uint32 { return CodeSharedFileListResponse }
func (m SharedFileListResponse) encode(w *Writer) {
	compressed(w, func(w *Writer) {
		writeDirs(w, m.Dirs)
		w.Uint32(0)
		writeDirs(w, m.PrivateDirs)
	})
}

// FileSearchResponse is a peer's answer to one of our searches, sent
// compressed.
type FileSearchResponse struct {
	Username     string
	Token        uint32
	Files        []File
	SlotFree     bool
	AvgSpeed     uint32 // bytes per second
	QueueLength  uint32
	PrivateFiles []File
}

func (FileSearchResponse) peerCode() uint32 { return CodeFileSearchResponse }
func (m FileSearchResponse) encode(w *Writer) {
	compressed(w, func(w *Writer) {
		w.String(m.Username)
		w.Uint32(m.Token)
		writeFiles(w, m.Files)
		w.Bool(m.SlotFree)
		w.Uint32(m.AvgSpeed)
		// Queue length followed by an always-zero uint32; some clients
		// read the pair as one uint64.
		w.Uint32(m.QueueLength)
		w.Uint32(0)
		writeFiles(w, m.PrivateFiles)
	})
}

type UserInfoRequest struct{}

func (UserInfoRequest) peerCode() uint32 { return CodeUserInfoRequest }
func (UserInfoRequest) encode(*Writer)   {}

type UserInfoResponse struct {
	Description     string
	Picture         []byte // nil when there is none
	UploadSlots     uint32
	QueueSize       uint32
	SlotsFree       bool
	UploadPermitted uint32
}

func (UserInfoResponse) peerCode() uint32 { return CodeUserInfoResponse }
func (m UserInfoResponse) encode(w *Writer) {
	w.String(m.Description)
	w.Bool(m.Picture != nil)
	if m.Picture != nil {
		w.String(string(m.Picture))
	}
	w.Uint32(m.UploadSlots)
	w.Uint32(m.QueueSize)
	w.Bool(m.SlotsFree)
	w.Uint32(m.UploadPermitted)
}

type FolderContentsRequest struct {
	Token  uint32
	Folder string
}

func (FolderContentsRequest) peerCode() uint32 { return CodeFolderContentsRequest }
func (m FolderContentsRequest) encode(w *Writer) {
	w.Uint32(m.Token)
	w.String(m.Folder)
}

// FolderContentsResponse lists a folder and its subfolders, sent
// compressed.
type FolderContentsResponse struct {
	Token  uint32
	Folder string
	Dirs   []Directory
}

func (FolderContentsResponse) peerCode() uint32 { return CodeFolderContentsResponse }
func (m FolderContentsResponse) encode(w *Writer) {
	compressed(w, func(w *Writer) {
		w.Uint32(m.Token)
		w.String(m.Folder)
		writeDirs(w, m.Dirs)
	})
}

type TransferRequest struct {
	Direction uint32
	Token     uint32
	Filename  string
	Size      uint64 // only sent for TransferUpload
}

func (TransferRequest) peerCode() uint32 { return CodeTransferRequest }
func (m TransferRequest) encode(w *Writer) {
	w.Uint32(m.Direction)
	w.Uint32(m.Token)
	w.String(m.Filename)
	if m.Direction == TransferUpload {
		w.Uint64(m.Size)
	}
}

type TransferResponse struct {
	Token   uint32
	Allowed bool
	Size    uint64 // optional, when allowing a download request
	Reason  string // when not allowed
}

func (TransferResponse) peerCode() uint32 { return CodeTransferResponse }
func (m TransferResponse) encode(w *Writer) {
	w.Uint32(m.Token)
	w.Bool(m.Allowed)
	switch {
	case m.Allowed && m.Size > 0:
		w.Uint64(m.Size)
	case !m.Allowed:
		w.String(m.Reason)
	}
}

type QueueUpload struct{ Filename string }

func (QueueUpload) peerCode() uint32   { return CodeQueueUpload }
func (m QueueUpload) encode(w *Writer) { w.String(m.Filename) }

type PlaceInQueueResponse struct {
	Filename string
	Place    uint32
}

func (PlaceInQueueResponse) peerCode() uint32 { return CodePlaceInQueueResponse }
func (m PlaceInQueueResponse) encode(w *Writer) {
	w.String(m.Filename)
	w.Uint32(m.Place)
}

type UploadFailed struct{ Filename string }

func (UploadFailed) peerCode() uint32   { return CodeUploadFailed }
func (m UploadFailed) encode(w *Writer) { w.String(m.Filename) }

type UploadDenied struct {
	Filename string
	Reason   string
}

func (UploadDenied) peerCode() uint32 { return CodeUploadDenied }
func (m UploadDenied) encode(w *Writer) {
	w.String(m.Filename)
	w.String(m.Reason)
}

type PlaceInQueueRequest struct{ Filename string }

func (PlaceInQueueRequest) peerCode() uint32   { return CodePlaceInQueueRequest }
func (m PlaceInQueueRequest) encode(w *Writer) { w.String(m.Filename) }

// DecodePeer decodes a peer message frame body (code + payload),
// decompressing payloads that are sent compressed. Unknown codes yield an
// UnknownMessage.
func DecodePeer(body []byte) (any, error) {
	r := NewReader(body)
	code := r.Uint32()
	if r.Err() != nil {
		return nil, r.Err()
	}
	switch code {
	case CodeSharedFileListResponse, CodeFileSearchResponse, CodeFolderContentsResponse:
		raw, err := Decompress(r.Rest(), MaxInflated)
		if err != nil {
			return nil, fmt.Errorf("decode peer message %d: %w", code, err)
		}
		r = NewReader(raw)
	}
	var m any
	switch code {
	case CodeSharedFileListRequest:
		m = SharedFileListRequest{}
	case CodeSharedFileListResponse:
		v := SharedFileListResponse{Dirs: readDirs(r)}
		if r.Remaining() >= 8 {
			r.Uint32()
			v.PrivateDirs = readDirs(r)
		}
		m = v
	case CodeFileSearchResponse:
		v := FileSearchResponse{Username: r.String(), Token: r.Uint32(), Files: readFiles(r)}
		v.SlotFree = r.Bool()
		v.AvgSpeed = r.Uint32()
		v.QueueLength = r.Uint32()
		if r.Remaining() >= 4 {
			r.Uint32()
		}
		if r.Remaining() >= 4 {
			v.PrivateFiles = readFiles(r)
		}
		m = v
	case CodeUserInfoRequest:
		m = UserInfoRequest{}
	case CodeUserInfoResponse:
		v := UserInfoResponse{Description: r.String()}
		if r.Bool() {
			v.Picture = []byte(r.String())
		}
		v.UploadSlots = r.Uint32()
		v.QueueSize = r.Uint32()
		v.SlotsFree = r.Bool()
		if r.Remaining() >= 4 {
			v.UploadPermitted = r.Uint32()
		}
		m = v
	case CodeFolderContentsRequest:
		m = FolderContentsRequest{Token: r.Uint32(), Folder: r.String()}
	case CodeFolderContentsResponse:
		m = FolderContentsResponse{Token: r.Uint32(), Folder: r.String(), Dirs: readDirs(r)}
	case CodeTransferRequest:
		v := TransferRequest{Direction: r.Uint32(), Token: r.Uint32(), Filename: r.String()}
		if v.Direction == TransferUpload {
			v.Size = r.Uint64()
		}
		m = v
	case CodeTransferResponse:
		v := TransferResponse{Token: r.Uint32(), Allowed: r.Bool()}
		switch {
		case v.Allowed && r.Remaining() >= 8:
			v.Size = r.Uint64()
		case !v.Allowed && r.Remaining() >= 4:
			v.Reason = r.String()
		}
		m = v
	case CodeQueueUpload:
		m = QueueUpload{Filename: r.String()}
	case CodePlaceInQueueResponse:
		m = PlaceInQueueResponse{Filename: r.String(), Place: r.Uint32()}
	case CodeUploadFailed:
		m = UploadFailed{Filename: r.String()}
	case CodeUploadDenied:
		m = UploadDenied{Filename: r.String(), Reason: r.String()}
	case CodePlaceInQueueRequest:
		m = PlaceInQueueRequest{Filename: r.String()}
	default:
		return UnknownMessage{Code: code, Payload: r.Rest()}, nil
	}
	if err := r.Err(); err != nil {
		return nil, fmt.Errorf("decode peer message %d: %w", code, err)
	}
	return m, nil
}
