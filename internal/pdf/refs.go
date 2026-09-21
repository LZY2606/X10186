package pdf

// refs.go: 收集对象间引用（含证据偏移），跳过压缩二进制流体。

import (
	"bytes"
	"fmt"
	"sort"
)

func (s *scanner) collectReferences() {
	seen := map[string]bool{}
	add := func(fromObj, fromCand, toObj, toGen, off int, expanded bool, cont int, ctx string) {
		key := fmt.Sprintf("%d:%d:%d:%d:%d:%v", fromObj, fromCand, toObj, toGen, off, expanded)
		if seen[key] {
			return
		}
		seen[key] = true
		s.rep.Edges = append(s.rep.Edges, &RefEdge{
			FromObj: fromObj, FromCand: fromCand, ToObj: toObj, ToGen: toGen,
			Offset: off, InExpanded: expanded, ContainerObj: cont, Context: ctx,
		})
	}

	// 对象体引用：在“可读区域”扫描 n g R（跳过 stream 原始字节）
	for _, c := range s.rep.Candidates {
		if !c.Valid || c.Free || c.IsCompressed() {
			continue
		}
		s.scanBodyRefs(c, add)
	}

	// Object stream 内部：对象在解压内容中的互相引用
	for _, c := range s.rep.Candidates {
		if !c.Valid || !c.InObjStm || c.InnerLen == 0 {
			continue
		}
		contID := c.ContainerCand
		res := s.readObjectAt(s.candByID[contID].Offset, s.limits.MaxExpand)
		if res.expanded == nil {
			continue
		}
		zone := res.expanded[c.InnerOff : c.InnerOff+c.InnerLen]
		for _, m := range refRe.FindAllIndex(zone, -1) {
			if m[1]-1 < 0 || m[1] > len(zone) || zone[m[1]-1] != 'R' {
				continue
			}
			if m[1] < len(zone) && isRegular(zone[m[1]]) {
				continue
			}
			abs := c.InnerOff + m[0]
			tri := parseRefAt(zone, m[0])
			if tri == nil {
				continue
			}
			add(c.ObjNum, c.ID, tri.num, tri.gen, abs, true, c.ContainerObj, "ObjStm 内引用")
		}
	}

	// trailer / Root 系统引用（fromObj=0）
	for _, sec := range s.sections {
		rev := len(s.sections) - 1 - sec.scanOrder
		if sec.rootNum > 0 {
			add(0, -1, sec.rootNum, sec.rootGen, sec.offset, false, 0,
				fmt.Sprintf("修订 %d trailer Root", rev))
		}
		if sec.trailer != nil {
			if info := sec.trailer.DictGet("Info"); info != nil && info.Kind == "ref" {
				add(0, -1, info.RefNum, info.RefGen, sec.offset, false, 0,
					fmt.Sprintf("修订 %d trailer Info", rev))
			}
		}
	}

	sort.SliceStable(s.rep.Edges, func(i, j int) bool {
		a, b := s.rep.Edges[i], s.rep.Edges[j]
		if a.FromObj != b.FromObj {
			return a.FromObj < b.FromObj
		}
		return a.Offset < b.Offset
	})
}

type refTri struct{ num, gen int }

func parseRefAt(zone []byte, at int) *refTri {
	n, p1, ok1 := readDecimal(zone, at)
	g, _, ok2 := readDecimal(zone, p1)
	if !ok1 || !ok2 {
		return nil
	}
	return &refTri{n, g}
}

// scanBodyRefs 在候选对象的可读字节中找引用，跳过流原始数据。
func (s *scanner) scanBodyRefs(c *Candidate, add func(int, int, int, int, int, bool, int, string)) {
	d := s.data
	bodyStart := c.Offset
	// 跳过 "n g obj" 头
	_, p, ok := readDecimal(d, skipSpace(d, bodyStart))
	if !ok {
		return
	}
	_, p, ok = readDecimal(d, p)
	if !ok {
		return
	}
	p = skipSpace(d, p)
	if p+3 > len(d) || !bytes.Equal(d[p:p+3], []byte("obj")) {
		return
	}
	p = skipSpace(d, p+3)

	scanEnd := c.EndOffset
	if c.Stream {
		// stream 关键字位置
		rel := bytes.Index(d[p:scanEnd], []byte("stream"))
		if rel >= 0 {
			skw := p + rel
			sdStart := skw + len("stream")
			if sdStart < len(d) && d[sdStart] == '\r' {
				sdStart++
			}
			if sdStart < len(d) && d[sdStart] == '\n' {
				sdStart++
			}
			// 仅扫描字典部分与 endstream/endobj 尾部，不扫流二进制
			s.scanRefRange(c, d, p, sdStart, false, add)
			tailStart := c.StreamOff + c.StreamLen
			if c.StreamLen >= 0 && tailStart <= scanEnd {
				s.scanRefRange(c, d, tailStart, scanEnd, false, add)
			}
			return
		}
	}
	s.scanRefRange(c, d, p, scanEnd, false, add)
}

func (s *scanner) scanRefRange(c *Candidate, d []byte, from, to int, expanded bool,
	add func(int, int, int, int, int, bool, int, string)) {
	if from < 0 || to > len(d) || from >= to {
		return
	}
	zone := d[from:to]
	for _, m := range refRe.FindAllIndex(zone, -1) {
		// m[1] 位于 R 之后；要求 R 后是分隔/空白/行尾，避免误匹配
		rPos := m[1] - 1
		if rPos < 0 || zone[rPos] != 'R' {
			continue
		}
		after := m[1]
		if after < len(zone) && isRegular(zone[after]) {
			continue
		}
		tri := parseRefAt(zone, m[0])
		if tri == nil {
			continue
		}
		add(c.ObjNum, c.ID, tri.num, tri.gen, from+m[0], expanded, 0, "对象体引用")
	}
}
