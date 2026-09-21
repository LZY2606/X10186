package pdf

// stream.go: 读取并核验流对象与普通对象的原始字节边界。
// 只有 Length、Filter、endstream/endobj 边界三者都核验通过才允许展开。

import (
	"bytes"
	"errors"
	"fmt"
)

// readObjectResult 读取一个间接对象的核验结果。
type readObjectResult struct {
	objNum      int
	gen         int
	offset      int // obj 头起点
	endOffset   int
	dict        *Value // 顶层字典（stream 对象）
	value       *Value // 完整解析值
	isStream    bool
	streamOff   int
	streamLen   int // 核验后的原始长度，-1 表示仅启发式
	lenVerified bool
	filters     []string
	filterOK    bool
	raw         []byte // 原始 stream 字节（边界核验通过时）
	expanded    []byte // 安全展开后的字节
	expErr      string
	note        string
}

// readObjectAt 从 offset 读取 "n g obj ... endobj"，全部基于原件。
func (s *scanner) readObjectAt(offset int, maxExpand int) *readObjectResult {
	d := s.data
	res := &readObjectResult{offset: offset, streamLen: -1, filterOK: true}
	q := skipSpace(d, offset)
	num, p1, ok1 := readDecimal(d, q)
	gen, p2, ok2 := readDecimal(d, p1)
	p3 := skipSpace(d, p2)
	if !ok1 || !ok2 || p3+3 > len(d) || !bytes.Equal(d[p3:p3+3], []byte("obj")) {
		res.note = "对象头不是 'n g obj'"
		return res
	}
	res.objNum, res.gen = num, gen
	bodyStart := skipSpace(d, p3+3)

	// parseObjectBody 已完成 dict→stream 检测与 stream 起点(EOL 后)计算。
	v, err := parseObjectBody(d, bodyStart, len(d))
	if err != nil {
		res.note = "对象体解析失败: " + err.Error()
		return res
	}
	res.value = v
	if v.Kind != "stream" {
		// 普通对象：从对象体就近定位 endobj，避免命中后面对象的标记。
		search := bodyStart
		if search < bodyStart {
			search = bodyStart
		}
		rel := bytes.Index(d[search:], []byte("endobj"))
		if rel < 0 {
			res.note = "缺少 endobj"
			res.endOffset = len(d)
			return res
		}
		res.endOffset = search + rel + len("endobj")
		return res
	}

	res.isStream = true
	res.dict = v
	res.streamOff = v.StreamOff

	// Filter 链
	if fv := v.DictGet("Filter"); fv != nil {
		name, ok := filterName(fv)
		if !ok {
			res.filterOK = false
			res.note = "Filter 含非名称元素，无法安全判定"
		} else {
			res.filters = splitCSV(name)
			for _, f := range res.filters {
				if !supportedFilter(f) {
					res.filterOK = false
					if res.note == "" {
						res.note = "过滤器 " + f + " 不受支持，保持原始字节不展开"
					}
				}
			}
		}
	}

	// Length 解析（直接整数或间接引用）
	declLen := -1
	if lv := v.DictGet("Length"); lv != nil {
		switch lv.Kind {
		case "int":
			declLen = int(lv.Int)
		case "ref":
			if off, ok := s.resolveOffset(lv.RefNum, lv.RefGen); ok {
				if iv, ierr := s.parseIntObject(off); ierr == nil {
					declLen = iv
				} else {
					s.diag(DiagWarn, "length-resolve", off,
						fmt.Sprintf("对象 %d 的 /Length 引用 %d %d R 无法解析", num, lv.RefNum, lv.RefGen),
						ierr.Error())
				}
			} else {
				s.diag(DiagWarn, "length-ref-missing", res.streamOff,
					fmt.Sprintf("对象 %d 的 /Length 引用 %d %d R 未在已追踪 xref 中声明", num, lv.RefNum, lv.RefGen), "")
			}
		}
	}

	dataStart := res.streamOff
	if declLen >= 0 && dataStart+declLen <= len(d) {
		// 边界核验：raw 之后跳过多余的一个 EOL，必须紧跟 endstream
		candEnd := dataStart + declLen
		if s.expectKeyword(candEnd, "endstream") {
			res.streamLen = declLen
			res.lenVerified = true
			res.raw = d[dataStart:candEnd]
		} else {
			s.diag(DiagError, "stream-boundary", candEnd,
				fmt.Sprintf("对象 %d 的声明 Length=%d 与 endstream 边界不符", num, declLen),
				snippet(d, candEnd, 16))
		}
	} else if declLen >= 0 {
		s.diag(DiagError, "stream-length-oob", dataStart,
			fmt.Sprintf("对象 %d 的 Length=%d 越界", num, declLen), "")
	}

	if !res.lenVerified {
		// 启发式边界：就近 endstream（仅作为标记，不改变 length 核验状态）
		idx := bytes.Index(d[dataStart:], []byte("endstream"))
		if idx < 0 {
			res.note = noteOr(res.note, "找不到 endstream，流边界无法核验")
			res.endOffset = len(d)
			return res
		}
		rawEnd := dataStart + idx
		for rawEnd > dataStart && (d[rawEnd-1] == '\r' || d[rawEnd-1] == '\n') {
			rawEnd--
		}
		res.streamLen = rawEnd - dataStart
		res.raw = d[dataStart:rawEnd]
		if res.note == "" {
			res.note = "Length 无效，边界由就近 endstream 启发式确定"
		}
	}

	// endobj 边界：从 raw 末尾跳过 EOL + endstream + 空白
	es := dataStart + res.streamLen
	tmp := es
	for tmp < len(d) && (d[tmp] == '\r' || d[tmp] == '\n') {
		tmp++
	}
	if tmp+len("endstream") <= len(d) && bytes.Equal(d[tmp:tmp+len("endstream")], []byte("endstream")) {
		tmp += len("endstream")
	}
	tmp2 := skipSpace(d, tmp)
	if tmp2+len("endobj") <= len(d) && bytes.Equal(d[tmp2:tmp2+len("endobj")], []byte("endobj")) {
		res.endOffset = tmp2 + len("endobj")
	} else {
		res.note = noteOr(res.note, "endstream 后缺少 endobj")
		res.endOffset = tmp2
	}

	// 仅在三重核验通过时展开
	if res.lenVerified && res.filterOK && len(res.filters) > 0 && maxExpand >= 0 {
		out, ierr := inflate(res.raw, maxExpand)
		if ierr != nil {
			var le *LimitError
			if errors.As(ierr, &le) {
				res.expErr = fmt.Sprintf("解压输出超过限额 %d 字节，已安全停止", maxExpand)
			} else {
				res.expErr = "FlateDecode 解压失败: " + ierr.Error()
			}
		} else {
			res.expanded = out
		}
	}
	if len(res.filters) == 0 {
		// 无过滤器：原始即“展开”内容（例如普通流），不另行标记
	}
	return res
}

