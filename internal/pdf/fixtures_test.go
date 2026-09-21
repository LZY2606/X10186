package pdf

// fixtures_test.go: 构造小型 PDF 字节用于测试，不依赖任何外部 PDF 程序。

import (
	"bytes"
	"compress/zlib"
	"fmt"
	"sort"
	"strings"
)

// classicSeg 构造一段经典 xref table 段；baseLen 为该段在文件中的起点，
// inuse/free 的对象号与代次按 size 连续输出。返回段字节与 xref 绝对偏移。
func classicSeg(baseLen, size int, rootRef string, inuse map[int]string,
	free map[int]int, prev int, trailerExtra string) ([]byte, int) {

	nums := make([]int, 0, len(inuse))
	for n := range inuse {
		nums = append(nums, n)
	}
	sort.Ints(nums)

	var body bytes.Buffer
	offsets := map[int]int{}
	for _, n := range nums {
		offsets[n] = baseLen + body.Len()
		body.WriteString(fmt.Sprintf("%d 0 obj\n", n))
		body.WriteString(inuse[n])
		if !strings.HasSuffix(inuse[n], "\n") {
			body.WriteString("\n")
		}
		body.WriteString("endobj\n")
	}
	xrefAbs := baseLen + body.Len()
	body.WriteString("xref\n")
	body.WriteString(fmt.Sprintf("0 %d\n", size))
	for i := 0; i < size; i++ {
		if g, isFree := free[i]; isFree {
			next := 0
			body.WriteString(fmt.Sprintf("%010d %05d f \r\n", next, g))
		} else if off, ok := offsets[i]; ok {
			body.WriteString(fmt.Sprintf("%010d %05d n \r\n", off, 0))
		} else {
			body.WriteString(fmt.Sprintf("%010d %05d f \r\n", 0, 0))
		}
	}
	body.WriteString("trailer\n")
	tr := fmt.Sprintf("<< /Size %d /Root %s ", size, rootRef)
	if prev >= 0 {
		tr += fmt.Sprintf("/Prev %d ", prev)
	}
	tr += trailerExtra + ">>\n"
	body.WriteString(tr)
	body.WriteString("startxref\n")
	body.WriteString(fmt.Sprintf("%d\n", xrefAbs))
	body.WriteString("%%EOF\n")
	return body.Bytes(), xrefAbs
}

func baseObjects() map[int]string {
	return map[int]string{
		1: "<< /Type /Catalog /Pages 2 0 R >>",
		2: "<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		3: "<< /Type /Page /Parent 2 0 R /Contents 4 0 R /Resources << /Font << /F1 5 0 R >> >> >>",
		4: "<< /Length 33 >>\nstream\nBT /F1 12 Tf 72 720 Td (Hi) Tj ET\nendstream",
		5: "<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>",
	}
}

func classicPDF() []byte {
	header := []byte("%PDF-1.4\n%\xe2\xe3\xcf\xd3\n")
	seg, _ := classicSeg(len(header), 6, "1 0 R", baseObjects(), map[int]int{0: 65535}, -1, "")
	return append(header, seg...)
}

// incrementalPDF 两修订：rev1 更新对象 3/4 并新增 6。
func incrementalPDF() []byte {
	base := classicPDF()
	rev1 := map[int]string{
		3: "<< /Type /Page /Parent 2 0 R /Contents 4 0 R /Annots [6 0 R] /Resources << /Font << /F1 5 0 R >> >> >>",
		4: "<< /Length 39 >>\nstream\nBT /F1 12 Tf 72 700 Td (Updated!) Tj ET\nendstream",
		6: "<< /Type /Annot /Subtype /Text /Rect [0 0 10 10] /Contents (note) >>",
	}
	prev := bytes.Index(base, []byte("xref\n"))
	seg, _ := classicSeg(len(base), 7, "1 0 R", rev1, map[int]int{0: 65535}, prev, "")
	return append(append([]byte{}, base...), seg...)
}

// freeGenPDF 第三段释放对象 6（gen 升 1）并新增对象 7。
func freeGenPDF() []byte {
	base := incrementalPDF()
	prev := lastStartXRef(base)
	rev2 := map[int]string{
		7: "<< /Type /Annot /Subtype /Text /Rect [1 1 9 9] /Contents (replacement) >>",
	}
	free := map[int]int{0: 65535, 6: 1}
	seg, _ := classicSeg(len(base), 8, "1 0 R", rev2, free, prev, "")
	return append(append([]byte{}, base...), seg...)
}

func lastStartXRef(b []byte) int {
	idx := bytes.LastIndex(b, []byte("startxref"))
	var n int
	fmt.Sscan(string(b[idx+len("startxref"):]), &n)
	return n
}

func flateBytes(in []byte) []byte {
	var b bytes.Buffer
	w := zlib.NewWriter(&b)
	w.Write(in)
	w.Close()
	return b.Bytes()
}

