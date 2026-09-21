package pdf

// scanner.go: 增量修订扫描器。
// 从尾部 startxref 沿 Prev 链逐段追踪 xref table / xref stream，
// 严格区分“按声明找到”与“启发式找到”，绝不修改输入字节。

import (
	"bytes"
	"fmt"
	"regexp"
	"sort"
	"strconv"
)

type entryType int

const (
	entryFree entryType = iota
	entryInUse
	entryCompressed
)

type xrefEntry struct {
	num    int
	typ    entryType
	field1 int // offset / compressed-with / free-next
	field2 int // gen / index-in-objstm
}

type xrefSection struct {
	offset    int
	kind      string // table / stream
	prev      int
	size      int
	rootNum   int
	rootGen   int
	trailer   *Value
	entries   []xrefEntry
	scanOrder int // 遍历顺序：0=最新
	valid     bool
	note      string
}

type scanner struct {
	data   []byte
	limits Limits
	rep    *Report

	sections []*xrefSection // 遍历顺序（最新在前）
	// raw declared entries, keyed by section scanOrder: num -> entry（保留首个）
	declared map[int]map[int]xrefEntry
	// 每个 section 的 entry 出现顺序
	declaredOrder map[int][]int

	heurByRev map[int]map[int]*Candidate // rev(scanOrder) -> offset -> candidate
	heurList  []*Candidate

	byObj       map[int][]*Candidate
	candByID    map[int]*Candidate
	visitedCont map[string]bool
	nextID      int
}

var (
	objHeaderRe = regexp.MustCompile(`(?s)(?:\A|[\x00\t\n\f\r %()<>\[\]{}/])\s*(\d+)\s+(\d+)\s+obj`)
	refRe       = regexp.MustCompile(`(\d+)\s+(\d+)\s+R`)
)

// ScanFile 对 PDF 原件执行探针扫描。
func ScanFile(data []byte, fileName string, limits Limits) *Report {
	s := &scanner{
		data:          data,
		limits:        limits,
		declared:      map[int]map[int]xrefEntry{},
		declaredOrder: map[int][]int{},
		heurByRev:     map[int]map[int]*Candidate{},
		byObj:         map[int][]*Candidate{},
		candByID:      map[int]*Candidate{},
		visitedCont:   map[string]bool{},
	}
	r := &Report{FileName: fileName, FileSize: len(data), TrailingFrom: -1}
	s.rep = r

	s.scanHeader()
	s.findStartXRef()
	if r.StartXRefOK {
		s.walkXRefs()
	}
	s.assignRevisionMetadata()
	s.findHeuristicObjects()
	s.buildCandidates()
	s.collectReferences()
	s.resolvePages()
	s.detectMixedAndTrailing()
	s.sortCandidates()
	return r
}

func (s *scanner) diag(level DiagLevel, code string, offset int, msg, evidence string) {
	s.rep.Diagnostics = append(s.rep.Diagnostics, &Diagnostic{
		Level: level, Code: code, Offset: offset, Message: msg, Evidence: evidence,
	})
}

// ---- header / startxref ----

func (s *scanner) scanHeader() {
	d := s.data
	if len(d) >= 8 && bytes.HasPrefix(d, []byte("%PDF-")) {
		s.rep.PDFVersion = string(d[5:8])
		s.rep.HeaderOK = true
	} else {
		s.rep.PDFVersion = ""
		s.diag(DiagWarn, "header-missing", 0,
			"未在文件起始找到 %PDF- 头", snippet(d, 0, 16))
	}
}

// findStartXRef 从文件尾部定位最后的 startxref。
func (s *scanner) findStartXRef() {
	d := s.data
	window := len(d)
	start := 0
	if window > 4096 {
		start = window - 4096
	}
	zone := d[start:]
	// 最后一个 startxref
	idx := bytes.LastIndex(zone, []byte("startxref"))
	if idx < 0 {
		s.rep.StartXRef = -1
		s.diag(DiagError, "startxref-missing", start,
			"文件尾部未找到 startxref", snippet(d, start, 64))
		return
	}
	abs := start + idx
	p := skipSpace(d, abs+len("startxref"))
	num, np, ok := readDecimal(d, p)
	if !ok {
		s.rep.StartXRef = -1
		s.diag(DiagError, "startxref-bad", abs,
			"startxref 后缺少有效偏移", snippet(d, abs, 48))
		return
	}
	s.rep.StartXRef = num
	// EOF 标记核验
	tailZone := d[abs:]
	if bytes.Index(tailZone, []byte("%%EOF")) < 0 {
		s.diag(DiagWarn, "eof-missing", abs, "startxref 后未找到 %%EOF", snippet(d, abs, 64))
	}
	if num < 0 || num >= len(d) {
		s.diag(DiagError, "startxref-oob", abs,
			fmt.Sprintf("startxref 偏移 %d 超出文件范围(0..%d)", num, len(d)-1),
			snippet(d, abs, np-abs))
		return
	}
	q := skipSpace(d, num)
	if q+4 <= len(d) && bytes.Equal(d[q:q+4], []byte("xref")) {
		s.rep.StartXRefOK = true
		return
	}
	if looksLikeObjStart(d, q) {
		s.rep.StartXRefOK = true
		return
	}
	// 指向空白或无关数据
	s.diag(DiagError, "startxref-target-bad", num,
		fmt.Sprintf("startxref 指向 0x%X，但该处既不是 xref 也不是对象", num),
		snippet(d, num, 32))
}

func readDecimal(d []byte, p int) (int, int, bool) {
	p = skipSpace(d, p)
	start := p
	if p < len(d) && (d[p] == '-' || d[p] == '+') {
		p++
	}
	n := p
	for n < len(d) && d[n] >= '0' && d[n] <= '9' {
		n++
	}
	if n == start || (n == start+1 && (d[start] == '-' || d[start] == '+')) {
		return 0, p, false
	}
	v, err := strconv.Atoi(string(d[start:n]))
	if err != nil {
		return 0, n, false
	}
	return v, n, true
}

// looksLikeObjStart 判断 q 处是否为 "<n> <g> obj"。
func looksLikeObjStart(d []byte, q int) bool {
	_, p1, ok := readDecimal(d, q)
	if !ok {
		return false
	}
	_, p2, ok := readDecimal(d, p1)
	if !ok {
		return false
	}
	p2 = skipSpace(d, p2)
	return p2+3 <= len(d) && bytes.Equal(d[p2:p2+3], []byte("obj"))
}

// snippet 取安全的证据片段（可打印化）。
func snippet(d []byte, off, n int) string {
	if off < 0 {
		off = 0
	}
	if off > len(d) {
		return ""
	}
	end := off + n
	if end > len(d) {
		end = len(d)
	}
	raw := d[off:end]
	var b bytes.Buffer
	for _, c := range raw {
		if c >= 32 && c < 127 {
			b.WriteByte(c)
		} else {
			b.WriteByte('.')
		}
	}
	return b.String()
}

func (s *scanner) sortCandidates() {
	sort.SliceStable(s.rep.Candidates, func(i, j int) bool {
		a, b := s.rep.Candidates[i], s.rep.Candidates[j]
		if a.ObjNum != b.ObjNum {
			return a.ObjNum < b.ObjNum
		}
		if a.Revision != b.Revision {
			return a.Revision < b.Revision
		}
		if a.Kind != b.Kind {
			return a.Kind == CandDeclared
		}
		return a.ID < b.ID
	})
}
