package forensic

import (
	"fmt"
	"strings"
	"testing"
)

func scanBytes(t *testing.T, name string, data []byte, lim ...Limits) *Report {
	t.Helper()
	l := DefaultLimits
	if len(lim) > 0 {
		l = lim[0]
	}
	return NewScanner(l).Scan(name, data)
}

func diagCodes(r *Report) []string {
	out := make([]string, 0, len(r.Diagnostics))
	for _, d := range r.Diagnostics {
		out = append(out, d.Code)
	}
	return out
}

func hasCode(r *Report, code string) bool {
	for _, c := range diagCodes(r) {
		if c == code {
			return true
		}
	}
	return false
}

// 1) incremental updates: two classic revisions linked by /Prev.
func TestIncrementalUpdates(t *testing.T) {
	b := newBuilder()
	b.put(1, 0, "<< /Type /Catalog /Pages 2 0 R >>")
	b.put(2, 0, "<< /Type /Pages /Kids [] /Count 0 >>")
	off1 := b.classicXRef(3, nil, []int{1, 2}, 0, false, 1)

	// revision 2 redefines object 1 (and declares object 2 again)
	b.put(1, 0, "<< /Type /Catalog /Pages 2 0 R /Version /2 >>")
	b.put(2, 0, "<< /Type /Pages /Kids [] /Count 0 /V 1 >>")
	b.classicXRef(3, nil, []int{1, 2}, off1, true, 1)

	r := scanBytes(t, "inc.pdf", b.bytes())
	if len(r.Revisions) != 2 {
		t.Fatalf("revisions = %d, want 2; diags=%v", len(r.Revisions), diagCodes(r))
	}
	if r.Revisions[0].Index != 0 || !r.Revisions[1].PrevPresent || r.Revisions[1].PrevOffset != int64(off1) {
		t.Fatalf("prev chain wrong: %+v", r.Revisions)
	}
	if r.Revisions[0].Kind != "table" || r.Revisions[1].Kind != "table" {
		t.Fatalf("kinds = %s,%s", r.Revisions[0].Kind, r.Revisions[1].Kind)
	}
	// object 1 has two declared versions across revisions
	var o1 []*Candidate
	for _, c := range r.Candidates {
		if c.Num == 1 && c.Origin == "declared" {
			o1 = append(o1, c)
		}
	}
	if len(o1) != 2 || o1[0].Revision != 0 || o1[1].Revision != 1 {
		t.Fatalf("object 1 versions = %d", len(o1))
	}
	if hasCode(r, "previous-cycle") || hasCode(r, "trailing-data") {
		t.Fatalf("unexpected diags: %v", diagCodes(r))
	}
}

// 2) free entry generations move across revisions.
func TestFreeEntryGenerations(t *testing.T) {
	b := newBuilder()
	b.put(1, 0, "<< /Type /Catalog >>")
	b.put(2, 0, "<< /Type /Pages /Count 1 >>")
	free1 := map[[2]int][2]int{{0, 0}: {3, 65535}}
	off1 := b.classicXRef(4, free1, []int{1, 2}, 0, false, 1)

	// revision 2 frees object 2 (next-free -> 0, gen bumps to 1)
	b.put(1, 0, "<< /Type /Catalog /V 2 >>")
	free2 := map[[2]int][2]int{
		{0, 0}: {2, 65535},
		{2, 1}: {0, 1},
	}
	b.classicXRef(4, free2, []int{1}, off1, true, 1)
	r := scanBytes(t, "free.pdf", b.bytes())
	var gotGen int = -1
	found := false
	for _, f := range r.Frees {
		if f.Num == 2 && f.Revision == 1 {
			gotGen = f.Generation
			found = true
			if f.NextFree != 0 {
				t.Fatalf("freed obj2 next = %d, want 0", f.NextFree)
			}
		}
	}
	if !found || gotGen != 1 {
		t.Fatalf("expected free obj2 gen=1 in rev1, frees=%+v", r.Frees)
	}
}

// 3) xref stream revision with compressed cross-reference data.
func TestXRefStreamRevision(t *testing.T) {
	b := newBuilder()
	b.put(1, 0, "<< /Type /Catalog /Pages 2 0 R >>")
	b.put(2, 0, "<< /Type /Pages /Count 0 >>")
	off1 := b.classicXRef(3, nil, []int{1, 2}, 0, false, 1)

	// revision 2 via xref stream (object 99), same objects, Prev->off1
	b.put(1, 0, "<< /Type /Catalog /Pages 2 0 R /V 2 >>")
	b.put(2, 0, "<< /Type /Pages /Count 0 /V 2 >>")
	ents := []sxEntry{
		{0, 0, 0},
		{1, int64(b.off[1]), 0},
		{1, int64(b.off[2]), 0},
	}
	off2, _ := b.streamXRef(99, 3, ents, nil, off1, true)
	_ = off2
	r := scanBytes(t, "xs.pdf", b.bytes())
	if len(r.Revisions) != 2 {
		t.Fatalf("revisions=%d diags=%v", len(r.Revisions), diagCodes(r))
	}
	if r.Revisions[1].Kind != "stream" {
		t.Fatalf("newest kind=%s want stream", r.Revisions[1].Kind)
	}
	if r.Revisions[1].PrevOffset != int64(off1) {
		t.Fatalf("stream prev=%d want %d", r.Revisions[1].PrevOffset, off1)
	}
	if r.Revisions[0].Kind != "table" {
		t.Fatalf("oldest kind=%s want table (mixed chain)", r.Revisions[0].Kind)
	}
	// newest object 1 parses
	c := newestDeclared(r, 1, 0)
	if c == nil || c.Node == nil || c.Node.Type != "dict" {
		t.Fatalf("newest obj1 not parsed")
	}
}

