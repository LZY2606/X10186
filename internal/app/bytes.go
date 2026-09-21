package app

import (
	"bytes"
	"compress/zlib"
	"fmt"
	"io"

	"pagevein/internal/pdf"
)

// CandidateBytes resolves the evidence bytes for one candidate.
// For ordinary objects these are the verbatim original file bytes.
// For compressed (ObjStm) objects, the host stream is expanded on demand
// (guarded by the same limit) and the object's slice of expanded content is
// returned; the original encoded host bytes are returned too.
type CandidateBytes struct {
	IsCompressed bool
	Original     []byte // verbatim bytes from the stored file
	Expanded     []byte // objstm content slice (only for compressed objects)
	Note         string
}

func (a *App) CandidateBytes(res *Result, cand *pdf.Candidate) (*CandidateBytes, error) {
	data := mustScanData(res)
	cb := &CandidateBytes{}
	if cand.Origin == pdf.OriginObjStm {
		cb.IsCompressed = true
		if cand.HostStart < 0 || cand.HostEnd <= cand.HostStart {
			return nil, fmt.Errorf("compressed object host range unavailable")
		}
		cb.Original = data[cand.HostStart:cand.HostEnd]
		zr, err := zlib.NewReader(bytes.NewReader(cb.Original))
		if err != nil {
			return nil, fmt.Errorf("zlib open: %w", err)
		}
		defer zr.Close()
		all, err := io.ReadAll(io.LimitReader(zr, int64(res.Scan.Limits.MaxExpandedBytes)+1))
		if err != nil {
			return nil, fmt.Errorf("zlib read: %w", err)
		}
		if len(all) > res.Scan.Limits.MaxExpandedBytes {
			return nil, fmt.Errorf("expansion exceeds limit of %d bytes (resource limit)", res.Scan.Limits.MaxExpandedBytes)
		}
		if cand.ByteStart < 0 || cand.ByteEnd > len(all) || cand.ByteStart >= cand.ByteEnd {
			return nil, fmt.Errorf("object range %d..%d invalid within expanded %d bytes", cand.ByteStart, cand.ByteEnd, len(all))
		}
		cb.Expanded = all[cand.ByteStart:cand.ByteEnd]
		cb.Note = fmt.Sprintf("decoded slice of verified /ObjStm %d (host file bytes %d..%d)", cand.HostNum, cand.HostStart, cand.HostEnd)
		return cb, nil
	}
	if cand.ByteStart < 0 || cand.ByteEnd < 0 || cand.ByteEnd <= cand.ByteStart {
		return nil, fmt.Errorf("candidate has no resolvable byte range")
	}
	if cand.ByteEnd > len(data) {
		return nil, fmt.Errorf("byte range %d..%d exceeds file size %d", cand.ByteStart, cand.ByteEnd, len(data))
	}
	cb.Original = data[cand.ByteStart:cand.ByteEnd]
	return cb, nil
}

// ReferencedBy returns reference edges whose target is (num, gen).
func ReferencedBy(res *Result, num, gen int) []pdf.Reference {
	var out []pdf.Reference
	for _, r := range res.Scan.References {
		if r.ToNum == num && r.ToGen == gen {
			out = append(out, r)
		}
	}
	return out
}

func mustScanData(res *Result) []byte {
	b, err := readOriginalPath(res.Document.Path)
	if err != nil {
		panic(err)
	}
	return b
}

func readOriginalPath(path string) ([]byte, error) {
	return readFile(path)
}
