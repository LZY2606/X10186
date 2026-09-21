package forensic

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
)

// Scanner runs the forensic analysis. It never writes to the input.
type Scanner struct {
	data     []byte
	fileName string
	limits   Limits
	r        *Report

	physical   map[Key]*parsedObject // resolved integer objects, by key
	claimed    map[int64]bool        // physical bytes already claimed by declared evidence
	streamMask []bool                // bytes inside verified streams (heuristic exclusion)
	halted     bool
	haltMsg    string
}

// NewScanner returns a scanner with the given expansion limits.
func NewScanner(limits Limits) *Scanner {
	return &Scanner{limits: limits}
}

// Scan builds the complete forensic report.
func (s *Scanner) Scan(fileName string, data []byte) *Report {
	s.data = data
	s.fileName = fileName
	s.physical = map[Key]*parsedObject{}
	s.claimed = map[int64]bool{}
	s.streamMask = make([]bool, len(data))
	sum := sha256.Sum256(data)
	s.r = &Report{
		FileName:   fileName,
		Size:       int64(len(data)),
		SHA256:     hex.EncodeToString(sum[:]),
		StartXRefs: findStartXRefs(data),
		Limits:     LimitReport{MaxExpand: s.limits.MaxExpand},
	}
	s.r.PDFHeader = ByteRange{0, 0}
	if len(data) >= 5 && string(data[0:5]) == "%PDF-" {
		end := 5
		for end < len(data) && data[end] != '\r' && data[end] != '\n' {
			end++
		}
		s.r.PDFHeader = ByteRange{0, int64(end)}
	} else {
		s.diag("no-pdf-header", "error", "missing %PDF- header", -1, ByteRange{0, 0}, "")
	}

	s.traceChain()
	s.declaredCandidates()
	s.parseDeclaredBodies()
	s.expandObjectStreams()
	s.markStreams()
	s.heuristicPass()
	s.buildGraph()
	s.coverageAndTrailing()
	s.ambiguities()
	s.sortReport()
	s.r.Limits.Halted = s.halted
	s.r.Limits.HaltReason = s.haltMsg
	return s.r
}

func (s *Scanner) diag(code, sev, msg string, rev int, at ByteRange, evidence string) {
	s.r.Diagnostics = append(s.r.Diagnostics, &Diagnostic{
		Code: code, Severity: sev, Message: msg, Revision: rev, At: at, Evidence: evidence,
	})
}

func (s *Scanner) claimRange(b ByteRange) {
	for p := b.Start; p < b.End && p < int64(len(s.data)); p++ {
		s.claimed[p] = true
	}
}

type tracedRevision struct {
	rev     Revision
	entries []XRefEntry
	tr      trailerInfo
	xrefObj *parsedObject // for stream xrefs
}

