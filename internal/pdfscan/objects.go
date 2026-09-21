package pdfscan

import (
	"bytes"
	"fmt"
	"strconv"
)

// RawObject is the result of parsing one indirect object at a byte offset.
type RawObject struct {
	ObjNum      int
	Gen         int
	Value       any
	ValueOffset int64
	Stream      *StreamInfo
	EndOffset   int64
}

// StreamInfo describes a stream payload attached to an indirect object.
type StreamInfo struct {
	Dict           Dict
	DataOffset     int64
	DataLength     int64
	LengthVerified bool
	Filter         string
}

const maxStreamSearch = 64 << 20

func matchEndstream(data []byte, pos int64) bool {
	i := int(pos)
	if i < len(data) && data[i] == '\r' {
		i++
		if i < len(data) && data[i] == '\n' {
			i++
		}
	} else if i < len(data) && data[i] == '\n' {
		i++
	}
	return i+9 <= len(data) && string(data[i:i+9]) == "endstream"
}

func filterOf(d Dict) string {
	f, ok := d["Filter"]
	if !ok {
		return ""
	}
	switch t := f.(type) {
	case Name:
		return string(t)
	case []any:
		var parts []string
		for _, e := range t {
			if n, ok := e.(Name); ok {
				parts = append(parts, string(n))
			}
		}
		if len(parts) > 0 {
			return joinComma(parts)
		}
	}
	return ""
}

func joinComma(parts []string) string {
	out := parts[0]
	for _, p := range parts[1:] {
		out += "," + p
	}
	return out
}

// parseIndirectObject parses "N G obj ... endobj" starting exactly at off.
func parseIndirectObject(data []byte, off int64) (*RawObject, error) {
	if off < 0 || off >= int64(len(data)) {
		return nil, fmt.Errorf("offset %d out of range (file size %d)", off, len(data))
	}
	p := &lparser{data: data, pos: int(off)}
	p.skipWS()
	tok1 := p.readToken()
	n1, err := strconv.ParseInt(tok1, 10, 64)
	if err != nil {
		return nil, fmt.Errorf("expected object number at %d, found %q", off, tok1)
	}
	p.skipWS()
	tok2 := p.readToken()
	n2, err := strconv.ParseInt(tok2, 10, 64)
	if err != nil {
		return nil, fmt.Errorf("expected generation number at %d, found %q", p.pos, tok2)
	}
	p.skipWS()
	if !p.keywordAhead("obj") {
		return nil, fmt.Errorf("expected 'obj' keyword at %d", p.pos)
	}
	p.pos += 3
	p.skipWS()
	voff := p.pos
	val, err := p.parseValue()
	if err != nil {
		return nil, err
	}
	ro := &RawObject{ObjNum: int(n1), Gen: int(n2), Value: val, ValueOffset: int64(voff)}
	p.skipWS()
	if p.keywordAhead("stream") {
		p.pos += len("stream")
		if p.pos < len(data) && data[p.pos] == '\r' {
			p.pos++
			if p.pos < len(data) && data[p.pos] == '\n' {
				p.pos++
			}
		} else if p.pos < len(data) && data[p.pos] == '\n' {
			p.pos++
		} else {
			return nil, fmt.Errorf("expected EOL after 'stream' at %d", p.pos)
		}
		sstart := p.pos
		si := &StreamInfo{DataOffset: int64(sstart)}
		if d, ok := val.(Dict); ok {
			si.Dict = d
			si.Filter = filterOf(d)
			if lv, ok := d["Length"].(int64); ok && lv >= 0 && int64(sstart)+lv <= int64(len(data)) {
				if matchEndstream(data, int64(sstart)+lv) {
					si.DataLength = lv
					si.LengthVerified = true
				}
			}
		}
		if si.LengthVerified {
			p.pos = int(si.DataOffset + si.DataLength)
			p.skipWS()
		} else {
			limit := len(data)
			if sstart+maxStreamSearch < limit {
				limit = sstart + maxStreamSearch
			}
			idx := bytes.Index(data[sstart:limit], []byte("endstream"))
			if idx < 0 {
				return nil, fmt.Errorf("endstream not found for object %d %d", n1, n2)
			}
			si.DataLength = int64(idx)
			for si.DataLength > 0 {
				c := data[int64(sstart)+si.DataLength-1]
				if c == '\n' || c == '\r' {
					si.DataLength--
				} else {
					break
				}
			}
			p.pos = sstart + idx + len("endstream")
		}
		if !p.keywordAhead("endstream") {
			return nil, fmt.Errorf("expected 'endstream' at %d", p.pos)
		}
		p.pos += len("endstream")
		ro.Stream = si
	}
	p.skipWS()
	if p.keywordAhead("endobj") {
		p.pos += len("endobj")
	}
	ro.EndOffset = int64(p.pos)
	return ro, nil
}
