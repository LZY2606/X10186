package pdf

// xrefwalk.go: 沿 Prev 链逐段读取 xref table / xref stream。

import (
	"bytes"
	"fmt"
	"strconv"
)

func (s *scanner) walkXRefs() {
	seen := map[int]int{} // offset -> scanOrder
	off := s.rep.StartXRef
	for order := 0; ; order++ {
		if off < 0 || off >= len(s.data) {
			s.diag(DiagError, "prev-oob", off,
				fmt.Sprintf("Prev 偏移 %d 越界，previous 链在此断开", off), "")
			break
		}
		if prevOrder, dup := seen[off]; dup {
			s.diag(DiagError, "prev-cycle", off,
				fmt.Sprintf("previous 链形成环路：0x%X 已在第 %d 段出现，停止追踪", off, prevOrder),
				snippet(s.data, off, 32))
			break
		}
		seen[off] = order

		sec := &xrefSection{offset: off, scanOrder: order, prev: -1}
		q := skipSpace(s.data, off)
		switch {
		case q+4 <= len(s.data) && bytes.Equal(s.data[q:q+4], []byte("xref")):
			sec.kind = "table"
			s.parseTable(sec, q)
		case looksLikeObjStart(s.data, q):
			sec.kind = "stream"
			s.parseXRefStream(sec, q)
		default:
			sec.valid = false
			sec.note = "xref 偏移既不指向 xref 关键字也不指向对象"
			s.diag(DiagError, "xref-target-bad", off,
				fmt.Sprintf("xref 偏移 0x%X 指向空白或无关数据", off), snippet(s.data, off, 32))
		}
		s.sections = append(s.sections, sec)
		if !sec.valid {
			s.diag(DiagError, "xref-section-invalid", off,
				fmt.Sprintf("第 %d 段 xref 无法解析：%s；停止沿 Prev 继续", order, sec.note),
				snippet(s.data, off, 32))
			break
		}
		if sec.prev < 0 {
			break
		}
		off = sec.prev
	}
}

// parseTable 解析经典 xref table + trailer。
func (s *scanner) parseTable(sec *xrefSection, xrefPos int) {
	d := s.data
	p := skipSpace(d, xrefPos+4)
	decl := map[int]xrefEntry{}
	var order []int
	for {
		p = skipSpace(d, p)
		// 子节头: first count
		first, p1, ok1 := readDecimal(d, p)
		if !ok1 {
			break
		}
		count, p2, ok2 := readDecimal(d, p1)
		if !ok2 || count < 0 {
			sec.valid = false
			sec.note = "xref 子节头格式错误"
			return
		}
		p = skipSpace(d, p2)
		for i := 0; i < count; i++ {
			// 每条 20 字节：nnnnnnnnnn ggggg f/n\r\n
			lineStart := p
			lineEnd := p
			for lineEnd < len(d) && d[lineEnd] != '\n' && d[lineEnd] != '\r' {
				lineEnd++
			}
			raw := bytes.TrimSpace(d[lineStart:lineEnd])
			fields := bytes.Fields(raw)
			p = lineEnd
			if p < len(d) && d[p] == '\r' {
				p++
			}
			if p < len(d) && d[p] == '\n' {
				p++
			}
			if len(fields) != 3 {
				s.diag(DiagError, "xref-entry-bad", lineStart,
					fmt.Sprintf("xref 子节(从 %d 起)第 %d 条不是 3 列", first, i), string(raw))
				sec.valid = false
				sec.note = "xref entry 列数错误"
				return
			}
			v0, _ := strconv.Atoi(string(fields[0]))
			v1, _ := strconv.Atoi(string(fields[1]))
			typ := fields[2][0]
			num := first + i
			e := xrefEntry{num: num, field1: v0, field2: v1}
			switch typ {
			case 'n':
				e.typ = entryInUse
			case 'f':
				e.typ = entryFree
			default:
				s.diag(DiagError, "xref-entry-type", lineStart,
					fmt.Sprintf("对象 %d 的 xref 条目类型 %q 非法", num, fields[2]), string(raw))
				sec.valid = false
				sec.note = "xref entry 类型非法"
				return
			}
			if _, exists := decl[num]; exists {
				s.diag(DiagWarn, "xref-duplicate-num", lineStart,
					fmt.Sprintf("xref table 段内重复对象号 %d，保留先出现的声明", num), string(raw))
			} else {
				decl[num] = e
				order = append(order, num)
			}
		}
		// 下一 token: "trailer" 或下一个子节头
		np := skipSpace(d, p)
		if np+7 <= len(d) && bytes.Equal(d[np:np+7], []byte("trailer")) {
			p = skipSpace(d, np+7)
			break
		}
		p = np
	}
	// trailer 字典
	pp := &parser{data: d, base: 0, end: len(d), maxD: 40}
	v, err := pp.parseValue(p)
	if err != nil || v.Kind != "dict" {
		sec.valid = false
		sec.note = "trailer 字典无法解析"
		s.diag(DiagError, "trailer-bad", p, "trailer 不是有效字典", snippet(d, p, 64))
		return
	}
	sec.trailer = v
	s.applyTrailer(sec, v)
	sec.entries = orderedEntries(decl, order)
	s.declared[sec.scanOrder] = decl
	s.declaredOrder[sec.scanOrder] = order
	sec.valid = true
}

func orderedEntries(decl map[int]xrefEntry, order []int) []xrefEntry {
	out := make([]xrefEntry, 0, len(order))
	for _, n := range order {
		out = append(out, decl[n])
	}
	return out
}

func (s *scanner) applyTrailer(sec *xrefSection, v *Value) {
	if sz := v.DictGet("Size"); sz != nil && sz.Kind == "int" {
		sec.size = int(sz.Int)
	} else {
		s.diag(DiagWarn, "trailer-size", sec.offset, "trailer 缺少整数 Size", "")
	}
	if root := v.DictGet("Root"); root != nil && root.Kind == "ref" {
		sec.rootNum, sec.rootGen = root.RefNum, root.RefGen
	}
	if pv := v.DictGet("Prev"); pv != nil {
		if pv.Kind == "int" {
			sec.prev = int(pv.Int)
		} else {
			s.diag(DiagWarn, "prev-type", sec.offset, "Prev 不是整数", "")
		}
	}
	if enc := v.DictGet("Encrypt"); enc != nil {
		s.diag(DiagInfo, "encrypted", sec.offset,
			"trailer 声明 Encrypt：文件已加密，探针不尝试解密", "")
	}
}
