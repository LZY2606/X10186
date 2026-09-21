package pdf

import (
	"bytes"
	"compress/zlib"
	"fmt"
	"io"
	"strings"
)

// ObjectParts describes a single indirect object located by its header.
type ObjectParts struct {
	HeaderStart int // offset of object number
	HeaderEnd   int // offset just after "obj"
	DictStart   int // offset of "<<", -1 if no dict
	DictEnd     int // offset just after ">>"
	Dict        map[string]string
	IsStream    bool
	StreamStart int // first byte of encoded stream data
	StreamEnd   int // one past the last encoded byte
	ObjectEnd   int // one past "endobj"
}

// findObjectHeader checks whether "N G obj" begins at or within tolerance of off.
// Returns the exact header start and true on match.
func findObjectHeader(data []byte, off, num, gen int) (int, bool) {
	if off < 0 || off >= len(data) {
		return -1, false
	}
	want := []byte(fmt.Sprintf("%d %d obj", num, gen))
	// exact match
	if atBytes(data, off, want) {
		return off, true
	}
	// tolerate only the whitespace between number and obj? Spec: header at offset.
	// Also allow a single leading whitespace (some writers pad); keep evidence.
	for _, d := range []int{1, 2} {
		c := off + d
		if c < len(data) && atBytes(data, c, want) {
			return c, true
		}
	}
	return -1, false
}

// parseObject parses one indirect object whose header starts at headerStart.
func parseObject(data []byte, headerStart, num, gen int, resolveLen func(n, g int) int) (*ObjectParts, error) {
	op := &ObjectParts{HeaderStart: headerStart, DictStart: -1}
	p := skipWS(data, headerStart)
	numTok, p := readToken(data, p)
	n, _ := parseInt([]byte(numTok))
	if n != num {
		return nil, fmt.Errorf("header object number mismatch: want %d got %q", num, numTok)
	}
	p = skipWS(data, p)
	genTok, p := readToken(data, p)
	g, _ := parseInt([]byte(genTok))
	if g != gen {
		return nil, fmt.Errorf("header generation mismatch: want %d got %q", gen, genTok)
	}
	p = skipWS(data, p)
	kw, p2 := readToken(data, p)
	if kw != "obj" {
		return nil, fmt.Errorf("missing obj keyword at %d", headerStart)
	}
	op.HeaderEnd = p2

	body := skipWS(data, p2)
	if body < len(data) && data[body] == '<' && body+1 < len(data) && data[body+1] == '<' {
		op.DictStart = body
		d, de := parseDictAt(data, body)
		op.Dict = d
		op.DictEnd = de
		after := skipWS(data, de)
		if atBytes(data, after, []byte("stream")) {
			op.IsStream = true
			ss := after + len("stream")
			// EOL after "stream": CRLF or single LF
			if ss+1 < len(data) && data[ss] == '\r' && data[ss+1] == '\n' {
				ss += 2
			} else if ss < len(data) && (data[ss] == '\n' || data[ss] == '\r') {
				ss++
			}
			op.StreamStart = ss
			length, resolvedNote := effectiveLength(data, d, ss, resolveLen)
			if resolvedNote != "" {
				return op, fmt.Errorf("stream boundary not verified: %s", resolvedNote)
			}
			end, note, ok := verifyStreamBoundaryAt(data, length, ss)
			if !ok {
				return op, fmt.Errorf("stream boundary not verified: %s", note)
			}
			op.StreamEnd = end
			// find endobj after endstream
			afterStream := end
			es := indexFrom(data, afterStream, []byte("endstream"))
			if es < 0 {
				return op, fmt.Errorf("missing endstream")
			}
			eo := indexFrom(data, es+len("endstream"), []byte("endobj"))
			if eo < 0 {
				// object may omit endobj (legal) — use endstream end as object end
				op.ObjectEnd = es + len("endstream")
				return op, nil
			}
			op.ObjectEnd = eo + len("endobj")
			return op, nil
		}
	}

	// non-stream object: value runs until "endobj"
	eo := indexFrom(data, body, []byte("endobj"))
	if eo < 0 {
		// truncated object: consume to EOF
		op.ObjectEnd = len(data)
		return op, nil
	}
	op.ObjectEnd = eo + len("endobj")
	return op, nil
}

