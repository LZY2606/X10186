package forensic

import (
	"bytes"
	"fmt"
	"strconv"
)

// Node is a parsed PDF value with its exact byte range.
type Node struct {
	Type   string      `json:"type"`
	At     ByteRange   `json:"at"`
	Bool   bool        `json:"bool,omitempty"`
	Int    int64       `json:"int,omitempty"`
	Real   float64     `json:"real,omitempty"`
	Str    []byte      `json:"str,omitempty"`
	Name   string      `json:"name,omitempty"`
	Items  []*Node     `json:"items,omitempty"`
	Fields []DictEntry `json:"fields,omitempty"`
	Ref    *Key        `json:"ref,omitempty"`
}

// DictEntry preserves dictionary order.
type DictEntry struct {
	Key   string `json:"key"`
	Value *Node  `json:"value"`
}

// Dict finds the first value of a name key, preserving ordered fields.
func (n *Node) Dict(name string) *Node {
	if n == nil || n.Type != "dict" {
		return nil
	}
	for i := range n.Fields {
		if n.Fields[i].Key == name {
			return n.Fields[i].Value
		}
	}
	return nil
}

const maxParseDepth = 200

func isWhitespace(b byte) bool {
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
	return false
}

// skipWS moves past whitespace and comments.
func skipWS(data []byte, pos int) int {
	for pos < len(data) {
		b := data[pos]
		if isWhitespace(b) {
			pos++
			continue
		}
		if b == '%' {
			for pos < len(data) && data[pos] != '\n' && data[pos] != '\r' {
				pos++
			}
			continue
		}
		break
	}
	return pos
}

// matchKeyword checks for keyword followed by a non-identifier byte.
func matchKeyword(data []byte, pos int, kw string) (bool, int) {
	if pos+len(kw) > len(data) || !bytes.Equal(data[pos:pos+len(kw)], []byte(kw)) {
		return false, pos
	}
	end := pos + len(kw)
	if end < len(data) {
		b := data[end]
		if !(isWhitespace(b) || isDelim(b)) {
			return false, pos
		}
	}
	return true, end
}