func newestDeclared(r *Report, num, gen int) *Candidate {
	var best *Candidate
	for _, c := range r.Candidates {
		if c.Num == num && c.Gen == gen && c.Origin == "declared" && !c.Compressed {
			if best == nil || c.Revision > best.Revision {
				best = c
			}
		}
	}
	return best
}

// 4) object streams: type-2 members expanded only after checks pass.
func TestObjectStreams(t *testing.T) {
	b := newBuilder()
	members := map[int]string{
		2: "<< /Type /Pages /Count 0 >>",
		3: "<< /Type /Page /Parent 2 0 R >>",
	}
	container := b.objStream(10, members)
	b.put(1, 0, "<< /Type /Catalog /Pages 2 0 R >>")
	ents := []sxEntry{
		{0, 0, 0},
		{1, int64(b.off[1]), 0},
		{2, int64(container), 0}, // obj 2 at index 0
		{2, int64(container), 1}, // obj 3 at index 1
	}
	// gap objects 4..9 are skipped via the second /Index subsection at 10
	ents = append(ents, sxEntry{1, int64(b.off[container]), 0}) // obj 10 = the ObjStm
	b.streamXRef(20, 11, ents, []int{0, 4, 10, 1}, 0, false)
	r := scanBytes(t, "objstm.pdf", b.bytes())
	var c2, c3 *Candidate
	for _, c := range r.Candidates {
		if c.Num == 2 && c.Compressed {
			c2 = c
		}
		if c.Num == 3 && c.Compressed {
			c3 = c
		}
	}
	if c2 == nil || c3 == nil {
		t.Fatalf("missing type-2 candidates, diags=%v", diagCodes(r))
	}
	if !c2.HeaderOK || !c3.HeaderOK {
		t.Fatalf("compressed members not verified: %s / %s", c2.Note, c3.Note)
	}
	if c2.ContainerRange == nil || c2.ContainerRange.Start != int64(b.off[container]) {
		t.Fatalf("container bytes wrong: %+v", c2.ContainerRange)
	}
	if c2.Node == nil || c2.Node.Type != "dict" {
		t.Fatalf("obj2 not parsed from decoded stream")
	}
	if len(c2.MemberRaw) == 0 {
		t.Fatalf("member raw evidence missing")
	}
	// container -> member edge
	foundEdge := false
	for _, e := range r.Edges {
		if e.From.Num == container && e.To.Num == 3 && e.Kind == "type2-container" {
			foundEdge = true
		}
	}
	if !foundEdge {
		t.Fatalf("container->member edge missing; edges=%+v", r.Edges)
	}
}

// 5) bad startxref offset points into blank space; declared invalid, heuristic recovery.
func TestBadOffsetRecovery(t *testing.T) {
	b := newBuilder()
	b.put(1, 0, "<< /Type /Catalog /Pages 2 0 R >>")
	b.put(2, 0, "<< /Type /Pages /Count 0 >>")
	good := b.classicXRef(3, nil, []int{1, 2}, 0, false, 1)
	_ = good
	// corrupt the startxref number to point into a blank gap
	raw := b.bytes()
	idx := strings.LastIndex(string(raw), "startxref\n")
	numStart := idx + len("startxref\n")
	// overwrite offset with a position inside inter-object whitespace: choose 16
	// (inside the binary comment blank region)
	copy(raw[numStart:], fmt.Sprintf("%010d", 20))
	r := scanBytes(t, "badptr.pdf", raw)
	if !hasCode(r, "xref-table-parse") {
		t.Fatalf("expected xref-table-parse, got %v", diagCodes(r))
	}
	// heuristic scan must still carve objects 1 and 2 from unclaimed bytes
	he := map[int]bool{}
	for _, c := range r.Candidates {
		if c.Origin == "heuristic" && c.HeaderOK {
			he[c.Num] = true
		}
	}
	if !he[1] || !he[2] {
		t.Fatalf("heuristic recovery missing objects, got %+v diags=%v", he, diagCodes(r))
	}
}

