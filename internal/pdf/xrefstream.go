package pdf

// xrefstream.go: 解析类型为 /XRef 的对象流中的压缩交叉引用表。

import (
	"bytes"
	"fmt"
)

func (s *scanner) parseXRefStream(sec *xrefSection, q int) {
	// xref stream 必须先可读；其 /Length 引用的对象可能在同段前面声明，
	// resolveOffset 可解析当前已遍历段（含本段尚未加入——因此先做裸头解析）。
	res := s.readXRefStreamObject(q)
	if res == nil || res.value == nil || res.value.Kind != "stream" {
		sec.valid = false
		sec.note = res.note
		if sec.note == "" {
			sec.note = "xref 对象无法读取"
		}
		return
	}
	dict := res.value
	if t := dict.DictGet("Type"); t == nil || t.Kind != "name" || t.Text != "XRef" {
		sec.valid = false
		sec.note = "对象不是 /Type /XRef"
		s.diag(DiagError, "xrefstream-type", q, "xref 偏移指向的对象不是 /XRef", snippet(s.data, q, 32))
		return
	}

	// 必须能取得流数据才能读条目
	raw := res.raw
	if raw == nil {
		sec.valid = false
		sec.note = "xref stream 数据边界无法核验"
		return
	}

	// 展开（仅允许 FlateDecode 或无 filter）
	content := raw
	if fv := dict.DictGet("Filter"); fv != nil {
		name, ok := filterName(fv)
		if !ok || !supportedFilter(name) {
			sec.valid = false
			sec.note = "xref stream 使用了不受支持的过滤器"
			s.diag(DiagError, "xrefstream-filter", q,
				"xref stream 过滤器无法安全展开: "+name, "")
			return
		}
		if name != "" {
			out, err := inflate(raw, s.limits.MaxExpand)
			if err != nil {
				sec.valid = false
				if _, ok := err.(*LimitError); ok {
					sec.note = fmt.Sprintf("xref stream 解压超过限额 %d", s.limits.MaxExpand)
					s.diag(DiagError, "xrefstream-limit", q, sec.note, "")
				} else {
					sec.note = "xref stream 解压失败: " + err.Error()
					s.diag(DiagError, "xrefstream-inflate", q, sec.note, "")
				}
				return
			}
			content = out
		}
	}

	wv := dict.DictGet("W")
	if wv == nil || wv.Kind != "array" || len(wv.Items) != 3 {
		sec.valid = false
		sec.note = "xref stream 缺少长度为 3 的 /W"
		s.diag(DiagError, "xrefstream-w", q, sec.note, "")
		return
	}
	widths := [3]int{}
	for i := 0; i < 3; i++ {
		it := wv.Items[i]
		if it.Kind != "int" || it.Int < 0 {
			sec.valid = false
			sec.note = "/W 元素必须为非负整数"
			return
		}
		widths[i] = int(it.Int)
	}
	recLen := widths[0] + widths[1] + widths[2]
	if recLen <= 0 {
		sec.valid = false
		sec.note = "/W 总宽度为 0"
		return
	}

	// /Index，默认 [0 /Size]
	var indexes []int
	if iv := dict.DictGet("Index"); iv != nil {
		if iv.Kind != "array" || len(iv.Items)%2 != 0 {
			sec.valid = false
			sec.note = "/Index 必须为偶数长度数组"
			return
		}
		for _, it := range iv.Items {
			if it.Kind != "int" {
				sec.valid = false
				sec.note = "/Index 元素必须为整数"
				return
			}
			indexes = append(indexes, int(it.Int))
		}
	} else {
		size := 0
		if sz := dict.DictGet("Size"); sz != nil && sz.Kind == "int" {
			size = int(sz.Int)
		}
		indexes = []int{0, size}
	}

	decl := map[int]xrefEntry{}
	var order []int
	pos := 0
	for sub := 0; sub < len(indexes); sub += 2 {
		first, count := indexes[sub], indexes[sub+1]
		for i := 0; i < count; i++ {
			if pos+recLen > len(content) {
				s.diag(DiagError, "xrefstream-truncated", sec.offset,
					fmt.Sprintf("xref stream 条目数据不足（子节从 %d 起，第 %d 条）", first, i), "")
				sec.valid = false
				sec.note = "xref stream 条目被截断"
				return
			}
			rec := content[pos : pos+recLen]
			pos += recLen
			typ := 1
			c := 0
			if widths[0] > 0 {
				typ = int(beUint(rec[c : c+widths[0]]))
			}
			c += widths[0]
			f1 := 0
			if widths[1] > 0 {
				f1 = int(beInt(rec[c : c+widths[1]]))
			}
			c += widths[1]
			f2 := 0
			if widths[2] > 0 {
				f2 = int(beInt(rec[c : c+widths[2]]))
			}
			num := first + i
			e := xrefEntry{num: num, field1: f1, field2: f2}
			switch typ {
			case 0:
				e.typ = entryFree
			case 1:
				e.typ = entryInUse
			case 2:
				e.typ = entryCompressed
			default:
				s.diag(DiagWarn, "xrefstream-entry-type", sec.offset,
					fmt.Sprintf("对象 %d 的条目类型 %d 非法，按 free 处理", num, typ), "")
				e.typ = entryFree
			}
			if _, dup := decl[num]; dup {
				s.diag(DiagWarn, "xref-duplicate-num", sec.offset,
					fmt.Sprintf("xref stream 段内重复对象号 %d，保留先出现的声明", num), "")
			} else {
				decl[num] = e
				order = append(order, num)
			}
		}
	}
	if pos != len(content) {
		s.diag(DiagInfo, "xrefstream-trailing-bytes", sec.offset,
			fmt.Sprintf("xref stream 解码 %d 条后仍余 %d 字节", len(order), len(content)-pos), "")
	}

	sec.trailer = dict
	s.applyTrailer(sec, dict)
	sec.entries = orderedEntries(decl, order)
	s.declared[sec.scanOrder] = decl
	s.declaredOrder[sec.scanOrder] = order
	sec.valid = true
}

