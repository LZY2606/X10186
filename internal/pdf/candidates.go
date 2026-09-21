package pdf

// candidates.go: 把 xref 声明与启发式发现转成候选版本，
// 并在长度/filter/边界核验后展开 object stream 内的压缩对象。

import (
	"fmt"
	"sort"
)

type pendingCompressed struct {
	cand  *Candidate
	order int
}

func (s *scanner) buildCandidates() {
	var pending []*pendingCompressed

	// 1) 按声明的条目（最旧段 -> 最新段，保证同对象先有旧版本）
	for i := len(s.sections) - 1; i >= 0; i-- {
		sec := s.sections[i]
		rev := len(s.sections) - 1 - sec.scanOrder
		decl := s.declared[sec.scanOrder]
		for _, num := range s.declaredOrder[sec.scanOrder] {
			e := decl[num]
			switch e.typ {
			case entryInUse:
				c := &Candidate{
					Kind: CandDeclared, ObjNum: num, DeclGen: e.field2, Gen: e.field2,
					Revision: rev, Offset: e.field1, StreamLen: -1,
				}
				s.verifyDeclared(c, sec)
				s.register(c)
			case entryFree:
				c := &Candidate{
					Kind: CandDeclared, ObjNum: num, DeclGen: e.field2, Gen: e.field2,
					Revision: rev, Offset: e.field1, Free: true, FreeNext: e.field1,
					Valid: true, Reason: "free entry（对象在此修订被释放）",
					Evidence: fmt.Sprintf("xref 第 %d 段声明 `%010d %05d f`，next=%d", sec.scanOrder, e.field1, e.field2, e.field1),
				}
				s.register(c)
			case entryCompressed:
				c := &Candidate{
					Kind: CandDeclared, ObjNum: num, DeclGen: 0, Gen: 0,
					Revision: rev, Offset: -1, StreamLen: -1,
					ContainerObj: e.field1, InnerIndex: e.field2,
					InObjStm: true,
					Reason:   "xref 类型 2：压缩在 object stream 内",
					Evidence: fmt.Sprintf("xref 第 %d 段声明对象 %d 位于 ObjStm %d 的索引 %d", sec.scanOrder, num, e.field1, e.field2),
				}
				s.register(c)
				pending = append(pending, &pendingCompressed{cand: c, order: sec.scanOrder})
			}
		}
	}

	// 2) 启发式候选（未被声明指向的对象头）
	for _, c := range s.heurList {
		s.verifyHeuristic(c)
		s.register(c)
	}

	// 3) 读取所有声明的 object stream，展开压缩对象
	s.expandObjectStreams(pending)

	// 4) 检测重复竞争
	s.reportDuplicates()
}

func (s *scanner) register(c *Candidate) {
	c.ID = s.nextID
	s.nextID++
	s.rep.Candidates = append(s.rep.Candidates, c)
	s.candByID[c.ID] = c
	s.byObj[c.ObjNum] = append(s.byObj[c.ObjNum], c)
}

func (s *scanner) verifyDeclared(c *Candidate, sec *xrefSection) {
	off := c.Offset
	if off < 0 || off >= len(s.data) {
		c.Valid = false
		c.EndOffset = off
		c.Reason = "xref 声明的偏移越界"
		c.Evidence = fmt.Sprintf("第 %d 段 xref 指向 0x%X，文件长度 %d", sec.scanOrder, off, len(s.data))
		s.diag(DiagError, "offset-oob", off,
			fmt.Sprintf("对象 %d 代 %d 的声明偏移越界", c.ObjNum, c.Gen), c.Evidence)
		return
	}
	res := s.readObjectAt(off, s.limits.MaxExpand)
	if res.value == nil || res.objNum != c.ObjNum || res.gen != c.Gen {
		c.Valid = false
		c.EndOffset = off
		c.Reason = "声明偏移处对象头不匹配（可能指向空白或别的对象）"
		c.Evidence = fmt.Sprintf("xref 声明 `%d %d n @0x%X`，该处为：%q",
			c.ObjNum, c.Gen, off, snippet(s.data, off, 24))
		s.diag(DiagError, "offset-mismatch", off,
			fmt.Sprintf("对象 %d 的声明偏移与对象头不符", c.ObjNum), c.Evidence)
		return
	}
	c.Valid = true
	c.EndOffset = res.endOffset
	c.Parsed = res.value
	if res.note != "" {
		c.ParseError = res.note
	}
	if res.isStream {
		c.Stream = true
		c.StreamOff = res.streamOff
		c.StreamLen = res.streamLen
		c.Filters = res.filters
		if res.lenVerified {
			c.Expanded = res.expanded != nil
			if res.expanded != nil {
				c.ExpandedLen = len(res.expanded)
			}
			if res.expErr != "" {
				c.ExpandNote = res.expErr
				if res.expErr != "" {
					s.diag(DiagWarn, "expand-stop", off,
						fmt.Sprintf("对象 %d：%s", c.ObjNum, res.expErr), "")
				}
			}
		} else {
			c.ExpandNote = res.note
		}
	}
	c.Evidence = fmt.Sprintf("xref 第 %d 段声明 `%d %d n @0x%X`，对象头与代次核验通过",
		sec.scanOrder, c.ObjNum, c.Gen, off)
}

