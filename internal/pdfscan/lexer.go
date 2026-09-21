package pdfscan

import (
	"bytes"
	"encoding/base64"
	"math"
	"strconv"
)

const (
	rawInlineLimit = 4096
	strPreviewLen  = 200
)

type parser struct {
	data []byte
	pos  int
}

func isWhitespace(c byte) bool {
	switch c {
	case 0, 9, 10, 12, 13, 32:
		return true
	}
	return false
}

func isRegular(c byte) bool {
	if c < 0x21 || c > 0x7e {
		return false
	}
	switch c {
	case '(', ')', '<', '>', '[', ']', '{', '}', '/', '%':
		return false
	}
	return true
}

func (p *parser) skipWSAndComments() {
	for p.pos < len(p.data) {
		c := p.data[p.pos]
		if isWhitespace(c) {
			p.pos++
			continue
		}
		if c == '%' {
			for p.pos < len(p.data) && p.data[p.pos] != '\n' && p.data[p.pos] != '\r' {
				p.pos++
			}
			continue
		}
		break
	}
}

func (p *parser) startsWithAt(pos int, s string) bool {
	if pos+len(s) > len(p.data) {
		return false
	}
	return string(p.data[pos:pos+len(s)]) == s
}

// streamKeywordAt 检查 pos（跳过空白/注释后）是否为 stream 关键字，
// 并返回流数据起始位置（关键字后的 EOL）。
func (p *parser) streamKeywordAt(pos int) (int, bool) {
	q := parser{data: p.data, pos: pos}
	q.skipWSAndComments()
	s := q.pos
	if !q.startsWithAt(s, "stream") {
		return 0, false
	}
	after := s + len("stream")
	if after < len(q.data) {
		if q.data[after] == '\r' && after+1 < len(q.data) && q.data[after+1] == '\n' {
			return after + 2, true
		}
		if q.data[after] == '\n' || q.data[after] == '\r' {
			return after + 1, true
		}
	}
	return 0, false
}

// parseValue 解析一个 PDF 值。stopAtStream 时字典后若跟 stream 关键字则返回 VStream。
func (p *parser) parseValue(stopAtStream bool) (*Value, error) {
	p.skipWSAndComments()
	if p.pos >= len(p.data) {
		return nil, errUnexpectedEnd
	}
	c := p.data[p.pos]
	switch {
	case c == '(':
		return p.parseLiteralString()
	case c == '<' && p.pos+1 < len(p.data) && p.data[p.pos+1] == '<':
		return p.parseDict(stopAtStream)
	case c == '<':
		return p.parseHexString()
	case c == '[':
		return p.parseArray()
	case c == '/':
		return p.parseName()
	case c == 't' || c == 'f':
		return p.parseBoolOrNull()
	case c == 'n':
		if p.startsWithAt(p.pos, "null") {
			p.pos += 4
			return &Value{Kind: VNull}, nil
		}
		return p.parseNumberOrRef(stopAtStream)
	case (c >= '0' && c <= '9') || c == '+' || c == '-' || c == '.':
		return p.parseNumberOrRef(stopAtStream)
	default:
		return nil, errUnexpectedToken
	}
}

func (p *parser) parseName() (*Value, error) {
	p.pos++ // consume /
	start := p.pos
	for p.pos < len(p.data) {
		c := p.data[p.pos]
		if isWhitespace(c) || c == '/' || c == '(' || c == ')' || c == '<' || c == '>' ||
			c == '[' || c == ']' || c == '%' {
			break
		}
		p.pos++
	}
	raw := p.data[start:p.pos]
	return &Value{Kind: VName, Name: decodeName(raw)}, nil
}

func decodeName(raw []byte) string {
	var out bytes.Buffer
	for i := 0; i < len(raw); i++ {
		if raw[i] == '#' && i+2 < len(raw) {
			if h, err := strconv.ParseUint(string(raw[i+1:i+3]), 16, 8); err == nil {
				out.WriteByte(byte(h))
				i += 2
				continue
			}
		}
		out.WriteByte(raw[i])
	}
	return out.String()
}

