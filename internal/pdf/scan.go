package pdf

import (
	"bytes"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"sort"
	"time"
)

// Scanner extracts the full incremental-revision version graph from raw bytes.
// It never writes to or "repairs" the input.
type Scanner struct {
	data   []byte
	limits Limits
	diag   []Diagnostic
}

func NewScanner(data []byte, limits Limits) *Scanner {
	return &Scanner{data: data, limits: limits}
}

func (s *Scanner) addDiag(code, severity string, rev, off int, msg, evidence string) {
	s.diag = append(s.diag, Diagnostic{
		Code: code, Severity: severity, Revision: rev, Offset: off,
		Message: msg, Evidence: evidence,
	})
}

// resolveIndirectLen reads object "num gen" and returns its integer value.
func (s *Scanner) resolveIndirectLen(num, gen int) int {
	// find "num gen obj" anywhere (used only for xref-stream length objects)
	needle := []byte(fmt.Sprintf("%d %d obj", num, gen))
	idx := bytes.Index(s.data, needle)
	if idx < 0 {
		return -1
	}
	p := skipWS(s.data, idx+len(needle))
	tok, _ := readToken(s.data, p)
	if n, ok := parseInt([]byte(tok)); ok {
		return n
	}
	return -1
}

// Scan performs the complete analysis.
func (s *Scanner) Scan() *ScanResult {
	res := &ScanResult{
		Size:      len(s.data),
		Limits:    s.limits,
		ScannedAt: time.Now().UTC().Format(time.RFC3339Nano),
	}

	// ---------- Stage 1: walk the declared xref previous-chain ----------
	markerOff, startOff, found := findStartXRef(s.data)
	if !found {
		s.addDiag("STARTXREF_MISSING", "error", -1, len(s.data)-1,
			"no startxref found in file tail", "tail scan of last 4096 bytes found no 'startxref' keyword")
	}
	if found && (startOff < 0 || startOff >= len(s.data)) {
		s.addDiag("STARTXREF_OOB", "error", -1, startOff,
			fmt.Sprintf("startxref offset %d is outside the file", startOff),
			fmt.Sprintf("file size %d bytes; startxref marker at %d", len(s.data), markerOff))
	}

	type chainNode struct {
		rev *Revision
		err error
	}
	var chain []*Revision
	visited := map[int]bool{}
	cur := startOff
	chainOrder := 0
	for found && cur >= 0 && cur < len(s.data) {
		if visited[cur] {
			s.addDiag("PREV_CYCLE", "error", chainOrder, cur,
				fmt.Sprintf("previous-chain cycle detected at xref offset %d", cur),
				"xref offsets already visited: "+visitedList(visited))
			break
		}
		visited[cur] = true
		rev, err := parseXRefAt(s.data, cur, s.resolveIndirectLen, s.limits)
		if err != nil {
			s.addDiag("XREF_PARSE", "error", chainOrder, cur,
				"failed to parse xref at declared offset", err.Error())
			chain = append(chain, nil)
			break
		}
		chain = append(chain, rev)
		chainOrder++
		if !rev.HasPrev {
			break
		}
		prev := rev.Prev
		if prev < 0 || prev >= len(s.data) {
			s.addDiag("PREV_BROKEN", "error", chainOrder-1, prev,
				fmt.Sprintf("Prev points outside file: %d", prev),
				fmt.Sprintf("file size %d", len(s.data)))
			break
		}
		if prev >= cur {
			// Prev must point backwards; treat as suspicious but still follow
			// (cycle guard above stops a loop).
			s.addDiag("PREV_NOT_BACKWARD", "warning", chainOrder-1, prev,
				fmt.Sprintf("Prev %d is not before current xref %d", prev, cur),
				"PDF requires /Prev to reference an earlier xref")
		}
		cur = prev
	}

	// chain is newest -> oldest; reverse for revision indexing (oldest = 0)
	revs := make([]Revision, 0, len(chain))
	for i := len(chain) - 1; i >= 0; i-- {
		if chain[i] != nil {
			revs = append(revs, *chain[i])
		}
	}
	for i := range revs {
		revs[i].Index = i
	}

	// Detect mixed table/stream adjacent revisions.
	for i := 1; i < len(revs); i++ {
		if revs[i].XRefKind != revs[i-1].XRefKind {
			s.addDiag("MIXED_XREF", "info", i, revs[i].XRefOffset,
				"revision mixes xref table and xref stream with previous revision",
				fmt.Sprintf("revision %d uses %s while revision %d uses %s",
					i-1, revs[i-1].XRefKind, i, revs[i].XRefKind))
		}
	}

	// Trailer tail ends (startxref offset + %%EOF) and segment byte ranges.
	s.computeSectionEnds(&revs)
	s.computeSegments(&revs)
	res.Revisions = revs

	// ---------- Stage 2: build candidates from declared entries ----------
	candByKey := map[string]*Candidate{}
	var candidates []*Candidate
	addCand := func(c *Candidate) *Candidate {
		id := candID(c)
		c.ID = id
		if existing, ok := candByKey[id]; ok {
			return existing
		}
		candByKey[id] = c
		candidates = append(candidates, c)
		return c
	}

	for ri := range revs {
		rev := &revs[ri]
		seenInRev := map[string]bool{}
		for ei := range rev.Entries {
			e := rev.Entries[ei]
			key := fmt.Sprintf("%d:%d:%d", ri, e.Num, e.Gen)
			if seenInRev[key] {
				s.addDiag("DUP_ENTRY_IN_XREF", "warning", ri, rev.XRefOffset,
					fmt.Sprintf("object %d %d appears more than once in this xref", e.Num, e.Gen),
					fmt.Sprintf("xref kind=%s offset=%d", rev.XRefKind, rev.XRefOffset))
			}
			seenInRev[key] = true
			switch e.Kind {
			case KindFree:
				c := &Candidate{
					Num: e.Num, Gen: e.Gen, Revision: ri, Origin: OriginFreeDecl,
					Offset: e.FreeNext, ActualOff: -1, ByteStart: -1, ByteEnd: -1,
					Status: StatusPending, Verified: false,
					Evidence: fmt.Sprintf("free entry in revision %d; /Next=%d", ri, e.FreeNext),
				}
				addCand(c)
			case KindUncompressed:
				c := s.buildUncompressed(e.Num, e.Gen, ri, e.Offset)
				addCand(c)
			case KindCompressed:
				// resolved after all host objects are known
				s.addCompressedPlaceholder(e, ri, addCand)
			}
		}
	}

	// ---------- Stage 3: expand verified object streams ----------
	s.resolveObjectStreams(&revs, candidates, candByKey, addCand)

	// ---------- Stage 4: heuristic sweep of raw bytes ----------
	s.heuristicSweep(res.Revisions, addCand, candByKey)

	// ---------- Stage 5: version competition ----------
	s.markContested(candidates)

	// ---------- Stage 6: parse values + reference graph ----------
	res.References = s.buildReferences(candidates, &revs)
	for _, c := range candidates {
		c.Value = s.parseCandidateValue(c)
	}
	res.Candidates = candidates

	// ---------- Stage 7: trailing, unreferenced data ----------
	res.TrailingData = s.findTrailing(revs, candidates)

	// dedupe diagnostics
	res.Diagnostics = s.dedupDiag()
	return res
}

