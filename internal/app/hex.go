package app

import (
	"fmt"
	"os"
	"strings"
)

func readFile(path string) ([]byte, error) { return os.ReadFile(path) }

// HexDump renders a classic offset / hex / ascii view.
func HexDump(b []byte, base int, maxRows int) string {
	if maxRows <= 0 {
		maxRows = 64
	}
	var sb strings.Builder
	rows := 0
	for i := 0; i < len(b); i += 16 {
		if rows >= maxRows {
			sb.WriteString(fmt.Sprintf("... (%d more bytes truncated in view; full bytes in export)\n", len(b)-i))
			break
		}
		chunk := b[i:]
		if len(chunk) > 16 {
			chunk = chunk[:16]
		}
		sb.WriteString(fmt.Sprintf("%08x  ", base+i))
		var hex, ascii strings.Builder
		for j := 0; j < 16; j++ {
			if j < len(chunk) {
				hex.WriteString(fmt.Sprintf("%02x ", chunk[j]))
				c := chunk[j]
				if c >= 32 && c < 127 {
					ascii.WriteByte(c)
				} else {
					ascii.WriteByte('.')
				}
			} else {
				hex.WriteString("   ")
				ascii.WriteByte(' ')
			}
			if j == 7 {
				hex.WriteString(" ")
			}
		}
		sb.WriteString(hex.String())
		sb.WriteString(" |")
		sb.WriteString(ascii.String())
		sb.WriteString("|\n")
		rows++
	}
	return sb.String()
}