func (p *parser) parseArray() (*Value, error) {
	p.pos++ // consume [
	arr := &Value{Kind: VArray}
	for {
		p.skipWSAndComments()
		if p.pos >= len(p.data) {
			return nil, errUnexpectedEnd
		}
		if p.data[p.pos] == ']' {
			p.pos++
			return arr, nil
		}
		v, err := p.parseValue(false)
		if err != nil {
			return nil, err
		}
		arr.Array = append(arr.Array, v)
	}
}

func (p *parser) parseDict(stopAtStream bool) (*Value, error) {
	dictStart := p.pos
	p.pos += 2 // consume <<
	d := &Value{Kind: VDict}
	for {
		p.skipWSAndComments()
		if p.pos+1 < len(p.data) && p.data[p.pos] == '>' && p.data[p.pos+1] == '>' {
			p.pos += 2
			if stopAtStream {
				if dataStart, ok := p.streamKeywordAt(p.pos); ok {
					d.Kind = VStream
					si := &StreamInfo{DictRange: ByteRange{Start: int64(dictStart), End: int64(p.pos)}}
					_ = dataStart
					d.Stream = si
				}
			}
			return d, nil
		}
		if p.pos >= len(p.data) {
			return nil, errUnexpectedEnd
		}
		keyVal, err := p.parseName()
		if err != nil {
			return nil, err
		}
		val, err := p.parseValue(false)
		if err != nil {
			return nil, err
		}
		d.Dict = append(d.Dict, KV{Key: keyVal.Name, Value: val})
	}
}

func (p *parser) parseBoolOrNull() (*Value, error) {
	if p.startsWithAt(p.pos, "true") {
		p.pos += 4
		return &Value{Kind: VBool, Bool: true}, nil
	}
	if p.startsWithAt(p.pos, "false") {
		p.pos += 5
		return &Value{Kind: VBool, Bool: false}, nil
	}
	return nil, errUnexpectedToken
}

func (p *parser) parseNumberOrRef(stopAtStream bool) (*Value, error) {
	start := p.pos
	if p.data[p.pos] == '+' || p.data[p.pos] == '-' {
		p.pos++
	}
	hasDigit := false
	isReal := false
	for p.pos < len(p.data) {
		c := p.data[p.pos]
		if c >= '0' && c <= '9' {
			hasDigit = true
			p.pos++
			continue
		}
		if c == '.' && !isReal {
			isReal = true
			p.pos++
			continue
		}
		break
	}
	if !hasDigit {
		return nil, errUnexpectedToken
	}
	tok := string(p.data[start:p.pos])

	// 探测 "N G R" 间接引用
	save := p.pos
	p.skipWSAndComments()
	gStart := p.pos
	if p.pos < len(p.data) && ((p.data[p.pos] >= '0' && p.data[p.pos] <= '9') || p.data[p.pos] == '-' || p.data[p.pos] == '+') {
		if p.data[p.pos] == '-' || p.data[p.pos] == '+' {
			p.pos++
		}
		gDigit := false
		gReal := false
		for p.pos < len(p.data) {
			c := p.data[p.pos]
			if c >= '0' && c <= '9' {
				gDigit = true
				p.pos++
				continue
			}
			if c == '.' && !gReal {
				gReal = true
				p.pos++
				continue
			}
			break
		}
		if gDigit && !gReal {
			p.skipWSAndComments()
			if p.startsWithAt(p.pos, "R") {
				genTok := string(p.data[gStart:p.pos])
				n, err1 := strconv.Atoi(tok)
				g, err2 := strconv.Atoi(genTok)
				if err1 == nil && err2 == nil && n >= 0 && g >= 0 {
					p.pos++ // consume R
					return &Value{Kind: VRef, RefNum: n, RefGen: g}, nil
				}
			}
		}
	}
	p.pos = save

	if isReal {
		f, err := strconv.ParseFloat(tok, 64)
		if err != nil {
			return nil, errUnexpectedToken
		}
		if math.IsInf(f, 0) || math.IsNaN(f) {
			return nil, errUnexpectedToken
		}
		return &Value{Kind: VReal, Real: f}, nil
	}
	n, err := strconv.ParseInt(tok, 10, 64)
	if err != nil {
		return nil, errUnexpectedToken
	}
	_ = stopAtStream
	return &Value{Kind: VInt, Int: n}, nil
}