// xrefStreamPDF 单段 PDF-1.5：ObjStm(8) 压缩 2..5，xref stream 为对象 1。
func xrefStreamPDF() []byte {
	var inner bytes.Buffer
	mark := func() int { return inner.Len() }
	o2 := mark()
	inner.WriteString("<< /Type /Catalog /Pages 3 0 R >>")
	o3 := mark()
	inner.WriteString("<< /Type /Pages /Kids [4 0 R] /Count 1 >>")
	o4 := mark()
	inner.WriteString("<< /Type /Page /Parent 3 0 R /Resources << /Font << /F1 5 0 R >> >> >>")
	o5 := mark()
	inner.WriteString("<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>")
	header := fmt.Sprintf("2 %d 3 %d 4 %d 5 %d ", o2, o3, o4, o5)
	first := len(header)
	objStmInner := append([]byte(header), inner.Bytes()...)
	comp := flateBytes(objStmInner)

	var b bytes.Buffer
	b.WriteString("%PDF-1.5\n")
	off8 := b.Len()
	b.WriteString(fmt.Sprintf("8 0 obj\n<< /Type /ObjStm /N 4 /First %d /Length %d /Filter /FlateDecode >>\nstream\n",
		first, len(comp)))
	b.Write(comp)
	b.WriteString("\nendstream\nendobj\n")
	off1 := b.Len()

	// W [1 3 1]
	type e3 struct{ a, f1, f2 int }
	entries := []e3{
		{0, 0, 0},                                  // 0 free
		{1, off1, 0},                               // 1 xref stream
		{2, 8, 0}, {2, 8, 1}, {2, 8, 2}, {2, 8, 3}, // 2..5 compressed
		{0, 0, 0},    // 6 free
		{0, 0, 0},    // 7 free
		{1, off8, 0}, // 8 ObjStm
	}
	var eb bytes.Buffer
	for _, e := range entries {
		eb.WriteByte(byte(e.a))
		eb.WriteByte(byte(e.f1 >> 16))
		eb.WriteByte(byte(e.f1 >> 8))
		eb.WriteByte(byte(e.f1))
		eb.WriteByte(byte(e.f2))
	}
	xc := flateBytes(eb.Bytes())
	b.WriteString(fmt.Sprintf("1 0 obj\n<< /Type /XRef /Size 9 /Root 2 0 R /W [1 3 1] /Length %d /Filter /FlateDecode >>\nstream\n", len(xc)))
	b.Write(xc)
	b.WriteString("\nendstream\nendobj\n")
	b.WriteString(fmt.Sprintf("startxref\n%d\n%%%%EOF\n", off1))
	return b.Bytes()
}

// mixedXRefPDF 先 xref stream 段，再追加经典 table 段（Prev 回指 stream）。
func mixedXRefPDF() []byte {
	base := xrefStreamPDF()
	prev := lastStartXRef(base)
	rev1 := map[int]string{
		6: "<< /Type /Annot /Subtype /Text /Rect [0 0 1 1] /Contents (mixed) >>",
	}
	seg, _ := classicSeg(len(base), 9, "2 0 R", rev1, map[int]int{0: 65535}, prev, "")
	return append(append([]byte{}, base...), seg...)
}

// badOffsetPDF 把 rev1 中对象 4 的 xref 偏移改成指向 "xref" 关键字（非对象头）。
func badOffsetPDF() []byte {
	base := []byte(incrementalPDF())
	idx := bytes.LastIndex(base, []byte("startxref"))
	var xoff int
	fmt.Sscan(string(base[idx+len("startxref"):]), &xoff)
	// 在 rev1 xref 表里找到对象 4 的条目并改成 xoff（xref 关键字，非对象头）
	prefix := []byte("xref\n0 7\n")
	pAt := bytes.Index(base[xoff:], prefix)
	if pAt < 0 {
		return base
	}
	target := xoff + pAt + len(prefix) + 4*21
	copy(base[target:target+10], []byte(fmt.Sprintf("%010d", xoff)))
	return base
}

// prevCyclePDF rev1 的 Prev 指回自己。
func prevCyclePDF() []byte {
	base := incrementalPDF()
	idx := bytes.LastIndex(base, []byte("startxref"))
	var self int
	fmt.Sscan(string(base[idx+len("startxref"):]), &self)
	// 替换 rev1 trailer 中的 /Prev 值
	revXref := self
	trailerAt := bytes.Index(base[revXref:], []byte("trailer"))
	zone := base[revXref+trailerAt:]
	m := bytes.Index(zone, []byte("/Prev "))
	if m >= 0 {
		start := revXref + trailerAt + m + len("/Prev ")
		end := start
		for end < len(base) && base[end] >= '0' && base[end] <= '9' {
			end++
		}
		copy(base[start:end], []byte(fmt.Sprintf("%d", self)))
	}
	return base
}

