package app

import (
	"archive/zip"
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"pagevein/internal/pdf"
	"pagevein/internal/store"
)

func fixturePDF() []byte { return pdf.ExportTestIncremental() }
func dupPDF() []byte     { return pdf.ExportTestDuplicate() }

func fileExists(p string) bool { _, err := os.Stat(p); return err == nil }

func newApp(t *testing.T) (*App, string) {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	a, err := New(st, dir)
	if err != nil {
		t.Fatal(err)
	}
	return a, dir
}

func TestImportScanAndReferenceGraph(t *testing.T) {
	a, _ := newApp(t)
	id, err := a.Import("inc.pdf", fixturePDF())
	if err != nil {
		t.Fatal(err)
	}
	res, err := a.GetResult(id)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Scan.Revisions) != 2 {
		t.Fatalf("want 2 revisions, got %d", len(res.Scan.Revisions))
	}
	// catalog 1 references pages 2
	found := false
	for _, r := range res.Scan.References {
		if r.FromNum == 1 && r.ToNum == 2 {
			found = true
		}
	}
	if !found {
		t.Fatalf("reference 1->2 missing: %+v", res.Scan.References)
	}
	// original bytes stored verbatim
	orig, name, err := a.OriginalBytes(id)
	if err != nil || name != "inc.pdf" || !bytes.Equal(orig, fixturePDF()) {
		t.Fatalf("original bytes not preserved verbatim: err=%v", err)
	}
}

func TestDecisionsPersistAcrossRestart(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	a, err := New(st, dir)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := a.Import("dup.pdf", dupPDF())
	res, _ := a.GetResult(id)

	// find two contested declared candidates for object 3
	var cands []*pdf.Candidate
	for _, c := range res.Scan.Candidates {
		if c.Num == 3 && c.Gen == 0 && c.Origin == pdf.OriginDeclared {
			cands = append(cands, c)
		}
	}
	if len(cands) != 2 || !cands[0].Contested {
		t.Fatalf("want 2 contested candidates, got %+v", cands)
	}
	chosen := cands[0]
	rejected := cands[1]
	if err := a.Decide(id, chosen.ID, string(pdf.StatusConfirmed), "human pick A"); err != nil {
		t.Fatal(err)
	}
	if err := a.Decide(id, rejected.ID, string(pdf.StatusRejected), "drop B"); err != nil {
		t.Fatal(err)
	}
	st.Close()

	// restart from the same files
	st2, err := store.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st2.Close()
	a2, err := New(st2, dir)
	if err != nil {
		t.Fatal(err)
	}
	res2, err := a2.GetResult(id)
	if err != nil {
		t.Fatal(err)
	}
	stat := map[string]pdf.CandidateStatus{}
	for _, c := range res2.Scan.Candidates {
		stat[c.ID] = c.Status
	}
	if stat[chosen.ID] != pdf.StatusConfirmed || stat[rejected.ID] != pdf.StatusRejected {
		t.Fatalf("decisions did not survive restart: %+v", stat)
	}
}

func TestBranchCreationAndRepro(t *testing.T) {
	a, _ := newApp(t)
	id, _ := a.Import("dup.pdf", dupPDF())
	res, _ := a.GetResult(id)
	var cands []*pdf.Candidate
	for _, c := range res.Scan.Candidates {
		if c.Num == 3 && c.Origin == pdf.OriginDeclared {
			cands = append(cands, c)
		}
	}
	b, err := a.CreateBranch(id, 3, 0, cands[1].ID, "解释分支-B", "")
	if err != nil {
		t.Fatal(err)
	}
	// after restart, branch is present and still points at the same candidate
	res2, _ := a.GetResult(id)
	if len(res2.Branches) != 1 || res2.Branches[0].ChosenCandidateID != b.ChosenCandidateID {
		t.Fatalf("branch not persisted: %+v", res2.Branches)
	}
	if !strings.Contains(res2.Branches[0].RejectedCandidateIDs, cands[0].ID) {
		t.Fatalf("rejected candidate id not recorded in branch")
	}
	// cannot create a branch for a non-contested object
	if _, err := a.CreateBranch(id, 1, 0, "", "x", ""); err == nil {
		t.Fatalf("expected error creating branch for uncontested object")
	}
}

func TestReviewBundleContents(t *testing.T) {
	a, _ := newApp(t)
	id, _ := a.Import("inc.pdf", fixturePDF())
	res, _ := a.GetResult(id)
	f, err := a.BuildReviewBundle(res)
	if err != nil {
		t.Fatal(err)
	}
	zr, err := zip.NewReader(bytes.NewReader(f.Content), int64(len(f.Content)))
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	var orig []byte
	for _, zf := range zr.File {
		names[zf.Name] = true
		if zf.Name == "original.pdf" {
			rc, _ := zf.Open()
			orig, _ = io.ReadAll(rc)
			rc.Close()
		}
	}
	for _, want := range []string{"original.pdf", "manifest.json", "README.txt", "objects/INDEX.txt"} {
		if !names[want] {
			t.Fatalf("bundle missing %s; have %v", want, names)
		}
	}
	if !bytes.Equal(orig, fixturePDF()) {
		t.Fatalf("bundled original.pdf is not byte-for-byte identical")
	}
	// ensure there is no re-saved pdf pretending to be evidence beyond original.pdf
	for n := range names {
		if strings.HasSuffix(n, ".pdf") && n != "original.pdf" {
			t.Fatalf("unexpected pdf in evidence bundle: %s", n)
		}
	}
}

func TestCandidateBytesVerbatim(t *testing.T) {
	a, _ := newApp(t)
	id, _ := a.Import("inc.pdf", fixturePDF())
	res, _ := a.GetResult(id)
	var cat *pdf.Candidate
	for _, c := range res.Scan.Candidates {
		if c.Num == 1 && c.Origin == pdf.OriginDeclared {
			cat = c
		}
	}
	if cat == nil {
		t.Fatal("catalog candidate missing")
	}
	cb, err := a.CandidateBytes(res, cat)
	if err != nil {
		t.Fatal(err)
	}
	want := fixturePDF()[cat.ByteStart:cat.ByteEnd]
	if !bytes.Equal(cb.Original, want) {
		t.Fatalf("candidate bytes mismatch")
	}
}

func TestDataDirectoryCreated(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nested", "data")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	dbPath := filepath.Join(dir, "t.db")
	t.Logf("db path: %q exists=%v", dbPath, fileExists(dir))
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if _, err := New(st, dir); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "originals")); err != nil {
		t.Fatalf("originals dir not created: %v", err)
	}
}
