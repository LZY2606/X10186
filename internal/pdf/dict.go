package pdf

import (
	"strconv"
	"strings"
)

// renderValue produces a compact, non-lossy textual rendering of one PDF value.
// References are rendered as "N G R"; structure is preserved.
func renderValue(data []byte, pos int) (string, int) {
	pos = skipWS(data, pos)
	if pos >= len(data) {
		return "", pos
	}
	start := pos
	switch data[pos] {
	case '(':
		s, end, _ := readLiteralString(data, pos)
		return s, end
	case '[':
		pos++
		var parts []string
		for {
			pos = skipWS(data, pos)
			if pos >= len(data) || data[pos] == ']' {
				if pos < len(data) {
					pos++
				}
				return "[" + strings.Join(parts, " ") + "]", pos
			}
			if n, g, _, isRef, np := readRefAt(data, pos); isRef {
				parts = append(parts, strconv.Itoa(n)+" "+strconv.Itoa(g)+" R")
				pos = np
				continue
			}
			v, np := renderValue(data, pos)
			parts = append(parts, v)
			pos = np
		}
	case '<':
		if pos+1 < len(data) && data[pos+1] == '<' {
			end := skipDict(data, pos+2)
			return compactSpace(data[start:end]), end
		}
		s, end, _ := readHexString(data, pos)
		return s, end
	case '/':
		pos++
		for pos < len(data) && isRegular(data[pos]) {
			pos++
		}
		return string(data[start:pos]), pos
	}
	tok, end := readToken(data, pos)
	return tok, end
}

// readRefAt returns the reference "N G R" starting at pos and the position after it.
func readRefAt(data []byte, pos int) (num, gen int, tok string, ok bool, next int) {
	p := skipWS(data, pos)
	t1, p2 := readToken(data, p)
	n, nOK := parseInt([]byte(t1))
	if !nOK {
		return 0, 0, "", false, pos
	}
	p3 := skipWS(data, p2)
	t2, p4 := readToken(data, p3)
	g, gOK := parseInt([]byte(t2))
	if !gOK {
		return 0, 0, "", false, pos
	}
	p5 := skipWS(data, p4)
	t3, p6 := readToken(data, p5)
	if t3 != "R" {
		return 0, 0, "", false, pos
	}
	return n, g, "R", true, p6
}

func compactSpace(b []byte) string {
	return strings.Join(strings.Fields(string(b)), " ")
}

// parseDictAt parses a "<<...>>" dictionary beginning at pos into key->rendered value.
// Returns the map and the position just after ">>".
func parseDictAt(data []byte, pos int) (map[string]string, int) {
	d := map[string]string{}
	pos = skipWS(data, pos)
	if pos >= len(data) || data[pos] != '<' || pos+1 >= len(data) || data[pos+1] != '<' {
		return d, pos
	}
	pos += 2
	for {
		pos = skipWS(data, pos)
		if pos >= len(data) {
			return d, pos
		}
		if data[pos] == '>' && pos+1 < len(data) && data[pos+1] == '>' {
			return d, pos + 2
		}
		if data[pos] != '/' {
			pos++ // tolerate a malformed byte instead of looping forever
			continue
		}
		ks := pos + 1
		ke := ks
		for ke < len(data) && isRegular(data[ke]) {
			ke++
		}
		key := string(data[ks:ke])
		pos = skipWS(data, ke)
		if n, g, _, isRef, np := readRefAt(data, pos); isRef {
			d[key] = strconv.Itoa(n) + " " + strconv.Itoa(g) + " R"
			pos = np
			continue
		}
		val, np := renderValue(data, pos)
		d[key] = val
		pos = np
	}
}

// dictRef interprets a dictionary value rendered as "N G R", returning N.
func dictRef(v string) (int, bool) {
	fields := strings.Fields(v)
	if len(fields) != 3 || fields[2] != "R" {
		return 0, false
	}
	n, err := strconv.Atoi(fields[0])
	if err != nil {
		return 0, false
	}
	return n, true
}

// dictInt parses an integer dictionary value.
func dictInt(v string) (int, bool) {
	n, ok := parseInt([]byte(strings.TrimSpace(v)))
	return n, ok
}

// dictNames splits a name array like "[ /Fl /Fl ]" or a single "/Fl".
func dictNames(v string) []string {
	var out []string
	for _, f := range strings.Fields(v) {
		if strings.HasPrefix(f, "/") {
			out = append(out, f[1:])
		}
	}
	return out
}
