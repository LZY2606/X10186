package main

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"

	"yemai/internal/forensic"
	"yemai/internal/store"
)

// slotChoice is the resolved candidate per (revision,num,gen) on a branch.
type slotChoice struct {
	Revision int
	Num, Gen int
	Cand     *forensic.Candidate
}

// resolveActive applies user decisions over the as-found defaults. For each
// revision each object version chooses the confirmed candidate; untouched
// slots keep a verified declared candidate (never "latest offset wins").
func resolveActive(rep *forensic.Report, eff map[string]*store.DecisionRow) []slotChoice {
	bySlot := map[string][]*forensic.Candidate{}
	var keys []string
	for _, c := range rep.Candidates {
		k := fmt.Sprintf("%d:%d:%d", c.Revision, c.Num, c.Gen)
		if _, ok := bySlot[k]; !ok {
			keys = append(keys, k)
		}
		bySlot[k] = append(bySlot[k], c)
	}
	sort.Strings(keys)
	var out []slotChoice
	for _, k := range keys {
		cands := bySlot[k]
		var rev, num, gen int
		fmt.Sscanf(k, "%d:%d:%d", &rev, &num, &gen)
		chosen := defaultChoice(cands)
		if d, ok := eff[k]; ok {
			for _, c := range cands {
				if c.ID == d.CandidateID {
					chosen = c
					break
				}
			}
		}
		if chosen != nil {
			out = append(out, slotChoice{rev, num, gen, chosen})
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Revision != out[j].Revision {
			return out[i].Revision < out[j].Revision
		}
		if out[i].Num != out[j].Num {
			return out[i].Num < out[j].Num
		}
		return out[i].Gen < out[j].Gen
	})
	return out
}

func defaultChoice(cands []*forensic.Candidate) *forensic.Candidate {
	var fallback *forensic.Candidate
	for _, c := range cands {
		if c.Origin == "declared" && c.HeaderOK {
			return c
		}
		if fallback == nil {
			fallback = c
		}
	}
	return fallback
}

func (s *Server) handleExport(w http.ResponseWriter, r *http.Request) {
	doc, ok := s.loadDoc(w, r)
	if !ok {
		return
	}
	data, err := os.ReadFile(s.st.OriginalPath(doc))
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	bid, _ := parseIntDefault(r.URL.Query().Get("branch"), 0)
	if bid == 0 {
		bid, _ = s.st.DefaultBranch(doc.ID)
	}
	eff, err := s.st.EffectiveDecisions(bid)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	active := resolveActive(doc.Report, eff)
	branches, _ := s.st.Branches(doc.ID)
	branchName := "默认分支（按声明）"
	for _, b := range branches {
		if b.ID == bid {
			branchName = b.Name
		}
	}

	zw := zip.NewWriter(w)
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition",
		fmt.Sprintf(`attachment; filename="yemai-doc%d-review.zip"`, doc.ID))

	put := func(name string, b []byte) error {
		h := &zip.FileHeader{Name: name, Method: zip.Deflate}
		h.SetMode(0o644)
		// mark UTF-8 names so Chinese checklist filenames decode correctly
		h.Flags |= 0x800
		f, err := zw.CreateHeader(h)
		if err != nil {
			return err
		}
		_, err = f.Write(b)
		return err
	}

	// 1) byte-exact original, never a re-saved PDF.
	if err := put("original/original.pdf", data); err != nil {
		return
	}
	// 2) full report JSON.
	rb, _ := json.MarshalIndent(doc.Report, "", "  ")
	if err := put("report.json", rb); err != nil {
		return
	}
	// 3) per-object bytes, sliced straight from the original.
	for _, sc := range active {
		c := sc.Cand
		dir := fmt.Sprintf("objects/rev%d/", c.Revision)
		name := fmt.Sprintf("%sobj-%d-gen%d-%s.bin", dir, c.Num, c.Gen, c.Origin)
		if c.Compressed {
			if len(c.MemberRaw) > 0 {
				if err := put("objects/decoded/"+fmt.Sprintf("obj-%d-gen%d-rev%d.decoded", c.Num, c.Gen, c.Revision), c.MemberRaw); err != nil {
					return
				}
			}
			continue
		}
		if c.ByteRange.Start >= 0 && c.ByteRange.End <= int64(len(data)) && c.ByteRange.End > c.ByteRange.Start {
			if err := put(name, data[c.ByteRange.Start:c.ByteRange.End]); err != nil {
				return
			}
		}
	}
	// 4) manifest.
	manifest := buildManifest(doc, bid, branchName, active)
	if err := put("manifest.json", manifest); err != nil {
		return
	}
	// 5) human review checklist.
	if err := put("REVIEW-审阅清单.txt", []byte(buildChecklist(doc, bid, branchName, active))); err != nil {
		return
	}
	if err := zw.Close(); err != nil {
		writeErr(w, 500, err.Error())
	}
}

func parseIntDefault(s string, def int64) (int64, error) {
	if s == "" {
		return def, nil
	}
	return strconvAtoi64(s)
}

func strconvAtoi64(s string) (int64, error) {
	var v int64
	_, err := fmt.Sscan(s, &v)
	return v, err
}

