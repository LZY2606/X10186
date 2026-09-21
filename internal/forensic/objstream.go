package forensic

import (
	"fmt"
	"sort"
)

// type2Need maps a type-2 candidate to its container declaration.
type type2Need struct {
	cand      *Candidate
	container Key
	idx       int
}

// expandObjectStreams verifies length/filter/boundaries, expands only Flate
// streams, parses N/N pairs and member objects.
func (s *Scanner) expandObjectStreams() {
	needs := map[Key][]type2Need{}
	for _, c := range s.r.Candidates {
		if c.Compressed {
			needs[*c.Container] = append(needs[*c.Container], type2Need{cand: c, container: *c.Container, idx: c.ObjIndex})
		}
	}
	if len(needs) == 0 {
		return
	}
	// Containers are regular type-1 objects parsed in parseDeclaredBodies order,
	// but that runs after us; parse the needed containers now, oldest first.
	containerRevs := s.latestContainerCandidates(needs)
	for _, ck := range containerRevs {
		po, ok := s.physical[ck]
		if !ok {
			decl := s.declaredAt(ck)
			if decl == nil || !decl.HeaderOK {
				s.complainMissingContainer(needs[ck], "no declared object body for container")
				continue
			}
			hm, hok := matchHeaderAt(s.data, int(decl.ByteRange.Start))
			if !hok {
				s.complainMissingContainer(needs[ck], "container header unverifiable")
				continue
			}
			p, err := parseIndirectObject(s.data, hm, s.resolveInt)
			if p == nil {
				s.complainMissingContainer(needs[ck], "container parse failed: "+err.Error())
				continue
			}
			po = p
			s.physical[ck] = p
			decl.ByteRange.End = int64(p.EndPos)
			decl.Node = p.Node
			decl.Stream = p.Stream
		}
		if po.Stream == nil {
			s.complainMissingContainer(needs[ck], "declared container is not a stream")
			continue
		}
		if po.Node == nil || po.Node.Type != "dict" {
			s.complainMissingContainer(needs[ck], "container has no dictionary")
			continue
		}
		if t := po.Node.Dict("Type"); t == nil || t.Type != "name" || t.Name != "ObjStm" {
			s.complainMissingContainer(needs[ck], "container /Type is not /ObjStm")
			continue
		}
		nNode := po.Node.Dict("N")
		fNode := po.Node.Dict("First")
		if nNode == nil || nNode.Type != "int" || fNode == nil || fNode.Type != "int" {
			s.complainMissingContainer(needs[ck], "container missing /N or /First")
			continue
		}
		n := int(nNode.Int)
		first := int(fNode.Int)
		// Re-verify stream boundary/filter here (length already checked during parse).
		raw, err := verifiedExpand(s.data, po.Node, po.Stream, s.limits)
		if err != nil {
			if le, ok := isLimitErr(err); ok {
				s.halted = true
				s.haltMsg = le.Msg
				s.diag("objstm-limit", "error",
					fmt.Sprintf("object stream %d not expanded: %s", ck.Num, le.Msg),
					s.containerRev(ck), ByteRange{po.Stream.DataStart, po.Stream.DataEnd}, "")
			} else {
				s.diag("objstm-filter", "error",
					fmt.Sprintf("object stream %d not expanded: %v", ck.Num, err),
					s.containerRev(ck), ByteRange{po.Stream.DataStart, po.Stream.DataEnd}, "")
			}
			s.complainMissingContainer(needs[ck], "expansion refused: "+err.Error())
			continue
		}
		if n < 0 || n > 1_000_000 {
			s.complainMissingContainer(needs[ck], "implausible /N")
			continue
		}
		type hdr struct{ num, off int }
		hdrEnd := first
		if hdrEnd > len(raw) {
			hdrEnd = len(raw)
		}
		var hdrs []hdr
		pos := 0
		for i := 0; i < n; i++ {
			p2 := skipWS(raw, pos)
			a := p2
			for a < hdrEnd && raw[a] >= '0' && raw[a] <= '9' {
				a++
			}
			if a == p2 {
				s.complainMissingContainer(needs[ck], fmt.Sprintf("object stream header truncated at pair %d", i))
				pos = -1
				break
			}
			p3 := skipWS(raw, a)
			b := p3
			for b < hdrEnd && raw[b] >= '0' && raw[b] <= '9' {
				b++
			}
			if b == p3 {
				s.complainMissingContainer(needs[ck], fmt.Sprintf("object stream header truncated at pair %d", i))
				pos = -1
				break
			}
			hdrs = append(hdrs, hdr{num: atoi(raw[p2:a]), off: atoi(raw[p3:b])})
			pos = b
		}
		if pos < 0 {
			continue
		}
		indexByNum := map[int]int{}
		for i, h := range hdrs {
			indexByNum[h.num] = i
		}
		// Sort member offsets to derive member boundaries.
		bounds := make([]hdr, len(hdrs))
		copy(bounds, hdrs)
		sort.Slice(bounds, func(i, j int) bool { return bounds[i].off < bounds[j].off })
		for _, nd := range needs[ck] {
			if nd.idx < 0 || nd.idx >= len(hdrs) {
				nd.cand.Note = fmt.Sprintf("type-2 index %d out of range (N=%d)", nd.idx, n)
				s.diag("objstm-index", "error",
					fmt.Sprintf("object %d: index %d out of range in ObjStm %d", nd.cand.Num, nd.idx, ck.Num),
					nd.cand.Revision, ByteRange{}, "")
				continue
			}
			h := hdrs[nd.idx]
			if h.num != nd.cand.Num {
				nd.cand.Note = fmt.Sprintf("header pair %d names object %d, xref expected %d", nd.idx, h.num, nd.cand.Num)
				s.diag("objstm-number-mismatch", "warning",
					fmt.Sprintf("object stream %d pair %d names %d not %d", ck.Num, nd.idx, h.num, nd.cand.Num),
					nd.cand.Revision, ByteRange{}, "")
			}
			start := first + h.off
			if start < 0 || start > len(raw) {
				nd.cand.Note = "member offset beyond decoded object stream"
				s.diag("objstm-offset", "error",
					fmt.Sprintf("object %d member offset %d beyond decoded length %d", h.num, h.off, len(raw)-first),
					nd.cand.Revision, ByteRange{}, "")
				continue
			}
			end := len(raw)
			for _, bo := range bounds {
				if bo.off > h.off && first+bo.off < end && first+bo.off > start {
					end = first + bo.off
				}
			}
			v, _, perr := parseValue(raw, start, 0)
			if perr != nil {
				nd.cand.Note = "member parse failed: " + perr.Error()
				s.diag("objstm-member-parse", "warning",
					fmt.Sprintf("object %d in stream %d: %v", h.num, ck.Num, perr),
					nd.cand.Revision, ByteRange{}, "")
				continue
			}
			nd.cand.Node = v
			nd.cand.HeaderOK = true
			nd.cand.ByteRange = ByteRange{Start: int64(start), End: int64(end)}
			nd.cand.MemberRaw = append([]byte(nil), raw[start:end]...)
			cr := ByteRange{Start: po.Header.Start, End: int64(po.EndPos)}
			nd.cand.ContainerRange = &cr
			nd.cand.Reason = fmt.Sprintf("decoded member %d of verified FlateDecode ObjStm %d at bytes %d-%d",
				nd.idx, ck.Num, cr.Start, cr.End)
		}
	}
}

