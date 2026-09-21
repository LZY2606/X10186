package pdf

// fixtures_pub.go: 供其它包测试/自检复用的小型 PDF 字节构造器。
// 仅生成确定性的最小 PDF，不依赖任何外部程序。

import (
	"bytes"
	"fmt"
	"sort"
	"strings"
)

// ClassicSegPublic 构造一段经典 xref table 段（公开版，供跨包测试）。
func ClassicSegPublic(baseLen, size int, rootRef string, inuse map[int]string,
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
	body.WriteString("xref\n0 ")
	body.WriteString(fmt.Sprintf("%d\n", size))
	for i := 0; i < size; i++ {
		if g, isFree := free[i]; isFree {
			body.WriteString(fmt.Sprintf("%010d %05d f \r\n", 0, g))
		} else if off, ok := offsets[i]; ok {
			body.WriteString(fmt.Sprintf("%010d %05d n \r\n", off, 0))
		} else {
			body.WriteString("0000000000 00000 f \r\n")
		}
	}
	body.WriteString("trailer\n<< /Size ")
	body.WriteString(fmt.Sprintf("%d /Root %s ", size, rootRef))
	if prev >= 0 {
		body.WriteString(fmt.Sprintf("/Prev %d ", prev))
	}
	body.WriteString(trailerExtra)
	body.WriteString(">>\nstartxref\n")
	body.WriteString(fmt.Sprintf("%d\n", xrefAbs))
	body.WriteString("%%EOF\n")
	return body.Bytes(), xrefAbs
}

// FixtureClassicPDF 最小单段 PDF。
func FixtureClassicPDF() []byte {
	header := []byte("%PDF-1.4\n%\xe2\xe3\xcf\xd3\n")
	objs := map[int]string{
		1: "<< /Type /Catalog /Pages 2 0 R >>",
		2: "<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		3: "<< /Type /Page /Parent 2 0 R /Contents 4 0 R /Resources << /Font << /F1 5 0 R >> >> >>",
		4: "<< /Length 33 >>\nstream\nBT /F1 12 Tf 72 720 Td (Hi) Tj ET\nendstream",
		5: "<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>",
	}
	seg, _ := ClassicSegPublic(len(header), 6, "1 0 R", objs, map[int]int{0: 65535}, -1, "")
	return append(header, seg...)
}

// FixtureDuplicateCandidates 构造“声明候选 + 同修订启发式候选竞争”的两段 PDF。
func FixtureDuplicateCandidates() []byte {
	base := FixtureClassicPDF()
	dup := []byte("4 0 obj\n<< /Length2 (duplicate-definition) >>\nendobj\n")
	rev1Objs := map[int]string{
		3: "<< /Type /Page /Parent 2 0 R /Contents 4 0 R /Annots [6 0 R] /Resources << /Font << /F1 5 0 R >> >> >>",
		4: "<< /Length 39 >>\nstream\nBT /F1 12 Tf 72 700 Td (Updated!) Tj ET\nendstream",
		6: "<< /Type /Annot /Subtype /Text /Rect [0 0 10 10] /Contents (note) >>",
	}
	nums := []int{3, 4, 6}
	var area bytes.Buffer
	offsets := map[int]int{}
	// 区域布局：先放重复对象（不进入 xref），再放真实对象
	dupLen := len(dup)
	for _, n := range nums {
		offsets[n] = len(base) + dupLen + area.Len()
		area.WriteString(fmt.Sprintf("%d 0 obj\n", n))
		area.WriteString(rev1Objs[n])
		area.WriteString("\nendobj\n")
	}
	objectArea := append(dup, area.Bytes()...)
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
	prev := bytes.Index(base, []byte("xref\n"))
	xb.WriteString(fmt.Sprintf("trailer\n<< /Size 7 /Root 1 0 R /Prev %d >>\n", prev))
	xb.WriteString("startxref\n")
	xb.WriteString(fmt.Sprintf("%d\n", xAbs))
	xb.WriteString("%%EOF\n")
	return append(append([]byte{}, base...), append(objectArea, xb.Bytes()...)...)
}
