package pdf

import "fmt"

// Test-fixture primitives (also used by cross-package tests).
func encode20(off, gen int, typ byte) string {
	return fmt.Sprintf("%010d %05d %c \n", off, gen, typ)
}
func fmt20(off, gen int, typ byte) string { return encode20(off, gen, typ) }
func zeroPad(n int) string {
	s := fmt.Sprintf("%d", n)
	for len(s) < 10 {
		s = "0" + s
	}
	return s
}
