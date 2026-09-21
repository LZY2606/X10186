package pdf

// report.go: 扫描结果模型。所有结构仅描述原件中的证据，不代表修复结果。

// DiagLevel 诊断级别。
type DiagLevel string

const (
	DiagInfo  DiagLevel = "info"
	DiagWarn  DiagLevel = "warn"
	DiagError DiagLevel = "error"
)

// Diagnostic 损坏/异常诊断，保留偏移与证据说明。
type Diagnostic struct {
	Level    DiagLevel `json:"level"`
	Code     string    `json:"code"`
	Offset   int       `json:"offset"`
	Message  string    `json:"message"`
	Evidence string    `json:"evidence"`
}

// CandidateKind 区分“按声明找到”与“启发式找到”。
type CandidateKind string

const (
	CandDeclared  CandidateKind = "declared"
	CandHeuristic CandidateKind = "heuristic"
)

// Candidate 是某个对象在某个修订中的一个候选版本。
type Candidate struct {
	ID          int           `json:"id"`
	Kind        CandidateKind `json:"kind"`
	ObjNum      int           `json:"objNum"`
	Gen         int           `json:"gen"`
	DeclGen     int           `json:"declGen,omitempty"` // xref 声明代次
	Revision    int           `json:"revision"`          // 所属修订（-1=尾部未归属）
	Offset      int           `json:"offset"`            // 原件起始（压缩对象为 -1）
	EndOffset   int           `json:"endOffset"`
	Valid       bool          `json:"valid"` // 对象头/边界是否核验通过
	Free        bool          `json:"free"`  // free entry
	FreeNext    int           `json:"freeNext,omitempty"`
	Reason      string        `json:"reason"`
	Evidence    string        `json:"evidence"`
	Stream      bool          `json:"stream"`
	StreamOff   int           `json:"streamOff,omitempty"`
	StreamLen   int           `json:"streamLen,omitempty"` // 核验后的长度，-1 未核验
	Filters     []string      `json:"filters,omitempty"`
	Expanded    bool          `json:"expanded,omitempty"`
	ExpandedLen int           `json:"expandedLen,omitempty"`
	ExpandNote  string        `json:"expandNote,omitempty"` // 未展开原因/超限说明
	// 压缩在 object stream 内的对象（xref 类型 2）
	ContainerObj  int    `json:"containerObj,omitempty"`
	ContainerCand int    `json:"containerCand,omitempty"`
	InnerIndex    int    `json:"innerIndex,omitempty"`
	InnerOff      int    `json:"innerOff,omitempty"`
	InnerLen      int    `json:"innerLen,omitempty"`
	Parsed        *Value `json:"parsed,omitempty"`
	ParseError    string `json:"parseError,omitempty"`
	InObjStm      bool   `json:"inObjStm,omitempty"`
}

// IsCompressed 是否为 object stream 内的压缩对象。
func (c *Candidate) IsCompressed() bool { return c.ContainerObj != 0 }

// Revision 一次增量修订（按 previous 链还原，index 0 为最旧）。
type Revision struct {
	Index       int    `json:"index"`
	Kind        string `json:"kind"` // table / stream
	XRefOffset  int    `json:"xrefOffset"`
	Prev        int    `json:"prev"`
	RegionStart int    `json:"regionStart"` // 该修订新增对象区起点
	RegionEnd   int    `json:"regionEnd"`   // xref 段起点
	BlockEnd    int    `json:"blockEnd"`    // 含 startxref/%%EOF 的段尾
	Size        int    `json:"size"`
	RootNum     int    `json:"rootNum,omitempty"`
	RootGen     int    `json:"rootGen,omitempty"`
}

// RefEdge 对象引用关系（含证据偏移）。
type RefEdge struct {
	FromObj      int    `json:"fromObj"` // 0 表示 trailer/系统
	FromCand     int    `json:"fromCand"`
	ToObj        int    `json:"toObj"`
	ToGen        int    `json:"toGen"`
	Offset       int    `json:"offset"` // 在原件或解压流中的字节偏移
	InExpanded   bool   `json:"inExpanded,omitempty"`
	ContainerObj int    `json:"containerObj,omitempty"`
	Context      string `json:"context"`
}

// PageInfo 页树解析结果（信息性展示）。
type PageInfo struct {
	Index    int `json:"index"`
	ObjNum   int `json:"objNum"`
	Gen      int `json:"gen"`
	Revision int `json:"revision"`
}

// Report 一次完整探针扫描的结果。
type Report struct {
	FileName     string        `json:"fileName,omitempty"`
	FileSize     int           `json:"fileSize"`
	PDFVersion   string        `json:"pdfVersion"`
	HeaderOK     bool          `json:"headerOK"`
	StartXRef    int           `json:"startXref"`
	StartXRefOK  bool          `json:"startXrefOK"`
	Revisions    []*Revision   `json:"revisions"`
	Candidates   []*Candidate  `json:"candidates"`
	Edges        []*RefEdge    `json:"edges"`
	Diagnostics  []*Diagnostic `json:"diagnostics"`
	Pages        []*PageInfo   `json:"pages"`
	TrailingFrom int           `json:"trailingFrom"`
	TrailingLen  int           `json:"trailingLen"`
	MixedXRef    bool          `json:"mixedXref"`
}

// Limits 解压与扫描资源限额。
type Limits struct {
	MaxExpand      int // 单个流解压后最大字节数
	MaxScanObjects int // 启发式对象头数量上限
}

// DefaultLimits 默认限额。
func DefaultLimits() Limits {
	return Limits{MaxExpand: 64 << 20, MaxScanObjects: 200000}
}
