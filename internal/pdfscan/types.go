package pdfscan

// ByteRange 是对象或片段在原始文件中的半开字节区间 [Start,End)。
type ByteRange struct {
	Start int64 `json:"start"`
	End   int64 `json:"end"`
}

func (r ByteRange) Len() int64 { return r.End - r.Start }

// Evidence 记录一条发现的证据：在哪个字节偏移、以何种方式找到。
type Evidence struct {
	Kind    string    `json:"kind"`              // startxref | xref-table-entry | xref-stream-entry | object-header | prev-chain | trailer | fallback | limit
	Offset  int64     `json:"offset"`            // 声明或定位到的字节偏移
	Range   ByteRange `json:"range"`             // 证据覆盖的原始字节范围
	Detail  string    `json:"detail"`            // 人读说明
	Declared bool     `json:"declared"`          // 是否来自 PDF 自身声明（vs 启发式）
	Verified bool     `json:"verified"`          // 声明的内容是否被字节级核验通过
}

// Problem 是一条损坏/异常诊断。
type Problem struct {
	Severity string `json:"severity"` // error | warning | info
	Code     string `json:"code"`
	Message  string `json:"message"`
	Offset   int64  `json:"offset"`
}

// CandidateKind 区分候选来源。
const (
	KindDeclared = "declared" // 按声明找到：xref table / xref stream / ObjStm 索引
	KindHeuristic = "heuristic" // 启发式找到：扫描裸对象头等
)

// EntryType 为 xref 条目的类型。
const (
	XRefUncompressed = 1
	XRefCompressed   = 2
	XRefFree         = 0
)

// Candidate 是某个对象号/代次的一个版本候选。
type Candidate struct {
	ID          string    `json:"id"`
	ObjectNum   int       `json:"objectNum"`
	Gen         int       `json:"gen"`
	RevisionIdx int       `json:"revisionIdx"` // 所属修订（0 为最初修订）
	Source      string    `json:"source"`      // KindDeclared | KindHeuristic
	EntryType   int       `json:"entryType"`   // 0 free | 1 normal | 2 compressed
	ObjectOffset int64    `json:"objectOffset"`
	ObjStmNum   int       `json:"objStmNum,omitempty"`   // 压缩对象所在 ObjStm 号
	ObjStmIndex int       `json:"objStmIndex,omitempty"` // 在 ObjStm 内的序号
	Range       ByteRange `json:"range"`                 // 对象头到 endobj（解压对象为逻辑头边界）
	HeaderVerified bool  `json:"headerVerified"`         // 偏移处是否真的是 "N G obj"
	Free        bool      `json:"free"`
	InUse       bool      `json:"inUse"`
	Stream      *StreamInfo `json:"stream,omitempty"`
	Evidences   []Evidence  `json:"evidences"`
	Note        string      `json:"note,omitempty"`
}

// StreamInfo 描述与对象字典关联的流边界与过滤器。
type StreamInfo struct {
	StreamRange  ByteRange `json:"streamRange"`  // stream\n 与 endstream 之间的原始字节
	DictRange    ByteRange `json:"dictRange"`    // 整个字典在对象内的范围
	Filters      []string  `json:"filters"`      // 规范化后的过滤器链
	LengthDecl   int64     `json:"lengthDeclared"`
	LengthActual int64     `json:"lengthActual"`
	LengthVerified bool    `json:"lengthVerified"`
	BoundaryVerified bool  `json:"boundaryVerified"`
	Expandable   bool      `json:"expandable"`
	ExpandNote   string    `json:"expandNote,omitempty"`
	ExpandedRange ByteRange `json:"expandedRange,omitempty"` // 解压成功时的逻辑范围（同 StreamRange 占位，字节在内存）
	ExpandedLen  int64     `json:"expandedLen,omitempty"`
}

