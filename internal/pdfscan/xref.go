package pdfscan

import (
	"bytes"
	"strconv"
)

// XRefEntry 是一条交叉引用条目。
type XRefEntry struct {
	ObjNum    int
	Gen       int
	Type      int // 0 free, 1 uncompressed, 2 compressed
	Offset    int64
	ObjStmNum int
	Index     int
	Raw       string // 原始行/解码字节摘要，作为证据
}

// XRefSection 是一个 xref 段的解析结果。
type XRefSection struct {
	Kind       string // table | stream
	Offset     int64
	Range      ByteRange
	Entries    []XRefEntry
	Trailer    *Value
	TrailerEnd int
	PrevOffset int64
	HasPrev    bool
	Problems   []Problem
	ObjNum     int // stream 时所属对象号
	ObjGen     int
	StreamObj  *IndirectObject
}

// parseXRefTable 从 offset（应指向 "xref"）解析经典 xref 表。
func parseXRefTable(data []byte, offset int64) *XRefSection {
	sec := &XRefSection{Kind: "table", Offset: offset}
	if offset < 0 || offset+4 > int64(len(data)) || string(data[offset:offset+4]) != "xref" {
		sec.Problems = append(sec.Problems, Problem{Severity: "error", Code: "xref-keyword-missing",
			Message: "声明的 xref 偏移处不是 xref 关键字", Offset: offset})
		return sec
	}
	pos := int(offset) + 4
	for {
		// 跳过空白
		for pos < len(data) && isWhitespace(data[pos]) {
			pos++
		}
		if pos >= len(data) {
			sec.Problems = append(sec.Problems, Problem{Severity: "error", Code: "trailer-missing", Message: "xref 表后缺少 trailer", Offset: offset})
			break
		}
		if pos+7 <= len(data) && string(data[pos:pos+7]) == "trailer" {
			pos += 7
			p := &parser{data: data, pos: pos}
			tv, err := p.parseValue(false)
			if err != nil || tv.Kind != VDict {
				sec.Problems = append(sec.Problems, Problem{Severity: "error", Code: "trailer-parse", Message: "trailer 字典解析失败", Offset: offset})
			} else {
				sec.Trailer = tv
				sec.TrailerEnd = p.pos
				if pv := tv.DictGet("Prev"); pv != nil && pv.Kind == VInt {
					sec.PrevOffset = pv.Int
					sec.HasPrev = true
				}
			}
			pos = p.pos
			break
		}
		// 解析子段头 "first count"
		lineEnd := bytes.IndexByte(data[pos:], '\n')
		var line []byte
		if lineEnd < 0 {
			line = data[pos:]
			pos = len(data)
		} else {
			line = data[pos : pos+lineEnd]
			pos = pos + lineEnd + 1
		}
		line = bytes.Trim(line, " \t\r")
		if len(line) == 0 {
			continue
		}
		fields := bytes.Fields(line)
		if len(fields) != 2 {
			sec.Problems = append(sec.Problems, Problem{Severity: "error", Code: "xref-subsection-header",
				Message: "无法解析 xref 子段头: " + string(line), Offset: offset})
			break
		}
		first, err1 := strconv.Atoi(string(fields[0]))
		count, err2 := strconv.Atoi(string(fields[1]))
		if err1 != nil || err2 != nil || count < 0 {
			sec.Problems = append(sec.Problems, Problem{Severity: "error", Code: "xref-subsection-header",
				Message: "xref 子段头数字非法", Offset: offset})
			break
		}
		for i := 0; i < count; i++ {
			// 读取一条 20 字节条目（容忍行尾差异）
			entryLine, npos := readXRefEntryLine(data, pos)
			pos = npos
			e, perr := parseXRefEntryLine(entryLine, first+i)
			if perr != nil {
				sec.Problems = append(sec.Problems, Problem{Severity: "error", Code: "xref-entry-parse",
					Message: "xref 条目解析失败: " + string(entryLine), Offset: offset})
				break
			}
			sec.Entries = append(sec.Entries, e)
		}
	}
	sec.Range = ByteRange{Start: offset, End: int64(pos)}
	return sec
}

