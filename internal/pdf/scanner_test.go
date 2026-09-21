package pdf

import (
	"strings"
	"testing"
)

func findCand(r *Report, obj, rev int, kind CandidateKind) *Candidate {
	for _, c := range r.Candidates {
		if c.ObjNum == obj && c.Revision == rev && c.Kind == kind {
			return c
		}
	}
	return nil
}

func hasDiag(r *Report, code string) bool {
	for _, d := range r.Diagnostics {
		if d.Code == code {
			return true
		}
	}
	return false
}

func TestClassicSingleRevision(t *testing.T) {
	r := ScanFile(classicPDF(), "classic.pdf", DefaultLimits())
	if !r.HeaderOK || !r.StartXRefOK {
		t.Fatalf("header/startxref not ok: %+v", r.Diagnostics)
	}
	if len(r.Revisions) != 1 {
		t.Fatalf("want 1 revision, got %d", len(r.Revisions))
	}
	c := findCand(r, 4, 0, CandDeclared)
	if c == nil || !c.Valid || !c.Stream || c.StreamLen != 33 {
		t.Fatalf("content object wrong: %+v", c)
	}
	if len(r.Pages) != 1 || r.Pages[0].ObjNum != 3 {
		t.Fatalf("pages = %+v", r.Pages)
	}
	// Root 系统引用
	found := false
	for _, e := range r.Edges {
		if e.FromObj == 0 && e.ToObj == 1 {
			found = true
		}
	}
	if !found {
		t.Fatal("missing trailer Root edge")
	}
}

func TestIncrementalUpdates(t *testing.T) {
	r := ScanFile(incrementalPDF(), "inc.pdf", DefaultLimits())
	if len(r.Revisions) != 2 {
		t.Fatalf("want 2 revisions, got %d", len(r.Revisions))
	}
	old := findCand(r, 4, 0, CandDeclared)
	new := findCand(r, 4, 1, CandDeclared)
	if old == nil || !old.Valid || new == nil || !new.Valid {
		t.Fatalf("missing versions: old=%v new=%v", old, new)
	}
	if old.Offset == new.Offset {
		t.Fatal("two revisions should have different offsets")
	}
	c6 := findCand(r, 6, 1, CandDeclared)
	if c6 == nil || !c6.Valid {
		t.Fatal("annot object 6 missing in rev1")
	}
}

func TestFreeEntryGeneration(t *testing.T) {
	r := ScanFile(freeGenPDF(), "free.pdf", DefaultLimits())
	if len(r.Revisions) != 3 {
		t.Fatalf("want 3 revisions, got %d", len(r.Revisions))
	}
	// 对象6: rev0/1 有效, rev2 free(gen1)
	freed := findCand(r, 6, 2, CandDeclared)
	if freed == nil || !freed.Free || freed.Gen != 1 {
		t.Fatalf("expected free gen1 candidate, got %+v", freed)
	}
	c7 := findCand(r, 7, 2, CandDeclared)
	if c7 == nil || !c7.Valid {
		t.Fatal("object 7 missing")
	}
}

func TestXRefStreamAndObjectStream(t *testing.T) {
	r := ScanFile(xrefStreamPDF(), "xs.pdf", DefaultLimits())
	if len(r.Revisions) != 1 || r.Revisions[0].Kind != "stream" {
		t.Fatalf("want single stream revision: %+v", r.Revisions)
	}
	c2 := findCand(r, 2, 0, CandDeclared)
	if c2 == nil || !c2.Valid || !c2.InObjStm || c2.ContainerObj != 8 {
		t.Fatalf("compressed catalog wrong: %+v", c2)
	}
	if c2.Parsed == nil || c2.Parsed.DictGet("Type") == nil {
		t.Fatalf("inner object not parsed: %+v", c2.Parsed)
	}
	c8 := findCand(r, 8, 0, CandDeclared)
	if c8 == nil || !c8.Stream || !c8.Expanded {
		t.Fatalf("ObjStm not expanded: %+v", c8)
	}
	if len(r.Pages) != 1 {
		t.Fatalf("pages through compressed root: %+v", r.Pages)
	}
}

func TestMixedXRef(t *testing.T) {
	r := ScanFile(mixedXRefPDF(), "mix.pdf", DefaultLimits())
	if !r.MixedXRef {
		t.Fatal("expected mixed xref flag")
	}
	if len(r.Revisions) != 2 || r.Revisions[0].Kind != "stream" || r.Revisions[1].Kind != "table" {
		t.Fatalf("unexpected revision kinds: %+v", r.Revisions)
	}
	if !hasDiag(r, "mixed-xref") {
		t.Fatal("missing mixed-xref diagnostic")
	}
}

