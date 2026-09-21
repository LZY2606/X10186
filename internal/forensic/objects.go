package forensic

import (
	"bytes"
	"compress/zlib"
	"fmt"
	"io"
)

// headerMatch describes a verified "N G obj" header at an offset.
type headerMatch struct {
	Num int
	Gen int
	Pos int // offset of first digit
	End int // offset after "obj"
}

func digitAt(data []byte, p int) bool { return p < len(data) && data[p] >= '0' && data[p] <= '9' }

// matchHeaderAt requires a whitespace/BOF boundary before the number and a
// whitespace/delimiter boundary after obj.
func matchHeaderAt(data []byte, p int) (headerMatch, bool) {
	if p < 0 || p >= len(data) || !digitAt(data, p) {
		return headerMatch{}, false
	}
	if p > 0 {
		prev := data[p-1]
		if !isWhitespace(prev) {
			return headerMatch{}, false
		}
	}
	q := p
	for q < len(data) && digitAt(data, q) {
		q++
	}
	numStart := p
	numEnd := q
	q = skipWS(data, q)
	genStart := q
	for q < len(data) && digitAt(data, q) {
		q++
	}
	if q == genStart {
		return headerMatch{}, false
	}
	q = skipWS(data, q)
	if ok, after := matchKeyword(data, q, "obj"); ok {
		num := atoi(data[numStart:numEnd])
		gen := atoi(data[genStart:q])
		return headerMatch{Num: num, Gen: gen, Pos: numStart, End: after}, true
	}
	return headerMatch{}, false
}

func atoi(b []byte) int {
	n := 0
	for _, c := range b {
		if c >= '0' && c <= '9' {
			n = n*10 + int(c-'0')
		}
	}
	return n
}

// findKeyword locates the next keyword at or after from, requiring boundaries.
func findKeyword(data []byte, from int, kw string) int {
	for from >= 0 {
		idx := bytes.Index(data[from:], []byte(kw))
		if idx < 0 {
			return -1
		}
		p := from + idx
		okBefore := p == 0 || isWhitespace(data[p-1]) || isDelim(data[p-1])
		end := p + len(kw)
		okAfter := end >= len(data) || isWhitespace(data[end]) || isDelim(data[end])
		if okBefore && okAfter {
			return p
		}
		from = p + 1
	}
	return -1
}

// parsedObject is a physically located indirect object.
type parsedObject struct {
	Num    int
	Gen    int
	Header ByteRange
	Node   *Node
	Stream *StreamInfo
	EndPos int
}

// parseIndirectObject verifies header, value, optional stream boundary and endobj.
// resolveInt resolves indirect numeric values such as /Length.
func parseIndirectObject(data []byte, hm headerMatch, resolveInt func(Key) (int64, bool)) (*parsedObject, error) {
	pos := hm.End
	v, after, err := parseValue(data, pos, 0)
	if err != nil {
		return nil, err
	}
	po := &parsedObject{Num: hm.Num, Gen: hm.Gen, Node: v,
		Header: ByteRange{Start: int64(hm.Pos), End: int64(hm.End)}}
	pos = after

	// Stream?
	sp := skipWS(data, pos)
	if ok, skw := matchKeyword(data, sp, "stream"); ok {
		si := &StreamInfo{KeywordStart: int64(sp)}
		d := skw
		// PDF requires CRLF or LF after stream; tolerate lone CR.
		if d < len(data) && data[d] == '\r' {
			d++
			if d < len(data) && data[d] == '\n' {
				d++
			}
		} else if d < len(data) && data[d] == '\n' {
			d++
		}
		si.DataStart = int64(d)

		var lengthDecl int64 = -1
		lengthKnown := false
		if ln := v.Dict("Length"); ln != nil {
			if ln.Type == "int" {
				lengthDecl = ln.Int
				lengthKnown = true
			} else if ln.Type == "ref" && ln.Ref != nil && resolveInt != nil {
				if lv, ok := resolveInt(*ln.Ref); ok {
					lengthDecl = lv
					lengthKnown = true
				}
			}
		}

		// Find endstream as physical evidence.
		endKW := findKeyword(data, d, "endstream")
		if endKW < 0 {
			si.BoundaryOK = false
			si.LengthActual = int64(len(data)) - int64(d)
			po.Stream = si
			return po, fmt.Errorf("no endstream found for object %d", hm.Num)
		}
		// data end per declared length
		if lengthKnown && lengthDecl >= 0 {
			si.LengthDecl = lengthDecl
			candidate := d + int(lengthDecl)
			si.LengthOK = eolBefore(data, candidate, endKW)
			if si.LengthOK {
				si.DataEnd = int64(candidate)
			} else {
				si.DataEnd = int64(dataEndBeforeKW(data, d, endKW))
			}
		} else {
			si.DataEnd = int64(dataEndBeforeKW(data, d, endKW))
		}
		si.LengthActual = si.DataEnd - si.DataStart
		si.BoundaryOK = true
		si.Filters = filterNames(v)

		afterStream := endKW + len("endstream")
		ep := skipWS(data, afterStream)
		if ok2, eend := matchKeyword(data, ep, "endobj"); ok2 {
			afterStream = eend
		}
		po.EndPos = afterStream
		po.Stream = si
		return po, nil
	}

	ep := skipWS(data, pos)
	if ok, eend := matchKeyword(data, ep, "endobj"); ok {
		po.EndPos = eend
	} else {
		// Header verified but body lacks endobj; keep what parsed.
		po.EndPos = pos
	}
	return po, nil
}

