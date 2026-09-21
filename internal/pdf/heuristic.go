package pdf

import (
	"bytes"
	"fmt"
	"sort"
)

// streamRegions returns byte ranges occupied by verified/declared stream payloads
// so the heuristic sweep does not mistake encoded bytes for object headers.
func (s *Scanner) streamRegions(candidates []*Candidate) [][2]int {
	var regs [][2]int
	seen := map[[2]int]bool{}
	for _, c := range candidates {
		if c.Origin != OriginDeclared || c.ActualOff < 0 {
			continue
		}
		op, err := parseObject(s.data, c.ActualOff, c.Num, c.Gen, s.resolveIndirectLen)
		if err != nil || !op.IsStream {
			continue
		}
		if op.StreamStart > 0 && op.StreamEnd > op.StreamStart {
			k := [2]int{op.StreamStart, op.StreamEnd}
			if !seen[k] {
				seen[k] = true
				regs = append(regs, k)
			}
		}
	}
	sort.Slice(regs, func(i, j int) bool { return regs[i][0] < regs[j][0] })
	return regs
}

func inRegions(regs [][2]int, pos int) bool {
	for _, r := range regs {
		if pos >= r[0] && pos < r[1] {
			return true
		}
	}
	return false
}

// revisionBeforeXRef assigns an object located at off to the newest revision
// whose xref offset is greater than off (objects of revision r appear before
// r's xref and after the previous revision's xref).
func revisionBeforeXRef(revs []Revision, off int) int {
	idx := 0
	for i := range revs {
		if off < revs[i].XRefOffset {
			idx = i
		}
	}
	return idx
}

// heuristicSweep scans the entire raw file for "N G obj" headers that were NOT
// found "as declared". Every such candidate keeps evidence that it is heuristic.
func (s *Scanner) heuristicSweep(revs []Revision, add func(*Candidate) *Candidate, byID map[string]*Candidate) {
	regs := s.streamRegions(collectForSweep(byID))
	needle := []byte("obj")
	count := 0
	p := 0
	for {
		if count >= s.limits.MaxScanObjects {
			s.addDiag("HEURISTIC_LIMIT", "warning", -1, p,
				"heuristic sweep stopped at object scan limit",
				fmt.Sprintf("limit=%d", s.limits.MaxScanObjects))
			break
		}
		idx := bytes.Index(s.data[p:], needle)
		if idx < 0 {
			break
		}
		abs := p + idx
		// try to read "N G obj" ending here: backtrack to two integers
		if hdr, num, gen, ok := s.headerEndingAt(abs); ok && !inRegions(regs, hdr) {
			count++
			s.addHeuristicCandidate(hdr, num, gen, revisionBeforeXRef(revs, hdr), add, byID)
		}
		p = abs + len(needle)
	}
}

func collectForSweep(byID map[string]*Candidate) []*Candidate {
	out := make([]*Candidate, 0, len(byID))
	for _, c := range byID {
		out = append(out, c)
	}
	return out
}

// headerEndingAt checks that objPos starts "obj" and the preceding tokens are
// "<num> <gen> ". Returns the header start and num/gen.
func (s *Scanner) headerEndingAt(objPos int) (int, int, int, bool) {
	// token before objPos
	q := objPos
	for q > 0 && isWhitespace(s.data[q-1]) {
		q--
	}
	gEnd := q
	for gEnd > 0 && s.data[gEnd-1] >= '0' && s.data[gEnd-1] <= '9' {
		gEnd--
	}
	if gEnd == q {
		return 0, 0, 0, false
	}
	gen, ok1 := parseInt(s.data[gEnd:q])
	q2 := gEnd
	for q2 > 0 && isWhitespace(s.data[q2-1]) {
		q2--
	}
	nEnd := q2
	for nEnd > 0 && s.data[nEnd-1] >= '0' && s.data[nEnd-1] <= '9' {
		nEnd--
	}
	if nEnd == q2 {
		return 0, 0, 0, false
	}
	num, ok2 := parseInt(s.data[nEnd:q2])
	// character before number must be a boundary (start of file or non-digit)
	if nEnd > 0 {
		prev := s.data[nEnd-1]
		if prev >= '0' && prev <= '9' {
			return 0, 0, 0, false
		}
	}
	// guard absurd generations/numbers seen in compressed noise
	if !ok1 || !ok2 || num < 0 || gen < 0 || num > 50_000_000 || gen > 65535 {
		return 0, 0, 0, false
	}
	// require a whitespace immediately before the number region boundary and
	// an EOL/whitespace after "obj" (real header), reducing false positives.
	after := objPos + 3
	if after < len(s.data) && !isWhitespace(s.data[after]) {
		return 0, 0, 0, false
	}
	return nEnd, num, gen, true
}

func (s *Scanner) addHeuristicCandidate(hdr, num, gen, rev int, add func(*Candidate) *Candidate, byID map[string]*Candidate) {
	// if a declared candidate already resolves this exact header, skip.
	for _, c := range byID {
		if c.Num == num && c.Gen == gen && c.Origin == OriginDeclared && c.ActualOff == hdr {
			return
		}
	}
	op, err := parseObject(s.data, hdr, num, gen, s.resolveIndirectLen)
	c := &Candidate{
		Num: num, Gen: gen, Revision: rev, Origin: OriginHeuristic,
		ActualOff: hdr, Offset: hdr,
		ByteStart: hdr, ByteEnd: lenSafe(s.data, hdr),
		Status:   StatusPending,
		HeaderOK: true,
		Evidence: fmt.Sprintf("heuristic scan found header %q at %d (not located via any xref entry)",
			fmt.Sprintf("%d %d obj", num, gen), hdr),
	}
	if err == nil && op != nil && op.ObjectEnd > hdr {
		c.ByteEnd = op.ObjectEnd
		c.Verified = !op.IsStream || (op.StreamStart >= 0 && op.StreamEnd > op.StreamStart)
	} else if err != nil {
		c.Evidence += "; parse note: " + err.Error()
	}
	add(c)
}

func lenSafe(data []byte, hdr int) int {
	if hdr > len(data) {
		return len(data)
	}
	return len(data)
}

// markContested flags live candidates that compete for the same (num, gen)
// version slot. Selection is never decided by latest offset automatically.
func (s *Scanner) markContested(candidates []*Candidate) {
	groups := map[string][]*Candidate{}
	for _, c := range candidates {
		if c.Origin == OriginFreeDecl {
			continue
		}
		key := fmt.Sprintf("%d:%d", c.Num, c.Gen)
		groups[key] = append(groups[key], c)
	}
	for _, g := range groups {
		if len(g) < 2 {
			continue
		}
		// only contest when at least two candidates are usable
		usable := 0
		for _, c := range g {
			if c.ByteStart >= 0 {
				usable++
			}
		}
		if usable >= 2 {
			for _, c := range g {
				c.Contested = true
			}
		}
	}
}
