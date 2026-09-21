package main

import (
	"os"

	"pagevein/internal/pdf"
)

func main() {
	dir := "/tmp/pvfix"
	_ = os.MkdirAll(dir, 0o755)
	_ = os.WriteFile(dir+"/inc.pdf", pdf.ExportTestIncremental(), 0o644)
	_ = os.WriteFile(dir+"/dup.pdf", pdf.ExportTestDuplicate(), 0o644)
	_ = os.WriteFile(dir+"/xrefstm.pdf", pdf.ExportTestXRefStream(), 0o644)
}