// Revision 是增量修订链上的一段。
type Revision struct {
	Index         int       `json:"index"`
	XRefOffset    int64     `json:"xrefOffset"`
	XRefKind      string    `json:"table"` // table | stream | broken
	XRefRange     ByteRange `json:"xrefRange"`
	TrailerOffset int64     `json:"trailerOffset"`
	TrailerDict   *Value    `json:"trailerDict,omitempty"`
	PrevOffset    int64     `json:"prevOffset"`
	PrevBroken    bool      `json:"prevBroken"`
	StartXRefVerified bool  `json:"startXRefVerified"`
	CandidateIDs  []string  `json:"candidateIds"`
	Summary       string    `json:"summary"`
}

// Ref 是对象间引用。
type Ref struct {
	FromObject int  `json:"fromObject"`
	FromRev    int  `json:"fromRev"`
	ToObject   int  `json:"toObject"`
	ToGen      int  `json:"toGen"`
	InStream   bool `json:"inStream"` // 引用来自解压后的 ObjStm 内容
}

// ObjectVersion 汇总某候选解析后的视图。
type ObjectVersion struct {
	CandidateID  string         `json:"candidateId"`
	ObjectNum    int            `json:"objectNum"`
	Gen          int            `json:"gen"`
	RevisionIdx  int            `json:"revisionIdx"`
	Type         string         `json:"type"`
	Subtype      string         `json:"subtype,omitempty"`
	IsPage       bool           `json:"isPage"`
	PageIndex    int            `json:"pageIndex"` // -1 表示非页
	IsCatalog    bool           `json:"isCatalog"`
	IsObjStm     bool           `json:"isObjStm"`
	IsXRef       bool           `json:"isXRef"`
	Range        ByteRange      `json:"range"`
	Value        *Value         `json:"value"`
	OutRefs      []Ref          `json:"outRefs"`
	IncomingRefs []Ref          `json:"incomingRefs"`
	ConflictKey  string         `json:"conflictKey,omitempty"`
	HasConflict  bool           `json:"hasConflict"`
	Chosen       bool           `json:"chosen"`
	Decompressed bool           `json:"decompressed"`
	Note         string         `json:"note,omitempty"`
}

// ScanResult 是一次完整扫描的产物，可直接 JSON 持久化。
type ScanResult struct {
	Size            int64             `json:"size"`
	Header          string            `json:"header"`
	HeaderValid     bool              `json:"headerValid"`
	StartXRefOffset int64             `json:"startXrefOffset"`
	StartXRefTarget int64             `json:"startXrefTarget"`
	StartXRefValid  bool              `json:"startXrefValid"`
	EOFPresent      bool              `json:"eofPresent"`
	Revisions       []Revision        `json:"revisions"`
	Candidates      []*Candidate      `json:"candidates"`
	Versions        []*ObjectVersion  `json:"versions"`
	Problems        []Problem         `json:"problems"`
	TrailingData    []ByteRange       `json:"trailingData"`
	AlternateStartXRefs []int64       `json:"alternateStartXrefs"`
	Refs            []Ref             `json:"refs"`
	RootNum         int               `json:"rootNum"`
	RootGen         int               `json:"rootGen"`
	Pages           []PageInfo        `json:"pages"`
}

// PageInfo 描述一个页面对象及其继承属性来源。
type PageInfo struct {
	ObjectNum  int `json:"objectNum"`
	Gen        int `json:"gen"`
	RevisionIdx int `json:"revisionIdx"`
	PageIndex  int `json:"pageIndex"`
	ParentNum  int `json:"parentNum"`
}

// Limits 控制解压等资源限额。
type Limits struct {
	MaxExpandedBytes int64 `json:"maxExpandedBytes"`
	MaxExpandRatio   int   `json:"maxExpandRatio"`
	MaxObjects       int   `json:"maxObjects"`
}

// DefaultLimits 为默认资源限额。
func DefaultLimits() Limits {
	return Limits{
		MaxExpandedBytes: 64 << 20, // 64 MiB
		MaxExpandRatio:   100,
		MaxObjects:       1_048_576,
	}
}