// readXRefEntryLine 读取一条 xref 条目（通常 20 字节含 EOL）。
func readXRefEntryLine(data []byte, pos int) ([]byte, int) {
	if pos >= len(data) {
		return nil, pos
	}
	// 标准条目恰好 20 字节：10 空格 5 空格 n\r\n 或 n \r / n \n
	if pos+20 <= len(data) {
		cand := data[pos : pos+20]
		if looksLikeXRefEntry(cand) {
			return cand, pos + 20
		}
	}
	// 回退：读到行尾
	end := pos
	for end < len(data) && data[end] != '\n' {
		end++
	}
	line := data[pos:end]
	if end < len(data) {
		end++
	}
	return bytes.TrimRight(line, "\r"), end
}

func looksLikeXRefEntry(b []byte) bool {
	if len(b) != 20 {
		return false
	}
	for i := 0; i < 10; i++ {
		if b[i] < '0' || b[i] > '9' {
			return false
		}
	}
	if b[10] != ' ' {
		return false
	}
	for i := 11; i < 16; i++ {
		if b[i] < '0' || b[i] > '9' {
			return false
		}
	}
	if b[16] != ' ' {
		return false
	}
	if b[17] != 'n' && b[17] != 'f' {
		return false
	}
	// 18,19: \r\n | " \r" | " \n"
	if b[18] == '\r' && b[19] == '\n' {
		return true
	}
	if b[18] == ' ' && (b[19] == '\r' || b[19] == '\n') {
		return true
	}
	return false
}

func parseXRefEntryLine(line []byte, objNum int) (XRefEntry, error) {
	e := XRefEntry{ObjNum: objNum, Raw: string(line)}
	trimmed := bytes.TrimRight(string(line), "\r\n ")
	if len(trimmed) < 17 {
		return e, errUnexpectedToken
	}
	offStr := trimmed[0:10]
	genStr := trimmed[11:16]
	typeCh := trimmed[16]
	off, err1 := strconv.ParseInt(offStr, 10, 64)
	gen, err2 := strconv.Atoi(genStr)
	if err1 != nil || err2 != nil {
		return e, errUnexpectedToken
	}
	e.Offset = off
	e.Gen = gen
	if typeCh == 'f' {
		e.Type = XRefFree
	} else {
		e.Type = XRefUncompressed
	}
	return e, nil
}

