package pdf

// syntax.go: 只读的 PDF 词法/语法解析。
// 所有解析均基于原始字节切片，绝不修改输入。

import (
	"bytes"
	"fmt"
	"strconv"
)

var eol = []byte{'\n'}

// isWhitespace 按 PDF 规范 §7.2.2 判定空白。
func isWhitespace(b byte) bool {
	switch b {
	case 0, 9, 10, 12, 13, 32:
		return true
	}
	return false
}

// isDelimiter 按 PDF 规范 §7.2.2 判定分隔符。
func isDelimiter(b byte) bool {
	switch b {
	case '(', ')', '<', '>', '[', ']', '{', '}', '/', '%':
		return true
	}
	return false
}

// isRegular 判定非空白非分隔的普通字节。
func isRegular(b byte) bool { return !isWhitespace(b) && !isDelimiter(b) }

// skipSpace 跳过空白与注释，返回首个有效位置。
func skipSpace(data []byte, p int) int {
	for p < len(data) {
		b := data[p]
		if isWhitespace(b) {
			p++
			continue
		}
		if b == '%' {
			for p < len(data) && data[p] != '\n' && data[p] != '\r' {
				p++
			}
			continue
		}
		break
	}
	return p
}

// readToken 读取由分隔符/空白界定的原子 token。
func readToken(data []byte, p int) ([]byte, int) {
	start := p
	for p < len(data) && isRegular(data[p]) {
		p++
	}
	return data[start:p], p
}

// Value 是解析后的 PDF 对象值（仅用于展示，不回写文件）。
type Value struct {
	Kind      string      `json:"kind"` // null bool int real name string array dict ref stream marker
	Text      string      `json:"text,omitempty"`
	Int       int64       `json:"int,omitempty"`
	Real      float64     `json:"real,omitempty"`
	Bool      bool        `json:"bool,omitempty"`
	Items     []*Value    `json:"items,omitempty"`
	Entries   []DictEntry `json:"entries,omitempty"`
	RefNum    int         `json:"refNum,omitempty"`
	RefGen    int         `json:"refGen,omitempty"`
	StreamOff int         `json:"stream,omitempty"` // stream 数据起始
	StreamLen int         `json:"streamLen,omitempty"`
}

// DictEntry 保留字典原始出现顺序（便于对照原字节）。
type DictEntry struct {
	Key   string `json:"key"`
	Value *Value `json:"value"`
}

func (v *Value) DictGet(key string) *Value {
	if v == nil || (v.Kind != "dict" && v.Kind != "stream") {
		return nil
	}
	for i := range v.Entries {
		if v.Entries[i].Key == key {
			return v.Entries[i].Value
		}
	}
	return nil
}

// ParseError 记录解析中断位置。
type ParseError struct {
	Offset int
	Msg    string
}

func (e *ParseError) Error() string { return fmt.Sprintf("parse error @%d: %s", e.Offset, e.Msg) }

// parser 在一个对象的字节区间内做深度有限的递归解析。
type parser struct {
	saved int
	data  []byte
	base  int // 区间起点相对文件的偏移
	end   int // 区间终点（文件绝对偏移）
	maxD  int
}

// parseObjectBody 解析一个间接对象的内容，返回值（可能含 stream 标记）。
func parseObjectBody(file []byte, start, end int) (*Value, error) {
	p := &parser{data: file, base: start, end: end, maxD: 40}
	q := skipSpace(file, start)
	v, err := p.parseValue(q)
	if err != nil {
		return nil, err
	}
	// stream 关键字紧跟在字典后，必须是独立 token，
	// 且后面紧跟一个行结束符（CRLF / 仅 LF / 仅 CR）。
	if v.Kind == "dict" {
		r := skipSpace(file, p.saved)
		if r+6 <= end && bytes.Equal(file[r:r+6], []byte("stream")) &&
			(r+6 == end || isDelimiter(file[r+6]) || isWhitespace(file[r+6])) {
			dataStart := -1
			switch {
			case r+7 < end && file[r+6] == '\r' && file[r+7] == '\n':
				dataStart = r + 8
			case r+6 < end && file[r+6] == '\n':
				dataStart = r + 7
			case r+6 < end && file[r+6] == '\r':
				dataStart = r + 7
			}
			if dataStart >= 0 {
				v.Kind = "stream"
				v.StreamOff = dataStart
				v.StreamLen = -1
			}
		}
	}
	return v, nil
}

