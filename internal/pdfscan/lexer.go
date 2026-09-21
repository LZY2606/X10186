package pdfscan

import (
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// Dict is a PDF dictionary. Keys are names without the leading slash.
type Dict map[string]any

// Name is a PDF name object (without the leading slash).
type Name string

// PString is a PDF string object (literal or hexadecimal).
type PString string

// Ref is an indirect reference "N G R".
type Ref struct {
	Obj int
	Gen int
}

// ErrSyntax marks malformed PDF syntax.
var ErrSyntax = errors.New("pdf syntax error")

type lparser struct {
	data []byte
	pos  int
}

func isWhite(b byte) bool {
	switch b {
	case 0, 9, 10, 12, 13, 32:
		return true
	}
	return false
}

func isDelim(b byte) bool {
	switch b {
	case '(', ')', '<', '>', '[', ']', '{', '}', '/', '%':
		return true
	}
	return isWhite(b)
}

func (p *lparser) skipWS() {
	for p.pos < len(p.data) {
		b := p.data[p.pos]
		if isWhite(b) {
			p.pos++
			continue
		}
		if b == '%' {
			for p.pos < len(p.data) && p.data[p.pos] != '\n' && p.data[p.pos] != '\r' {
				p.pos++
			}
			continue
		}
		break
	}
}

func (p *lparser) keywordAhead(kw string) bool {
	if p.pos+len(kw) > len(p.data) {
		return false
	}
	if string(p.data[p.pos:p.pos+len(kw)]) != kw {
		return false
	}
	if p.pos+len(kw) < len(p.data) && !isDelim(p.data[p.pos+len(kw)]) {
		return false
	}
	return true
}

func (p *lparser) readToken() string {
	start := p.pos
	for p.pos < len(p.data) && !isDelim(p.data[p.pos]) && !isWhite(p.data[p.pos]) {
		p.pos++
	}
	return string(p.data[start:p.pos])
}

func (p *lparser) parseValue() (any, error) {
	p.skipWS()
	if p.pos >= len(p.data) {
		return nil, fmt.Errorf("%w: unexpected end of data", ErrSyntax)
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
		return p.parseName()
	case b == '+' || b == '-' || b == '.' || (b >= '0' && b <= '9'):
		return p.parseNumberOrRef()
	case b == 't':
		if p.keywordAhead("true") {
			p.pos += 4
			return true, nil
		}
	case b == 'f':
		if p.keywordAhead("false") {
			p.pos += 5
			return false, nil
		}
	case b == 'n':
		if p.keywordAhead("null") {
			p.pos += 4
			return nil, nil
		}
	}
	return nil, fmt.Errorf("%w: unexpected byte %q at offset %d", ErrSyntax, b, p.pos)
}

func (p *lparser) parseNumberOrRef() (any, error) {
	tok := p.readToken()
	n1, err := strconv.ParseInt(tok, 10, 64)
	if err != nil {
		f, ferr := strconv.ParseFloat(tok, 64)
		if ferr != nil {
			return nil, fmt.Errorf("%w: bad number %q", ErrSyntax, tok)
		}
		return f, nil
	}
	save := p.pos
	p.skipWS()
	tok2 := p.readToken()
	n2, err2 := strconv.ParseInt(tok2, 10, 64)
	if err2 != nil || n2 < 0 || strings.ContainsAny(tok2, "+-.") {
		p.pos = save
		return n1, nil
	}
	p.skipWS()
	if p.pos < len(p.data) && p.data[p.pos] == 'R' &&
		(p.pos+1 >= len(p.data) || isDelim(p.data[p.pos+1])) {
		p.pos++
		return Ref{Obj: int(n1), Gen: int(n2)}, nil
	}
	p.pos = save
	return n1, nil
}

func (p *lparser) parseName() (any, error) {
	p.pos++ // consume '/'
	start := p.pos
	for p.pos < len(p.data) && !isDelim(p.data[p.pos]) && !isWhite(p.data[p.pos]) {
		p.pos++
	}
	return Name(string(p.data[start:p.pos])), nil
}

func (p *lparser) parseLiteralString() (any, error) {
	p.pos++ // consume '('
	var sb strings.Builder
	depth := 1
	for p.pos < len(p.data) {
		b := p.data[p.pos]
		p.pos++
		switch b {
		case '\\':
			if p.pos >= len(p.data) {
				return nil, fmt.Errorf("%w: unterminated escape", ErrSyntax)
			}
			e := p.data[p.pos]
			p.pos++
			switch e {
			case 'n':
				sb.WriteByte('\n')
			case 'r':
				sb.WriteByte('\r')
			case 't':
				sb.WriteByte('\t')
			case 'b':
				sb.WriteByte('\b')
			case 'f':
				sb.WriteByte('\f')
			case '(', ')', '\\':
				sb.WriteByte(e)
			case '\n':
			case '\r':
				if p.pos < len(p.data) && p.data[p.pos] == '\n' {
					p.pos++
				}
			default:
				if e >= '0' && e <= '7' {
					v := int(e - '0')
					for i := 0; i < 2 && p.pos < len(p.data) && p.data[p.pos] >= '0' && p.data[p.pos] <= '7'; i++ {
						v = v*8 + int(p.data[p.pos]-'0')
						p.pos++
					}
					sb.WriteByte(byte(v))
				} else {
					sb.WriteByte(e)
				}
			}
		case '(':
			depth++
			sb.WriteByte(b)
		case ')':
			depth--
			if depth == 0 {
				return PString(sb.String()), nil
			}
			sb.WriteByte(b)
		default:
			sb.WriteByte(b)
		}
	}
	return nil, fmt.Errorf("%w: unterminated string", ErrSyntax)
}

func hexVal(b byte) (int, bool) {
	switch {
	case b >= '0' && b <= '9':
		return int(b - '0'), true
	case b >= 'a' && b <= 'f':
		return int(b-'a') + 10, true
	case b >= 'A' && b <= 'F':
		return int(b-'A') + 10, true
	}
	return 0, false
}

func (p *lparser) parseHexString() (any, error) {
	p.pos++ // consume '<'
	var out []byte
	hi := -1
	for p.pos < len(p.data) {
		b := p.data[p.pos]
		p.pos++
		if b == '>' {
			if hi >= 0 {
				out = append(out, byte(hi<<4))
			}
			return PString(string(out)), nil
		}
		if isWhite(b) {
			continue
		}
		v, ok := hexVal(b)
		if !ok {
			return nil, fmt.Errorf("%w: bad hex digit %q", ErrSyntax, b)
		}
		if hi < 0 {
			hi = v
		} else {
			out = append(out, byte(hi<<4|v))
			hi = -1
		}
	}
	return nil, fmt.Errorf("%w: unterminated hex string", ErrSyntax)
}

func (p *lparser) parseArray() (any, error) {
	p.pos++ // consume '['
	var out []any
	for {
		p.skipWS()
		if p.pos >= len(p.data) {
			return nil, fmt.Errorf("%w: unterminated array", ErrSyntax)
		}
		if p.data[p.pos] == ']' {
			p.pos++
			return out, nil
		}
		v, err := p.parseValue()
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
}

func (p *lparser) parseDict() (any, error) {
	p.pos += 2 // consume '<<'
	d := Dict{}
	for {
		p.skipWS()
		if p.pos >= len(p.data) {
			return nil, fmt.Errorf("%w: unterminated dict", ErrSyntax)
		}
		if p.data[p.pos] == '>' {
			if p.pos+1 < len(p.data) && p.data[p.pos+1] == '>' {
				p.pos += 2
				return d, nil
			}
			return nil, fmt.Errorf("%w: lone '>' in dict", ErrSyntax)
		}
		if p.data[p.pos] != '/' {
			return nil, fmt.Errorf("%w: dict key must be a name at %d", ErrSyntax, p.pos)
		}
		kv, err := p.parseName()
		if err != nil {
			return nil, err
		}
		v, err := p.parseValue()
		if err != nil {
			return nil, err
		}
		d[string(kv.(Name))] = v
	}
}

// Render produces a compact, bounded textual form of a parsed PDF value.
func Render(v any) string {
	var sb strings.Builder
	renderValue(&sb, v, 0)
	return sb.String()
}

const renderMaxDepth = 8
const renderMaxString = 96

func renderValue(sb *strings.Builder, v any, depth int) {
	if depth > renderMaxDepth {
		sb.WriteString("...")
		return
	}
	switch t := v.(type) {
	case nil:
		sb.WriteString("null")
	case bool:
		if t {
			sb.WriteString("true")
		} else {
			sb.WriteString("false")
		}
	case int64:
		sb.WriteString(strconv.FormatInt(t, 10))
	case float64:
		sb.WriteString(strconv.FormatFloat(t, 'g', -1, 64))
	case Name:
		sb.WriteByte('/')
		sb.WriteString(string(t))
	case PString:
		s := string(t)
		if len(s) > renderMaxString {
			s = s[:renderMaxString] + "..."
		}
		sb.WriteByte('(')
		sb.WriteString(strings.NewReplacer("\\", "\\\\", "(", "\\(", ")", "\\)", "\n", "\\n", "\r", "\\r").Replace(s))
		sb.WriteByte(')')
	case Ref:
		fmt.Fprintf(sb, "%d %d R", t.Obj, t.Gen)
	case []any:
		sb.WriteByte('[')
		for i, e := range t {
			if i > 0 {
				sb.WriteByte(' ')
			}
			renderValue(sb, e, depth+1)
		}
		sb.WriteByte(']')
	case Dict:
		sb.WriteString("<<")
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			sb.WriteString(" /")
			sb.WriteString(k)
			sb.WriteByte(' ')
			renderValue(sb, t[k], depth+1)
		}
		sb.WriteString(" >>")
	default:
		sb.WriteString(fmt.Sprintf("%v", t))
	}
}

// WalkRefs calls fn for every indirect reference found in v (recursively).
func WalkRefs(v any, fn func(Ref)) {
	switch t := v.(type) {
	case Ref:
		fn(t)
	case []any:
		for _, e := range t {
			WalkRefs(e, fn)
		}
	case Dict:
		for _, e := range t {
			WalkRefs(e, fn)
		}
	}
}

// ToJSON converts a parsed PDF value into JSON-marshalable data.
func ToJSON(v any) any {
	switch t := v.(type) {
	case nil, bool, int64, float64:
		return t
	case Name:
		return "/" + string(t)
	case PString:
		return string(t)
	case Ref:
		return map[string]any{"R": []int{t.Obj, t.Gen}}
	case []any:
		out := make([]any, len(t))
		for i, e := range t {
			out[i] = ToJSON(e)
		}
		return out
	case Dict:
		out := map[string]any{}
		for k, e := range t {
			out[k] = ToJSON(e)
		}
		return out
	}
	return fmt.Sprintf("%v", t)
}
