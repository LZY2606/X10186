package pdf

import (
	"bytes"
	"compress/zlib"
	"io"
	"testing"
)

func mustZlib(t *testing.T, b []byte) []byte {
	t.Helper()
	zr, err := zlib.NewReader(bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	out, err := io.ReadAll(zr)
	if err != nil {
		t.Fatal(err)
	}
	return out
}