func candID(c *Candidate) string {
	h := sha1.Sum([]byte(fmt.Sprintf("%s|%d|%d|%d|%d|%d", c.Origin, c.Num, c.Gen, c.Revision, c.Offset, c.ActualOff)))
	return "c_" + hex.EncodeToString(h[:10])
}

func visitedList(m map[int]bool) string {
	var ks []int
	for k := range m {
		ks = append(ks, k)
	}
	sort.Ints(ks)
	s := ""
	for i, k := range ks {
		if i > 0 {
			s += ", "
		}
		s += fmt.Sprintf("%d", k)
	}
	return s
}

func (s *Scanner) computeSegments(revs *[]Revision) {
	// A revision r contains objects appended after the previous revision's xref
	// and up to r's own xref offset. Segments are defined by xref offsets.
	for i := range *revs {
		rev := &(*revs)[i]
		rev.SegmentStart = 0
		rev.SegmentEnd = rev.XRefOffset
		if i > 0 {
			rev.SegmentStart = (*revs)[i-1].XRefOffset
		}
	}
}

func (s *Scanner) buildUncompressed(num, gen, revIdx, declaredOff int) *Candidate {
	c := &Candidate{
		Num: num, Gen: gen, Revision: revIdx, Origin: OriginDeclared,
		Offset: declaredOff, ActualOff: -1, ByteStart: -1, ByteEnd: -1,
		Status:   StatusPending,
		Evidence: fmt.Sprintf("xref entry type 1 in revision %d declares offset %d", revIdx, declaredOff),
	}
	if declaredOff < 0 || declaredOff >= len(s.data) {
		c.HeaderOK = false
		s.addDiag("OFFSET_OOB", "error", revIdx, declaredOff,
			fmt.Sprintf("object %d %d declared at offset %d outside file", num, gen, declaredOff),
			fmt.Sprintf("file size %d", len(s.data)))
		return c
	}
	hdr, ok := findObjectHeader(s.data, declaredOff, num, gen)
	if !ok {
		c.HeaderOK = false
		s.addDiag("OFFSET_BLANK_OR_MISMATCH", "error", revIdx, declaredOff,
			fmt.Sprintf("object %d %d: offset %d does not point at its object header", num, gen, declaredOff),
			fmt.Sprintf("bytes at offset: %q", snippet(s.data, declaredOff, 24)))
		return c
	}
	c.HeaderOK = true
	c.ActualOff = hdr
	op, perr := parseObject(s.data, hdr, num, gen, s.resolveIndirectLen)
	if perr != nil {
		s.addDiag("OBJECT_PARSE", "warning", revIdx, hdr,
			fmt.Sprintf("object %d %d parsed with problems: %s", num, gen, perr.Error()),
			fmt.Sprintf("header at %d", hdr))
		// still capture the raw range of what the header covers
		c.ByteStart = hdr
		if op != nil && op.ObjectEnd > hdr {
			c.ByteEnd = op.ObjectEnd
		} else {
			c.ByteEnd = min(hdr+len(fmt.Sprintf("%d %d obj", num, gen)), len(s.data))
		}
		return c
	}
	c.ByteStart = op.HeaderStart
	c.ByteEnd = op.ObjectEnd
	c.Verified = !op.IsStream || streamVerified(op)
	if op.IsStream && !c.Verified {
		s.addDiag("STREAM_UNVERIFIED", "warning", revIdx, hdr,
			fmt.Sprintf("object %d %d stream boundary could not be verified", num, gen),
			"length/filter/boundary check failed; stream not expanded")
	}
	return c
}