// eolBefore reports whether the bytes at p consist of optional EOL then endKW.
func eolBefore(data []byte, p, endKW int) bool {
	q := p
	if q < len(data) && data[q] == '\r' {
		q++
		if q < len(data) && data[q] == '\n' {
			q++
		}
	} else if q < len(data) && data[q] == '\n' {
		q++
	}
	return q == endKW
}

func dataEndBeforeKW(data []byte, d, endKW int) int {
	p := endKW
	if p > d && data[p-1] == '\n' {
		p--
		if p > d && data[p-1] == '\r' {
			p--
		}
	} else if p > d && data[p-1] == '\r' {
		p--
	}
	return p
}

func filterNames(dict *Node) []string {
	var out []string
	add := func(n *Node) {
		switch n.Type {
		case "name":
			out = append(out, n.Name)
		case "array":
			for _, it := range n.Items {
				if it.Type == "name" {
					out = append(out, it.Name)
				}
			}
		}
	}
	if f := dict.Dict("Filter"); f != nil {
		add(f)
	}
	return out
}

// Limits controls safe expansion of compressed data.
type Limits struct {
	MaxExpand int64
}

// DefaultLimits bounds a single stream expansion to 64 MiB.
var DefaultLimits = Limits{MaxExpand: 64 << 20}

// ErrLimit means expansion exceeded the configured resource quota.
type ErrLimit struct{ Msg string }

func (e *ErrLimit) Error() string { return e.Msg }

func isLimitErr(err error) (*ErrLimit, bool) {
	if e, ok := err.(*ErrLimit); ok {
		return e, true
	}
	return nil, false
}

// inflateFlate expands a zlib stream, halting safely at max bytes.
func inflateFlate(src []byte, max int64) ([]byte, error) {
	zr, err := zlib.NewReader(bytes.NewReader(src))
	if err != nil {
		// Some producers emit raw-deflate despite FlateDecode.
		return nil, err
	}
	defer zr.Close()
	out := make([]byte, 0, len(src)*2)
	buf := make([]byte, 32*1024)
	var total int64
	for {
		n, rerr := zr.Read(buf)
		total += int64(n)
		if max > 0 && total > max {
			return nil, &ErrLimit{Msg: fmt.Sprintf("expansion exceeded %d bytes", max)}
		}
		out = append(out, buf[:n]...)
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			return nil, rerr
		}
	}
	return out, nil
}

// verifiedExpand expands a stream only after length, filter and boundary checks.
// It fills the StreamInfo fields; unsupported filters are left unexpanded.
func verifiedExpand(data []byte, dict *Node, si *StreamInfo, lim Limits) ([]byte, error) {
	filters := filterNames(dict)
	si.Filters = filters
	if len(filters) == 0 {
		return data[si.DataStart:si.DataEnd], nil
	}
	if len(filters) != 1 || filters[0] != "FlateDecode" {
		si.DecodeError = "unsupported filter chain: " + joinNames(filters)
		return nil, fmt.Errorf("%s", si.DecodeError)
	}
	src := data[si.DataStart:si.DataEnd]
	out, err := inflateFlate(src, lim.MaxExpand)
	if err != nil {
		if le, ok := isLimitErr(err); ok {
			si.Truncated = true
			si.DecodeError = le.Msg
			return nil, err
		}
		si.DecodeError = err.Error()
		return nil, err
	}
	si.Expanded = true
	si.ExpandedSize = int64(len(out))
	return out, nil
}

func joinNames(ns []string) string {
	out := ""
	for i, n := range ns {
		if i > 0 {
			out += ","
		}
		out += n
	}
	return out
}
