package pdf

import (
	"fmt"
	"sort"
)

// resolveObjectStreams verifies each object stream (length/filter/boundary),
// expands it under the resource limit, and binds compressed-object candidates
// to their real bytes inside the (logical) object stream content.
func (s *Scanner) resolveObjectStreams(revs *[]Revision, candidates []*Candidate,
	byID map[string]*Candidate, add func(*Candidate) *Candidate) {

	// index declared uncompressed candidates that look like object streams
	hostCand := map[int]*Candidate{} // host object num -> candidate (latest declared)
	for _, c := range candidates {
		if c.Origin != OriginDeclared || !c.HeaderOK {
			continue
		}
		hostCand[c.Num] = c
	}

	// gather compressed placeholders grouped by host
	type pending struct {
		c *Candidate
	}
	byHost := map[int][]*Candidate{}
	for _, c := range candidates {
		if c.Origin == OriginObjStm && c.ByteStart < 0 {
			byHost[c.StreamObj] = append(byHost[c.StreamObj], c)
		}
	}
	hostNums := make([]int, 0, len(byHost))
	for h := range byHost {
		hostNums = append(hostNums, h)
	}
	sort.Ints(hostNums)

	for _, hostNum := range hostNums {
		pl := byHost[hostNum]
		hc := hostCand[hostNum]
		revForDiag := pl[0].Revision
		if hc == nil {
			s.addDiag("OBJSTM_MISSING_HOST", "error", revForDiag, -1,
				fmt.Sprintf("%d compressed object(s) reference object stream %d which is not declared", len(pl), hostNum),
				"no type-1 xref entry resolves the ObjStm host; compressed objects cannot be verified")
			continue
		}
		op, err := parseObject(s.data, hc.ActualOff, hostNum, 0, s.resolveIndirectLen)
		if err != nil || !op.IsStream {
			s.addDiag("OBJSTM_NOT_STREAM", "error", hc.Revision, hc.ActualOff,
				fmt.Sprintf("declared object stream %d is not a parsable stream", hostNum),
				errStr(err))
			continue
		}
		filters := dictNames(op.Dict["Filter"])
		ok, reason := supportedExpansion(filters)
		if !ok {
			s.addDiag("OBJSTM_FILTER", "error", hc.Revision, hc.ActualOff,
				fmt.Sprintf("object stream %d not expanded", hostNum), reason)
			continue
		}
		if op.StreamEnd <= op.StreamStart || op.StreamEnd > len(s.data) {
			s.addDiag("OBJSTM_BOUNDARY", "error", hc.Revision, hc.ActualOff,
				fmt.Sprintf("object stream %d boundary invalid", hostNum),
				fmt.Sprintf("stream %d..%d", op.StreamStart, op.StreamEnd))
			continue
		}
		expanded, exErr := expandFlate(s.data[op.StreamStart:op.StreamEnd], s.limits.MaxExpandedBytes)
		if exErr != nil {
			s.addDiag("OBJSTM_EXPAND_LIMIT", "error", hc.Revision, hc.ActualOff,
				fmt.Sprintf("object stream %d expansion stopped safely", hostNum), exErr.Error())
			continue
		}
		typeVal := op.Dict["Type"]
		if typeVal != "" && typeVal != "/ObjStm" {
			s.addDiag("OBJSTM_TYPE", "warning", hc.Revision, hc.ActualOff,
				fmt.Sprintf("object stream %d has /Type %s (expected /ObjStm)", hostNum, typeVal),
				"continuing to parse N/first pairs")
		}
		nCount, okN := dictInt(op.Dict["N"])
		first, okF := dictInt(op.Dict["First"])
		if !okN || !okF || nCount < 0 || first < 0 {
			s.addDiag("OBJSTM_HEADER", "error", hc.Revision, hc.ActualOff,
				fmt.Sprintf("object stream %d has invalid /N or /First", hostNum),
				fmt.Sprintf("N=%q First=%q", op.Dict["N"], op.Dict["First"]))
			continue
		}

		objs, perr := parseObjStmIndex(expanded, nCount, first)
		if perr != nil {
			s.addDiag("OBJSTM_INDEX", "error", hc.Revision, hc.ActualOff,
				fmt.Sprintf("object stream %d index parse failed", hostNum), perr.Error())
			continue
		}
		// mark host candidate verified
		hc.Verified = true
		for _, pc := range pl {
			info, exists := objs[pc.Num]
			if !exists {
				s.addDiag("OBJSTM_OBJECT_ABSENT", "error", pc.Revision, hc.ActualOff,
					fmt.Sprintf("object %d not present in object stream %d (declared index %d)",
						pc.Num, hostNum, pc.IndexInStm),
					fmt.Sprintf("object stream declares %d objects", nCount))
				continue
			}
			if info.order != pc.IndexInStm {
				s.addDiag("OBJSTM_INDEX_MISMATCH", "warning", pc.Revision, hc.ActualOff,
					fmt.Sprintf("object %d xref index %d differs from object-stream order %d",
						pc.Num, pc.IndexInStm, info.order),
					"using object-stream byte offsets (authoritative for content location)")
			}
			pc.Verified = true
			pc.ActualOff = info.off
			// Byte range is inside the *expanded* content; store logical offsets
			// with the host range recorded in evidence (no rewritten bytes).
			end := len(expanded)
			if info.order+1 < nCount {
				if next := objsByOrder(objs, info.order+1); next > info.off {
					end = next
				}
			}
			pc.ByteStart = info.off
			pc.ByteEnd = end
			pc.HostNum = hostNum
			pc.HostStart = op.StreamStart
			pc.HostEnd = op.StreamEnd
			pc.Evidence = fmt.Sprintf(
				"xref type 2 -> /ObjStm %d (file bytes %d..%d, verified, filter %v); expanded offset %d..%d",
				hostNum, op.StreamStart, op.StreamEnd, filters, info.off, end)
		}
	}
}

func errStr(err error) string {
	if err == nil {
		return "not a stream object"
	}
	return err.Error()
}

type objStmEntry struct {
	off   int // offset of object content within expanded stream
	order int // order in N pair list
}

func parseObjStmIndex(expanded []byte, n, first int) (map[int]objStmEntry, error) {
	out := map[int]objStmEntry{}
	p := 0
	for i := 0; i < n; i++ {
		p = skipWS(expanded, p)
		numTok, np := readToken(expanded, p)
		num, ok := parseInt([]byte(numTok))
		if !ok {
			return nil, fmt.Errorf("pair %d: bad object number %q", i, numTok)
		}
		np = skipWS(expanded, np)
		offTok, np2 := readToken(expanded, np)
		rel, ok2 := parseInt([]byte(offTok))
		if !ok2 {
			return nil, fmt.Errorf("pair %d: bad offset %q", i, offTok)
		}
		abs := first + rel
		if abs < 0 || abs > len(expanded) {
			return nil, fmt.Errorf("object %d expanded offset %d out of range (%d bytes)", num, abs, len(expanded))
		}
		out[num] = objStmEntry{off: abs, order: i}
		p = np2
	}
	return out, nil
}

func objsByOrder(m map[int]objStmEntry, order int) int {
	for _, e := range m {
		if e.order == order {
			return e.off
		}
	}
	return -1
}
