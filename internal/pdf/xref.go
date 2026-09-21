package pdf

import (
	"bytes"
	"fmt"
)

// findStartXRef reads the final startxref offset from the tail of the file.
// It scans backwards so appended data after %%EOF does not hide earlier markers:
// the *last* startxref in the file is the declared current entry point.
func findStartXRef(data []byte) (markerOff, xrefOff int, ok bool) {
	window := 4096
	from := len(data) - window
	if from < 0 {
		from = 0
	}
	idx := bytes.LastIndex(data[from:], []byte("startxref"))
	if idx < 0 {
		return -1, 0, false
	}
	abs := from + idx
	p := skipWS(data, abs+len("startxref"))
	tok, _ := readToken(data, p)
	off, parsable := parseInt([]byte(tok))
	if !parsable {
		return abs, 0, false
	}
	return abs, off, true
}

// parseXRefAt parses either an xref table or an xref stream object at off.
// resolveLen resolves an indirect /Length ("N G R") to an integer when needed.
func parseXRefAt(data []byte, off int, resolveLen func(num, gen int) int, limits Limits) (*Revision, error) {
	if off < 0 || off >= len(data) {
		return nil, fmt.Errorf("xref offset %d points outside file (size %d)", off, len(data))
	}
	p := skipWS(data, off)
	if atBytes(data, p, []byte("xref")) {
		after := p + len("xref")
		if after < len(data) && (isWhitespace(data[after]) || data[after] >= '0' && data[after] <= '9') {
			return parseXRefTable(data, off)
		}
	}
	return parseXRefStreamAt(data, off, resolveLen, limits)
}

func parseXRefTable(data []byte, off int) (*Revision, error) {
	rev := &Revision{XRefOffset: off, XRefKind: "table", Trailer: map[string]string{}}
	p := skipWS(data, off)
	p += len("xref")
	rev.XRefStart = off
	curFirst, curCount := 0, 0
	for {
		p = skipWS(data, p)
		if p >= len(data) {
			return nil, fmt.Errorf("xref table truncated before trailer")
		}
		if atBytes(data, p, []byte("trailer")) {
			p += len("trailer")
			break
		}
		lineStart := p
		t1, p2 := readToken(data, p)
		f, ok1 := parseInt([]byte(t1))
		p2 = skipWS(data, p2)
		t2, p3 := readToken(data, p2)
		c, ok2 := parseInt([]byte(t2))
		if !ok1 || !ok2 || c <= 0 {
			return nil, fmt.Errorf("malformed xref subsection header at %d", lineStart)
		}
		curFirst, curCount = f, c
		p = skipWS(data, p3)
		for i := 0; i < curCount; i++ {
			entryStart := p
			if p+20 > len(data) {
				return nil, fmt.Errorf("xref entry %d truncated", i)
			}
			e, err := parseTableEntry(data[p:p+20], curFirst+i)
			if err != nil {
				return nil, fmt.Errorf("xref entry at %d: %w", entryStart, err)
			}
			rev.Entries = append(rev.Entries, e)
			p += 20
		}
	}
	td := skipWS(data, p)
	dict, de := parseDictAt(data, td)
	rev.Trailer = dict
	rev.XRefEnd = de
	if pv, ok := dict["Prev"]; ok {
		if n, ok := dictInt(pv); ok {
			rev.HasPrev = true
			rev.Prev = n
		}
	}
	return rev, nil
}

func parseTableEntry(line []byte, num int) (XRefEntry, error) {
	sp1 := bytes.IndexByte(line, ' ')
	if sp1 < 0 {
		return XRefEntry{}, fmt.Errorf("no field separator")
	}
	off, ok1 := parseInt(line[:sp1])
	rest := line[sp1+1:]
	sp2 := bytes.IndexByte(rest, ' ')
	if sp2 < 0 {
		return XRefEntry{}, fmt.Errorf("no type separator")
	}
	gen, ok2 := parseInt(rest[:sp2])
	typ := rest[sp2+1]
	if !ok1 || !ok2 {
		return XRefEntry{}, fmt.Errorf("non-numeric fields")
	}
	e := XRefEntry{Num: num, Gen: gen}
	switch typ {
	case 'n':
		e.Kind = KindUncompressed
		e.Offset = off
	case 'f':
		e.Kind = KindFree
		e.FreeNext = off
	default:
		return XRefEntry{}, fmt.Errorf("unknown type %q", typ)
	}
	return e, nil
}