func buildManifest(doc *store.DocumentRow, bid int64, branchName string, active []slotChoice) []byte {
	type item struct {
		Revision int    `json:"revision"`
		Num      int    `json:"num"`
		Gen      int    `json:"gen"`
		CandID   string `json:"candidate_id"`
		Origin   string `json:"origin"`
		Source   string `json:"source"`
		Start    int64  `json:"start"`
		End      int64  `json:"end"`
		HeaderOK bool   `json:"header_ok"`
		Note     string `json:"note,omitempty"`
	}
	var items []item
	for _, sc := range active {
		c := sc.Cand
		items = append(items, item{c.Revision, c.Num, c.Gen, c.ID, c.Origin, c.Source,
			c.ByteRange.Start, c.ByteRange.End, c.HeaderOK, c.Note})
	}
	m := map[string]any{
		"tool":        "页脉探针 / yemai",
		"exported_at": time.Now().UTC().Format(time.RFC3339),
		"file_name":   doc.FileName,
		"size":        doc.Size,
		"sha256":      doc.SHA256,
		"branch_id":   bid,
		"branch_name": branchName,
		"note":        "original.pdf is the byte-exact import; evidence ranges index into it, not a re-saved file",
		"objects":     items,
	}
	b, _ := json.MarshalIndent(m, "", "  ")
	return b
}

func buildChecklist(doc *store.DocumentRow, bid int64, branchName string, active []slotChoice) string {
	var b strings.Builder
	b.WriteString("页脉探针 — PDF 增量修订审阅清单\n")
	b.WriteString("=====================================\n\n")
	fmt.Fprintf(&b, "文件: %s\n", doc.FileName)
	fmt.Fprintf(&b, "大小: %d 字节\n", doc.Size)
	fmt.Fprintf(&b, "SHA-256: %s\n", doc.SHA256)
	fmt.Fprintf(&b, "导出时间(UTC): %s\n", time.Now().UTC().Format(time.RFC3339))
	fmt.Fprintf(&b, "解释分支: #%d %s\n\n", bid, branchName)

	b.WriteString("一、修订时间轴\n")
	for _, rv := range doc.Report.Revisions {
		fmt.Fprintf(&b, "  [R%d] %-6s xref@%-8d size=%d", rv.Index, rv.Kind, rv.XRefOffset, rv.Size)
		if rv.PrevPresent {
			fmt.Fprintf(&b, " prev=%d", rv.PrevOffset)
		} else {
			b.WriteString(" prev=<无>")
		}
		if rv.Root != nil {
			fmt.Fprintf(&b, " root=%d %d R", rv.Root.Num, rv.Root.Gen)
		}
		b.WriteByte('\n')
	}
	b.WriteString("\n二、损坏诊断\n")
	if len(doc.Report.Diagnostics) == 0 {
		b.WriteString("  无\n")
	}
	for _, d := range doc.Report.Diagnostics {
		fmt.Fprintf(&b, "  [%s/%s] R%d bytes %d-%d: %s\n",
			strings.ToUpper(d.Severity), d.Code, d.Revision, d.At.Start, d.At.End, d.Message)
		if d.Evidence != "" {
			fmt.Fprintf(&b, "      证据片段: %q\n", d.Evidence)
		}
	}
	b.WriteString("\n三、按声明 vs 启发式（候选来源不可合并）\n")
	nd, nh := 0, 0
	for _, sc := range active {
		if sc.Cand.Origin == "declared" {
			nd++
		} else {
			nh++
		}
	}
	fmt.Fprintf(&b, "  已采用：按声明 %d 个；启发式 %d 个\n\n", nd, nh)

	b.WriteString("四、竞争候选（必须人工确认，系统不按偏移裁决）\n")
	if len(doc.Report.Ambiguities) == 0 {
		b.WriteString("  无\n")
	}
	for _, a := range doc.Report.Ambiguities {
		fmt.Fprintf(&b, "  对象 %d %d R%d: 候选 %s；默认建议 %s\n",
			a.Key.Num, a.Key.Gen, a.Revision, strings.Join(a.IDs, ", "), a.DefaultID)
		fmt.Fprintf(&b, "      %s\n", a.Note)
	}
	b.WriteString("\n五、本分支采用的对象版本\n")
	for _, sc := range active {
		c := sc.Cand
		tag := "声明"
		if c.Origin == "heuristic" {
			tag = "启发"
		}
		if c.Compressed {
			fmt.Fprintf(&b, "  R%d 对象 %d %d [%s/压缩] 容器 %d %d\n",
				c.Revision, c.Num, c.Gen, tag, c.Container.Num, c.Container.Gen)
		} else {
			fmt.Fprintf(&b, "  R%d 对象 %d %d [%s] 原始字节 %d-%d 头校验=%v\n",
				c.Revision, c.Num, c.Gen, tag, c.ByteRange.Start, c.ByteRange.End, c.HeaderOK)
		}
		if c.Note != "" {
			fmt.Fprintf(&b, "      备注: %s\n", c.Note)
		}
	}
	if doc.Report.Trailing.End > doc.Report.Trailing.Start {
		fmt.Fprintf(&b, "\n六、尾随数据: %d 字节位于 %d-%d（未被 xref 引用）\n",
			doc.Report.Trailing.End-doc.Report.Trailing.Start,
			doc.Report.Trailing.Start, doc.Report.Trailing.End)
	}
	b.WriteString("\n证据说明: original/original.pdf 为导入时逐字节保存的原件；")
	b.WriteString("objects/ 下的文件是从原件按偏移切出的对象字节；")
	b.WriteString("objects/decoded/ 仅包含在长度/Filter/边界全部核验后展开的 ObjStm 成员。")
	b.WriteString("不存在重新保存的 PDF 充当证据。\n")
	return b.String()
}

var _ = bytes.NewBuffer