func TestBadOffset(t *testing.T) {
	r := ScanFile(badOffsetPDF(), "bad.pdf", DefaultLimits())
	if !hasDiag(r, "offset-mismatch") && !hasDiag(r, "offset-oob") {
		t.Fatalf("expected offset diagnostic, got: %+v", r.Diagnostics)
	}
	c := findCand(r, 4, 1, CandDeclared)
	if c == nil || c.Valid {
		t.Fatalf("corrupted candidate must remain invalid: %+v", c)
	}
}

func TestPrevCycle(t *testing.T) {
	r := ScanFile(prevCyclePDF(), "cycle.pdf", DefaultLimits())
	if !hasDiag(r, "prev-cycle") {
		t.Fatalf("expected prev-cycle diagnostic: %+v", r.Diagnostics)
	}
	if len(r.Revisions) < 1 {
		t.Fatal("should still keep the section read before the cycle")
	}
}

func TestDuplicateAndTrailing(t *testing.T) {
	r := ScanFile(duplicateCandidatePDF(), "dup.pdf", DefaultLimits())
	var heur *Candidate
	for _, c := range r.Candidates {
		if c.ObjNum == 4 && c.Kind == CandHeuristic {
			heur = c
		}
	}
	if heur == nil || !heur.Valid {
		t.Fatalf("expected heuristic duplicate for object 4: %+v", heur)
	}
	if !hasDiag(r, "duplicate-candidates") {
		t.Fatal("expected duplicate-candidates diagnostic")
	}

	r2 := ScanFile(trailingPDF(), "trail.pdf", DefaultLimits())
	if r2.TrailingLen == 0 {
		t.Fatal("expected trailing data for classic+garbage")
	}
	if !strings.Contains(diagEvidence(r2, "trailing-data"), "GARBAGE") {
		t.Fatal("trailing evidence should capture appended bytes")
	}
}

func diagEvidence(r *Report, code string) string {
	for _, d := range r.Diagnostics {
		if d.Code == code {
			return d.Evidence
		}
	}
	return ""
}

func TestDecompressionLimit(t *testing.T) {
	lim := DefaultLimits()
	lim.MaxExpand = 64 * 1024 // 64KiB 上限，流声明解压后 5MB
	r := ScanFile(bombPDF(), "bomb.pdf", lim)
	c := findCand(r, 4, 0, CandDeclared)
	if c == nil {
		t.Fatal("bomb object missing")
	}
	if c.Expanded {
		t.Fatal("bomb must not be marked expanded under tight limit")
	}
	if c.ExpandNote == "" || !strings.Contains(c.ExpandNote, "限额") {
		t.Fatalf("expected limit-stop note, got %q", c.ExpandNote)
	}
	if !hasDiag(r, "expand-stop") {
		t.Fatalf("expected expand-stop diagnostic: %+v", r.Diagnostics)
	}
}

func TestDeclaredVsHeuristicSeparated(t *testing.T) {
	r := ScanFile(incrementalPDF(), "inc.pdf", DefaultLimits())
	for _, c := range r.Candidates {
		if c.Kind == CandHeuristic {
			t.Fatalf("clean incremental file should have no heuristic candidates, got #%d obj %d", c.ID, c.ObjNum)
		}
	}
}

func TestReferenceEdgesCollected(t *testing.T) {
	r := ScanFile(classicPDF(), "c.pdf", DefaultLimits())
	want := map[[2]int]bool{
		{1, 2}: true, {2, 3}: true, {3, 4}: true, {3, 5}: true, {0, 1}: true,
	}
	got := map[[2]int]bool{}
	for _, e := range r.Edges {
		got[[2]int{e.FromObj, e.ToObj}] = true
	}
	for k := range want {
		if !got[k] {
			t.Errorf("missing edge %d->%d", k[0], k[1])
		}
	}
	// 每条边必须带证据偏移
	for _, e := range r.Edges {
		if e.Offset < 0 {
			t.Errorf("edge %d->%d missing evidence offset", e.FromObj, e.ToObj)
		}
	}
}

func TestObjectStreamInnerReferences(t *testing.T) {
	r := ScanFile(xrefStreamPDF(), "xs.pdf", DefaultLimits())
	// 压缩 catalog(2)->pages(3) 的引用应来自解压内容
	found := false
	for _, e := range r.Edges {
		if e.FromObj == 2 && e.ToObj == 3 && e.InExpanded && e.ContainerObj == 8 {
			found = true
		}
	}
	if !found {
		t.Fatalf("missing compressed inner ref 2->3; edges=%+v", r.Edges)
	}
}
