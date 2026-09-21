package pdf

import (
	"bytes"
	"strconv"
)

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

func isRegular(b byte) bool {
	return b > 32 && !isDelim(b)
}

// skipWS advances past whitespace and comments.
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

// readToken reads one non-delimiter token (keyword/number/name-less).
func readToken(data []byte, pos int) (string, int) {
	start := pos
	for pos < len(data) && isRegular(data[pos]) {
		pos++
	}
	return string(data[start:pos]), pos
}

func atBytes(data []byte, pos int, want []byte) bool {
	return pos+len(want) <= len(data) && bytes.Equal(data[pos:pos+len(want)], want)
}

// indexFrom finds token bytes bounded by whitespace/delimiter context, raw search.
func indexFrom(data []byte, from int, want []byte) int {
	if from < 0 {
		from = 0
	}
	idx := bytes.Index(data[from:], want)
	if idx < 0 {
		return -1
	}
	return from + idx
}

func parseInt(b []byte) (int, bool) {
	s := string(bytes.TrimSpace(b))
	if s == "" {
		return 0, false
	}
	neg := false
	i := 0
	if s[0] == '+' || s[0] == '-' {
		neg = s[0] == '-'
		i = 1
		if i == len(s) {
			return 0, false
		}
	}
	n := 0
	for ; i < len(s); i++ {
		c := s[i]
		if c < '0' || c > '9' {
			return 0, false
		}
		n = n*10 + int(c-'0')
	}
	if neg {
		n = -n
	}
	return n, true
}

func itoa(n int) string { return strconv.Itoa(n) }

// LiteralString reads a parenthesized string honoring nested () and backslash.
func readLiteralString(data []byte, pos int) (string, int, bool) {
	if pos >= len(data) || data[pos] != '(' {
		return "", pos, false
	}
	depth := 1
	start := pos
	pos++
	for pos < len(data) {
		c := data[pos]
		switch c {
		case '\\':
			pos += 2
			if pos > len(data) {
				return string(data[start:len(data)]), len(data), false
			}
			continue
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				return string(data[start : pos+1]), pos + 1, true
			}
		}
		pos++
	}
	return string(data[start:]), len(data), false
}

// readHexString reads a <...> hex string (must not be confused with << dict).
func readHexString(data []byte, pos int) (string, int, bool) {
	start := pos
	pos++
	for pos < len(data) {
		if data[pos] == '>' {
			return string(data[start : pos+1]), pos + 1, true
		}
		pos++
	}
	return string(data[start:]), len(data), false
}

// skipValue skips one PDF value starting at pos, returning the end position.
// Strings/dicts/arrays are traversed structurally (no decoding).
func skipValue(data []byte, pos int) int {
	pos = skipWS(data, pos)
	if pos >= len(data) {
		return pos
	}
	switch data[pos] {
	case '(':
		_, end, _ := readLiteralString(data, pos)
		return end
	case '[':
		pos++
		for {
			pos = skipWS(data, pos)
			if pos >= len(data) {
				return pos
			}
			if data[pos] == ']' {
				return pos + 1
			}
			pos = skipValue(data, pos)
		}
	case '<':
		if pos+1 < len(data) && data[pos+1] == '<' {
			return skipDict(data, pos+2)
		}
		_, end, _ := readHexString(data, pos)
		return end
	}
	// name, number, keyword, bool, null, reference numbers (skipped as tokens)
	_, end := readToken(data, pos)
	if end == pos { // unknown byte
		return pos + 1
	}
	return end
}

// skipDict assumes pos points just after "<<".
func skipDict(data []byte, pos int) int {
	for {
		pos = skipWS(data, pos)
		if pos >= len(data) {
			return pos
		}
		if data[pos] == '>' && pos+1 < len(data) && data[pos+1] == '>' {
			return pos + 2
		}
		if data[pos] == '/' {
			pos++
			for pos < len(data) && isRegular(data[pos]) {
				pos++
			}
			continue
		}
		pos = skipValue(data, pos)
	}
}