func (s *scanner) verifyHeuristic(c *Candidate) {
	off := c.Offset
	res := s.readObjectAt(off, -1) // 启发式流不自动展开
	if res.value == nil || res.objNum != c.ObjNum || res.gen != c.Gen {
		c.Valid = false
		c.EndOffset = off
		c.Reason = "启发式字节模式未通过对象头核验"
		return
	}
	c.Valid = true
	c.EndOffset = res.endOffset
	c.Parsed = res.value
	if res.isStream {
		c.Stream = true
		c.StreamOff = res.streamOff
		c.StreamLen = res.streamLen
		c.Filters = res.filters
		c.ExpandNote = "启发式候选：需要用户确认后才允许展开"
	}
}

// expandObjectStreams 展开类型 2 压缩对象（仅核验通过的 ObjStm）。
func (s *scanner) expandObjectStreams(pending []*pendingCompressed) {
	// 找全所有对象流容器候选（声明、有效、/Type /ObjStm）
	containers := map[int][]*Candidate{} // objNum -> candidates(revision asc)
	for _, c := range s.rep.Candidates {
		if c.Stream && c.Valid && c.Parsed != nil {
			if t := c.Parsed.DictGet("Type"); t != nil && t.Kind == "name" && t.Text == "ObjStm" {
				containers[c.ObjNum] = append(containers[c.ObjNum], c)
			}
		}
	}
	for _, list := range containers {
		sort.Slice(list, func(i, j int) bool { return list[i].Revision < list[j].Revision })
	}

	for _, pc := range pending {
		c := pc.cand
		clist := containers[c.ContainerObj]
		var cont *Candidate
		// 选择不晚于引用修订的最新容器版本
		for _, cc := range clist {
			if cc.Revision <= c.Revision {
				cont = cc
			}
		}
		if cont == nil && len(clist) > 0 {
			cont = clist[0]
		}
		if cont == nil {
			c.Valid = false
			c.Reason = "xref 类型 2 指向的 object stream 未找到"
			s.diag(DiagError, "objstm-missing", c.Offset,
				fmt.Sprintf("对象 %d 引用的 ObjStm %d 不存在或未通过核验", c.ObjNum, c.ContainerObj), "")
			continue
		}
		// 重新读取容器以拿到解压内容（容器先前可能由声明流程展开过，但未保存内容）
		res := s.readObjectAt(cont.Offset, s.limits.MaxExpand)
		if !res.lenVerified || !res.filterOK || res.expanded == nil {
			note := "object stream 未通过长度/filter/边界核验，拒绝展开"
			if res.expErr != "" {
				note = res.expErr
			}
			c.Valid = false
			c.Reason = note
			s.diag(DiagWarn, "objstm-expand", cont.Offset, note, "")
			continue
		}
		content := res.expanded
		nCount := intVal(cont.Parsed.DictGet("N"), -1)
		first := intVal(cont.Parsed.DictGet("First"), -1)
		if nCount <= 0 || first < 0 {
			c.Valid = false
			c.Reason = "ObjStm 的 /N 或 /First 无效"
			continue
		}
		// 解析头部的 N 对 (objnum offset)
		type io struct{ num, off int }
		var infos []io
		pp := &parser{data: content, base: 0, end: len(content), maxD: 10}
		pos := 0
		ok := true
		for i := 0; i < nCount; i++ {
			nv, err := pp.parseValue(skipSpace(content, pos))
			if err != nil || nv.Kind != "int" {
				ok = false
				break
			}
			pos = pp.saved
			ov, err := pp.parseValue(skipSpace(content, pos))
			if err != nil || ov.Kind != "int" {
				ok = false
				break
			}
			pos = pp.saved
			infos = append(infos, io{int(nv.Int), int(ov.Int)})
		}
		if !ok || c.InnerIndex >= len(infos) {
			c.Valid = false
			c.Reason = fmt.Sprintf("ObjStm 内索引 %d 越界（N=%d）", c.InnerIndex, nCount)
			s.diag(DiagWarn, "objstm-index", cont.Offset, c.Reason, "")
			continue
		}
		info := infos[c.InnerIndex]
		innerStart := first + info.off
		if innerStart < 0 || innerStart > len(content) {
			c.Valid = false
			c.Reason = "压缩对象内层偏移越界"
			continue
		}
		innerEnd := len(content)
		if c.InnerIndex+1 < len(infos) {
			if e := first + infos[c.InnerIndex+1].off; e > innerStart && e <= len(content) {
				innerEnd = e
			}
		}
		inner := content[innerStart:innerEnd]
		pp2 := &parser{data: inner, base: 0, end: len(inner), maxD: 40}
		v, err := pp2.parseValue(0)
		if err != nil {
			c.Valid = false
			c.Reason = "压缩对象内层值解析失败: " + err.Error()
			continue
		}
		if info.num != c.ObjNum {
			s.diag(DiagInfo, "objstm-nummismatch", cont.Offset,
				fmt.Sprintf("xref 称 ObjStm 索引 %d 为对象 %d，头部表为 %d", c.InnerIndex, c.ObjNum, info.num), "")
		}
		c.Valid = true
		c.ContainerCand = cont.ID
		c.InnerOff = innerStart
		c.InnerLen = len(inner)
		c.Parsed = v
		c.EndOffset = cont.EndOffset
		c.Expanded = true
		c.ExpandedLen = len(inner)
		c.ExpandNote = fmt.Sprintf("从 ObjStm %d（候选 #%d，修订 %d）展开，内层区间 [%d,%d)",
			cont.ObjNum, cont.ID, cont.Revision, innerStart, innerEnd)
	}
}