// traceChain walks startxref -> Prev, detecting broken links and cycles.
// Collected order is newest->oldest; it is reversed at the end.
func (s *Scanner) traceChain() {
	if len(s.r.StartXRefs) == 0 {
		s.diag("no-startxref", "error", "no startxref found", -1, ByteRange{int64(len(s.data)), int64(len(s.data))}, "")
		s.r.Revisions = nil
		return
	}
	visited := map[int64]bool{}
	var traced []tracedRevision
	cur := s.r.StartXRefs[len(s.r.StartXRefs)-1]
	startOrder := 0
	for cur >= 0 {
		if visited[cur] {
			s.diag("previous-cycle", "error",
				fmt.Sprintf("Prev chain loops back to xref offset %d", cur),
				len(traced), ByteRange{cur, cur + 1}, fmt.Sprintf("offset %d revisited", cur))
			break
		}
		visited[cur] = true
		if cur >= int64(len(s.data)) {
			s.diag("xref-out-of-bounds", "error",
				fmt.Sprintf("xref offset %d is beyond file size %d", cur, len(s.data)),
				len(traced), ByteRange{cur, cur}, "")
			break
		}
		p := int(cur)
		isStream := false
		if _, ok := matchHeaderAt(s.data, p); ok {
			isStream = true
		}
		var tr trailerInfo
		var entries []XRefEntry
		var eofStart, eofEnd int64 = -1, -1
		var xrefObj *parsedObject
		note := ""
		if isStream {
			es, t, po, err := parseStreamXRef(s.data, cur, s.limits, s.resolveInt)
			entries, tr, xrefObj = es, t, po
			if err != nil {
				s.diag("xref-stream-parse", "error", err.Error(), len(traced),
					ByteRange{Start: cur, End: cur + 1}, "")
				if le, ok := isLimitErr(err); ok {
					s.halted = true
					s.haltMsg = le.Msg
					note = "halted: " + le.Msg
				}
				break
			}
			if po != nil {
				eofStart, eofEnd = findEOFAfter(s.data, po.EndPos)
				// record the xref stream object body physically
				s.physical[Key{po.Num, po.Gen}] = po
			}
		} else {
			es, t, esStart, esEnd, err := parseTableXRef(s.data, cur)
			entries, tr, eofStart, eofEnd = es, t, esStart, esEnd
			if err != nil {
				s.diag("xref-table-parse", "error", err.Error(), len(traced),
					ByteRange{Start: cur, End: cur + 1}, s.snippet(cur, 16))
				break
			}
		}
		// claim entry declaration bytes for table xrefs
		if !isStream {
			s.claimRange(ByteRange{Start: cur, End: tr.At.End})
		} else if xrefObj != nil {
			s.claimRange(ByteRange{Start: xrefObj.Header.Start, End: int64(xrefObj.EndPos)})
		}

		rev := Revision{
			Kind: kindOf(isStream), XRefOffset: cur,
			PrevOffset: tr.Prev, PrevPresent: tr.HasPrev, Size: tr.Size, Root: tr.Root,
			Entries: entries, TrailerDict: tr.At, EOFStart: eofStart, EOFEnd: eofEnd, ChainNote: note,
		}
		traced = append(traced, tracedRevision{rev: rev, entries: entries, tr: tr, xrefObj: xrefObj})

		if len(s.r.StartXRefs) > 1 {
			_ = startOrder
		}
		if !tr.HasPrev {
			break
		}
		if tr.Prev == cur {
			s.diag("previous-cycle", "error",
				fmt.Sprintf("Prev points to itself at %d", cur), len(traced), ByteRange{cur, cur + 1}, "")
			break
		}
		if tr.Prev < 0 || tr.Prev >= int64(len(s.data)) {
			s.diag("previous-broken", "error",
				fmt.Sprintf("Prev offset %d is outside the file", tr.Prev), len(traced),
				ByteRange{tr.Prev, tr.Prev}, "")
			break
		}
		cur = tr.Prev
	}

	// Additional startxref markers pointing at bytes outside the chain are evidence
	// of dangling/orphan revisions.
	chainOff := map[int64]bool{}
	for _, t := range traced {
		chainOff[t.rev.XRefOffset] = true
	}
	for _, sx := range s.r.StartXRefs {
		if !chainOff[sx] {
			s.diag("orphan-startxref", "warning",
				fmt.Sprintf("startxref names %d, which is not on the followed chain", sx),
				-1, ByteRange{sx, sx + 1}, "")
		}
	}

	// Reverse so index 0 is the oldest revision.
	for i, j := 0, len(traced)-1; i < j; i, j = i+1, j-1 {
		traced[i], traced[j] = traced[j], traced[i]
	}
	for i, t := range traced {
		t.rev.Index = i
		s.r.Revisions = append(s.r.Revisions, t.rev)
	}
	// Diagnostics recorded newest-first used len(traced) as pseudo index; remap
	// after reversal by fixing offsets created with stable rev pointer is not
	// possible, so they were attached to -1/0 conservatively. Re-index explicit ones:
}

func kindOf(stream bool) string {
	if stream {
		return "stream"
	}
	return "table"
}

func (s *Scanner) snippet(off int64, n int) string {
	p := int(off)
	if p < 0 || p >= len(s.data) {
		return ""
	}
	e := p + n
	if e > len(s.data) {
		e = len(s.data)
	}
	return safeASCII(s.data[p:e])
}

func safeASCII(b []byte) string {
	out := make([]byte, 0, len(b))
	for _, c := range b {
		if c >= 32 && c < 127 {
			out = append(out, c)
		} else {
			out = append(out, '.')
		}
	}
	return string(out)
}

func (s *Scanner) resolveInt(k Key) (int64, bool) {
	po, ok := s.physical[k]
	if !ok {
		return 0, false
	}
	if po.Node != nil && po.Node.Type == "int" {
		return po.Node.Int, true
	}
	return 0, false
}

func candidateID(origin string, rev, num, gen int, extra int) string {
	tag := "d"
	if origin == "heuristic" {
		tag = "h"
	}
	return fmt.Sprintf("%s-r%d-%d-%d-%d", tag, rev, num, gen, extra)
}

