package pdf

import (
	"bytes"
	"fmt"
	"strconv"
)

// EntryType classifies an xref entry.
type EntryType string

const (
	EntryInUse      EntryType = "n"          // regular in-use object
	EntryFree       EntryType = "f"          // free object (generation tracking)
	EntryCompressed EntryType = "compressed" // inside an object stream
)

// XrefEntry is one row of an xref table or decoded xref stream.
type XrefEntry struct {
	Num       int       `json:"num"`
	Gen       int       `json:"gen"`
	Type      EntryType `json:"type"`
	Offset    int64     `json:"offset"`
	StreamNum int       `json:"streamNum,omitempty"` // object stream number (compressed)
	StreamIdx int       `json:"streamIdx,omitempty"` // index inside the object stream
}

// Section is one parsed xref section (table or stream) plus its trailer.
type Section struct {
	Offset      int64       `json:"offset"`
	Kind        string      `json:"kind"` // "table" or "stream"
	Entries     []XrefEntry `json:"entries"`
	Trailer     Dict        `json:"-"`
	TrailerText string      `json:"trailer"`
	Prev        int64       `json:"prev"`
	HasPrev     bool        `json:"hasPrev"`
	End         int64       `json:"end"`
}

// parseXrefTableAt parses a classic "xref ... trailer" section at off.
func parseXrefTableAt(data []byte, off int64) (*Section, error) {
	if off < 0 || off+4 > int64(len(data)) || !bytes.Equal(data[off:off+4], []byte("xref")) {
		return nil, fmt.Errorf("no xref table at %d", off)
	}
	pos := int(off) + 4
	sec := &Section{Offset: off, Kind: "table", Entries: []XrefEntry{}}
	for {
		for pos < len(data) && isWS(data[pos]) {
			pos++
		}
		if pos >= len(data) {
			return nil, fmt.Errorf("xref table at %d: unexpected EOF", off)
		}
		if bytes.HasPrefix(data[pos:], []byte("trailer")) {
			pos += len("trailer")
			break
		}
		// subsection header: start count
		p := &parser{data: data, pos: pos}
		startTok := p.token()
		start, err1 := strconv.Atoi(startTok)
		p.skipWS()
		countTok := p.token()
		count, err2 := strconv.Atoi(countTok)
		if err1 != nil || err2 != nil || start < 0 || count < 0 {
			return nil, fmt.Errorf("xref table at %d: bad subsection header %q %q", off, startTok, countTok)
		}
		pos = p.pos
		// Entries: exactly 20 bytes each, but tolerate short lines.
		for i := 0; i < count; i++ {
			for pos < len(data) && (data[pos] == '\n' || data[pos] == '\r') {
				pos++
			}
			if pos+10 > len(data) {
				return nil, fmt.Errorf("xref table at %d: truncated entry", off)
			}
			line := data[pos:]
			eol := bytes.IndexByte(line, '\n')
			if eol < 0 {
				eol = len(line)
			}
			entryLine := string(bytes.TrimRight(line[:eol], "\r"))
			pos += eol
			if pos < len(data) {
				pos++
			}
			var offV, genV int64
			var typ byte
			if n, _ := fmt.Sscanf(entryLine, "%d %d %c", &offV, &genV, &typ); n < 3 {
				return nil, fmt.Errorf("xref table at %d: malformed entry %q", off, entryLine)
			}
			sec.Entries = append(sec.Entries, XrefEntry{
				Num: start + i, Gen: int(genV), Offset: offV,
				Type: EntryType(string(typ)),
			})
		}
	}
	// trailer dictionary
	p := &parser{data: data, pos: pos}
	p.skipWS()
	v, err := p.parseValue()
	if err != nil {
		return nil, fmt.Errorf("xref table at %d: trailer: %w", off, err)
	}
	tr, ok := v.(Dict)
	if !ok {
		return nil, fmt.Errorf("xref table at %d: trailer is not a dictionary", off)
	}
	sec.Trailer = tr
	sec.TrailerText = Render(tr)
	if prev, ok := tr.Int("Prev"); ok {
		sec.Prev = prev
		sec.HasPrev = true
	}
	sec.End = int64(p.pos)
	return sec, nil
}

