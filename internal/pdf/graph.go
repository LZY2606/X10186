package pdf

import (
	"bytes"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// buildReferences scans each candidate's raw bytes for "N G R" references.
// Stream payloads and compressed (objstm) content are skipped to avoid noise;
// dictionaries/arrays of verified objects are walked structurally.
func (s *Scanner) buildReferences(candidates []*Candidate, revs *[]Revision) []Reference {
	var refs []Reference
	// map each byte offset to the candidate that owns it (for FromNum/FromRev)
	type owner struct {
		num int
		rev int
	}
	owned := []struct {
		start, end int
		o          owner
	}{}
	for _, c := range candidates {
		if c.Origin == OriginObjStm || c.Origin == OriginFreeDecl || c.ByteStart < 0 {
			continue
		}
		owned = append(owned, struct {
			start, end int
			o          owner
		}{c.ByteStart, c.ByteEnd, owner{c.Num, c.Revision}})
	}
	sort.Slice(owned, func(i, j int) bool { return owned[i].start < owned[j].start })
	ownerAt := func(pos int) (owner, bool) {
		for _, r := range owned {
			if pos >= r.start && pos < r.end {
				return r.o, true
			}
		}
		return owner{}, false
	}

	// Structural walk per candidate that parses cleanly.
	for _, c := range candidates {
		if c.Origin == OriginFreeDecl || c.ByteStart < 0 {
			continue
		}
		if c.Origin == OriginObjStm {
			continue // bytes live inside expanded host; references need expansion mapping
		}
		s.walkRefsInObject(c, &refs)
	}

	// Whole-file "N G R" sweep within object regions (covers heuristics/broken
	// headers that fail full parsing), excluding stream payloads.
	streamRegs := s.streamRegions(candidates)
	p := 0
	for p < len(s.data) {
		// find next digit
		for p < len(s.data) && !(s.data[p] >= '0' && s.data[p] <= '9') {
			p++
		}
		if p >= len(s.data) {
			break
		}
		if n, g, _, isRef, np := readRefAt(s.data, p); isRef && !inRegions(streamRegs, p) {
			if o, ok := ownerAt(p); ok {
				// avoid duplicates added by structural walk
				refs = appendRefOnce(refs, Reference{
					FromNum: o.num, FromRev: o.rev, ToNum: n, ToGen: g,
					Context: "raw", FromOffset: p,
				})
			}
			p = np
			continue
		}
		p++
	}
	return refs
}

func appendRefOnce(refs []Reference, r Reference) []Reference {
	for _, e := range refs {
		if e.FromNum == r.FromNum && e.ToNum == r.ToNum && e.ToGen == r.ToGen && e.Context == r.Context {
			return refs
		}
	}
	return append(refs, r)
}

// walkRefsInObject walks dictionary keys and values structurally.
func (s *Scanner) walkRefsInObject(c *Candidate, refs *[]Reference) {
	if c.ActualOff < 0 {
		return
	}
	op, err := parseObject(s.data, c.ActualOff, c.Num, c.Gen, s.resolveIndirectLen)
	if err != nil || op.DictStart < 0 {
		return
	}
	// iterate dictionary key/value pairs
	data := s.data
	pos := op.DictStart + 2
	limit := op.DictEnd - 2
	for pos < limit {
		pos = skipWS(data, pos)
		if pos >= limit || (data[pos] == '>' && pos+1 < limit && data[pos+1] == '>') {
			break
		}
		if data[pos] != '/' {
			pos++
			continue
		}
		ks := pos + 1
		ke := ks
		for ke < limit && isRegular(data[ke]) {
			ke++
		}
		key := string(data[ks:ke])
		pos = skipWS(data, ke)
		if n, g, _, isRef, np := readRefAt(data, pos); isRef {
			*refs = appendRefOnce(*refs, Reference{
				FromNum: c.Num, FromRev: c.Revision, ToNum: n, ToGen: g,
				Context: key, FromOffset: pos,
			})
			pos = np
			continue
		}
		// arrays may contain references
		if pos < limit && data[pos] == '[' {
			depth := 1
			ap := pos + 1
			for ap < limit && depth > 0 {
				ap = skipWS(data, ap)
				if ap >= limit {
					break
				}
				if data[ap] == ']' {
					depth--
					ap++
					continue
				}
				if n, g, _, isRef, npp := readRefAt(data, ap); isRef {
					*refs = appendRefOnce(*refs, Reference{
						FromNum: c.Num, FromRev: c.Revision, ToNum: n, ToGen: g,
						Context: key + "[]", FromOffset: ap,
					})
					ap = npp
					continue
				}
				ap = skipValue(data, ap)
			}
			pos = ap
			continue
		}
		pos = skipValue(data, pos)
	}
}

// parseCandidateValue builds a display value with stream metadata.
func (s *Scanner) parseCandidateValue(c *Candidate) *ParsedValue {
	if c.Origin == OriginFreeDecl {
		return &ParsedValue{Type: "free", Preview: fmt.Sprintf("free; next=%d", c.Offset)}
	}
	if c.Origin == OriginObjStm {
		pv := &ParsedValue{
			Type:       "compressed-object",
			StreamOff:  c.ByteStart,
			StreamLen:  c.ByteEnd - c.ByteStart,
			Expandable: c.Verified,
			Expanded:   c.Verified,
			ExpandNote: c.Evidence,
		}
		return pv
	}
	if c.ActualOff < 0 {
		return &ParsedValue{Type: "unresolved", Preview: "header not located at declared offset"}
	}
	op, err := parseObject(s.data, c.ActualOff, c.Num, c.Gen, s.resolveIndirectLen)
	pv := &ParsedValue{}
	if err != nil || op == nil {
		pv.Type = "unparsed"
		pv.Preview = errStr(err)
		return pv
	}
	if op.DictStart >= 0 {
		pv.Dict = op.Dict
	}
	if op.IsStream {
		pv.Type = "stream"
		pv.StreamOff = op.StreamStart
		pv.StreamLen = op.StreamEnd - op.StreamStart
		pv.Filters = dictNames(op.Dict["Filter"])
		pv.Expandable, pv.ExpandNote = supportedExpansion(pv.Filters)
		pv.Expanded = false
		// attempt verified expansion for preview under the same limit
		if pv.Expandable && op.StreamEnd > op.StreamStart {
			if out, exErr := expandFlate(s.data[op.StreamStart:op.StreamEnd], s.limits.MaxExpandedBytes); exErr == nil {
				pv.Expanded = true
				pv.Preview = previewBytes(out, 400)
			} else {
				pv.ExpandNote = exErr.Error()
			}
		} else {
			pv.Preview = previewBytes(s.data[op.StreamStart:min(op.StreamStart+64, op.StreamEnd)], 64)
		}
		return pv
	}
	pv.Type = "object"
	bodyStart := skipWS(s.data, op.HeaderEnd)
	bodyEnd := op.ObjectEnd
	if eo := bytes.LastIndex(s.data[bodyStart:bodyEnd], []byte("endobj")); eo >= 0 {
		bodyEnd = bodyStart + eo
	}
	pv.Preview = strings.TrimSpace(previewBytes(s.data[bodyStart:bodyEnd], 400))
	if pv.Dict == nil {
		pv.Type = classifyScalar(pv.Preview)
	}
	return pv
}

func classifyScalar(s string) string {
	s = strings.TrimSpace(s)
	switch s {
	case "true", "false":
		return "bool"
	case "null":
		return "null"
	}
	if strings.HasPrefix(s, "[") {
		return "array"
	}
	if strings.HasPrefix(s, "(") {
		return "string"
	}
	if _, err := strconv.ParseFloat(strings.TrimRight(s, "-+"), 64); err == nil && s != "" {
		return "number"
	}
	return "object"
}

// findTrailing identifies appended data after the last xref that no xref points
// into, plus blobs between the current xref's end and the file end that are not
// part of any object.
func (s *Scanner) findTrailing(revs []Revision, candidates []*Candidate) []TrailingBlob {
	var blobs []TrailingBlob
	if len(revs) == 0 {
		// no usable xref: whole file is candidate content, not "trailing"
		return nil
	}
	// Build coverage of bytes referenced by xref structures + resolved objects.
	cov := make([]bool, len(s.data))
	cover := func(a, b int) {
		if a < 0 {
			a = 0
		}
		if b > len(s.data) {
			b = len(s.data)
		}
		for i := a; i < b; i++ {
			cov[i] = true
		}
	}
	for _, r := range revs {
		cover(r.XRefStart, r.SectionEnd)
	}
	// header
	if h := bytes.Index(s.data, []byte("%PDF-")); h >= 0 {
		end := h
		for end < len(s.data) && s.data[end] != '\n' && s.data[end] != '\r' {
			end++
		}
		cover(h, end)
	}

	// trailing region: bytes after the newest revision's final %%EOF
	start := revs[len(revs)-1].SectionEnd
	var curStart = -1
	for i := start; i < len(s.data); i++ {
		if !cov[i] && !isWhitespace(s.data[i]) {
			if curStart < 0 {
				curStart = i
			}
		} else if curStart >= 0 {
			s.appendBlob(&blobs, curStart, i)
			curStart = -1
		}
	}
	if curStart >= 0 {
		s.appendBlob(&blobs, curStart, len(s.data))
	}
	return blobs
}

func (s *Scanner) appendBlob(blobs *[]TrailingBlob, start, end int) {
	// trim surrounding whitespace already handled by caller; include to EOL-ish
	trimEnd := end
	for trimEnd > start && isWhitespace(s.data[trimEnd-1]) {
		trimEnd--
	}
	if trimEnd <= start {
		return
	}
	raw := s.data[start:trimEnd]
	*blobs = append(*blobs, TrailingBlob{
		Start: start, End: trimEnd,
		Hex:   hexPreview(raw, 96),
		ASCII: previewBytes(raw, 96),
	})
}

func hexPreview(b []byte, max int) string {
	if len(b) > max {
		b = b[:max]
	}
	var sb strings.Builder
	for i, c := range b {
		if i > 0 && i%16 == 0 {
			sb.WriteByte('\n')
		}
		sb.WriteString(fmt.Sprintf("%02x ", c))
	}
	return strings.TrimSpace(sb.String())
}
