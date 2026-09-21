package pdf

import (
	"bytes"
	"strings"
	"testing"
)

func findCand(cs []*Candidate, num, gen int, origin CandidateOrigin, rev int) *Candidate {
	for _, c := range cs {
		if c.Num == num && c.Gen == gen && c.Origin == origin && c.Revision == rev {
			return c
		}
	}
	return nil
}

func hasDiag(ds []Diagnostic, code string) bool {
	for _, d := range ds {
		if d.Code == code {
			return true
		}
	}
	return false
}

func TestIncrementalTableUpdates(t *testing.T) {
	data := buildIncrementalTablePDF()
	res := NewScanner(data, DefaultLimits()).Scan()

	if len(res.Revisions) != 2 {
		t.Fatalf("want 2 revisions, got %d; diag=%+v", len(res.Revisions), res.Diagnostics)
	}
	for i, r := range res.Revisions {
		if r.XRefKind != "table" {
			t.Fatalf("rev %d kind=%s", i, r.XRefKind)
		}
	}
	// object 4 only exists in revision index 1
	if c := findCand(res.Candidates, 4, 0, OriginDeclared, 1); c == nil || c.ByteStart < 0 {
		t.Fatalf("object 4 declared in newest revision not found: %+v", c)
	}
	if c := findCand(res.Candidates, 1, 0, OriginDeclared, 0); c == nil {
		t.Fatalf("catalog object in revision 0 missing")
	}
	// revision 0 xref must not claim object 4
	if c := findCand(res.Candidates, 4, 0, OriginDeclared, 0); c != nil {
		t.Fatalf("object 4 should not exist in revision 0")
	}
	if len(res.Diagnostics) != 0 {
		t.Fatalf("clean file should have no diagnostics, got %+v", res.Diagnostics)
	}
}

func TestFreeEntryGeneration(t *testing.T) {
	data := buildFreeEntryPDF()
	res := NewScanner(data, DefaultLimits()).Scan()
	c := findCand(res.Candidates, 5, 2, OriginFreeDecl, 0)
	if c == nil {
		t.Fatalf("free entry 5 gen 2 missing")
	}
	if c.Value == nil || c.Value.Type != "free" {
		t.Fatalf("want free parsed value, got %+v", c.Value)
	}
}

func TestXRefStreamAndObjectStream(t *testing.T) {
	data := buildXRefStreamPDF()
	res := NewScanner(data, DefaultLimits()).Scan()
	if len(res.Revisions) != 1 || res.Revisions[0].XRefKind != "stream" {
		t.Fatalf("want 1 xref-stream revision, got %+v (diag %+v)", res.Revisions, res.Diagnostics)
	}
	// uncompressed objects declared by the stream
	if c := findCand(res.Candidates, 1, 0, OriginDeclared, 0); c == nil || !c.HeaderOK {
		t.Fatalf("catalog from xref stream missing/!headerOK")
	}
	// compressed objects 4 and 5 must be verified with expanded byte ranges
	for _, num := range []int{4, 5} {
		c := findCand(res.Candidates, num, 0, OriginObjStm, 0)
		if c == nil {
			t.Fatalf("compressed object %d missing", num)
		}
		if !c.Verified || c.HostNum != 3 || c.ByteStart < 0 || c.ByteEnd <= c.ByteStart {
			t.Fatalf("object %d not verified inside objstm: %+v", num, c)
		}
	}
	if len(res.Diagnostics) != 0 {
		t.Fatalf("expected clean, got %+v", res.Diagnostics)
	}
}

func TestObjectStreamExpandedContent(t *testing.T) {
	data := buildXRefStreamPDF()
	res := NewScanner(data, DefaultLimits()).Scan()
	c := findCand(res.Candidates, 4, 0, OriginObjStm, 0)
	// expand host and slice the object
	zr := mustZlib(t, data[c.HostStart:c.HostEnd])
	expanded := zr
	if c.ByteEnd > len(expanded) {
		t.Fatalf("object end %d beyond expanded %d", c.ByteEnd, len(expanded))
	}
	slice := string(expanded[c.ByteStart:c.ByteEnd])
	if !strings.Contains(slice, "/Type /Page") {
		t.Fatalf("expanded slice wrong: %q", slice)
	}
}

