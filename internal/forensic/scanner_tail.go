package forensic

import (
	"bytes"
	"fmt"
	"sort"
)

// markStreams flags every byte lying between verified stream/endstream
// markers, so the heuristic header scan cannot mistake stream payload bytes
// for object headers.
func (s *Scanner) markStreams() {
	for _, po := range s.physical {
		if po.Stream == nil || !po.Stream.BoundaryOK {
			continue
		}
		st := int(po.Stream.KeywordStart)
		en := findKeyword(s.data, int(po.Stream.DataEnd), "endstream")
		if en < 0 {
			en = len(s.data)
		}
		en += len("endstream")
		if en > len(s.data) {
			en = len(s.data)
		}
		for p := st; p < en; p++ {
			if p >= 0 {
				s.streamMask[p] = true
			}
		}
	}
}

// heuristicPass scans unclaimed bytes for "N G obj" headers and records each
// as a clearly-labelled heuristic candidate with evidence.
func (s *Scanner) heuristicPass() {
	n := len(s.data)
	for p := 0; p < n; {
		if s.streamMask[p] || s.claimed[int64(p)] {
			p++
			continue
		}
		hm, ok := matchHeaderAt(s.data, p)
		if !ok {
			p++
			continue
		}
		// An object body already declared at exactly this offset is not a new find.
		already := false
		for _, c := range s.r.Candidates {
			if !c.Compressed && c.ByteRange.Start == int64(p) {
				already = true
				break
			}
		}
		nearest := s.nearestRevision(int64(p))
		if !already {
			c := &Candidate{
				ID:  candidateID("heuristic", nearest, hm.Num, hm.Gen, len(s.r.Candidates)),
				Num: hm.Num, Gen: hm.Gen, Revision: nearest,
				Origin: "heuristic", Source: "header-scan",
				Reason:    fmt.Sprintf("carved: byte pattern %d %d obj at offset %d, outside declared xref coverage", hm.Num, hm.Gen, p),
				ByteRange: ByteRange{Start: int64(p)},
				HeaderOK:  true,
			}
			po, err := parseIndirectObject(s.data, hm, s.resolveInt)
			if po != nil {
				c.ByteRange.End = int64(po.EndPos)
				c.Node = po.Node
				c.Stream = po.Stream
				if err != nil {
					c.Note = "carved body incomplete: " + err.Error()
				}
			} else {
				c.ByteRange.End = int64(hm.End)
				c.Note = "header carved but body could not be parsed: " + err.Error()
			}
			s.r.Candidates = append(s.r.Candidates, c)
		}
		// Skip past the entire match even if a declared body already owns it.
		skip := hm.End
		if !already {
			if po, _ := parseIndirectObject(s.data, hm, s.resolveInt); po != nil && po.EndPos > skip {
				skip = po.EndPos
			}
		} else {
			if po := s.physicalAt(int64(p)); po != nil && po.EndPos > skip {
				skip = po.EndPos
			}
		}
		if skip <= p {
			skip = p + 1
		}
		p = skip
	}
}

func (s *Scanner) physicalAt(off int64) *parsedObject {
	for _, po := range s.physical {
		if po.Header.Start == off {
			return po
		}
	}
	return nil
}

// nearestRevision assigns a carved object to the last revision whose xref
// starts at or before its offset (i.e. the revision region it lies in).
func (s *Scanner) nearestRevision(off int64) int {
	rev := 0
	found := false
	for i := range s.r.Revisions {
		if s.r.Revisions[i].XRefOffset <= off {
			rev = s.r.Revisions[i].Index
			found = true
		}
	}
	if !found && len(s.r.Revisions) > 0 {
		return 0
	}
	return rev
}

