package store

import (
	"path/filepath"
	"testing"

	"yemai/internal/forensic"
)

// minimalPDF builds a two-revision classic PDF via the forensic builder
// indirectly: here we craft bytes inline to avoid an import cycle concern
// (both are internal, so reuse is allowed through a small local helper).
func minimalPDF() []byte {
	return buildTwoRevs()
}

func TestRestartKeepsBranchesAndDecisions(t *testing.T) {
	dir := t.TempDir()

	st, err := Open(filepath.Join(dir, "data"))
	if err != nil {
		t.Fatal(err)
	}
	data := minimalPDF()
	rep := forensic.NewScanner(forensic.DefaultLimits).Scan("x.pdf", data)
	if len(rep.Candidates) == 0 || len(rep.Ambiguities) == 0 {
		t.Fatalf("fixture needs competing candidates: cands=%d amb=%d",
			len(rep.Candidates), len(rep.Ambiguities))
	}
	doc, err := st.SaveDocument("x.pdf", data, rep)
	if err != nil {
		t.Fatal(err)
	}
	// byte-exact original preserved
	orig, err := readBytes(st.OriginalPath(doc))
	if err != nil {
		t.Fatal(err)
	}
	if string(orig) != string(data) {
		t.Fatal("original file was rewritten, not byte-exact")
	}
	defaultBranch, err := st.DefaultBranch(doc.ID)
	if err != nil {
		t.Fatal(err)
	}
	var amb *forensic.Ambiguity
	for i := range rep.Ambiguities {
		if rep.Ambiguities[i].Key.Num == 5 {
			amb = &rep.Ambiguities[i]
		}
	}
	if amb == nil {
		t.Fatal("no ambiguity for object 5")
	}
	var heal *forensic.Candidate
	for _, c := range rep.Candidates {
		if c.Num == amb.Key.Num && c.Gen == amb.Key.Gen && c.Revision == amb.Revision && c.Origin == "heuristic" {
			heal = c
		}
	}
	if heal == nil {
		t.Fatal("fixture ambiguity must include a heuristic candidate")
	}
	// Fork a branch on the OLD branch, then confirm the heuristic recovery.
	fork, err := st.ForkBranch(doc.ID, defaultBranch, "采纳恢复候选", "reviewer accepted carved body")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.PutDecision(fork.ID, doc.ID, heal.Revision, heal.Num, heal.Gen, heal.ID, "confirmed"); err != nil {
		t.Fatal(err)
	}
	st.Close()

	// Restart from the same SQLite + project directory.
	st2, err := Open(filepath.Join(dir, "data"))
	if err != nil {
		t.Fatal(err)
	}
	defer st2.Close()
	doc2, err := st2.GetDocument(doc.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	if doc2.SHA256 != rep.SHA256 || len(doc2.Report.Candidates) != len(rep.Candidates) {
		t.Fatal("report did not survive restart")
	}
	branches, err := st2.Branches(doc.ID)
	if err != nil || len(branches) != 2 {
		t.Fatalf("branches after restart = %d (%v)", len(branches), err)
	}
	eff, err := st2.EffectiveDecisions(fork.ID)
	if err != nil {
		t.Fatal(err)
	}
	got, ok := eff[key(heal.Revision, heal.Num, heal.Gen)]
	if !ok || got.CandidateID != heal.ID {
		t.Fatalf("confirmed choice lost after restart: %+v", eff)
	}
	// Old default branch remains reproducible: it has no decisions, so its
	// effective map is empty and the as-found default is unchanged.
	oldEff, err := st2.EffectiveDecisions(defaultBranch)
	if err != nil || len(oldEff) != 0 {
		t.Fatalf("default branch mutated: %+v", oldEff)
	}
}

func key(rev, num, gen int) string {
	return itoa(rev) + ":" + itoa(num) + ":" + itoa(gen)
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}

func readBytes(p string) ([]byte, error) {
	return readFileAll(p)
}
