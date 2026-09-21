package forensic

import (
	"bytes"
	"fmt"
	"sort"
)

// findStartXRefs returns the offset named by every "startxref N" pair,
// in file order. The last one is where a conforming reader begins.
func findStartXRefs(data []byte) []int64 {
	var out []int64
	from := 0
	for {
		idx := bytes.Index(data[from:], []byte("startxref"))
		if idx < 0 {
			return out
		}
		p := from + idx
		end := p + len("startxref")
		if !bytes.HasPrefix(data[end:], []byte("\r")) && !bytes.HasPrefix(data[end:], []byte("\n")) {
			from = p + 1
			continue
		}
		q := skipWS(data, end)
		n := q
		for n < len(data) && (data[n] >= '0' && data[n] <= '9') {
			n++
		}
		if n > q {
			out = append(out, int64(atoi(data[q:n])))
		}
		from = p + 1
	}
}

type trailerInfo struct {
	Dict    *Node
	At      ByteRange
	Prev    int64
	HasPrev bool
	Size    int
	Root    *Key
}

func trailerFromDict(d *Node, at ByteRange) trailerInfo {
	t := trailerInfo{Dict: d, At: at}
	if p := d.Dict("Prev"); p != nil && p.Type == "int" {
		t.Prev = p.Int
		t.HasPrev = true
	}
	if s := d.Dict("Size"); s != nil && s.Type == "int" {
		t.Size = int(s.Int)
	}
	if r := d.Dict("Root"); r != nil && r.Type == "ref" {
		t.Root = r.Ref
	}
	return t
}

// parseTableXRef parses a classic cross-reference table at offset.
// Returns entries and trailer. entryAt ranges are captured.
func parseTableXRef(data []byte, off int64) ([]XRefEntry, trailerInfo, int64, int64, error) {
	p := int(off)
	p = skipWS(data, p)
	if ok, after := matchKeyword(data, p, "xref"); !ok {
		return nil, trailerInfo{}, 0, 0, fmt.Errorf("xref table expected at %d", off)
	} else {
		p = after
	}
	var entries []XRefEntry
	for {
		q := skipWS(data, p)
		// trailer keyword ends subsections
		if ok, _ := matchKeyword(data, q, "trailer"); ok {
			p = q
			break
		}
		first := q
		for first < len(data) && data[first] >= '0' && data[first] <= '9' {
			first++
		}
		if first == q {
			return nil, trailerInfo{}, 0, 0, fmt.Errorf("xref subsection expected at %d", q)
		}
		sp := skipWS(data, first)
		nn := sp
		for nn < len(data) && data[nn] >= '0' && data[nn] <= '9' {
			nn++
		}
		if nn == sp {
			return nil, trailerInfo{}, 0, 0, fmt.Errorf("xref subsection count expected at %d", sp)
		}
		start := atoi(data[q:first])
		count := atoi(data[sp:nn])
		p = skipWS(data, nn)
		for i := 0; i < count; i++ {
			rowStart := p
			// find end-of-line for this 20-byte row first
			eol := p
			for eol < len(data) && data[eol] != '\r' && data[eol] != '\n' {
				eol++
			}
			nextRow := eol
			if nextRow < len(data) && data[nextRow] == '\r' {
				nextRow++
				if nextRow < len(data) && data[nextRow] == '\n' {
					nextRow++
				}
			} else if nextRow < len(data) && data[nextRow] == '\n' {
				nextRow++
			}
			a := p
			for a < len(data) && data[a] != ' ' && data[a] != '\r' && data[a] != '\n' {
				a++
			}
			aEnd := skipWS(data, a)
			b := aEnd
			for b < len(data) && data[b] != ' ' && data[b] != '\r' && data[b] != '\n' {
				b++
			}
			t := b
			for t < eol && (data[t] == ' ' || data[t] == '\t') {
				t++
			}
			if t >= eol {
				return nil, trailerInfo{}, 0, 0, fmt.Errorf("truncated xref row for %d", start+i)
			}
			typ := data[t]
			e := XRefEntry{
				Num:      start + i,
				Offset:   int64(atoi(data[p:a])),
				Gen:      atoi(data[aEnd:b]),
				Declared: ByteRange{Start: int64(rowStart), End: int64(nextRow)},
			}
			switch typ {
			case 'n':
				e.Type = 1
			case 'f':
				e.Type = 0
			default:
				return nil, trailerInfo{}, 0, 0, fmt.Errorf("bad xref type %q at %d", string(typ), t)
			}
			entries = append(entries, e)
			p = nextRow
		}
	}
	tp := skipWS(data, p)
	if ok, after := matchKeyword(data, tp, "trailer"); !ok {
		return nil, trailerInfo{}, 0, 0, fmt.Errorf("trailer expected at %d", tp)
	} else {
		tp = skipWS(data, after)
	}
	d, dend, err := parseDict(data, tp, 0)
	if err != nil {
		return nil, trailerInfo{}, 0, 0, fmt.Errorf("trailer dict: %w", err)
	}
	eofStart, eofEnd := findEOFAfter(data, dend)
	return entries, trailerFromDict(d, ByteRange{Start: int64(tp), End: int64(dend)}), eofStart, eofEnd, nil
}