func (p *parser) parseLiteralString() (*Value, error) {
	start := p.pos
	p.pos++ // (
	depth := 1
	var out bytes.Buffer
	for p.pos < len(p.data) {
		c := p.data[p.pos]
		if c == '\\' {
			p.pos++
			if p.pos >= len(p.data) {
				break
			}
			e := p.data[p.pos]
			switch e {
			case 'n':
				out.WriteByte('\n')
			case 'r':
				out.WriteByte('\r')
			case 't':
				out.WriteByte('\t')
			case 'b':
				out.WriteByte('\b')
			case 'f':
				out.WriteByte('\f')
			case '(':
				out.WriteByte('(')
			case ')':
				out.WriteByte(')')
			case '\\':
				out.WriteByte('\\')
			case '\r':
				p.pos++
				if p.pos < len(p.data) && p.data[p.pos] == '\n' {
				}
				continue
			case '\n':
				// line continuation
			default:
				if e >= '0' && e <= '7' {
					val := int(e - '0')
					for k := 0; k < 2 && p.pos+1 < len(p.data) && p.data[p.pos+1] >= '0' && p.data[p.pos+1] <= '7'; k++ {
						p.pos++
						val = val*8 + int(p.data[p.pos]-'0')
					}
					out.WriteByte(byte(val & 0xff))
				} else {
					out.WriteByte(e)
				}
			}
			p.pos++
			continue
		}
		if c == '(' {
			depth++
			out.WriteByte(c)
			p.pos++
			continue
		}
		if c == ')' {
			depth--
			p.pos++
			if depth == 0 {
				raw := p.data[start:p.pos]
				return makeStringValue(raw, out.Bytes(), false), nil
			}
			out.WriteByte(c)
			continue
		}
		if c == '\r' {
			out.WriteByte('\n')
			if p.pos+1 < len(p.data) && p.data[p.pos+1] == '\n' {
				p.pos++
			}
		} else {
			out.WriteByte(c)
		}
		p.pos++
	}
	return nil, errUnterminatedString
}

func (p *parser) parseHexString() (*Value, error) {
	start := p.pos
	p.pos++ // <
	var digits []byte
	for p.pos < len(p.data) {
		c := p.data[p.pos]
		if c == '>' {
			p.pos++
			raw := p.data[start:p.pos]
			if len(digits)%2 == 1 {
				digits = append(digits, '0')
			}
			decoded := make([]byte, len(digits)/2)
			for i := 0; i < len(digits); i += 2 {
				hi := hexVal(digits[i])
				lo := hexVal(digits[i+1])
				decoded[i/2] = byte(hi<<4 | lo)
			}
			return makeStringValue(raw, decoded, true), nil
		}
		if !isWhitespace(c) {
			digits = append(digits, c)
		}
		p.pos++
	}
	return nil, errUnterminatedHex
}

func hexVal(c byte) int {
	switch {
	case c >= '0' && c <= '9':
		return int(c - '0')
	case c >= 'a' && c <= 'f':
		return int(c-'a') + 10
	case c >= 'A' && c <= 'F':
		return int(c-'A') + 10
	}
	return 0
}

func makeStringValue(raw, decoded []byte, hex bool) *Value {
	v := &Value{Kind: VString, Hex: hex, RawLen: len(raw)}
	preview := printablePreview(decoded)
	if len(preview) > strPreviewLen {
		v.Text = string(preview[:strPreviewLen])
		v.Truncated = true
	} else {
		v.Text = string(preview)
	}
	if len(raw) <= rawInlineLimit {
		v.RawBase64 = base64.StdEncoding.EncodeToString(raw)
	}
	return v
}

func printablePreview(b []byte) []byte {
	out := make([]byte, 0, len(b))
	for _, c := range b {
		if c == '\n' || c == '\r' || c == '\t' || (c >= 0x20 && c < 0x7f) || c >= 0x80 {
			out = append(out, c)
		} else {
			out = append(out, '.')
		}
	}
	return out
}
