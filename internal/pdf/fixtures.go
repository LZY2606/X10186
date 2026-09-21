package pdf

import (
	"bytes"
	"compress/zlib"
	"fmt"
	"strings"
)

// tb is a tiny byte buffer that records the offset at each checkpoint.
type tb struct{ b bytes.Buffer }

func (t *tb) w(s string) int {
	off := t.b.Len()
	t.b.WriteString(s)
	return off
}
func (t *tb) bytes() []byte { return t.b.Bytes() }

func xrefTable(entries []struct {
	num, f1, f2 int
	typ         byte
}, size int, root int, prev int, hasPrev bool) string {
	var sb strings.Builder
	sb.WriteString("xref\n")
	// one contiguous subsection 0..max
	max := 0
	for _, e := range entries {
		if e.num > max {
			max = e.num
		}
	}
	sb.WriteString(fmt.Sprintf("0 %d\n", max+1))
	byNum := map[int]struct {
		f1, f2 int
		typ    byte
	}{}
	for _, e := range entries {
		byNum[e.num] = struct {
			f1, f2 int
			typ    byte
		}{e.f1, e.f2, e.typ}
	}
	for i := 0; i <= max; i++ {
		e, ok := byNum[i]
		if !ok {
			e = struct {
				f1, f2 int
				typ    byte
			}{0, 65535, 'f'}
		}
		sb.WriteString(fmt.Sprintf("%010d %05d %c \n", e.f1, e.f2, e.typ))
	}
	sb.WriteString("trailer\n")
	sb.WriteString(fmt.Sprintf("<< /Size %d /Root %d 0 R", size, root))
	if hasPrev {
		sb.WriteString(fmt.Sprintf(" /Prev %d", prev))
	}
	sb.WriteString(" >>\nstartxref\n")
	return sb.String()
}

func flateBytes(s string) []byte {
	var buf bytes.Buffer
	zw := zlib.NewWriter(&buf)
	zw.Write([]byte(s))
	zw.Close()
	return buf.Bytes()
}

// buildIncrementalTablePDF constructs a two-revision PDF using classic xref tables.
func buildIncrementalTablePDF() []byte {
	var t tb
	t.w("%PDF-1.4\n%\xe2\xe3\xcf\xd3\n")
	off1 := t.w("1 0 obj\n<< /Type /Catalog /Pages 2 0 R >>\nendobj\n")
	off2 := t.w("2 0 obj\n<< /Type /Pages /Kids [3 0 R] /Count 1 >>\nendobj\n")
	off3 := t.w("3 0 obj\n<< /Type /Page /Parent 2 0 R >>\nendobj\n")
	xref1 := xrefTable([]struct {
		num, f1, f2 int
		typ         byte
	}{
		{1, off1, 0, 'n'}, {2, off2, 0, 'n'}, {3, off3, 0, 'n'},
	}, 4, 1, 0, false)
	sx1 := t.b.Len()
	t.w(xref1)
	t.w(fmt.Sprintf("%d\n%%%%EOF\n", sx1))

	// revision 2: add object 4 (annotation), catalog unchanged
	off4 := t.w("4 0 obj\n<< /Type /Annot /Subtype /Text /Contents (added in rev 1) >>\nendobj\n")
	xref2 := xrefTable([]struct {
		num, f1, f2 int
		typ         byte
	}{
		{1, off1, 0, 'n'}, {2, off2, 0, 'n'}, {3, off3, 0, 'n'}, {4, off4, 0, 'n'},
	}, 5, 1, sx1, true)
	sx2 := t.b.Len()
	t.w(xref2)
	t.w(fmt.Sprintf("%d\n%%%%EOF\n", sx2))
	return t.bytes()
}

// buildFreeEntryPDF includes a free entry for object 5 generation 2.
func buildFreeEntryPDF() []byte {
	var t tb
	t.w("%PDF-1.4\n")
	off1 := t.w("1 0 obj\n<< /Type /Catalog /Pages 2 0 R >>\nendobj\n")
	off2 := t.w("2 0 obj\n<< /Type /Pages /Kids [] /Count 0 >>\nendobj\n")
	x := xrefTable([]struct {
		num, f1, f2 int
		typ         byte
	}{
		{1, off1, 0, 'n'}, {2, off2, 0, 'n'}, {5, 0, 2, 'f'},
	}, 6, 1, 0, false)
	sx := t.b.Len()
	t.w(x)
	t.w(fmt.Sprintf("%d\n%%%%EOF\n", sx))
	return t.bytes()
}