// 6) previous-chain cycle must terminate safely.
func TestPreviousCycle(t *testing.T) {
	b := newBuilder()
	b.put(1, 0, "<< /Type /Catalog >>")
	off1 := b.classicXRef(2, nil, []int{1}, 0, false, 1)

	b.put(1, 0, "<< /Type /Catalog /V 2 >>")
	// deliberately point Prev back to THIS revision's own upcoming offset.
	// We don't know offset ahead, so place Prev=off1 then patch after.
	off2 := b.classicXRef(2, nil, []int{1}, off1, true, 1)
	raw := b.bytes()
	// patch the second trailer /Prev to point at off2 itself
	needle := fmt.Sprintf(" /Prev %d ", off1)
	last := strings.LastIndex(string(raw), needle)
	if last < 0 {
		t.Fatal("prev token not found")
	}
	rep := fmt.Sprintf(" /Prev %d ", off2)
	copy(raw[last:last+len(needle)], rep)
	r := scanBytes(t, "cycle.pdf", raw)
	if !hasCode(r, "previous-cycle") {
		t.Fatalf("expected previous-cycle, got %v", diagCodes(r))
	}
	if len(r.Revisions) < 1 {
		t.Fatalf("should still record revisions before cycle")
	}
}

// 7) duplicate candidates compete; never resolved by latest offset; ambiguity recorded.
func TestDuplicateCandidatesAmbiguity(t *testing.T) {
	b := newBuilder()
	// two physical copies of object 5, xref declares the first
	b.put(1, 0, "<< /Type /Catalog >>")
	b.put(5, 0, "<< /Marker (first) >>")
	first5 := b.off[5]
	b.put(5, 0, "<< /Marker (second) >>")
	second5 := b.off[5] // builder overwrites; capture before second put instead
	b.off[5] = first5   // xref must name the first
	_ = second5
	b.classicXRef(6, nil, []int{1, 5}, 0, false, 1)
	r := scanBytes(t, "dup.pdf", b.bytes())
	var amb *Ambiguity
	for i := range r.Ambiguities {
		if r.Ambiguities[i].Key.Num == 5 {
			amb = &r.Ambiguities[i]
		}
	}
	if amb == nil {
		t.Fatalf("expected ambiguity for object 5, got %+v", r.Ambiguities)
	}
	if len(amb.IDs) < 2 {
		t.Fatalf("ambiguity should list declared+heuristic, got %v", amb.IDs)
	}
	// default must be the declared verified candidate, not the higher offset
	def := findCand(r, amb.DefaultID)
	if def == nil || def.Origin != "declared" || def.ByteRange.Start != int64(first5) {
		t.Fatalf("default should be declared first body at %d, got %+v", first5, def)
	}
}

func findCand(r *Report, id string) *Candidate {
	for _, c := range r.Candidates {
		if c.ID == id {
			return c
		}
	}
	return nil
}

// 8) trailing data after final %%EOF is reported.
func TestTrailingData(t *testing.T) {
	b := newBuilder()
	b.put(1, 0, "<< /Type /Catalog >>")
	b.classicXRef(2, nil, []int{1}, 0, false, 1)
	b.write("GARBAGE-NOT-REFERENCED-BY-XREF-1234567890")
	r := scanBytes(t, "trail.pdf", b.bytes())
	if !hasCode(r, "trailing-data") {
		t.Fatalf("expected trailing-data, got %v", diagCodes(r))
	}
	if r.Trailing.End-r.Trailing.Start != 42 {
		t.Fatalf("trailing range wrong: %+v", r.Trailing)
	}
}

// 9) decompression bomb halts at limit without exhausting resources.
func TestDecompressionLimit(t *testing.T) {
	// huge highly-compressible content in a stream labeled FlateDecode
	big := make([]byte, 4<<20)
	for i := range big {
		big[i] = 'A'
	}
	comp := zlibBytes(big)
	b := newBuilder()
	// place object 2 as the flate bomb, object 1 catalog references it
	b.putRaw(2, 0, fmt.Sprintf("<< /Length %d /Filter /FlateDecode >>\nstream\n", len(comp)))
	b.buf.Write(comp)
	b.write("\nendstream\nendobj\n")
	b.put(1, 0, "<< /Type /Catalog /Dests 2 0 R >>")
	b.classicXRef(3, nil, []int{1, 2}, 0, false, 1)
	lim := Limits{MaxExpand: 1 << 20} // 1 MiB cap vs 4 MiB payload
	r := scanBytes(t, "bomb.pdf", b.bytes(), lim)
	if !r.Limits.Halted {
		t.Fatalf("expected scan to report safe halt; limits=%+v diags=%v", r.Limits, diagCodes(r))
	}
}

// 10) mixed table + xref stream revisions in one Prev chain.
func TestMixedTableAndStream(t *testing.T) {
	b := newBuilder()
	b.put(1, 0, "<< /Type /Catalog >>")
	off1 := b.classicXRef(2, nil, []int{1}, 0, false, 1)
	b.put(1, 0, "<< /Type /Catalog /V 2 >>")
	ents := []sxEntry{{0, 0, 0}, {1, int64(b.off[1]), 0}}
	b.streamXRef(8, 2, ents, nil, off1, true)
	r := scanBytes(t, "mixed.pdf", b.bytes())
	if len(r.Revisions) != 2 || r.Revisions[0].Kind != "table" || r.Revisions[1].Kind != "stream" {
		t.Fatalf("mixed chain wrong: %+v diags=%v", r.Revisions, diagCodes(r))
	}
}