// parseStreamXRef parses an xref stream object at offset (the N G obj header).
func parseStreamXRef(data []byte, off int64, lim Limits, resolveInt func(Key) (int64, bool)) ([]XRefEntry, trailerInfo, *parsedObject, error) {
	p := int(off)
	hm, ok := matchHeaderAt(data, p)
	if !ok {
		return nil, trailerInfo{}, nil, fmt.Errorf("xref stream object header expected at %d", off)
	}
	po, err := parseIndirectObject(data, hm, resolveInt)
	if err != nil && po == nil {
		return nil, trailerInfo{}, nil, fmt.Errorf("xref stream: %w", err)
	}
	if po.Stream == nil {
		return nil, trailerInfo{}, po, fmt.Errorf("object at %d is not a stream", off)
	}
	if po.Node == nil || po.Node.Type != "dict" {
		return nil, trailerInfo{}, po, fmt.Errorf("xref stream dict missing")
	}
	wn := po.Node.Dict("W")
	if wn == nil || wn.Type != "array" || len(wn.Items) != 3 {
		return nil, trailerInfo{}, po, fmt.Errorf("xref /W array missing")
	}
	var w [3]int
	for i := 0; i < 3; i++ {
		if wn.Items[i].Type != "int" {
			return nil, trailerInfo{}, po, fmt.Errorf("xref /W[%d] not int", i)
		}
		w[i] = int(wn.Items[i].Int)
	}
	index := []int{0, 0}
	if in := po.Node.Dict("Index"); in != nil && in.Type == "array" {
		if len(in.Items)%2 != 0 {
			return nil, trailerInfo{}, po, fmt.Errorf("xref /Index odd length")
		}
		index = index[:0]
		for _, it := range in.Items {
			if it.Type != "int" {
				return nil, trailerInfo{}, po, fmt.Errorf("xref /Index non-int")
			}
			index = append(index, int(it.Int))
		}
	} else {
		size := 0
		if s := po.Node.Dict("Size"); s != nil && s.Type == "int" {
			size = int(s.Int)
		}
		index = []int{0, size}
	}

	raw, derr := verifiedExpand(data, po.Node, po.Stream, lim)
	if derr != nil {
		return nil, trailerInfo{}, po, derr
	}
	entryBytes := w[0] + w[1] + w[2]
	var entries []XRefEntry
	pos := 0
	for pair := 0; pair+1 < len(index); pair += 2 {
		first, count := index[pair], index[pair+1]
		for i := 0; i < count; i++ {
			if pos+entryBytes > len(raw) {
				return nil, trailerInfo{}, po, fmt.Errorf("xref stream truncated at object %d", first+i)
			}
			row := raw[pos : pos+entryBytes]
			pos += entryBytes
			t := 1
			if w[0] > 0 {
				t = int(row[w[0]-1]) // spec: type field is a single-byte value
			}
			f2 := int64(0)
			if w[1] > 0 {
				f2 = readFixed(row[w[0] : w[0]+w[1]])
			}
			f3 := 0
			if w[2] > 0 {
				f3 = int(readFixed(row[w[0]+w[1] : entryBytes]))
			}
			e := XRefEntry{Num: first + i, Gen: f3, Type: t,
				Declared: ByteRange{Start: int64(i), End: int64(i + 1)}}
			switch t {
			case 0:
				e.Offset = f2 // next free
			case 1:
				e.Offset = f2
			case 2:
				e.Stream = int(f2)
				e.Index = f3
			default:
				return nil, trailerInfo{}, po, fmt.Errorf("xref stream bad type %d at object %d", t, first+i)
			}
			entries = append(entries, e)
		}
	}
	_, _ = findEOFAfter(data, po.EndPos)
	po.Stream.Expanded = true
	po.Stream.ExpandedSize = int64(len(raw))
	tr := trailerFromDict(po.Node, po.Node.At)
	tr.At = po.Node.At
	_ = sort.Reverse
	return entries, tr, po, nil
}

func readFixed(b []byte) int64 {
	var v int64
	for _, c := range b {
		v = v*10 + int64(c-'0')
	}
	return v
}

// findEOFAfter locates %%EOF following a position.
func findEOFAfter(data []byte, from int) (int64, int64) {
	idx := bytes.Index(data[from:], []byte("%%EOF"))
	if idx < 0 {
		return -1, -1
	}
	s := from + idx
	return int64(s), int64(s + 5)
}