// parseXrefStreamAt parses an xref stream object at off.
func parseXrefStreamAt(data []byte, off int64, lim Limits) (*Section, error) {
	obj, err := ParseIndirectAt(data, off)
	if err != nil {
		return nil, fmt.Errorf("no xref stream at %d: %w", off, err)
	}
	d := obj.StreamDict
	if d == nil {
		return nil, fmt.Errorf("object at %d is not a stream", off)
	}
	if t, _ := d.Name("Type"); t != "XRef" {
		return nil, fmt.Errorf("stream at %d is /Type %q, not /XRef", off, t)
	}
	if !obj.BoundaryOK {
		return nil, fmt.Errorf("xref stream at %d: stream boundary not verified", off)
	}
	decoded, err := DecodeStream(d, obj.StreamData, lim)
	if err != nil {
		return nil, fmt.Errorf("xref stream at %d: %w", off, err)
	}
	wArr, ok := d.Get("W")
	if !ok {
		return nil, fmt.Errorf("xref stream at %d: missing /W", off)
	}
	wList, ok := wArr.(Array)
	if !ok || len(wList) != 3 {
		return nil, fmt.Errorf("xref stream at %d: bad /W", off)
	}
	var w [3]int64
	for i := 0; i < 3; i++ {
		n, ok := wList[i].(int64)
		if !ok || n < 0 || n > 8 {
			return nil, fmt.Errorf("xref stream at %d: bad /W entry", off)
		}
		w[i] = n
	}
	type indexRange struct{ start, count int64 }
	var ranges []indexRange
	if idx, ok := d.Get("Index"); ok {
		arr, ok := idx.(Array)
		if !ok || len(arr)%2 != 0 {
			return nil, fmt.Errorf("xref stream at %d: bad /Index", off)
		}
		for i := 0; i < len(arr); i += 2 {
			s, ok1 := arr[i].(int64)
			c, ok2 := arr[i+1].(int64)
			if !ok1 || !ok2 {
				return nil, fmt.Errorf("xref stream at %d: bad /Index entry", off)
			}
			ranges = append(ranges, indexRange{s, c})
		}
	} else {
		size, ok := d.Int("Size")
		if !ok {
			return nil, fmt.Errorf("xref stream at %d: missing /Size", off)
		}
		ranges = []indexRange{{0, size}}
	}
	sec := &Section{Offset: off, Kind: "stream", Entries: []XrefEntry{}}
	recLen := w[0] + w[1] + w[2]
	pos := 0
	for _, rg := range ranges {
		for i := int64(0); i < rg.count; i++ {
			if pos+int(recLen) > len(decoded) {
				return nil, fmt.Errorf("xref stream at %d: entry data truncated", off)
			}
			field := func(wi int64, def uint64) uint64 {
				if wi == 0 {
					return def
				}
				var v uint64
				for j := int64(0); j < wi; j++ {
					v = v<<8 | uint64(decoded[pos+int(j)])
				}
				pos += int(wi)
				return v
			}
			typ := field(w[0], 1)
			f2 := field(w[1], 0)
			f3 := field(w[2], 0)
			num := int(rg.start + i)
			switch typ {
			case 0:
				sec.Entries = append(sec.Entries, XrefEntry{Num: num, Gen: int(f3), Offset: int64(f2), Type: EntryFree})
			case 1:
				sec.Entries = append(sec.Entries, XrefEntry{Num: num, Gen: int(f3), Offset: int64(f2), Type: EntryInUse})
			case 2:
				sec.Entries = append(sec.Entries, XrefEntry{Num: num, Gen: 0, StreamNum: int(f2), StreamIdx: int(f3), Type: EntryCompressed})
			default:
				return nil, fmt.Errorf("xref stream at %d: unknown entry type %d", off, typ)
			}
		}
	}
	sec.Trailer = d
	sec.TrailerText = Render(d)
	if prev, ok := d.Int("Prev"); ok {
		sec.Prev = prev
		sec.HasPrev = true
	}
	sec.End = obj.End
	return sec, nil
}

// parseSectionAt detects the section kind at off and parses it.
func parseSectionAt(data []byte, off int64, lim Limits) (*Section, error) {
	if off < 0 || off >= int64(len(data)) {
		return nil, fmt.Errorf("offset %d out of range", off)
	}
	if bytes.HasPrefix(data[off:], []byte("xref")) {
		return parseXrefTableAt(data, off)
	}
	return parseXrefStreamAt(data, off, lim)
}