// parseXRefStream 解析 offset 处的 xref stream 对象。
func parseXRefStream(data []byte, offset int64, limits Limits) *XRefSection {
	sec := &XRefSection{Kind: "stream", Offset: offset}
	obj, probs := parseIndirectObject(data, offset, limits)
	sec.Problems = append(sec.Problems, probs...)
	if obj == nil || obj.Value == nil {
		sec.Problems = append(sec.Problems, Problem{Severity: "error", Code: "xref-stream-parse",
			Message: "xref stream 对象解析失败", Offset: offset})
		return sec
	}
	sec.ObjNum = obj.Num
	sec.ObjGen = obj.Gen
	sec.StreamObj = obj
	if obj.Value.Kind != VStream {
		sec.Problems = append(sec.Problems, Problem{Severity: "error", Code: "not-xref-stream",
			Message: "对象不是流，无法作为 xref stream", Offset: offset})
		return sec
	}
	tv := obj.Value
	tv.Kind = VDict // 作为 trailer 字典看待
	sec.Trailer = tv
	if pv := tv.DictGet("Prev"); pv != nil && pv.Kind == VInt {
		sec.PrevOffset = pv.Int
		sec.HasPrev = true
	}
	typ, _ := tv.DictGet("Type").AsName()
	if typ != "XRef" {
		sec.Problems = append(sec.Problems, Problem{Severity: "warning", Code: "xref-stream-type",
			Message: "流对象 /Type 不是 /XRef", Offset: offset})
	}
	wv := tv.DictGet("W")
	if wv == nil || wv.Kind != VArray || len(wv.Array) < 3 {
		sec.Problems = append(sec.Problems, Problem{Severity: "error", Code: "xref-stream-w",
			Message: "xref stream 缺少 /W 数组", Offset: offset})
		return sec
	}
	var w [3]int
	for i := 0; i < 3; i++ {
		wi, ok := wv.Array[i].AsInt()
		if !ok || wi < 0 || wi > 8 {
			sec.Problems = append(sec.Problems, Problem{Severity: "error", Code: "xref-stream-w",
				Message: "/W 数组元素非法", Offset: offset})
			return sec
		}
		w[i] = int(wi)
	}
	// 解码流数据
	raw := obj.StreamData
	decoded, derr := decodeStreamData(raw, tv, limits)
	if derr != nil {
		sec.Problems = append(sec.Problems, Problem{Severity: "error", Code: "xref-stream-decode",
			Message: "xref stream 解压失败: " + derr.Error(), Offset: offset})
		return sec
	}
	// 索引：/Index 或默认 [0 Size]
	var indexPairs [][2]int
	if iv := tv.DictGet("Index"); iv != nil && iv.Kind == VArray && len(iv.Array)%2 == 0 {
		for i := 0; i < len(iv.Array); i += 2 {
			a, ok1 := iv.Array[i].AsInt()
			b, ok2 := iv.Array[i+1].AsInt()
			if !ok1 || !ok2 {
				sec.Problems = append(sec.Problems, Problem{Severity: "error", Code: "xref-stream-index",
					Message: "/Index 数组非法", Offset: offset})
				return sec
			}
			indexPairs = append(indexPairs, [2]int{int(a), int(b)})
		}
	} else {
		sz, ok := tv.DictGet("Size").AsInt()
		if !ok {
			sec.Problems = append(sec.Problems, Problem{Severity: "error", Code: "xref-stream-size",
				Message: "缺少 /Size", Offset: offset})
			return sec
		}
		indexPairs = append(indexPairs, [2]int{0, int(sz)})
	}
	entryLen := w[0] + w[1] + w[2]
	if entryLen == 0 {
		sec.Problems = append(sec.Problems, Problem{Severity: "error", Code: "xref-stream-w",
			Message: "/W 数组全零", Offset: offset})
		return sec
	}
	pos := 0
	for _, pair := range indexPairs {
		for i := 0; i < pair[1]; i++ {
			if pos+entryLen > len(decoded) {
				sec.Problems = append(sec.Problems, Problem{Severity: "error", Code: "xref-stream-short",
					Message: "xref stream 数据长度不足", Offset: offset})
				sec.Range = ByteRange{Start: offset, End: obj.Range.End}
				return sec
			}
			f1 := readIntBE(decoded[pos:pos+w[0]], 1)
			f2 := readIntBE(decoded[pos+w[0]:pos+w[0]+w[1]], 0)
			f3 := readIntBE(decoded[pos+w[0]+w[1]:pos+entryLen], 0)
			pos += entryLen
			e := XRefEntry{ObjNum: pair[0] + i, Type: int(f1), Raw: "decoded"}
			switch f1 {
			case 0:
				e.Offset = f2
				e.Gen = int(f3)
			case 1:
				e.Offset = f2
				e.Gen = int(f3)
			case 2:
				e.ObjStmNum = int(f2)
				e.Index = int(f3)
			default:
				sec.Problems = append(sec.Problems, Problem{Severity: "warning", Code: "xref-stream-type-unknown",
					Message: "未知 xref 条目类型", Offset: offset})
			}
			sec.Entries = append(sec.Entries, e)
		}
	}
	sec.Range = ByteRange{Start: offset, End: obj.Range.End}
	return sec
}

func readIntBE(b []byte, def int64) int64 {
	if len(b) == 0 {
		return def
	}
	var v int64
	for _, c := range b {
		v = v<<8 | int64(c)
	}
	return v
}
