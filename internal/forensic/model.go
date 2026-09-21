package forensic

// Key identifies a PDF object version by number and generation.
type Key struct {
	Num int `json:"num"`
	Gen int `json:"gen"`
}

// ByteRange is a half-open byte interval inside the original file.
type ByteRange struct {
	Start int64 `json:"start"`
	End   int64 `json:"end"`
}

// Diagnostic is a single structural finding with an evidence pointer.
type Diagnostic struct {
	Code     string    `json:"code"`
	Severity string    `json:"severity"` // error | warning | info
	Message  string    `json:"message"`
	Revision int       `json:"revision"`
	At       ByteRange `json:"at"`
	Evidence string    `json:"evidence"`
}

// XRefEntry is one row of a cross-reference table/stream, as declared.
type XRefEntry struct {
	Num      int       `json:"num"`
	Gen      int       `json:"gen"`
	Type     int       `json:"type"` // 0 free, 1 uncompressed, 2 compressed
	Offset   int64     `json:"offset"`
	Stream   int       `json:"stream"`
	Index    int       `json:"index"`
	Declared ByteRange `json:"declared"` // bytes of the entry itself
}

// Revision is one link in the startxref/Prev chain, strictly as found.
type Revision struct {
	Index       int         `json:"index"` // 0 = oldest, N-1 = newest
	Kind        string      `json:"kind"`  // table | stream | broken
	XRefOffset  int64       `json:"xref_offset"`
	PrevOffset  int64       `json:"prev_offset"`
	PrevPresent bool        `json:"prev_present"`
	Size        int         `json:"size"`
	Root        *Key        `json:"root,omitempty"`
	Entries     []XRefEntry `json:"entries"`
	TrailerDict ByteRange   `json:"trailer_dict"` // dict bytes location
	EOFStart    int64       `json:"eof_start"`
	EOFEnd      int64       `json:"eof_end"`
	ChainNote   string      `json:"chain_note,omitempty"`
}

// Candidate is a possible version/body of an object. Declared and heuristic
// candidates are kept separate; the system never silently merges them.
type Candidate struct {
	ID       string `json:"id"`
	Num      int    `json:"num"`
	Gen      int    `json:"gen"`
	Revision int    `json:"revision"` // revision that introduces/claims it

	Origin string `json:"origin"` // declared | heuristic
	Source string `json:"source"` // table | xref-stream | object-stream | header-scan
	Reason string `json:"reason"`

	ByteRange      ByteRange  `json:"byte_range"` // for type-2: header range inside decoded ObjStm
	Compressed     bool       `json:"compressed"`
	Container      *Key       `json:"container,omitempty"`
	ObjIndex       int        `json:"obj_index,omitempty"`       // position within an ObjStm
	ContainerRange *ByteRange `json:"container_range,omitempty"` // physical bytes of ObjStm

	HeaderOK bool   `json:"header_ok"` // verified "N G obj ... endobj"
	Note     string `json:"note,omitempty"`

	Node   *Node       `json:"node,omitempty"`
	Stream *StreamInfo `json:"stream,omitempty"`

	// MemberRaw holds decoded ObjStm member bytes; not persisted to JSON.
	MemberRaw []byte `json:"-"`
}

// StreamInfo records boundary verification of a physical stream.
type StreamInfo struct {
	KeywordStart int64    `json:"keyword_start"`
	DataStart    int64    `json:"data_start"`
	DataEnd      int64    `json:"data_end"`
	LengthDecl   int64    `json:"length_decl"`
	LengthActual int64    `json:"length_actual"`
	LengthOK     bool     `json:"length_ok"`
	Filters      []string `json:"filters"`
	BoundaryOK   bool     `json:"boundary_ok"` // endstream token verified
	DecodeError  string   `json:"decode_error,omitempty"`
	Expanded     bool     `json:"expanded"`
	ExpandedSize int64    `json:"expanded_size,omitempty"`
	Truncated    bool     `json:"truncated,omitempty"`
}

// FreeEntry tracks a generation-0 free slot as it moves through revisions.
type FreeEntry struct {
	Num        int `json:"num"`
	NextFree   int `json:"next_free"`
	Generation int `json:"generation"`
	Revision   int `json:"revision"`
}

// RefEdge is a directed reference between object versions.
type RefEdge struct {
	From  Key       `json:"from"`
	To    Key       `json:"to"`
	Where ByteRange `json:"where"`
	Kind  string    `json:"kind"` // value | type2-container
}

// Ambiguity groups candidates competing for the same slot.
type Ambiguity struct {
	Key       Key      `json:"key"`
	Revision  int      `json:"revision"`
	IDs       []string `json:"ids"`
	DefaultID string   `json:"default_id"`
	Note      string   `json:"note"`
}

// Report is the full forensic result of one scan. It never mutates the file.
type Report struct {
	FileName    string        `json:"file_name"`
	Size        int64         `json:"size"`
	SHA256      string        `json:"sha256"`
	PDFHeader   ByteRange     `json:"pdf_header"`
	StartXRefs  []int64       `json:"startxrefs"` // every literal startxref in file order
	Revisions   []Revision    `json:"revisions"`  // index 0 = oldest
	Candidates  []*Candidate  `json:"candidates"`
	Ambiguities []Ambiguity   `json:"ambiguities"`
	Frees       []FreeEntry   `json:"free_entries"`
	Diagnostics []*Diagnostic `json:"diagnostics"`
	Edges       []RefEdge     `json:"edges"`
	Trailing    ByteRange     `json:"trailing"` // bytes after last %%EOF
	Covered     []ByteRange   `json:"covered"`
	Gaps        []ByteRange   `json:"gaps"`
	Limits      LimitReport   `json:"limits"`
}

// LimitReport summarizes resource-limit behavior during a scan.
type LimitReport struct {
	MaxExpand  int64  `json:"max_expand"`
	Halted     bool   `json:"halted"`
	HaltReason string `json:"halt_reason,omitempty"`
}