func (s *scanner) readXRefStreamObject(off int) *readObjectResult {
	// 先解析头部字典，再解析可能指向本段前面对象的 /Length。
	d := s.data
	q := skipSpace(d, off)
	num, p1, ok1 := readDecimal(d, q)
	_, p2, ok2 := readDecimal(d, p1)
	p3 := skipSpace(d, p2)
	if !ok1 || !ok2 || p3+3 > len(d) || !stringEq(d[p3:p3+3], "obj") {
		return &readObjectResult{note: "xref 对象头格式错误"}
	}
	body := skipSpace(d, p3+3)
	v, err := parseObjectBody(d, body, len(d))
	if err != nil || v.Kind != "stream" {
		note := "xref 对象体解析失败"
		if err != nil {
			note += ": " + err.Error()
		}
		return &readObjectResult{note: note}
	}
	res := &readObjectResult{objNum: num, value: v, isStream: true, streamOff: v.StreamOff, streamLen: -1}
	dataStart := v.StreamOff
	declLen := -1
	if lv := v.DictGet("Length"); lv != nil {
		switch lv.Kind {
		case "int":
			declLen = int(lv.Int)
		case "ref":
			if o, ok := s.resolveOffset(lv.RefNum, lv.RefGen); ok {
				if iv, err := s.parseIntObject(o); err == nil {
					declLen = iv
				}
			}
			// Length 对象常在 xref stream 同段开头声明，但本段尚未入库：
			// 用全局启发式扫描兜底（只在声明之外提供证据，不影响条目录入）。
			if declLen < 0 {
				if iv, o2, ok := s.heuristicIntObject(lv.RefNum, lv.RefGen, off); ok {
					declLen = iv
					s.diag(DiagInfo, "xref-length-heuristic", o2,
						fmt.Sprintf("xref stream 的 /Length(%d %d R) 通过启发式定位", lv.RefNum, lv.RefGen),
						snippet(d, o2, 24))
				}
			}
		}
	}
	if declLen >= 0 && dataStart+declLen <= len(d) && s.expectKeyword(dataStart+declLen, "endstream") {
		res.streamLen = declLen
		res.lenVerified = true
		res.raw = d[dataStart : dataStart+declLen]
		return res
	}
	idx := indexFrom(d, dataStart, "endstream")
	if idx >= 0 && idx <= len(d) {
		rawEnd := dataStart + idx
		for rawEnd > dataStart && (d[rawEnd-1] == '\r' || d[rawEnd-1] == '\n') {
			rawEnd--
		}
		res.streamLen = rawEnd - dataStart
		res.raw = d[dataStart:rawEnd]
		return res
	}
	res.note = "xref stream 找不到 endstream"
	return res
}

func stringEq(b []byte, s string) bool {
	if len(b) != len(s) {
		return false
	}
	for i := range b {
		if b[i] != s[i] {
			return false
		}
	}
	return true
}

func indexFrom(d []byte, from int, kw string) int {
	if from >= len(d) {
		return -1
	}
	rel := bytes.Index(d[from:], []byte(kw))
	if rel < 0 {
		return -1
	}
	return from + rel
}

func beUint(b []byte) uint64 {
	var v uint64
	for _, c := range b {
		v = v<<8 | uint64(c)
	}
	return v
}

func beInt(b []byte) int64 {
	v := beUint(b)
	n := uint(len(b) * 8)
	if n > 0 && v&(1<<(n-1)) != 0 {
		v |= ^uint64(0) << n
	}
	return int64(v)
}
