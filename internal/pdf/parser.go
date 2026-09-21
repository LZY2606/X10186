package pdf

import (
	"bytes"
	"fmt"
	"strconv"
)

func isWS(b byte) bool {
	switch b {
	case 0, 9, 10, 12, 13, 32:
		return true
	}
	return false
}

func isDelim(b byte) bool {
	if isWS(b) {
		return true
	}
	switch b {
	case '(', ')', '<', '>', '[', ']', '{', '}', '/', '%':
		return true
	}
	return false
}

type parser struct {
	data []byte
	pos  int
}

func (p *parser) skipWS() {
	for p.pos < len(p.data) {
		b := p.data[p.pos]
		if b == '%' {
			for p.pos < len(p.data) && p.data[p.pos] != '\n' && p.data[p.pos] != '\r' {
				p.pos++
			}
			continue
		}
		if !isWS(b) {
			return
		}
		p.pos++
	}
}

// token reads one regular token (name body, number, keyword...).
func (p *parser) token() string {
	start := p.pos
	for p.pos < len(p.data) && !isDelim(p.data[p.pos]) {
		p.pos++
	}
	return string(p.data[start:p.pos])
}

func (p *parser) hasPrefix(s string) bool {
	return bytes.HasPrefix(p.data[p.pos:], []byte(s))
}

// parseValue parses one PDF object at the current position.
func (p *parser) parseValue() (Value, error) {
	p.skipWS()
	if p.pos >= len(p.data) {
		return nil, fmt.Errorf("unexpected end of data at %d", p.pos)
	}
	b := p.data[p.pos]
	switch {
	case b == '<':
		if p.pos+1 < len(p.data) && p.data[p.pos+1] == '<' {
			return p.parseDict()
		}
		return p.parseHexString()
	case b == '[':
		return p.parseArray()
	case b == '(':
		return p.parseLiteralString()
	case b == '/':
		p.pos++
		return Name(p.token()), nil
	}
	tok := p.token()
	switch tok {
	case "true":
		return true, nil
	case "false":
		return false, nil
	case "null":
		return nil, nil
	case "":
		return nil, fmt.Errorf("empty token at %d", p.pos)
	}
	if n, err := strconv.ParseInt(tok, 10, 64); err == nil {
		// Look ahead for "num gen R".
		save := p.pos
		p.skipWS()
		tok2 := p.token()
		if g, err := strconv.ParseInt(tok2, 10, 64); err == nil {
			p.skipWS()
			if p.pos < len(p.data) && p.data[p.pos] == 'R' &&
				(p.pos+1 >= len(p.data) || isDelim(p.data[p.pos+1])) {
				p.pos++
				return Ref{Num: int(n), Gen: int(g)}, nil
			}
		}
		p.pos = save
		return n, nil
	}
	if f, err := strconv.ParseFloat(tok, 64); err == nil {
		return f, nil
	}
	return nil, fmt.Errorf("unexpected token %q at %d", tok, p.pos)
}

func (p *parser) parseDict() (Dict, error) {
	p.pos += 2 // consume <<
	d := Dict{}
	for {
		p.skipWS()
		if p.pos >= len(p.data) {
			return nil, fmt.Errorf("unterminated dictionary")
		}
		if p.hasPrefix(">>") {
			p.pos += 2
			return d, nil
		}
		if p.data[p.pos] != '/' {
			return nil, fmt.Errorf("expected name in dict at %d", p.pos)
		}
		p.pos++
		key := p.token()
		v, err := p.parseValue()
		if err != nil {
			return nil, err
		}
		d[key] = v
	}
}

func (p *parser) parseArray() (Array, error) {
	p.pos++ // consume [
	var arr Array
	for {
		p.skipWS()
		if p.pos >= len(p.data) {
			return nil, fmt.Errorf("unterminated array")
		}
		if p.data[p.pos] == ']' {
			p.pos++
			return arr, nil
		}
		v, err := p.parseValue()
		if err != nil {
			return nil, err
		}
		arr = append(arr, v)
	}
}

func (p *parser) parseLiteralString() (PdfString, error) {
	p.pos++ // consume (
	var buf bytes.Buffer
	depth := 1
	for p.pos < len(p.data) {
		b := p.data[p.pos]
		p.pos++
		switch b {
		case '\\':
			if p.pos >= len(p.data) {
				break
			}
			c := p.data[p.pos]
			p.pos++
			switch c {
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
			case '(', ')', '\\':
				buf.WriteByte(c)
			case '\r':
				if p.pos < len(p.data) && p.data[p.pos] == '\n' {
					p.pos++
				}
			case '\n':
			default:
				if c >= '0' && c <= '7' {
					oct := []byte{c}
					for i := 0; i < 2 && p.pos < len(p.data); i++ {
						d := p.data[p.pos]
						if d < '0' || d > '7' {
							break
						}
						oct = append(oct, d)
						p.pos++
					}
					v, _ := strconv.ParseUint(string(oct), 8, 8)
					buf.WriteByte(byte(v))
				} else {
					buf.WriteByte(c)
				}
			}
		case '(':
			depth++
			buf.WriteByte(b)
		case ')':
			depth--
			if depth == 0 {
				return PdfString(buf.String()), nil
			}
			buf.WriteByte(b)
		default:
			buf.WriteByte(b)
		}
	}
	return "", fmt.Errorf("unterminated string")
}