// effectiveLength resolves the declared /Length, following one indirect
// reference ("N G R") through resolveLen. An indirect length that cannot be
// resolved prevents verification (we never guess).
func effectiveLength(data []byte, d map[string]string, streamStart int, resolveLen func(n, g int) int) (int, string) {
	lengthVal, hasLen := d["Length"]
	if !hasLen {
		return 0, "no /Length declared"
	}
	if length, ok := dictInt(lengthVal); ok {
		if length < 0 {
			return 0, "negative /Length"
		}
		return length, ""
	}
	if ln, ok := dictRef(lengthVal); ok {
		// generation in the reference
		fields := strings.Fields(lengthVal)
		g := 0
		fmt.Sscanf(fields[1], "%d", &g)
		if resolveLen == nil {
			return 0, "/Length indirect reference cannot be resolved in this context"
		}
		length := resolveLen(ln, g)
		if length < 0 {
			return 0, fmt.Sprintf("/Length refers to object %d %d which is not readable", ln, g)
		}
		return length, ""
	}
	return 0, "unparseable /Length value: " + lengthVal
}

// verifyStreamBoundaryAt requires an EOL + "endstream" at streamStart+length.
func verifyStreamBoundaryAt(data []byte, length, streamStart int) (int, string, bool) {
	end := streamStart + length
	if end > len(data) {
		return 0, fmt.Sprintf("/Length %d runs past EOF (%d)", length, len(data)), false
	}
	p := end
	if p+1 < len(data) && data[p] == '\r' && data[p+1] == '\n' {
		p += 2
	} else if p < len(data) && (data[p] == '\n' || data[p] == '\r') {
		p++
	}
	if !atBytes(data, p, []byte("endstream")) {
		es := indexFrom(data, streamStart, []byte("endstream"))
		if es >= 0 {
			return 0, fmt.Sprintf("declared end %d not followed by endstream (endstream at %d)", end, es), false
		}
		return 0, fmt.Sprintf("declared end %d not followed by endstream", end), false
	}
	return end, "", true
}

// supportedExpansion reports whether all filters are ones we can safely verify.
func supportedExpansion(filters []string) (bool, string) {
	for _, f := range filters {
		switch f {
		case "", "FlateDecode", "Fl":
		default:
			return false, "unsupported filter /" + f
		}
	}
	// Multiple /Fl layers are allowed but we only expand one layer for object
	// streams; nested flate is rare and guarded by the size limit.
	return true, ""
}

// expandFlate expands a zlib (FlateDecode) stream, enforcing the byte ceiling.
// It never mutates the input.
func expandFlate(encoded []byte, limit int) ([]byte, error) {
	if len(encoded) == 0 {
		return nil, fmt.Errorf("empty stream")
	}
	zr, err := zlib.NewReader(bytes.NewReader(encoded))
	if err != nil {
		return nil, fmt.Errorf("zlib header: %w", err)
	}
	defer zr.Close()
	out := make([]byte, 0, len(encoded)*2)
	buf := make([]byte, 32*1024)
	for {
		n, rerr := zr.Read(buf)
		if n > 0 {
			if len(out)+n > limit {
				return nil, fmt.Errorf("expansion exceeds limit of %d bytes (resource limit)", limit)
			}
			out = append(out, buf[:n]...)
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			return nil, fmt.Errorf("zlib data: %w", rerr)
		}
	}
	return out, nil
}

// previewBytes renders a short ASCII-safe preview of decoded content.
func previewBytes(b []byte, max int) string {
	if len(b) > max {
		b = b[:max]
	}
	var sb strings.Builder
	for _, c := range b {
		if c == '\r' || c == '\n' || c == '\t' || (c >= 32 && c < 127) {
			sb.WriteByte(c)
		} else {
			sb.WriteByte('.')
		}
	}
	return sb.String()
}