func streamVerified(op *ObjectParts) bool {
	return op.StreamStart >= 0 && op.StreamEnd > op.StreamStart
}

func snippet(data []byte, off, n int) string {
	if off < 0 || off >= len(data) {
		return "<out of range>"
	}
	end := off + n
	if end > len(data) {
		end = len(data)
	}
	b := data[off:end]
	var out []byte
	for _, c := range b {
		if c == '\r' || c == '\n' || c == '\t' || (c >= 32 && c < 127) {
			out = append(out, c)
		} else {
			out = append(out, '.')
		}
	}
	return string(out)
}

func (s *Scanner) addCompressedPlaceholder(e XRefEntry, revIdx int, add func(*Candidate) *Candidate) {
	c := &Candidate{
		Num: e.Num, Gen: e.Gen, Revision: revIdx, Origin: OriginObjStm,
		StreamObj: e.Stream, IndexInStm: e.Offset,
		ActualOff: -1, ByteStart: -1, ByteEnd: -1,
		Status:   StatusPending,
		Verified: false,
		Evidence: fmt.Sprintf("xref type 2 in revision %d: inside object %d at index %d", revIdx, e.Stream, e.Offset),
	}
	add(c)
}

func (s *Scanner) dedupDiag() []Diagnostic {
	seen := map[string]bool{}
	var out []Diagnostic
	for _, d := range s.diag {
		k := fmt.Sprintf("%s|%d|%d|%s", d.Code, d.Revision, d.Offset, d.Message)
		if !seen[k] {
			seen[k] = true
			out = append(out, d)
		}
	}
	return out
}

// computeSectionEnds finds the "startxref" + final "%%EOF" after each parsed
// xref section so trailing-data detection only flags bytes after the newest
// revision's final %%EOF.
func (s *Scanner) computeSectionEnds(revs *[]Revision) {
	for i := range *revs {
		r := &(*revs)[i]
		pos := r.XRefEnd
		sx := indexFrom(s.data, pos, []byte("startxref"))
		if sx < 0 {
			r.SectionEnd = r.XRefEnd
			continue
		}
		// find the %%EOF that follows this startxref (before any later xref)
		limit := len(s.data)
		if i+1 < len(*revs) {
			// next revision in slice ordering is the *newer* one, located later
			limit = (*revs)[i+1].XRefStart
		}
		eof := -1
		searchFrom := sx
		for {
			j := indexFrom(s.data, searchFrom, []byte("%%EOF"))
			if j < 0 || j > limit {
				break
			}
			eof = j + len("%%EOF")
			searchFrom = j + 1
		}
		if eof > 0 {
			r.SectionEnd = eof
		} else {
			r.SectionEnd = r.XRefEnd
		}
	}
}
