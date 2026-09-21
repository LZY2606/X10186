package pdfscan

// EntryType classifies an xref entry.
type EntryType string

const (
	EntryFree       EntryType = "free"
	EntryInUse      EntryType = "inuse"
	EntryCompressed EntryType = "compressed"
)

// XRefEntry is a single xref entry (from a table or a decoded stream).
type XRefEntry struct {
	ObjNum int
	Gen    int
	Type   EntryType
	Offset int64 // in-use: byte offset; free: next free object number
	ObjStm int   // compressed: object stream number
	Index  int   // compressed: index within the object stream
}

// Revision is one xref section reached via the startxref//Prev chain.
type Revision struct {
	Index      int // 0 = oldest
	Kind       string
	XRefOffset int64
	PrevOffset int64
	HasPrev    bool
	Trailer    Dict
	Entries    []XRefEntry
}

// StreamInfo describes the "stream ... endstream" payload of an object.
type StreamInfo struct {
	Dict           Dict
	DataOffset     int64
	DataLength     int64
	LengthVerified bool
	Filter         string
	Expanded       bool
	ExpandError    string
}

// ObjectVersion is one version of an indirect object in one revision.
type ObjectVersion struct {
	ObjNum         int
	Gen            int
	Revision       int
	Active         bool // latest visible version for (ObjNum, Gen)
	Offset         int64
	EndOffset      int64
	Source         string // declared-table | declared-stream | declared-objstm
	Value          any
	ValueText      string
	Stream         *StreamInfo
	Container      int // object stream number, 0 = none
	ContainerIndex int
	MemberOff      int64 // offset within decompressed object stream
	MemberLen      int64
	Notes          []string
}

// ReferenceEdge is a "from object references to object" relation.
type ReferenceEdge struct {
	FromObj int
	FromGen int
	ToObj   int
	ToGen   int
}

// Diagnostic is a corruption/anomaly finding.
type Diagnostic struct {
	Severity string // error | warning | info
	Code     string
	Message  string
	Offset   int64
	Revision int // -1 when not revision-specific
}

// Candidate is a recovery candidate for an object version.
type Candidate struct {
	ID        int64 // assigned by the store
	ObjNum    int
	Gen       int
	Offset    int64
	EndOffset int64
	FoundBy   string // declared | heuristic
	Evidence  []string
	ValueText string
}

// ScanResult is the full outcome of scanning one PDF file.
type ScanResult struct {
	Revisions     []Revision
	Objects       []ObjectVersion
	References    []ReferenceEdge
	Diagnostics   []Diagnostic
	Candidates    []Candidate
	TrailingBytes int64
	EOFOffset     int64
}