func TestBadOffsetPointsToBlank(t *testing.T) {
	// take the clean incremental file and corrupt object 4's declared offset
	data := buildIncrementalTablePDF()
	// locate object 4 header
	idx := bytes.Index(data, []byte("4 0 obj"))
	if idx < 0 {
		t.Fatal("object 4 header not found")
	}
	// its declared xref field is the last occurrence of the 20-char n entry;
	// find the field whose value equals idx
	field := []byte(encode20(idx, 0, 'n'))
	fi := bytes.LastIndex(data, field)
	if fi < 0 {
		t.Fatalf("xref field for off %d not found", idx)
	}
	// point it at an offset inside whitespace/EOF region
	bad := len(data) - 5
	copy(data[fi:fi+10], []byte(zeroPad(bad)))
	res := NewScanner(data, DefaultLimits()).Scan()
	if !hasDiag(res.Diagnostics, "OFFSET_OOB") && !hasDiag(res.Diagnostics, "OFFSET_BLANK_OR_MISMATCH") {
		t.Fatalf("want OFFSET_OOB or OFFSET_BLANK_OR_MISMATCH, got %+v", res.Diagnostics)
	}
	c := findCand(res.Candidates, 4, 0, OriginDeclared, 1)
	if c == nil || c.HeaderOK {
		t.Fatalf("declared candidate must remain present but header not OK")
	}
	// heuristic sweep must still recover the real object, marked heuristic
	hc := findCand(res.Candidates, 4, 0, OriginHeuristic, 0)
	_ = hc // revision assignment is segment-based; at least one heuristic candidate
	var heur []*Candidate
	for _, cc := range res.Candidates {
		if cc.Num == 4 && cc.Origin == OriginHeuristic {
			heur = append(heur, cc)
		}
	}
	if len(heur) == 0 {
		t.Fatalf("heuristic recovery candidate missing")
	}
	// the recovered object physically sits in revision 1's section
	if heur[0].Revision != 1 {
		t.Fatalf("heuristic candidate assigned to revision %d, want 1", heur[0].Revision)
	}
}

func TestPreviousChainCycle(t *testing.T) {
	data := buildIncrementalTablePDF()
	// find /Prev field value sx1 inside newest trailer and rewrite to sx2 (self)
	// locate second "startxref" offset number (sx2) - we know it appears after trailer.
	// Simpler: find the second trailer's /Prev and set it to the second startxref value.
	prevKw := []byte("/Prev ")
	pi := bytes.LastIndex(data, prevKw)
	if pi < 0 {
		t.Fatal("no /Prev")
	}
	digStart := pi + len(prevKw)
	digEnd := digStart
	for digEnd < len(data) && data[digEnd] >= '0' && data[digEnd] <= '9' {
		digEnd++
	}
	// the second startxref value: find last startxref
	sxKw := []byte("startxref\n")
	si := bytes.LastIndex(data, sxKw)
	numStart := si + len(sxKw)
	numEnd := numStart
	for numEnd < len(data) && data[numEnd] >= '0' && data[numEnd] <= '9' {
		numEnd++
	}
	val := string(data[numStart:numEnd])
	// pad /Prev to same width
	for len(val) < digEnd-digStart {
		val = " " + val
	}
	copy(data[digStart:digEnd], []byte(val[:digEnd-digStart]))
	res := NewScanner(data, DefaultLimits()).Scan()
	if !hasDiag(res.Diagnostics, "PREV_CYCLE") {
		t.Fatalf("want PREV_CYCLE, got %+v", res.Diagnostics)
	}
}