// buildGraph constructs reference edges from parsed values plus container edges
// for type-2 compressed members, then computes ReferencedBy back-references.
func (s *Scanner) buildGraph() {
	var edges []RefEdge
	active := s.latestPerKey()
	for k, c := range active {
		if c.Node != nil {
			collectRefs(c.Node, &edges, k, c.ByteRange)
		}
		if c.Compressed && c.Container != nil {
			edges = append(edges, RefEdge{
				From: *c.Container, To: k,
				Where: c.ByteRange, Kind: "type2-container",
			})
		}
	}
	sort.SliceStable(edges, func(i, j int) bool {
		if edges[i].From != edges[j].From {
			return keyLess(edges[i].From, edges[j].From)
		}
		return keyLess(edges[i].To, edges[j].To)
	})
	s.r.Edges = edges
}

// latestPerKey returns the newest declared/heuristic candidate per key for graph
// display; ambiguous decisions are applied later in the browser/store layer.
func (s *Scanner) latestPerKey() map[Key]*Candidate {
	out := map[Key]*Candidate{}
	for _, c := range s.r.Candidates {
		if !c.HeaderOK {
			continue
		}
		k := Key{c.Num, c.Gen}
		if ex, ok := out[k]; !ok || c.Revision > ex.Revision ||
			(c.Revision == ex.Revision && rankOrigin(c.Origin) > rankOrigin(ex.Origin)) {
			out[k] = c
		}
	}
	return out
}

func rankOrigin(o string) int {
	if o == "declared" {
		return 2
	}
	return 1
}

func keyLess(a, b Key) bool {
	if a.Num != b.Num {
		return a.Num < b.Num
	}
	return a.Gen < b.Gen
}

// coverageAndTrailing merges declared/verified ranges and reports the bytes
// after the final %%EOF plus internal unclaimed gaps.
func (s *Scanner) coverageAndTrailing() {
	var ranges []ByteRange
	claim := func(b ByteRange) {
		if b.Start < 0 || b.End < b.Start {
			return
		}
		ranges = append(ranges, b)
	}
	claim(s.r.PDFHeader)
	for _, c := range s.r.Candidates {
		if c.Compressed {
			if c.ContainerRange != nil {
				claim(*c.ContainerRange)
			}
			continue
		}
		if c.ByteRange.End > c.ByteRange.Start {
			claim(c.ByteRange)
		}
	}
	for i := range s.r.Revisions {
		r := &s.r.Revisions[i]
		if r.Kind == "table" {
			claim(ByteRange{Start: r.XRefOffset, End: r.TrailerDict.End})
		}
		if r.EOFStart >= 0 {
			claim(ByteRange{Start: r.EOFStart, End: r.EOFEnd})
		}
	}
	for _, sx := range s.r.StartXRefs {
		claim(ByteRange{Start: sx, End: sx + 1})
	}
	sort.Slice(ranges, func(i, j int) bool {
		if ranges[i].Start != ranges[j].Start {
			return ranges[i].Start < ranges[j].Start
		}
		return ranges[i].End < ranges[j].End
	})
	var merged []ByteRange
	for _, b := range ranges {
		if len(merged) == 0 || b.Start > merged[len(merged)-1].End {
			merged = append(merged, b)
		} else if b.End > merged[len(merged)-1].End {
			merged[len(merged)-1].End = b.End
		}
	}
	s.r.Covered = merged

	var lastEOF int64 = -1
	for i := range s.r.Revisions {
		if s.r.Revisions[i].EOFEnd > lastEOF {
			lastEOF = s.r.Revisions[i].EOFEnd
		}
	}
	if lastEOF < 0 {
		lastEOF = 0
	}
	// Trim trailing whitespace from "after EOF" region.
	cut := int64(len(s.data))
	for cut > lastEOF {
		b := s.data[cut-1]
		if !isWhitespace(b) {
			break
		}
		cut--
	}
	regionStart := cut
	for regionStart > lastEOF {
		b := s.data[regionStart-1]
		if !isWhitespace(b) {
			break
		}
		regionStart--
	}
	if regionStart > lastEOF {
		s.r.Trailing = ByteRange{Start: lastEOF, End: regionStart}
		s.diag("trailing-data", "warning",
			fmt.Sprintf("%d bytes appended after last %%%%EOF and not referenced by xref", regionStart-lastEOF),
			len(s.r.Revisions)-1, s.r.Trailing, s.snippet(lastEOF, 32))
	}
	// Internal gaps among merged ranges.
	var cursor int64
	for _, m := range merged {
		if m.Start > cursor {
			s.r.Gaps = append(s.r.Gaps, ByteRange{cursor, m.Start})
		}
		if m.End > cursor {
			cursor = m.End
		}
	}
}

