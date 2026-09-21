package pdfscan

import (
	"bytes"
	"strconv"
)

// IndirectObject 是一次间接对象解析的结果。
type IndirectObject struct {
	Num        int
	Gen        int
	HeaderEnd  int // 对象体开始位置（obj 关键字之后）
	Value      *Value
	ValueEnd   int // 值结束位置（stream 时为字典结束位置）
	EndPos     int // endobj 之后的位置
	Range      ByteRange
	Stream     *StreamInfo
	StreamData []byte // 未解压的原始流字节
	StreamEnd  int    // endstream 结束位置
	HasStream  bool
	EndObjFound bool
}

// matchObjectHeader 在 data[pos] 处尝试匹配 "N G obj"。返回 num, gen, 体起始位置。
func matchObjectHeader(data []byte, pos int) (num, gen, bodyStart int, ok bool) {
	i := pos
	if i >= len(data) || data[i] < '0' || data[i] > '9' {
		return 0, 0, 0, false
	}
	start := i
	for i < len(data) && data[i] >= '0' && data[i] <= '9' {
		i++
	}
	numTok := string(data[start:i])
	if i >= len(data) || !isWhitespace(data[i]) {
		return 0, 0, 0, false
	}
	for i < len(data) && isWhitespace(data[i]) {
		i++
	}
	if i >= len(data) || data[i] < '0' || data[i] > '9' {
		return 0, 0, 0, false
	}
	gstart := i
	for i < len(data) && data[i] >= '0' && data[i] <= '9' {
		i++
	}
	genTok := string(data[gstart:i])
	if i >= len(data) || !isWhitespace(data[i]) {
		return 0, 0, 0, false
	}
	for i < len(data) && isWhitespace(data[i]) {
		i++
	}
	if i+3 > len(data) || string(data[i:i+3]) != "obj" {
		return 0, 0, 0, false
	}
	after := i + 3
	if after < len(data) && !isWhitespace(data[after]) && data[after] != '<' && data[after] != '[' &&
		data[after] != '(' && data[after] != '/' && data[after] != '%' {
		return 0, 0, 0, false
	}
	n, err1 := strconv.Atoi(numTok)
	g, err2 := strconv.Atoi(genTok)
	if err1 != nil || err2 != nil {
		return 0, 0, 0, false
	}
	return n, g, after, true
}

// parseIndirectObject 从 offset 解析一个间接对象。
func parseIndirectObject(data []byte, offset int64, limits Limits) (*IndirectObject, []Problem) {
	var problems []Problem
	if offset < 0 || offset >= int64(len(data)) {
		return nil, []Problem{{Severity: "error", Code: "offset-out-of-range", Message: "对象偏移超出文件范围", Offset: offset}}
	}
	num, gen, bodyStart, ok := matchObjectHeader(data, int(offset))
	if !ok {
		return nil, []Problem{{Severity: "error", Code: "bad-object-header", Message: "偏移处不是合法的间接对象头", Offset: offset}}
	}
	obj := &IndirectObject{Num: num, Gen: gen, HeaderEnd: bodyStart}
	p := &parser{data: data, pos: bodyStart}
	val, err := p.parseValue(true)
	if err != nil {
		problems = append(problems, Problem{Severity: "error", Code: "parse-value", Message: "对象体解析失败: " + err.Error(), Offset: offset})
		obj.Range = ByteRange{Start: offset, End: int64(bodyStart)}
		obj.Value = nil
		return obj, problems
	}
	obj.Value = val
	obj.ValueEnd = p.pos

	if val.Kind == VStream {
		obj.HasStream = true
		dataStart, ok2 := p.streamKeywordAt(p.pos)
		if !ok2 {
			problems = append(problems, Problem{Severity: "error", Code: "stream-keyword-missing", Message: "stream 关键字缺失", Offset: offset})
		} else {
			si := val.Stream
			if si == nil {
				si = &StreamInfo{}
			}
			obj.Stream = si
			endPos, streamEnd, sdata, sprobs := locateStream(data, dataStart, val, si, offset)
			problems = append(problems, sprobs...)
			obj.StreamData = sdata
			obj.StreamEnd = streamEnd
			obj.ValueEnd = endPos
			p.pos = endPos
		}
	}

	// 查找 endobj
	rest := data[min(p.pos, len(data)):]
	idx := bytes.Index(rest, []byte("endobj"))
	if idx >= 0 {
		obj.EndObjFound = true
		obj.EndPos = p.pos + idx + len("endobj")
	} else {
		obj.EndObjFound = false
		obj.EndPos = p.pos
		problems = append(problems, Problem{Severity: "warning", Code: "endobj-missing", Message: "未找到 endobj", Offset: offset})
	}
	obj.Range = ByteRange{Start: offset, End: int64(obj.EndPos)}
	return obj, problems
}

// locateStream 依据 /Length 与 endstream 边界核验流数据。
func locateStream(data []byte, dataStart int, dict *Value, si *StreamInfo, objOffset int64) (endPos, streamEnd int, streamData []byte, problems []Problem) {
	si.StreamRange.Start = int64(dataStart)
	declared, hasDecl := resolveLengthDirect(dict)
	if hasDecl {
		si.LengthDecl = declared
	}
	endIdx := bytes.Index(data[dataStart:], []byte("endstream"))
	if endIdx < 0 {
		problems = append(problems, Problem{Severity: "error", Code: "endstream-missing", Message: "未找到 endstream", Offset: objOffset})
		si.BoundaryVerified = false
		si.StreamRange.End = int64(len(data))
		return len(data), len(data), nil, problems
	}
	streamEnd = dataStart + endIdx
	actual := streamEnd - dataStart
	// 去掉 endstream 前可能的 EOL
	trimmed := actual
	for trimmed > 0 && (data[dataStart+trimmed-1] == '\n' || data[dataStart+trimmed-1] == '\r') {
		trimmed--
	}
	si.LengthActual = int64(trimmed)
	si.StreamRange.End = int64(dataStart + trimmed)
	si.BoundaryVerified = true
	if hasDecl {
		if int64(trimmed) == declared || int64(actual) == declared {
			si.LengthVerified = true
		} else {
			si.LengthVerified = false
			problems = append(problems, Problem{Severity: "warning", Code: "stream-length-mismatch",
				Message: "声明长度与实际边界不一致", Offset: objOffset})
		}
	}
	streamData = data[dataStart : dataStart+trimmed]
	endPos = streamEnd + len("endstream")
	return endPos, endPos, streamData, problems
}

// resolveLengthDirect 仅解析字典里直接给出的整数 /Length（间接引用由扫描器二次解析）。
func resolveLengthDirect(dict *Value) (int64, bool) {
	lv := dict.DictGet("Length")
	if lv == nil {
		return 0, false
	}
	if lv.Kind == VInt {
		return lv.Int, true
	}
	return 0, false
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