func TestDuplicateCandidates(t *testing.T) {
	// craft a single xref table with two subsections that both declare object 3,
	// at two different valid offsets (two physical copies of "3 0 obj").
	var tb tb
	tb.w("%PDF-1.4\n")
	off1 := tb.w("1 0 obj\n<< /Type /Catalog /Pages 2 0 R >>\nendobj\n")
	off2 := tb.w("2 0 obj\n<< /Type /Pages /Kids [3 0 R] /Count 1 >>\nendobj\n")
	off3a := tb.w("3 0 obj\n<< /Type /Page /V (A) >>\nendobj\n")
	off3b := tb.w("3 0 obj\n<< /Type /Page /V (B) >>\nendobj\n")
	data := tb.bytes()
	var x bytes.Buffer
	xoff := len(data)
	x.WriteString("xref\n0 4\n")
	x.WriteString(fmt20(0, 65535, 'f'))
	x.WriteString(fmt20(off1, 0, 'n'))
	x.WriteString(fmt20(off2, 0, 'n'))
	x.WriteString(fmt20(off3a, 0, 'n'))
	// second subsection redeclares 3 at the other offset
	x.WriteString("3 1\n")
	x.WriteString(fmt20(off3b, 0, 'n'))
	x.WriteString("trailer\n<< /Size 4 /Root 1 0 R >>\nstartxref\n")
	data = append(data, x.Bytes()...)
	data = append(data, []byte(itoa(xoff))...)
	data = append(data, '\n')
	data = append(data, []byte("%%EOF\n")...)

	res := NewScanner(data, DefaultLimits()).Scan()
	var decls []*Candidate
	for _, c := range res.Candidates {
		if c.Num == 3 && c.Gen == 0 && c.Origin == OriginDeclared {
			decls = append(decls, c)
		}
	}
	if len(decls) != 2 {
		t.Fatalf("want 2 declared duplicate candidates for object 3, got %d", len(decls))
	}
	if !decls[0].Contested || !decls[1].Contested {
		t.Fatalf("duplicate candidates must be marked contested")
	}
	if !hasDiag(res.Diagnostics, "DUP_ENTRY_IN_XREF") {
		t.Fatalf("want DUP_ENTRY_IN_XREF diagnostic")
	}
}

func TestTrailingDataDetected(t *testing.T) {
	data := buildIncrementalTablePDF()
	data = append(data, []byte("GARBAGE_APPENDED_BYTES_NOT_REFERENCED")...)
	res := NewScanner(data, DefaultLimits()).Scan()
	if len(res.TrailingData) == 0 {
		t.Fatalf("want trailing data detection")
	}
	found := false
	for _, bl := range res.TrailingData {
		if strings.Contains(bl.ASCII, "GARBAGE_APPENDED") {
			found = true
		}
	}
	if !found {
		t.Fatalf("trailing blob text missing: %+v", res.TrailingData)
	}
}

func TestDecompressionLimitSafeStop(t *testing.T) {
	// a valid small flate stream but with a tiny limit must produce a limit
	// diagnostic for the objstm, not a panic/hang.
	data := buildXRefStreamPDF()
	lim := DefaultLimits()
	lim.MaxExpandedBytes = 2 // impossibly small
	res := NewScanner(data, lim).Scan()
	if !hasDiag(res.Diagnostics, "OBJSTM_EXPAND_LIMIT") && !hasDiag(res.Diagnostics, "XREF_PARSE") {
		// xref stream itself may fail first because its expansion also exceeds 2
		if len(res.Revisions) != 0 && !hasDiag(res.Diagnostics, "OBJSTM_EXPAND_LIMIT") {
			t.Fatalf("want a safe-stop limit diagnostic, got %+v", res.Diagnostics)
		}
	}
}

func TestMixedTableAndStream(t *testing.T) {
	data := buildMixedXRefPDF()
	res := NewScanner(data, DefaultLimits()).Scan()
	if len(res.Revisions) != 2 {
		t.Fatalf("want 2 revisions, got %d: %+v", len(res.Revisions), res.Diagnostics)
	}
	if res.Revisions[0].XRefKind != "table" || res.Revisions[1].XRefKind != "stream" {
		t.Fatalf("want table then stream, got %s then %s", res.Revisions[0].XRefKind, res.Revisions[1].XRefKind)
	}
	if !res.Revisions[1].HasPrev {
		t.Fatalf("stream revision must carry /Prev")
	}
	if c := findCand(res.Candidates, 4, 0, OriginDeclared, 1); c == nil || !c.HeaderOK {
		t.Fatalf("new object from stream revision missing")
	}
	if !hasDiag(res.Diagnostics, "MIXED_XREF") {
		t.Fatalf("want MIXED_XREF info diagnostic, got %+v", res.Diagnostics)
	}
}

func TestDeclaredAndHeuristicKeptSeparate(t *testing.T) {
	data := buildIncrementalTablePDF()
	res := NewScanner(data, DefaultLimits()).Scan()
	// a clean object appears exactly once as declared and never duplicated heuristic
	for _, num := range []int{1, 2, 3} {
		var n int
		for _, c := range res.Candidates {
			if c.Num == num {
				n++
				if c.Origin == OriginHeuristic {
					t.Fatalf("clean object %d unexpectedly got heuristic duplicate", num)
				}
			}
		}
		if n == 0 {
			t.Fatalf("object %d missing", num)
		}
	}
}