func (s *Scanner) latestContainerCandidates(needs map[Key][]type2Need) []Key {
	// Process containers in ascending revision then object number for determinism.
	var keys []Key
	revOf := map[Key]int{}
	for k, ns := range needs {
		keys = append(keys, k)
		r := 0
		for _, nd := range ns {
			if nd.cand.Revision > r {
				r = nd.cand.Revision
			}
		}
		revOf[k] = r
	}
	sort.Slice(keys, func(i, j int) bool {
		if revOf[keys[i]] != revOf[keys[j]] {
			return revOf[keys[i]] < revOf[keys[j]]
		}
		if keys[i].Num != keys[j].Num {
			return keys[i].Num < keys[j].Num
		}
		return keys[i].Gen < keys[j].Gen
	})
	return keys
}

func (s *Scanner) containerRev(k Key) int {
	best := -1
	for _, c := range s.r.Candidates {
		if c.Num == k.Num && c.Gen == k.Gen && c.Origin == "declared" && c.HeaderOK && c.Revision > best {
			best = c.Revision
		}
	}
	return best
}

func (s *Scanner) declaredAt(k Key) *Candidate {
	var best *Candidate
	for _, c := range s.r.Candidates {
		if c.Num == k.Num && c.Gen == k.Gen && c.Origin == "declared" {
			if best == nil || c.Revision > best.Revision {
				best = c
			}
		}
	}
	return best
}

func (s *Scanner) complainMissingContainer(ns []type2Need, msg string) {
	for _, nd := range ns {
		nd.cand.Note = msg
	}
}