// parseValue 从 pos 解析单个值。
func (p *parser) parseValue(pos int) (*Value, error) {
	if p.maxD <= 0 {
		return nil, &ParseError{Offset: pos, Msg: "nesting limit"}
	}
	pos = skipSpace(p.data, pos)
	if pos >= p.end {
		return nil, &ParseError{Offset: pos, Msg: "unexpected end"}
	}
	b := p.data[pos]
	switch {
	case b == '<' && pos+1 < p.end && p.data[pos+1] == '<':
		p.maxD--
		defer func() { p.maxD++ }()
		return p.parseDict(pos)
	case b == '[':
		p.maxD--
		defer func() { p.maxD++ }()
		return p.parseArray(pos)
	case b == '(':
		return p.parseLiteral(pos)
	case b == '<':
		return p.parseHex(pos)
	case b == '/':
		return p.parseName(pos)
	default:
		return p.parseAtom(pos)
	}
}

func (p *parser) parseDict(pos int) (*Value, error) {
	q := pos + 2
	d := &Value{Kind: "dict"}
	for {
		q = skipSpace(p.data, q)
		if q+2 <= p.end && p.data[q] == '<' && p.data[q+1] == '<' {
			q += 2
		}
		q = skipSpace(p.data, q)
		if q+2 <= p.end && p.data[q] == '>' && p.data[q+1] == '>' {
			q += 2
			break
		}
		if q >= p.end {
			return nil, &ParseError{Offset: q, Msg: "unterminated dict"}
		}
		if p.data[q] != '/' {
			return nil, &ParseError{Offset: q, Msg: "dict key must be name"}
		}
		keyVal, err := p.parseName(q)
		if err != nil {
			return nil, err
		}
		q = skipSpace(p.data, p.saved)
		val, err := p.parseValue(q)
		if err != nil {
			return nil, err
		}
		q = p.saved
		d.Entries = append(d.Entries, DictEntry{Key: keyVal.Text, Value: val})
	}
	p.saved = q
	return d, nil
}

func (p *parser) parseArray(pos int) (*Value, error) {
	q := pos + 1
	a := &Value{Kind: "array"}
	for {
		q = skipSpace(p.data, q)
		if q >= p.end {
			return nil, &ParseError{Offset: q, Msg: "unterminated array"}
		}
		if p.data[q] == ']' {
			q++
			break
		}
		v, err := p.parseValue(q)
		if err != nil {
			return nil, err
		}
		q = p.saved
		a.Items = append(a.Items, v)
	}
	p.saved = q
	return a, nil
}

func (p *parser) parseName(pos int) (*Value, error) {
	q := pos + 1
	start := q
	for q < p.end && isRegular(p.data[q]) {
		q++
	}
	raw := p.data[start:q]
	name := decodeName(raw)
	p.saved = q
	return &Value{Kind: "name", Text: name}, nil
}

// decodeName 处理 #xx 十六进制转义。
func decodeName(raw []byte) string {
	if bytes.IndexByte(raw, '#') < 0 {
		return string(raw)
	}
	out := make([]byte, 0, len(raw))
	for i := 0; i < len(raw); i++ {
		if raw[i] == '#' && i+2 < len(raw) {
			if h, ok := hexVal(raw[i+1]); ok {
				if l, ok2 := hexVal(raw[i+2]); ok2 {
					out = append(out, h<<4|l)
					i += 2
					continue
				}
			}
		}
		out = append(out, raw[i])
	}
	return string(out)
}

func hexVal(b byte) (byte, bool) {
	switch {
	case b >= '0' && b <= '9':
		return b - '0', true
	case b >= 'a' && b <= 'f':
		return b - 'a' + 10, true
	case b >= 'A' && b <= 'F':
		return b - 'A' + 10, true
	}
	return 0, false
}