func parseName(data []byte, pos int) (string, int) {
	pos++ // '/'
	var buf []byte
	for pos < len(data) {
		b := data[pos]
		if isWhitespace(b) || isDelim(b) {
			break
		}
		if b == '#' && pos+2 < len(data) {
			if h, ok := hexVal(data[pos+1]); ok {
				if l, ok2 := hexVal(data[pos+2]); ok2 {
					buf = append(buf, h<<4|l)
					pos += 3
					continue
				}
			}
		}
		buf = append(buf, b)
		pos++
	}
	return string(buf), pos
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

func parseLiteralString(data []byte, pos int) ([]byte, int, error) {
	depth := 1
	pos++ // '('
	var buf []byte
	for pos < len(data) {
		b := data[pos]
		if b == '\\' {
			if pos+1 >= len(data) {
				return nil, pos, fmt.Errorf("unterminated escape")
			}
			e := data[pos+1]
			switch e {
			case 'n':
				buf = append(buf, '\n')
			case 'r':
				buf = append(buf, '\r')
			case 't':
				buf = append(buf, '\t')
			case 'b':
				buf = append(buf, '\b')
			case 'f':
				buf = append(buf, '\f')
			case '(':
				buf = append(buf, '(')
			case ')':
				buf = append(buf, ')')
			case '\\':
				buf = append(buf, '\\')
			case '\r':
				pos++
				if pos+1 < len(data) && data[pos+1] == '\n' {
					pos++
				}
				continue
			case '\n':
				pos++
				continue
			case '0', '1', '2', '3', '4', '5', '6', '7':
				var v int
				k := 0
				for k < 3 && pos+1+k < len(data) && data[pos+1+k] >= '0' && data[pos+1+k] <= '7' {
					v = v*8 + int(data[pos+1+k]-'0')
					k++
				}
				buf = append(buf, byte(v))
				pos += k
				continue
			default:
				buf = append(buf, e)
			}
			pos += 2
			continue
		}
		if b == '(' {
			depth++
			buf = append(buf, b)
			pos++
			continue
		}
		if b == ')' {
			depth--
			if depth == 0 {
				return buf, pos + 1, nil
			}
			buf = append(buf, b)
			pos++
			continue
		}
		if b == '\r' && pos+1 < len(data) && data[pos+1] == '\n' {
			buf = append(buf, '\n')
			pos += 2
			continue
		}
		buf = append(buf, b)
		pos++
	}
	return nil, pos, fmt.Errorf("unterminated literal string")
}

func parseHexString(data []byte, pos int) ([]byte, int) {
	pos++ // '<'
	var nibbles []byte
	for pos < len(data) && data[pos] != '>' {
		b := data[pos]
		if !isWhitespace(b) {
			nibbles = append(nibbles, b)
		}
		pos++
	}
	if pos < len(data) {
		pos++ // '>'
	}
	if len(nibbles)%2 == 1 {
		nibbles = append(nibbles, '0')
	}
	out := make([]byte, 0, len(nibbles)/2)
	for i := 0; i+1 < len(nibbles); i += 2 {
		h, ok1 := hexVal(nibbles[i])
		l, ok2 := hexVal(nibbles[i+1])
		if ok1 && ok2 {
			out = append(out, h<<4|l)
		}
	}
	return out, pos
}

// parseValue parses one PDF value.
func parseValue(data []byte, pos0 int, depth int) (*Node, int, error) {
	if depth > maxParseDepth {
		return nil, pos0, fmt.Errorf("nesting depth exceeded")
	}
	start := pos0
	pos := skipWS(data, pos0)
	if pos >= len(data) {
		return nil, pos, fmt.Errorf("unexpected end")
	}
	b := data[pos]

	switch {
	case b == '/':
		name, next := parseName(data, pos)
		return &Node{Type: "name", Name: name, At: ByteRange{int64(start), int64(next)}}, next, nil
	case b == '(':
		s, next, err := parseLiteralString(data, pos)
		if err != nil {
			return nil, next, err
		}
		return &Node{Type: "string", Str: s, At: ByteRange{int64(start), int64(next)}}, next, nil
	case b == '<' && pos+1 < len(data) && data[pos+1] == '<':
		return parseDict(data, pos, depth)
	case b == '<':
		s, next := parseHexString(data, pos)
		return &Node{Type: "string", Str: s, At: ByteRange{int64(start), int64(next)}}, next, nil
	case b == '[':
		return parseArray(data, pos, depth)
	}

	if b == '+' || b == '-' || b == '.' || (b >= '0' && b <= '9') {
		n, next, isInt, ok := scanNumber(data, pos)
		if ok {
			// Indirect reference?
			afterNum := skipWS(data, next)
			m, afterGen, isInt2, ok2 := scanNumber(data, afterNum)
			if ok2 && isInt2 {
				afterGenWS := skipWS(data, afterGen)
				if ok3, afterR := matchKeyword(data, afterGenWS, "R"); ok3 {
					_ = m
					return &Node{Type: "ref", At: ByteRange{int64(start), int64(afterR)}, Ref: &Key{Num: int(n), Gen: int(m)}}, afterR, nil
				}
			}
			if isInt {
				return &Node{Type: "int", Int: n, At: ByteRange{int64(start), int64(next)}}, next, nil
			}
			f, _ := strconv.ParseFloat(string(data[pos:next]), 64)
			return &Node{Type: "real", Real: f, At: ByteRange{int64(start), int64(next)}}, next, nil
		}
	}

	for _, kw := range []string{"true", "false", "null"} {
		if ok, next := matchKeyword(data, pos, kw); ok {
			return &Node{Type: kw, Bool: kw == "true", At: ByteRange{int64(start), int64(next)}}, next, nil
		}
	}

	return nil, pos, fmt.Errorf("unexpected byte %q at %d", string(b), pos)
}

func scanNumber(data []byte, pos int) (val int64, next int, isInt bool, ok bool) {
	start := pos
	if pos < len(data) && (data[pos] == '+' || data[pos] == '-') {
		pos++
	}
	digits := 0
	for pos < len(data) && data[pos] >= '0' && data[pos] <= '9' {
		pos++
		digits++
	}
	frac := false
	if pos < len(data) && data[pos] == '.' {
		frac = true
		pos++
		for pos < len(data) && data[pos] >= '0' && data[pos] <= '9' {
			pos++
		}
	}
	if pos < len(data) && (data[pos] == 'e' || data[pos] == 'E') {
		t := pos + 1
		if t < len(data) && (data[t] == '+' || data[t] == '-') {
			t++
		}
		if t < len(data) && data[t] >= '0' && data[t] <= '9' {
			pos = t + 1
			for pos < len(data) && data[pos] >= '0' && data[pos] <= '9' {
				pos++
			}
			frac = true
		}
	}
	tok := data[start:pos]
	if len(tok) == 0 || string(tok) == "+" || string(tok) == "-" || string(tok) == "." {
		return 0, pos, false, false
	}
	if !frac && digits > 0 {
		v, err := strconv.ParseInt(string(tok), 10, 64)
		if err == nil {
			return v, pos, true, true
		}
	}
	f, err := strconv.ParseFloat(string(tok), 64)
	if err != nil {
		return 0, pos, false, false
	}
	return int64(f), pos, false, true
}

func parseArray(data []byte, pos0 int, depth int) (*Node, int, error) {
	start := pos0
	pos := skipWS(data, pos0) + 1 // '['
	arr := &Node{Type: "array", At: ByteRange{Start: int64(start)}}
	for {
		pos = skipWS(data, pos)
		if pos >= len(data) {
			return nil, pos, fmt.Errorf("unterminated array")
		}
		if data[pos] == ']' {
			pos++
			arr.At.End = int64(pos)
			return arr, pos, nil
		}
		v, next, err := parseValue(data, pos, depth+1)
		if err != nil {
			return nil, next, err
		}
		arr.Items = append(arr.Items, v)
		pos = next
	}
}

func parseDict(data []byte, pos0 int, depth int) (*Node, int, error) {
	start := pos0
	pos := skipWS(data, pos0) + 2 // '<<'
	d := &Node{Type: "dict", At: ByteRange{Start: int64(start)}}
	for {
		pos = skipWS(data, pos)
		if pos+1 < len(data) && data[pos] == '>' && data[pos+1] == '>' {
			pos += 2
			d.At.End = int64(pos)
			return d, pos, nil
		}
		if pos >= len(data) {
			return nil, pos, fmt.Errorf("unterminated dict")
		}
		keyStart := skipWS(data, pos)
		if keyStart >= len(data) || data[keyStart] != '/' {
			// allow stray '>' boundaries etc.
			if keyStart+1 < len(data) && data[keyStart] == '>' && data[keyStart+1] == '>' {
				pos = keyStart + 2
				d.At.End = int64(pos)
				return d, pos, nil
			}
			return nil, keyStart, fmt.Errorf("dict key expected at %d", keyStart)
		}
		name, next := parseName(data, keyStart)
		v, after, err := parseValue(data, next, depth+1)
		if err != nil {
			return nil, after, err
		}
		d.Fields = append(d.Fields, DictEntry{Key: name, Value: v})
		pos = after
	}
}

// collectRefs walks parsed nodes and yields all indirect references.
func collectRefs(n *Node, out *[]RefEdge, from Key, base ByteRange) {
	if n == nil {
		return
	}
	switch n.Type {
	case "ref":
		if n.Ref != nil {
			*out = append(*out, RefEdge{From: from, To: *n.Ref, Where: n.At, Kind: "value"})
		}
	case "dict":
		for i := range n.Fields {
			collectRefs(n.Fields[i].Value, out, from, base)
		}
	case "array":
		for _, c := range n.Items {
			collectRefs(c, out, from, base)
		}
	}
}