// buildXRefStreamPDF constructs one revision with an xref stream and an ObjStm.
func buildXRefStreamPDF() []byte {
	obj4 := "<< /Type /Page /Parent 2 0 R >>"
	obj5 := "<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>"

	// Object-stream header is "4 0 5 <off>\n". The second object's offset
	// depends on the header length, which depends on the number of digits in
	// that offset. Search for a self-consistent width (1..6 digits).
	var hdr, content string
	first := 0
	found := false
	rel5 := len(obj4) + 1 // value5 relative offset = value4 length + separating newline
	for digits := 1; digits <= 6 && !found; digits++ {
		hdr = fmt.Sprintf("4 0 5 %0*d\n", digits, rel5)
		// value4 begins at /First, i.e. exactly after the pair header line
		content = hdr + obj4 + "\n" + obj5
		first = len(hdr)
		// value5 must really sit at first+rel5
		if first+rel5 == len(hdr)+len(obj4)+1 {
			found = true
		}
	}
	if !found {
		panic("cannot stabilize objstm header")
	}
	enc := flateBytes(content)

	var pre bytes.Buffer
	pre.WriteString("%PDF-1.5\n")
	off1 := pre.Len()
	pre.WriteString("1 0 obj\n<< /Type /Catalog /Pages 2 0 R >>\nendobj\n")
	off2 := pre.Len()
	pre.WriteString("2 0 obj\n<< /Type /Pages /Kids [4 0 R] /Count 1 >>\nendobj\n")
	off3 := pre.Len()
	pre.WriteString(fmt.Sprintf(
		"3 0 obj\n<< /Type /ObjStm /N 2 /First %d /Length %d /Filter /FlateDecode >>\nstream\n",
		first, len(enc)))
	pre.Write(enc)
	pre.WriteString("\nendstream\nendobj\n")

	off6 := pre.Len()
	type e struct{ t, a, b int }
	entries := []e{
		{0, 0, 0}, // object 0 free (gen 0)
		{1, 0, off1},
		{1, 0, off2},
		{1, 0, off3},
		{2, 3, 0}, // object 4 compressed in ObjStm 3, index 0
		{2, 3, 1}, // object 5 compressed in ObjStm 3, index 1
		{1, 0, off6},
	}
	var raw bytes.Buffer
	put := func(v int) {
		raw.WriteByte(byte(v >> 24))
		raw.WriteByte(byte(v >> 16))
		raw.WriteByte(byte(v >> 8))
		raw.WriteByte(byte(v))
	}
	for _, en := range entries {
		raw.WriteByte(byte(en.t))
		put(en.a)
		put(en.b)
	}
	xenc := flateBytes(raw.String())

	pre.WriteString(fmt.Sprintf(
		"6 0 obj\n<< /Type /XRef /Size 7 /Root 1 0 R /W [1 4 4] /Length %d /Filter /FlateDecode >>\nstream\n",
		len(xenc)))
	pre.Write(xenc)
	pre.WriteString("\nendstream\nendobj\n")
	pre.WriteString(fmt.Sprintf("startxref\n%d\n%%%%EOF\n", off6))
	return pre.Bytes()
}

// buildMixedXRefPDF builds revision 0 as a classic xref table and revision 1 as
// an xref stream (hybrid incremental update).
func buildMixedXRefPDF() []byte {
	var t tb
	t.w("%PDF-1.5\n")
	off1 := t.w("1 0 obj\n<< /Type /Catalog /Pages 2 0 R >>\nendobj\n")
	off2 := t.w("2 0 obj\n<< /Type /Pages /Kids [3 0 R] /Count 1 >>\nendobj\n")
	off3 := t.w("3 0 obj\n<< /Type /Page /Parent 2 0 R >>\nendobj\n")
	x0 := xrefTable([]struct {
		num, f1, f2 int
		typ         byte
	}{
		{1, off1, 0, 'n'}, {2, off2, 0, 'n'}, {3, off3, 0, 'n'},
	}, 4, 1, 0, false)
	sx0 := t.b.Len()
	t.w(x0)
	t.w(fmt.Sprintf("%d\n%%%%EOF\n", sx0))

	// revision 1: new object 4 plus xref stream object 5
	off4 := t.w("4 0 obj\n<< /Type /Annot /Contents (stream-rev) >>\nendobj\n")
	off5 := t.b.Len()
	type e struct{ t, a, b int }
	entries := []e{
		{0, 0, 0},
		{1, 0, off1},
		{1, 0, off2},
		{1, 0, off3},
		{1, 0, off4},
		{1, 0, off5},
	}
	var raw bytes.Buffer
	put := func(v int) {
		raw.WriteByte(byte(v >> 24))
		raw.WriteByte(byte(v >> 16))
		raw.WriteByte(byte(v >> 8))
		raw.WriteByte(byte(v))
	}
	for _, en := range entries {
		raw.WriteByte(byte(en.t))
		put(en.a)
		put(en.b)
	}
	xenc := flateBytes(raw.String())
	t.w(fmt.Sprintf("5 0 obj\n<< /Type /XRef /Size 6 /Root 1 0 R /Prev %d /W [1 4 4] /Length %d /Filter /FlateDecode >>\nstream\n", sx0, len(xenc)))
	t.b.Write(xenc)
	t.w("\nendstream\nendobj\n")
	t.w(fmt.Sprintf("startxref\n%d\n%%EOF\n", off5))
	return t.bytes()
}