func intVal(v *Value, def int) int {
	if v != nil && v.Kind == "int" {
		return int(v.Int)
	}
	return def
}

func (s *scanner) reportDuplicates() {
	for obj, list := range s.byObj {
		// 同一修订内、同对象号存在多个有效候选，即“竞争”
		byRev := map[int][]*Candidate{}
		for _, c := range list {
			byRev[c.Revision] = append(byRev[c.Revision], c)
		}
		revs := make([]int, 0, len(byRev))
		for r := range byRev {
			revs = append(revs, r)
		}
		sort.Ints(revs)
		for _, r := range revs {
			cs := byRev[r]
			nValid := 0
			for _, c := range cs {
				if c.Valid {
					nValid++
				}
			}
			if nValid > 1 {
				s.diag(DiagWarn, "duplicate-candidates", firstOff(cs),
					fmt.Sprintf("对象 %d 在修订 %d 有 %d 个竞争候选，不能按偏移自动决定", obj, r, nValid),
					dupEvidence(cs))
			}
		}
	}
}

func firstOff(cs []*Candidate) int {
	for _, c := range cs {
		if c.Offset >= 0 {
			return c.Offset
		}
	}
	return 0
}

func dupEvidence(cs []*Candidate) string {
	out := ""
	for _, c := range cs {
		if !c.Valid {
			continue
		}
		if out != "" {
			out += "；"
		}
		out += fmt.Sprintf("#%d %s @0x%X", c.ID, c.Kind, c.Offset)
	}
	return out
}
