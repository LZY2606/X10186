package store

import (
	"bytes"
	"fmt"
	"os"
)

// buildTwoRevs creates a PDF where object 5 physically appears twice and the
// xref declares the first copy, leaving the second as a heuristic competing
// candidate (the ambiguity scenario that requires confirmation).
func buildTwoRevs() []byte {
	var b bytes.Buffer
	b.WriteString("%PDF-1.7\n")
	off := map[string]int{}
	put := func(num, gen int, body string) {
		off[fmt.Sprintf("%d", num)] = b.Len()
		fmt.Fprintf(&b, "%d %d obj\n%s\nendobj\n", num, gen, body)
	}
	put(1, 0, "<< /Type /Catalog >>")
	put(5, 0, "<< /Marker (first) >>")
	first5 := off["5"]
	put(5, 0, "<< /Marker (second) >>")
	xoff := b.Len()
	b.WriteString("xref\n0 2\n")
	fmt.Fprintf(&b, "0000000000 65535 f \n")
	fmt.Fprintf(&b, "%010d 00000 n \n", off["1"])
	b.WriteString("5 1\n")
	fmt.Fprintf(&b, "%010d 00000 n \n", first5) // declare the FIRST body
	fmt.Fprintf(&b, "trailer\n<< /Size 6 /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", xoff)
	return b.Bytes()
}

func readFileAll(p string) ([]byte, error) {
	return os.ReadFile(p)
}