// ambiguities groups competing candidates for the same (revision,key). A bad
// declared pointer with a carved recovery candidate is also flagged; the system
// never picks the winner by highest/lowest offset automatically.
func (s *Scanner) ambiguities() {
	type slot struct {
		key  Key
		rev  int
		ids  []string
		decl string
		note string
	}
	slots := map[string]*slot{}
	var order []string
	for _, c := range s.r.Candidates {
		if c.Compressed {
			continue
		}
		id := fmt.Sprintf("%d:%d:%d", c.Revision, c.Num, c.Gen)
		sl, ok := slots[id]
		if !ok {
			sl = &slot{key: Key{c.Num, c.Gen}, rev: c.Revision}
			slots[id] = sl
			order = append(order, id)
		}
		sl.ids = append(sl.ids, c.ID)
		if c.Origin == "declared" && c.HeaderOK && sl.decl == "" {
			sl.decl = c.ID
		}
	}
	// Bad declared pointer + heuristic recovery in the same region joins the slot.
	sort.Strings(order)
	for _, id := range order {
		sl := slots[id]
		// dedupe ids
		seen := map[string]bool{}
		uniq := sl.ids[:0]
		for _, x := range sl.ids {
			if !seen[x] {
				seen[x] = true
				uniq = append(uniq, x)
			}
		}
		sl.ids = uniq
		competing := len(sl.ids) > 1
		badDecl := false
		for _, c := range s.r.Candidates {
			if c.Revision == sl.rev && c.Num == sl.key.Num && c.Gen == sl.key.Gen &&
				c.Origin == "declared" && !c.HeaderOK {
				badDecl = true
			}
		}
		if !competing && !badDecl {
			continue
		}
		note := "multiple candidates claim this object version; no winner is chosen by offset"
		def := sl.decl
		if def == "" {
			def = sl.ids[0]
			note = "no verified declared body; first candidate only shown as suggestion"
		}
		s.r.Ambiguities = append(s.r.Ambiguities, Ambiguity{
			Key: sl.key, Revision: sl.rev, IDs: sl.ids, DefaultID: def, Note: note,
		})
	}
	sort.SliceStable(s.r.Ambiguities, func(i, j int) bool {
		if s.r.Ambiguities[i].Revision != s.r.Ambiguities[j].Revision {
			return s.r.Ambiguities[i].Revision < s.r.Ambiguities[j].Revision
		}
		return keyLess(s.r.Ambiguities[i].Key, s.r.Ambiguities[j].Key)
	})
}

func (s *Scanner) sortReport() {
	sort.SliceStable(s.r.Candidates, func(i, j int) bool {
		a, b := s.r.Candidates[i], s.r.Candidates[j]
		if a.Revision != b.Revision {
			return a.Revision < b.Revision
		}
		if a.Num != b.Num {
			return a.Num < b.Num
		}
		if a.Gen != b.Gen {
			return a.Gen < b.Gen
		}
		if a.Origin != b.Origin {
			return a.Origin == "declared"
		}
		return a.ByteRange.Start < b.ByteRange.Start
	})
	sort.SliceStable(s.r.Frees, func(i, j int) bool {
		if s.r.Frees[i].Revision != s.r.Frees[j].Revision {
			return s.r.Frees[i].Revision < s.r.Frees[j].Revision
		}
		return s.r.Frees[i].Num < s.r.Frees[j].Num
	})
	// Stable diagnostic order as produced, but sort revisions are final.
	_ = bytes.Index
}