func parseXRefStreamAt(data []byte, off int, resolveLen func(num, gen int) int, limits Limits) (*Revision, error) {
	rev := &Revision{XRefOffset: off, XRefKind: "stream", Trailer: map[string]string{}}
	p := skipWS(data, off)
	tok, p2 := readToken(data, p)
	objNum, ok1 := parseInt([]byte(tok))
	p3 := skipWS(data, p2)
	tok2, p4 := readToken(data, p3)
	objGen, ok2 := parseInt([]byte(tok2))
	p5 := skipWS(data, p4)
	kw, _ := readToken(data, p5)
	if !ok1 || !ok2 || kw != "obj" {
		return nil, fmt.Errorf("xref offset %d does not start an object (got %q %q %q)", off, tok, tok2, kw)
	}
	_ = objNum
	_ = objGen
	dictPos := skipWS(data, p5+len("obj"))
	if dictPos >= len(data) || data[dictPos] != '<' {
		return nil, fmt.Errorf("xref object at %d has no dictionary", off)
	}
	dict, dictEnd := parseDictAt(data, dictPos)
	rev.Trailer = dict
	rev.XRefStart = off
	if t, ok := dict["Type"]; ok && t != "/XRef" {
		return nil, fmt.Errorf("object at %d is /Type %s, not /XRef", off, t)
	}
	wFields := dict["W"]
	if wFields == "" {
		return nil, fmt.Errorf("xref stream missing /W")
	}
	w := parseNumArray(wFields)
	if len(w) != 3 {
		return nil, fmt.Errorf("xref stream /W must have 3 fields, got %v", w)
	}
	if pv, ok := dict["Prev"]; ok {
		if n, ok := dictInt(pv); ok {
			rev.HasPrev = true
			rev.Prev = n
		}
	}
	// index ranges
	var ranges [][2]int
	if iv, ok := dict["Index"]; ok {
		nums := parseNumArray(iv)
		if len(nums)%2 != 0 {
			return nil, fmt.Errorf("xref /Index has odd count")
		}
		for i := 0; i+1 < len(nums); i += 2 {
			ranges = append(ranges, [2]int{nums[i], nums[i+1]})
		}
	} else {
		size, _ := dictInt(dict["Size"])
		ranges = [][2]int{{0, size}}
	}

	// locate the stream bytes
	sp := skipWS(data, dictEnd)
	if !atBytes(data, sp, []byte("stream")) {
		return nil, fmt.Errorf("xref stream missing stream keyword")
	}
	ss := sp + len("stream")
	if ss+1 < len(data) && data[ss] == '\r' && data[ss+1] == '\n' {
		ss += 2
	} else if ss < len(data) && (data[ss] == '\n' || data[ss] == '\r') {
		ss++
	}
	length, lenNote := effectiveLength(data, dict, ss, resolveLen)
	if lenNote != "" {
		return nil, fmt.Errorf("xref stream %s", lenNote)
	}
	streamEnd, bad, ok := verifyStreamBoundaryAt(data, length, ss)
	if !ok {
		return nil, fmt.Errorf("xref stream boundary not verified: %s", bad)
	}
	encoded := data[ss:streamEnd]
	filters := dictNames(dict["Filter"])
	ok, reason := supportedExpansion(filters)
	if !ok {
		return nil, fmt.Errorf("xref stream not expanded: %s", reason)
	}
	raw, err := expandFlate(encoded, limits.MaxExpandedBytes)
	if err != nil {
		return nil, fmt.Errorf("xref stream expansion: %w", err)
	}

	// decode entries
	total := 0
	for _, r := range ranges {
		total += r[1]
	}
	recLen := w[0] + w[1] + w[2]
	if recLen == 0 {
		return nil, fmt.Errorf("xref stream /W sums to zero")
	}
	if len(raw) < total*recLen {
		return nil, fmt.Errorf("xref stream decoded %d bytes but %d entries need %d", len(raw), total, total*recLen)
	}
	pos := 0
	for _, r := range ranges {
		first, count := r[0], r[1]
		for i := 0; i < count; i++ {
			t1 := readBigInt(raw[pos : pos+w[0]])
			t2 := readBigInt(raw[pos+w[0] : pos+w[0]+w[1]])
			t3 := readBigInt(raw[pos+w[0]+w[1] : pos+recLen])
			_ = t2
			pos += recLen
			num := first + i
			switch t1 {
			case 0:
				rev.Entries = append(rev.Entries, XRefEntry{Num: num, Gen: t3, Kind: KindFree, FreeNext: t2})
			case 1:
				rev.Entries = append(rev.Entries, XRefEntry{Num: num, Gen: t2, Kind: KindUncompressed, Offset: t3})
			case 2:
				rev.Entries = append(rev.Entries, XRefEntry{Num: num, Gen: 0, Kind: KindCompressed, Stream: t2, Offset: t3})
			default:
				// unknown type field: keep as unset for evidence
				rev.Entries = append(rev.Entries, XRefEntry{Num: num, Gen: t2, Kind: KindUnset, Offset: t3})
			}
		}
	}
	es := indexFrom(data, streamEnd, []byte("endobj"))
	ee := streamEnd
	if es >= 0 {
		ee = es + len("endobj")
	}
	rev.XRefEnd = ee
	return rev, nil
}

func parseNumArray(s string) []int {
	var out []int
	cur := 0
	have := false
	flush := func() {
		if have {
			out = append(out, cur)
			cur = 0
			have = false
		}
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= '0' && c <= '9' {
			cur = cur*10 + int(c-'0')
			have = true
		} else {
			flush()
		}
	}
	flush()
	return out
}

// readBigInt decodes a big-endian unsigned integer field (0 bytes => 0).
func readBigInt(b []byte) int {
	n := 0
	for _, c := range b {
		n = n<<8 + int(c)
	}
	return n
}