// duplicateCandidatePDF 在 rev1 区域内、xref 之前插入另一个 "4 0 obj"，
// 但 xref 仍指向原对象 -> 声明候选与启发式候选在同修订竞争。
// 插入会改变其后偏移，因此整段重建。
func duplicateCandidatePDF() []byte {
	base := classicPDF()
	dup := []byte("4 0 obj\n<< /Length2 (duplicate-definition) >>\nendobj\n")
	rev1Objs := map[int]string{
		3: "<< /Type /Page /Parent 2 0 R /Contents 4 0 R /Annots [6 0 R] /Resources << /Font << /F1 5 0 R >> >> >>",
		4: "<< /Length 33 >>\nstream\nBT /F1 12 Tf 72 700 Td (Updated!) Tj ET\nendstream",
		6: "<< /Type /Annot /Subtype /Text /Rect [0 0 10 10] /Contents (note) >>",
	}
	nums := []int{3, 4, 6}
	var body bytes.Buffer
	offsets := map[int]int{}
	for _, n := range nums {
		offsets[n] = len(base) + body.Len() + len(dup)
		body.WriteString(fmt.Sprintf("%d 0 obj\n", n))
		body.WriteString(rev1Objs[n])
		body.WriteString("\nendobj\n")
	}
	// 对象区：真实对象，然后插入未被 xref 指向的重复对象头
	objectArea := body.Bytes()
	objectArea = append(objectArea, dup...)

	xAbs := len(base) + len(objectArea)
	var xb bytes.Buffer
	xb.WriteString("xref\n0 7\n")
	for i := 0; i < 7; i++ {
		switch i {
		case 0:
			xb.WriteString("0000000000 65535 f \r\n")
		case 3, 4, 6:
			xb.WriteString(fmt.Sprintf("%010d 00000 n \r\n", offsets[i]))
		default:
			xb.WriteString("0000000000 00000 f \r\n")
		}
	}
	xb.WriteString("trailer\n")
	xb.WriteString(fmt.Sprintf("<< /Size 7 /Root 1 0 R /Prev %d >>\n", bytes.Index(base, []byte("xref\n"))))
	xb.WriteString("startxref\n")
	xb.WriteString(fmt.Sprintf("%d\n", xAbs))
	xb.WriteString("%%EOF\n")
	seg := append(objectArea, xb.Bytes()...)
	return append(append([]byte{}, base...), seg...)
}

// trailingPDF 最新 %%EOF 后追加任意数据。
func trailingPDF() []byte {
	base := classicPDF()
	return append(base, []byte("\nGARBAGE-NOT-REFERENCED-BY-ANY-XREF\n")...)
}

// bombPDF 单段 PDF：内容流是高压缩零数据，声明解压后 5MB；测试用小限额触发安全停止。
func bombPDF() []byte {
	declared := 5 << 20
	zeros := make([]byte, declared)
	comp := flateBytes(zeros)
	objs := map[int]string{
		1: "<< /Type /Catalog /Pages 2 0 R >>",
		2: "<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		3: "<< /Type /Page /Parent 2 0 R /Contents 4 0 R >>",
		4: fmt.Sprintf("<< /Length %d /Filter /FlateDecode >>\nstream\n", len(comp)),
	}
	// 对象4需要把压缩字节放进流，classicSeg 不支持二进制；手工拼。
	var b bytes.Buffer
	b.WriteString("%PDF-1.4\n")
	off := map[int]int{}
	writeObj := func(n int, body []byte) {
		off[n] = b.Len()
		b.WriteString(fmt.Sprintf("%d 0 obj\n", n))
		b.Write(body)
		if body[len(body)-1] != '\n' {
			b.WriteString("\n")
		}
		b.WriteString("endobj\n")
	}
	writeObj(1, []byte(objs[1]))
	writeObj(2, []byte(objs[2]))
	writeObj(3, []byte(objs[3]))
	off[4] = b.Len()
	b.WriteString("4 0 obj\n<< /Length ")
	b.WriteString(fmt.Sprintf("%d", len(comp)))
	b.WriteString(" /Filter /FlateDecode >>\nstream\n")
	b.Write(comp)
	b.WriteString("\nendstream\nendobj\n")
	xref := b.Len()
	b.WriteString("xref\n0 5\n")
	for i := 0; i < 5; i++ {
		if i == 0 {
			b.WriteString(fmt.Sprintf("%010d 65535 f \n", 0))
		} else {
			b.WriteString(fmt.Sprintf("%010d 00000 n \n", off[i]))
		}
	}
	b.WriteString("trailer\n<< /Size 5 /Root 1 0 R >>\nstartxref\n")
	b.WriteString(fmt.Sprintf("%d\n", xref))
	b.WriteString("%%EOF\n")
	return b.Bytes()
}
