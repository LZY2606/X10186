package pdf

// heuristics.go: 修订区域计算与“启发式找到”的对象头扫描。
// 启发式结果与 xref 声明严格分开标注。

import (
	"bytes"
	"fmt"
)

func (s *scanner) assignRevisionMetadata() {
	n := len(s.sections)
	// 每段块尾（%%EOF 之后）
	blockEnd := make([]int, n) // index scanOrder
	for i, sec := range s.sections {
		off := sec.offset
		rel := bytes.Index(s.data[off:], []byte("%%EOF"))
		if rel >= 0 {
			blockEnd[i] = off + rel + len("%%EOF")
		} else {
			blockEnd[i] = off
			s.diag(DiagWarn, "eof-marker-missing", off,
				fmt.Sprintf("第 %d 段 xref 之后缺少 %%EOF", i), "")
		}
	}
	for order, sec := range s.sections {
		rev := n - 1 - order
		rs := 0
		if order+1 < n {
			rs = blockEnd[order+1]
		}
		revision := &Revision{
			Index:       rev,
			Kind:        sec.kind,
			XRefOffset:  sec.offset,
			Prev:        sec.prev,
			RegionStart: rs,
			RegionEnd:   sec.offset,
			BlockEnd:    blockEnd[order],
			Size:        sec.size,
			RootNum:     sec.rootNum,
			RootGen:     sec.rootGen,
		}
		s.rep.Revisions = append(s.rep.Revisions, revision)
	}
	// Revisions 以 scanOrder（最新在前）填充，翻转为最旧在前。
	for i, j := 0, len(s.rep.Revisions)-1; i < j; i, j = i+1, j-1 {
		s.rep.Revisions[i], s.rep.Revisions[j] = s.rep.Revisions[j], s.rep.Revisions[i]
	}
}

// declaredOffsets 收集所有按声明给出的类型 1 偏移。
func (s *scanner) declaredOffsets() map[int]bool {
	m := map[int]bool{}
	for _, sec := range s.sections {
		for _, e := range s.declared[sec.scanOrder] {
			if e.typ == entryInUse {
				m[e.field1] = true
			}
		}
	}
	return m
}

func (s *scanner) findHeuristicObjects() {
	declOff := s.declaredOffsets()
	d := s.data
	count := 0
	for _, loc := range objHeaderRe.FindAllIndex(d, -1) {
		start := loc[0]
		// 正则的首个字节可能是分隔/空白，定位数字起点
		for start < loc[1] && !isDigit(d[start]) {
			start++
		}
		if count >= s.limits.MaxScanObjects {
			s.diag(DiagWarn, "heuristic-cap", start,
				fmt.Sprintf("启发式对象头数量超过限额 %d，停止扫描", s.limits.MaxScanObjects), "")
			break
		}
		num, p1, ok1 := readDecimal(d, start)
		gen, _, ok2 := readDecimal(d, p1)
		if !ok1 || !ok2 {
			continue
		}
		if declOff[start] {
			continue // 与声明重合，不重复记为启发式
		}
		count++
		rev := s.revisionForOffset(start)
		c := &Candidate{
			Kind:     CandHeuristic,
			ObjNum:   num,
			Gen:      gen,
			Revision: rev,
			Offset:   start,
			Reason:   "对象头字节模式 'n g obj' 与 xref 声明无关",
			Evidence: fmt.Sprintf("启发式扫描在 0x%X 发现 `%d %d obj`（未被任何已追踪 xref 条目指向）", start, num, gen),
		}
		s.addHeuristic(c)
	}
}

func isDigit(b byte) bool { return b >= '0' && b <= '9' }

func (s *scanner) addHeuristic(c *Candidate) {
	if s.heurByRev[c.Revision] == nil {
		s.heurByRev[c.Revision] = map[int]*Candidate{}
	}
	if _, dup := s.heurByRev[c.Revision][c.Offset]; dup {
		return
	}
	s.heurByRev[c.Revision][c.Offset] = c
	s.heurList = append(s.heurList, c)
}

// revisionForOffset 把字节位置映射到修订区域；越出最新块尾归为 -1（尾部）。
func (s *scanner) revisionForOffset(pos int) int {
	for _, rev := range s.rep.Revisions {
		if pos >= rev.RegionStart && pos < rev.BlockEnd {
			return rev.Index
		}
	}
	return -1
}

// heuristicIntObject 在 before 之前启发式查找 num/gen 指向的整数对象（用于 xref /Length）。
func (s *scanner) heuristicIntObject(num, gen, before int) (int, int, bool) {
	d := s.data
	zone := d
	if before >= 0 && before < len(d) {
		zone = d[:before]
	}
	for _, loc := range objHeaderRe.FindAllIndex(zone, -1) {
		start := loc[0]
		for start < loc[1] && !isDigit(d[start]) {
			start++
		}
		n, p1, ok1 := readDecimal(d, start)
		g, _, ok2 := readDecimal(d, p1)
		if !ok1 || !ok2 || n != num || g != gen {
			continue
		}
		if iv, err := s.parseIntObject(start); err == nil {
			return iv, start, true
		}
	}
	return 0, 0, false
}
