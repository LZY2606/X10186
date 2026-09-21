package app

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"fmt"
	"path"
	"sort"
	"strings"
	"time"

	"pagevein/internal/pdf"
)

type ExportFile struct {
	Name    string
	Content []byte
}

// BuildReviewBundle creates a self-contained review package:
//   - original.pdf           : the verbatim imported bytes (never re-saved)
//   - manifest.json          : revisions, candidates, references, diagnostics,
//     decisions and branches
//   - objects/<...>.bin      : the exact evidence bytes of each version
//   - objects/<...>.hex.txt  : human hex view
//   - README.txt             : provenance and limits
//
// It deliberately never embeds a rewritten/repaired PDF as evidence.
func (a *App) BuildReviewBundle(res *Result) (*ExportFile, error) {
	a.mu.Lock()
	data, err := readOriginalPath(res.Document.Path)
	a.mu.Unlock()
	if err != nil {
		return nil, err
	}

	var zipBuf bytes.Buffer
	zw := zip.NewWriter(&zipBuf)

	write := func(name string, b []byte) error {
		w, err := zw.Create(name)
		if err != nil {
			return err
		}
		_, err = w.Write(b)
		return err
	}

	if err := write("original.pdf", data); err != nil {
		return nil, err
	}

	// manifest: scan + decisions + provenance
	manifest := map[string]interface{}{
		"document": map[string]interface{}{
			"id":         res.Document.ID,
			"name":       res.Document.Name,
			"size":       res.Document.Size,
			"sha256":     res.Document.SHA256,
			"importedAt": res.Document.ImportedAt,
			"scannedAt":  res.Document.ScannedAt,
		},
		"scan":       res.Scan,
		"decisions":  res.Decisions,
		"branches":   res.Branches,
		"exportedAt": time.Now().UTC().Format(time.RFC3339Nano),
		"note":       "Evidence is the verbatim original.pdf and extracted byte slices. No re-saved PDF is used as evidence.",
	}
	mj, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return nil, err
	}
	if err := write("manifest.json", mj); err != nil {
		return nil, err
	}

	// object version bytes
	cands := append([]*pdf.Candidate(nil), res.Scan.Candidates...)
	sort.Slice(cands, func(i, j int) bool {
		if cands[i].Num != cands[j].Num {
			return cands[i].Num < cands[j].Num
		}
		if cands[i].Gen != cands[j].Gen {
			return cands[i].Gen < cands[j].Gen
		}
		return cands[i].ID < cands[j].ID
	})
	var index []string
	index = append(index, "object evidence files (one per candidate/version):")
	for _, c := range cands {
		if c.Origin == pdf.OriginFreeDecl {
			continue
		}
		cb, err := a.CandidateBytes(res, c)
		if err != nil {
			index = append(index, fmt.Sprintf("# %s: unavailable (%v)", c.ID, err))
			continue
		}
		base := fmt.Sprintf("objects/obj%d_g%d_rev%d_%s", c.Num, c.Gen, c.Revision, string(c.Origin))
		binName := base + ".bin"
		if err := write(binName, cb.Original); err != nil {
			return nil, err
		}
		hexName := base + ".hex.txt"
		dump := HexDump(cb.Original, c.ByteStart, 4096)
		if cb.IsCompressed {
			dump = "# encoded host stream slice (original file bytes)\n" + dump +
				"\n# expanded object stream content for this object:\n" +
				HexDump(cb.Expanded, c.ByteStart, 4096)
		}
		if err := write(hexName, []byte(dump)); err != nil {
			return nil, err
		}
		index = append(index, fmt.Sprintf("- object %d gen %d rev %d origin=%s status=%s contested=%v -> %s | %s",
			c.Num, c.Gen, c.Revision, c.Origin, c.Status, c.Contested, binName, hexName))
	}
	if err := write("objects/INDEX.txt", []byte(strings.Join(index, "\n")+"\n")); err != nil {
		return nil, err
	}

	readme := strings.Join([]string{
		"页脉探针 / Page-Vein Probe review bundle",
		"",
		"original.pdf = byte-for-byte imported file. It is the only file evidence source;",
		"the probe never rewrites, normalizes or repairs it.",
		"",
		"manifest.json = every revision, candidate (declared vs heuristic vs objstm),",
		"references, corruption diagnostics, human decisions and interpretation branches.",
		"",
		"objects/*.bin = exact byte slices for each object version.",
		"objects/*.hex.txt = offset/hex/ascii view of those slices.",
		"",
		"When two candidates compete for one object number+generation, the winner is the",
		"human-confirmed choice recorded under decisions/branches; older branches remain",
		"reproducible from this same bundle.",
		"",
	}, "\n")
	if err := write("README.txt", []byte(readme)); err != nil {
		return nil, err
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	name := sanitizeName(res.Document.Name)
	return &ExportFile{
		Name:    path.Base(strings.TrimSuffix(name, ".pdf")) + "-review.zip",
		Content: zipBuf.Bytes(),
	}, nil
}

func sanitizeName(name string) string {
	name = path.Base(name)
	name = strings.ReplaceAll(name, " ", "_")
	var sb strings.Builder
	for _, r := range name {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' || r == '-' || r == '.' {
			sb.WriteRune(r)
		} else {
			sb.WriteByte('_')
		}
	}
	if sb.Len() == 0 {
		return "document.pdf"
	}
	return sb.String()
}
