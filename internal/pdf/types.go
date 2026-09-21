package pdf

// EntryKind describes how an xref entry points at an object.
type EntryKind int

const (
	KindUnset EntryKind = iota
	KindFree
	KindUncompressed // type 1: object at a byte offset
	KindCompressed   // type 2: object inside an object stream
)

func (k EntryKind) String() string {
	switch k {
	case KindFree:
		return "free"
	case KindUncompressed:
		return "uncompressed"
	case KindCompressed:
		return "compressed"
	default:
		return "unset"
	}
}

// CandidateOrigin separates "found as declared" from "found heuristically".
type CandidateOrigin string

const (
	OriginDeclared  CandidateOrigin = "declared"  // declared by an xref table/stream
	OriginHeuristic CandidateOrigin = "heuristic" // found by scanning raw bytes
	OriginObjStm    CandidateOrigin = "objstm"    // declared inside a verified object stream
	OriginFreeDecl  CandidateOrigin = "free"      // declared as a free entry
)

// CandidateStatus is the user-decision state of a candidate.
type CandidateStatus string

const (
	StatusPending   CandidateStatus = "pending"
	StatusConfirmed CandidateStatus = "confirmed"
	StatusRejected  CandidateStatus = "rejected"
)

// XRefEntry is one entry (one object version) inside an xref table/stream.
type XRefEntry struct {
	Num      int
	Gen      int
	Kind     EntryKind
	Offset   int // type 1: byte offset; type 2: index within object stream
	Stream   int // type 2: object number of the containing object stream
	FreeNext int // type 0: next free object number
}

// Candidate is one possible version of an object found during the scan.
type Candidate struct {
	ID         string          `json:"id"`
	Num        int             `json:"num"`
	Gen        int             `json:"gen"`
	Revision   int             `json:"revision"` // 0-based revision index (oldest=0)
	Origin     CandidateOrigin `json:"origin"`
	Offset     int             `json:"offset"`              // declared offset (uncompressed)
	ActualOff  int             `json:"actualOff"`           // offset where "N G obj" was really found
	End        int             `json:"end"`                 // end of the object bytes (exclusive)
	StreamObj  int             `json:"streamObj"`           // containing object stream (compressed)
	IndexInStm int             `json:"indexInStm"`          // index within the object stream
	ByteStart  int             `json:"byteStart"`           // raw byte range start (object); for objstm: offset in expanded content
	ByteEnd    int             `json:"byteEnd"`             // raw byte range end; for objstm: end in expanded content
	HostNum    int             `json:"hostNum,omitempty"`   // objstm host object number
	HostStart  int             `json:"hostStart,omitempty"` // objstm encoded stream start in file
	HostEnd    int             `json:"hostEnd,omitempty"`   // objstm encoded stream end in file
	HeaderOK   bool            `json:"headerOK"`            // declared offset points at "N G obj"
	Status     CandidateStatus `json:"status"`
	Verified   bool            `json:"verified"`  // stream length/filter/boundary verified
	Evidence   string          `json:"evidence"`  // human-readable evidence
	Contested  bool            `json:"contested"` // competes with another live candidate
	Value      *ParsedValue    `json:"value,omitempty"`
}

// ParsedValue is the parsed view of an object without rewriting any bytes.
type ParsedValue struct {
	Type       string            `json:"type"` // dictionary, stream, array, name, number, string, bool, null, ref
	Dict       map[string]string `json:"dict,omitempty"`
	StreamOff  int               `json:"streamOff,omitempty"`
	StreamLen  int               `json:"streamLen,omitempty"`
	Filters    []string          `json:"filters,omitempty"`
	Preview    string            `json:"preview,omitempty"`
	Expandable bool              `json:"expandable,omitempty"`
	Expanded   bool              `json:"expanded,omitempty"`
	ExpandNote string            `json:"expandNote,omitempty"`
}

// Reference is an edge "RefBy object (revision) -> target".
type Reference struct {
	FromNum    int    `json:"fromNum"`
	FromRev    int    `json:"fromRev"`
	ToNum      int    `json:"toNum"`
	ToGen      int    `json:"toGen"`
	Context    string `json:"context"` // dictionary key or "array" or "stream-content"
	FromOffset int    `json:"fromOffset"`
}

// Revision is one incremental update section.
type Revision struct {
	Index        int               `json:"index"` // 0 = oldest
	XRefOffset   int               `json:"xrefOffset"`
	XRefKind     string            `json:"xrefKind"` // table | stream
	XRefStart    int               `json:"xrefStart"`
	XRefEnd      int               `json:"xrefEnd"`    // end of parsed table dict / stream object
	SectionEnd   int               `json:"sectionEnd"` // end of startxref..%%EOF trailer tail
	Trailer      map[string]string `json:"trailer"`
	Entries      []XRefEntry       `json:"entries"`
	Prev         int               `json:"prev"`
	HasPrev      bool              `json:"hasPrev"`
	SegmentStart int               `json:"segmentStart"` // first object byte belonging to this revision
	SegmentEnd   int               `json:"segmentEnd"`   // end (exclusive) of this revision's section
}

// Diagnostic records one corruption/anomaly finding with evidence.
type Diagnostic struct {
	Code     string `json:"code"`
	Severity string `json:"severity"` // error | warning | info
	Revision int    `json:"revision"`
	Offset   int    `json:"offset"`
	Message  string `json:"message"`
	Evidence string `json:"evidence"`
}

// ScanResult is the full version graph extracted from raw bytes.
type ScanResult struct {
	Size         int            `json:"size"`
	Revisions    []Revision     `json:"revisions"`
	Candidates   []*Candidate   `json:"candidates"`
	References   []Reference    `json:"references"`
	Diagnostics  []Diagnostic   `json:"diagnostics"`
	TrailingData []TrailingBlob `json:"trailingData"`
	Limits       Limits         `json:"limits"`
	ScannedAt    string         `json:"scannedAt"`
}

// TrailingBlob is appended data not referenced by any xref.
type TrailingBlob struct {
	Start int    `json:"start"`
	End   int    `json:"end"`
	Hex   string `json:"hex"`
	ASCII string `json:"ascii"`
}

// Limits guard decompression and scanning effort.
type Limits struct {
	MaxExpandedBytes int `json:"maxExpandedBytes"`
	MaxScanObjects   int `json:"maxScanObjects"`
}

// DefaultLimits are conservative resource ceilings.
func DefaultLimits() Limits {
	return Limits{
		MaxExpandedBytes: 64 * 1024 * 1024,
		MaxScanObjects:   200_000,
	}
}