func noteOr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func (s *scanner) expectKeyword(off int, kw string) bool {
	p := off
	// Length 不含结尾 EOL；允许恰好一个行结束序列
	if p < len(s.data) && s.data[p] == '\r' {
		p++
	}
	if p < len(s.data) && s.data[p] == '\n' {
		p++
	}
	return p+len(kw) <= len(s.data) && bytes.Equal(s.data[p:p+len(kw)], []byte(kw))
}

// resolveOffset 在所有已解析段的声明中查找对象偏移（类型 1）。
func (s *scanner) resolveOffset(obj, gen int) (int, bool) {
	for _, sec := range s.sections {
		if e, ok := s.declared[sec.scanOrder][obj]; ok && e.typ == entryInUse {
			return e.field1, true
		}
	}
	return 0, false
}

// parseIntObject 读取一个只含整数的间接对象（用于 /Length 引用）。
func (s *scanner) parseIntObject(off int) (int, error) {
	d := s.data
	q := skipSpace(d, off)
	_, p1, ok1 := readDecimal(d, q)
	if !ok1 {
		return 0, errors.New("bad object header")
	}
	_, p2, ok2 := readDecimal(d, p1)
	if !ok2 {
		return 0, errors.New("bad object header")
	}
	p3 := skipSpace(d, p2)
	if p3+3 > len(d) || !bytes.Equal(d[p3:p3+3], []byte("obj")) {
		return 0, errors.New("bad object header")
	}
	pp := &parser{data: d, base: 0, end: len(d), maxD: 10}
	v, err := pp.parseValue(skipSpace(d, p3+3))
	if err != nil {
		return 0, err
	}
	if v.Kind != "int" {
		return 0, fmt.Errorf("Length 对象不是整数(kind=%s)", v.Kind)
	}
	return int(v.Int), nil
}

func splitCSV(s string) []string {
	if s == "" {
		return nil
	}
	var out []string
	cur := ""
	for _, c := range s {
		if c == ',' {
			out = append(out, cur)
			cur = ""
		} else {
			cur += string(c)
		}
	}
	return append(out, cur)
}