// declaredCandidates creates one candidate per type-1/type-2 xref entry,
// verifying physical headers for type-1.
func (s *Scanner) declaredCandidates() {
	for ri := range s.r.Revisions {
		rev := &s.r.Revisions[ri]
		for _, e := range rev.Entries {
			switch e.Type {
			case 0:
				s.r.Frees = append(s.r.Frees, FreeEntry{
					Num: e.Num, NextFree: int(e.Offset), Generation: e.Gen, Revision: rev.Index,
				})
			case 1:
				c := &Candidate{
					ID:  candidateID("declared", rev.Index, e.Num, e.Gen, len(s.r.Candidates)),
					Num: e.Num, Gen: e.Gen, Revision: rev.Index,
					Origin: "declared", Source: revKind(rev),
					Reason:    fmt.Sprintf("xref %s entry at %d points to byte %d", rev.Kind, e.Declared.Start, e.Offset),
					ByteRange: ByteRange{Start: e.Offset},
				}
				if e.Offset < 0 || e.Offset >= int64(len(s.data)) {
					c.Note = "declared offset is outside the file"
					c.ByteRange.End = e.Offset
					s.diag("declared-offset-whitespace", "error",
						fmt.Sprintf("object %d %d: declared offset %d is outside file", e.Num, e.Gen, e.Offset),
						rev.Index, ByteRange{e.Offset, e.Offset + 1}, "")
					s.r.Candidates = append(s.r.Candidates, c)
					continue
				}
				hm, ok := matchHeaderAt(s.data, int(e.Offset))
				if !ok || hm.Num != e.Num || hm.Gen != e.Gen {
					c.HeaderOK = false
					c.Note = "declared offset does not begin with matching object header"
					c.ByteRange.End = e.Offset
					s.diag("declared-offset-bad", "error",
						fmt.Sprintf("object %d %d: byte %d does not contain its header", e.Num, e.Gen, e.Offset),
						rev.Index, ByteRange{e.Offset, e.Offset + int64(min(16, len(s.data)-int(e.Offset)))},
						s.snippet(e.Offset, 16))
					s.r.Candidates = append(s.r.Candidates, c)
					continue
				}
				c.HeaderOK = true
				s.r.Candidates = append(s.r.Candidates, c)
			case 2:
				c := &Candidate{
					ID:  candidateID("declared", rev.Index, e.Num, e.Gen, len(s.r.Candidates)),
					Num: e.Num, Gen: e.Gen, Revision: rev.Index,
					Origin: "declared", Source: "object-stream",
					Compressed: true,
					Container:  &Key{Num: e.Stream},
					ObjIndex:   e.Index,
					Reason:     fmt.Sprintf("xref stream type-2 row places object %d at index %d of ObjStm %d", e.Num, e.Index, e.Stream),
				}
				s.r.Candidates = append(s.r.Candidates, c)
			}
		}
	}
}

func revKind(r *Revision) string {
	if r.Kind == "stream" {
		return "xref-stream"
	}
	return "table"
}

// parseDeclaredBodies physically parses every valid type-1 candidate (oldest first),
// so newer versions win in s.physical.
func (s *Scanner) parseDeclaredBodies() {
	order := make([]*Candidate, 0, len(s.r.Candidates))
	for _, c := range s.r.Candidates {
		if c.Origin == "declared" && !c.Compressed && c.HeaderOK {
			order = append(order, c)
		}
	}
	sort.SliceStable(order, func(i, j int) bool {
		if order[i].Revision != order[j].Revision {
			return order[i].Revision < order[j].Revision
		}
		if order[i].Num != order[j].Num {
			return order[i].Num < order[j].Num
		}
		return order[i].ByteRange.Start < order[j].ByteRange.Start
	})
	for _, c := range order {
		hm, ok := matchHeaderAt(s.data, int(c.ByteRange.Start))
		if !ok {
			continue
		}
		po, err := parseIndirectObject(s.data, hm, s.resolveInt)
		if po == nil {
			s.diag("object-parse", "warning",
				fmt.Sprintf("object %d %d: %v", c.Num, c.Gen, err),
				c.Revision, ByteRange{c.ByteRange.Start, c.ByteRange.Start + 1}, "")
			continue
		}
		c.ByteRange.End = int64(po.EndPos)
		c.Node = po.Node
		c.Stream = po.Stream
		s.physical[Key{c.Num, c.Gen}] = po
		if c.ByteRange.End > c.ByteRange.Start {
			s.claimRange(c.ByteRange)
		}
		if po.Stream != nil && po.Stream.BoundaryOK && len(po.Stream.Filters) > 0 && !s.halted {
			if _, xerr := verifiedExpand(s.data, po.Node, po.Stream, s.limits); xerr != nil {
				if le, ok := isLimitErr(xerr); ok {
					s.halted = true
					s.haltMsg = le.Msg
					s.diag("stream-expand-limit", "error",
						fmt.Sprintf("object %d %d expansion halted: %s", c.Num, c.Gen, le.Msg),
						c.Revision, c.ByteRange, "")
				}
			}
		}
		if err != nil {
			s.diag("object-body-incomplete", "warning",
				fmt.Sprintf("object %d %d parsed with caveat: %v", c.Num, c.Gen, err),
				c.Revision, c.ByteRange, "")
		}
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
