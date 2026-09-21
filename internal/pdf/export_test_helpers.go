package pdf

// Test-only fixture constructors exported for other packages.
func ExportTestIncremental() []byte { return buildIncrementalTablePDF() }
func ExportTestDuplicate() []byte {
	var tb tb
	tb.w("%PDF-1.4\n")
	off1 := tb.w("1 0 obj\n<< /Type /Catalog /Pages 2 0 R >>\nendobj\n")
	off2 := tb.w("2 0 obj\n<< /Type /Pages /Kids [3 0 R] /Count 1 >>\nendobj\n")
	off3a := tb.w("3 0 obj\n<< /Type /Page /V (A) >>\nendobj\n")
	off3b := tb.w("3 0 obj\n<< /Type /Page /V (B) >>\nendobj\n")
	data := tb.bytes()
	var x = struct {
		b []byte
		w func(string) int
	}{}
	_ = x
	var buf []byte
	line := func(s string) { buf = append(buf, []byte(s)...) }
	xoff := len(data)
	line("xref\n0 4\n")
	line(encode20(0, 65535, 'f'))
	line(encode20(off1, 0, 'n'))
	line(encode20(off2, 0, 'n'))
	line(encode20(off3a, 0, 'n'))
	line("3 1\n")
	line(encode20(off3b, 0, 'n'))
	line("trailer\n<< /Size 4 /Root 1 0 R >>\nstartxref\n")
	data = append(data, buf...)
	data = append(data, []byte(itoa(xoff))...)
	data = append(data, '\n')
	data = append(data, []byte("%%EOF\n")...)
	return data
}

func ExportTestXRefStream() []byte { return buildXRefStreamPDF() }
