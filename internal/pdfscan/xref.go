package pdfscan

import (
	"errors"
	"fmt"
	"strconv"
)

// parseXRef detects the xref kind at off and parses it.
func parseXRef(data []byte, off int64, maxInflate int64) (*Revision, error) {
	if off < 0 || off >= int64(len(data)) {
		return nil, fmt.Errorf("xref offset %d out of range (file size %d)", off, len(data))
	}
	p := &lparser{data: data, pos: int(off)}
	p.skipWS()
	if p.keywordAhead("xref") {
		return parseXRefTable(data, off)
	}
	return parseXRefStream(data, off, maxInflate)
}

func parseXRefTable(data []byte, off int64) (*Revision, error) {
	p := &lparser{data: data, pos: int(off)}
	p.skipWS()
	if !p.keywordAhead("xref") {
		return nil, fmt.Errorf("missing 'xref' keyword at %d", off)
	}
	p.pos += 4
	rev := &Revision{Kind: "table", XRefOffset: off, Trailer: Dict{}}
	for {
		p.skipWS()
		if p.pos >= len(p.data) {
			return nil, errors.New("unexpected end of data inside xref table")
		}
		if p.keywordAhead("trailer") {
			p.pos += len("trailer")
			break
		}
		startTok := p.readToken()
		start, err1 := strconv.Atoi(startTok)
		p.skipWS()
		cntTok := p.readToken()
		cnt, err2 := strconv.Atoi(cntTok)
		if err1 != nil || err2 != nil || cnt < 0 || cnt > 1<<22 {
			return nil, fmt.Errorf("bad xref subsection header %q %q at %d", startTok, cntTok, p.pos)
		}
		for i := 0; i < cnt; i++ {
			p.skipWS()
			oTok := p.readToken()
			p.skipWS()
			gTok := p.readToken()
			p.skipWS()
			tTok := p.readToken()
			o, e1 := strconv.ParseInt(oTok, 10, 64)
			g, e2 := strconv.Atoi(gTok)
			if e1 != nil || e2 != nil || tTok == "" {
				return nil, fmt.Errorf("bad xref entry %d in subsection %d", i, start)
			}
			e := XRefEntry{ObjNum: start + i, Gen: g, Offset: o}
			switch tTok[0] {
			case 'n':
				e.Type = EntryInUse
			case 'f':
				e.Type = EntryFree
			default:
				return nil, fmt.Errorf("unknown xref entry type %q", tTok)
			}
			rev.Entries = append(rev.Entries, e)
		}
	}
	v, err := p.parseValue()
	if err != nil {
		return nil, fmt.Errorf("bad trailer dictionary: %w", err)
	}
	d, ok := v.(Dict)
	if !ok {
		return nil, errors.New("trailer is not a dictionary")
	}
	rev.Trailer = d
	if pv, ok := d["Prev"].(int64); ok {
		rev.HasPrev = true
		rev.PrevOffset = pv
	}
	return rev, nil
}

func intSlice(v any) []int64 {
	arr, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]int64, 0, len(arr))
	for _, e := range arr {
		n, ok := e.(int64)
		if !ok {
			return nil
		}
		out = append(out, n)
	}
	return out
}

func parseXRefStream(data []byte, off int64, maxInflate int64) (*Revision, error) {
	ro, err := parseIndirectObject(data, off)
	if err != nil {
		return nil, fmt.Errorf("xref stream object: %w", err)
	}
	d, ok := ro.Value.(Dict)
	if !ok || ro.Stream == nil {
		return nil, errors.New("object at xref offset is not a stream")
	}
	if t, _ := d["Type"].(Name); t != "XRef" {
		return nil, fmt.Errorf("stream at %d has /Type %q, expected /XRef", off, t)
	}
	rev := &Revision{Kind: "stream", XRefOffset: off, Trailer: d}
	if !ro.Stream.LengthVerified {
		return nil, errors.New("xref stream length could not be verified; refusing to decode")
	}
	raw := data[ro.Stream.DataOffset : ro.Stream.DataOffset+ro.Stream.DataLength]
	dec, err := applyFilter(raw, ro.Stream.Filter, maxInflate)
	if err != nil {
		return nil, fmt.Errorf("cannot decode xref stream: %w", err)
	}
	w := intSlice(d["W"])
	if len(w) != 3 {
		return nil, errors.New("xref stream missing valid /W array")
	}
	index := intSlice(d["Index"])
	if len(index) == 0 {
		size, _ := d["Size"].(int64)
		index = []int64{0, size}
	}
	pos := 0
	for i := 0; i+1 < len(index); i += 2 {
		start, cnt := index[i], index[i+1]
		if cnt < 0 || cnt > 1<<22 {
			return nil, fmt.Errorf("implausible xref stream index count %d", cnt)
		}
		for j := int64(0); j < cnt; j++ {
			var fields [3]int64
			for k := 0; k < 3; k++ {
				wk := int(w[k])
				if wk < 0 || wk > 8 || pos+wk > len(dec) {
					return nil, errors.New("xref stream entry data truncated")
				}
				var v int64
				for _, b := range dec[pos : pos+wk] {
					v = v<<8 | int64(b)
				}
				pos += wk
				fields[k] = v
			}
			typ := fields[0]
			if w[0] == 0 {
				typ = 1
			}
			e := XRefEntry{ObjNum: int(start + j)}
			switch typ {
			case 0:
				e.Type = EntryFree
				e.Offset = fields[1]
				e.Gen = int(fields[2])
			case 1:
				e.Type = EntryInUse
				e.Offset = fields[1]
				e.Gen = int(fields[2])
			case 2:
				e.Type = EntryCompressed
				e.ObjStm = int(fields[1])
				e.Index = int(fields[2])
			default:
				return nil, fmt.Errorf("unknown xref stream entry type %d", typ)
			}
			rev.Entries = append(rev.Entries, e)
		}
	}
	if pv, ok := d["Prev"].(int64); ok {
		rev.HasPrev = true
		rev.PrevOffset = pv
	}
	return rev, nil
}