func (p *parser) parseLiteral(pos int) (*Value, error) {
	q := pos + 1
	depth := 1
	var buf bytes.Buffer
	for q < p.end {
		b := p.data[q]
		switch b {
		case '\\':
			if q+1 >= p.end {
				q++
				continue
			}
			e := p.data[q+1]
			switch e {
			case 'n':
				buf.WriteByte('\n')
			case 'r':
				buf.WriteByte('\r')
			case 't':
				buf.WriteByte('\t')
			case 'b':
				buf.WriteByte('\b')
			case 'f':
				buf.WriteByte('\f')
			case '(':
				buf.WriteByte('(')
			case ')':
				buf.WriteByte(')')
			case '\\':
				buf.WriteByte('\\')
			case '\r':
				if q+2 < p.end && p.data[q+2] == '\n' {
					q++
				}
				// 行连接：不写入
			case '\n':
				// 行连接
			default:
				if h, ok := hexVal(e); ok {
					val := h
					k := q + 2
					for j := 0; j < 2 && k < p.end; j, k = j+1, k+1 {
						if h2, ok2 := hexVal(p.data[k]); ok2 {
							val = val<<4 | h2
						} else {
							break
						}
					}
					buf.WriteByte(val)
					q = k - 1
				} else {
					buf.WriteByte(e)
				}
			}
			q += 2
			continue
		case '(':
			depth++
			buf.WriteByte(b)
		case ')':
			depth--
			if depth == 0 {
				q++
				p.saved = q
				return &Value{Kind: "string", Text: buf.String()}, nil
			}
			buf.WriteByte(b)
		default:
			buf.WriteByte(b)
		}
		q++
	}
	return nil, &ParseError{Offset: pos, Msg: "unterminated literal string"}
}

func (p *parser) parseHex(pos int) (*Value, error) {
	q := pos + 1
	var nibs []byte
	for q < p.end {
		b := p.data[q]
		if isWhitespace(b) {
			q++
			continue
		}
		if b == '>' {
			q++
			break
		}
		h, ok := hexVal(b)
		if !ok {
			return nil, &ParseError{Offset: q, Msg: "bad hex digit"}
		}
		nibs = append(nibs, h)
		q++
	}
	var out []byte
	for i := 0; i+1 < len(nibs); i += 2 {
		out = append(out, nibs[i]<<4|nibs[i+1])
	}
	if len(nibs)%2 == 1 {
		out = append(out, nibs[len(nibs)-1]<<4)
	}
	p.saved = q
	return &Value{Kind: "string", Text: string(out)}, nil
}

func (p *parser) parseAtom(pos int) (*Value, error) {
	tok, next := readToken(p.data, pos)
	p.saved = next
	s := string(tok)
	if s == "" {
		return nil, &ParseError{Offset: pos, Msg: "empty token"}
	}
	if s == "true" {
		return &Value{Kind: "bool", Bool: true}, nil
	}
	if s == "false" {
		return &Value{Kind: "bool", Bool: false}, nil
	}
	if s == "null" {
		return &Value{Kind: "null"}, nil
	}
	if i, err := strconv.ParseInt(s, 10, 64); err == nil {
		// 间接引用: int int R
		q := skipSpace(p.data, next)
		t2, n2 := readToken(p.data, q)
		if len(t2) > 0 {
			if gen, err2 := strconv.Atoi(string(t2)); err2 == nil {
				q2 := skipSpace(p.data, n2)
				if q2 < p.end && p.data[q2] == 'R' {
					after := q2 + 1
					if after >= p.end || !isRegular(p.data[after]) {
						p.saved = after
						return &Value{Kind: "ref", RefNum: int(i), RefGen: gen}, nil
					}
				}
			}
		}
		return &Value{Kind: "int", Int: i}, nil
	}
	if r, err := strconv.ParseFloat(s, 64); err == nil && bytes.ContainsAny(tok, ".") {
		return &Value{Kind: "real", Real: r}, nil
	}
	// 其它（如 stream/endobj 关键字出现在意外位置）当作标记
	return &Value{Kind: "marker", Text: s}, nil
}