func (p *parser) parseHexString() (PdfString, error) {
	p.pos++ // consume <
	var nibbles []byte
	for p.pos < len(p.data) {
		b := p.data[p.pos]
		p.pos++
		if b == '>' {
			break
		}
		if isWS(b) {
			continue
		}
		nibbles = append(nibbles, b)
	}
	if len(nibbles)%2 == 1 {
		nibbles = append(nibbles, '0')
	}
	out := make([]byte, 0, len(nibbles)/2)
	for i := 0; i+1 < len(nibbles); i += 2 {
		v, err := strconv.ParseUint(string(nibbles[i:i+2]), 16, 8)
		if err != nil {
			return "", fmt.Errorf("bad hex string")
		}
		out = append(out, byte(v))
	}
	return PdfString(out), nil
}

// Indirect is a fully parsed indirect object with byte-range evidence.
type Indirect struct {
	Num, Gen   int
	Value      Value
	StreamData []byte // encoded stream bytes, nil when not a stream
	StreamDict Dict
	// DeclaredLen is the /Length value when it is a direct integer.
	DeclaredLen int64
	HasLength   bool
	BoundaryOK  bool // stream bytes verified against /Length and endstream
	Start       int64
	End         int64 // offset just past endobj (best effort)
	StreamStart int64
	StreamEnd   int64
	LengthIsRef bool
	Notes       []string
}

var endstreamPat = []byte("endstream")

// ParseIndirectAt parses "num gen obj ... endobj" starting at off.
// It never modifies data and tolerates minor damage, recording notes.
func ParseIndirectAt(data []byte, off int64) (*Indirect, error) {
	if off < 0 || off >= int64(len(data)) {
		return nil, fmt.Errorf("offset %d out of range", off)
	}
	p := &parser{data: data, pos: int(off)}
	p.skipWS()
	numTok := p.token()
	num, err1 := strconv.Atoi(numTok)
	p.skipWS()
	genTok := p.token()
	gen, err2 := strconv.Atoi(genTok)
	p.skipWS()
	kw := p.token()
	if err1 != nil || err2 != nil || kw != "obj" {
		return nil, fmt.Errorf("no indirect object at %d (got %q %q %q)", off, numTok, genTok, kw)
	}
	obj := &Indirect{Num: num, Gen: gen, Start: off}
	v, err := p.parseValue()
	if err != nil {
		return nil, fmt.Errorf("object %d %d: %w", num, gen, err)
	}
	obj.Value = v
	p.skipWS()
	if p.hasPrefix("stream") {
		p.pos += len("stream")
		// EOL after the stream keyword: CRLF or LF (a lone CR is tolerated).
		if p.pos+1 < len(data) && data[p.pos] == '\r' && data[p.pos+1] == '\n' {
			p.pos += 2
		} else if p.pos < len(data) && (data[p.pos] == '\n' || data[p.pos] == '\r') {
			p.pos++
		} else {
			obj.Notes = append(obj.Notes, "stream keyword not followed by EOL")
		}
		obj.StreamStart = int64(p.pos)
		d, _ := v.(Dict)
		obj.StreamDict = d
		if d != nil {
			if ln, ok := d.Int("Length"); ok && ln >= 0 {
				obj.DeclaredLen = ln
				obj.HasLength = true
			} else if r, ok2 := d.Get("Length"); ok2 {
				if _, isRef := r.(Ref); isRef {
					obj.LengthIsRef = true
				}
			}
		}
		if obj.HasLength {
			end := int64(p.pos) + obj.DeclaredLen
			if end <= int64(len(data)) {
				obj.StreamData = data[p.pos:end]
				obj.StreamEnd = end
				rest := data[end:]
				obj.BoundaryOK = bytes.HasPrefix(rest, endstreamPat) ||
					bytes.HasPrefix(rest, []byte("\r\nendstream")) ||
					bytes.HasPrefix(rest, []byte("\nendstream")) ||
					bytes.HasPrefix(rest, []byte("\rendstream"))
				if obj.BoundaryOK {
					idx := bytes.Index(rest, endstreamPat)
					p.pos = int(end) + idx + len(endstreamPat)
				} else {
					obj.Notes = append(obj.Notes, "endstream not found at declared length")
					idx := bytes.Index(data[p.pos:], endstreamPat)
					if idx < 0 {
						return nil, fmt.Errorf("object %d %d: endstream missing", num, gen)
					}
					obj.StreamData = data[p.pos : p.pos+idx]
					obj.StreamEnd = int64(p.pos + idx)
					p.pos += idx + len(endstreamPat)
				}
			} else {
				obj.Notes = append(obj.Notes, "declared length exceeds file size")
				idx := bytes.Index(data[p.pos:], endstreamPat)
				if idx < 0 {
					return nil, fmt.Errorf("object %d %d: endstream missing", num, gen)
				}
				obj.StreamData = data[p.pos : p.pos+idx]
				obj.StreamEnd = int64(p.pos + idx)
				p.pos += idx + len(endstreamPat)
			}
		} else {
			idx := bytes.Index(data[p.pos:], endstreamPat)
			if idx < 0 {
				return nil, fmt.Errorf("object %d %d: endstream missing", num, gen)
			}
			obj.StreamData = data[p.pos : p.pos+idx]
			obj.StreamEnd = int64(p.pos + idx)
			p.pos += idx + len(endstreamPat)
			obj.Notes = append(obj.Notes, "no direct /Length; endstream located by scan")
		}
		obj.Value = &Stream{Dict: obj.StreamDict, Data: obj.StreamData}
	}
	// Best-effort endobj.
	rest := data[p.pos:]
	if idx := bytes.Index(rest, []byte("endobj")); idx >= 0 && idx < 64 {
		p.pos += idx + len("endobj")
	}
	obj.End = int64(p.pos)
	return obj, nil
}
