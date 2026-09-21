package store

// export.go: 导出独立审阅清单(Markdown)与对象原始字节(ZIP)。
// 导出内容来自原件切片与扫描证据，不生成/重写任何 PDF。

import (
	"archive/zip"
	"bytes"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"
)

// ExportBundle 导出结果：清单文本 + ZIP 字节。
type ExportBundle struct {
	Manifest []byte
	Zip      []byte
}

// BuildExport 生成 ZIP 与清单。
func (st *Store) BuildExport(docID, branchID int64) (*ExportBundle, error) {
	doc, err := st.GetDocument(docID)
	if err != nil {
		return nil, err
	}
	g, err := st.Resolve(docID, branchID)
	if err != nil {
		return nil, err
	}
	data, err := st.ReadOriginal(doc)
	if err != nil {
		return nil, err
	}

	var zipBuf bytes.Buffer
	zw := zip.NewWriter(&zipBuf)

	put := func(name string, content []byte) error {
		w, err := zw.Create(name)
		if err != nil {
			return err
		}
		_, err = io.Copy(w, bytes.NewReader(content))
		return err
	}

	// 原件副本（证据基准），只写不重编码
	if err := put("original.bin", data); err != nil {
		return nil, err
	}

	var md bytes.Buffer
	fmt.Fprintf(&md, "# 页脉探针 · 审阅清单\n\n")
	fmt.Fprintf(&md, "- 文件：`%s`\n", doc.Name)
	fmt.Fprintf(&md, "- SHA-256：`%s`\n", doc.Hash)
	fmt.Fprintf(&md, "- 大小：%d 字节\n", doc.Size)
	fmt.Fprintf(&md, "- 导出时间：%s\n", time.Now().UTC().Format(time.RFC3339))
	fmt.Fprintf(&md, "- 修订数：%d（混合 xref：%v）\n", len(doc.Report.Revisions), doc.Report.MixedXRef)
	fmt.Fprintf(&md, "- 尾随数据：%d 字节\n\n", doc.Report.TrailingLen)

	fmt.Fprintf(&md, "## 修订时间轴\n\n")
	for _, rev := range doc.Report.Revisions {
		fmt.Fprintf(&md, "- 修订 %d · %s · xref@0x%X · Prev=%d · 对象区 [0x%X,0x%X)\n",
			rev.Index, rev.Kind, rev.XRefOffset, rev.Prev, rev.RegionStart, rev.RegionEnd)
	}

	fmt.Fprintf(&md, "\n## 分支与人工确认\n\n")
	branches, _ := st.ListBranches(docID)
	for _, b := range branches {
		mark := ""
		if b.ID == branchID {
			mark = " ←当前导出"
		}
		fmt.Fprintf(&md, "- 分支 #%d `%s`%s（父分支 #%v）%s\n", b.ID, b.Name, mark, b.ParentID, note(b.Note))
	}
	for _, ch := range g.Choices {
		fmt.Fprintf(&md, "  - 确认：对象 %d 修订 %d → 候选 #%d（%s）\n",
			ch.ObjNum, ch.Revision, ch.CandID, ch.CreatedAt)
	}
	if len(g.Conflicts) > 0 {
		fmt.Fprintf(&md, "\n**待人工确认的竞争：** %s\n\n", strings.Join(g.Conflicts, ", "))
	}

	fmt.Fprintf(&md, "\n## 损坏诊断\n\n")
	for _, d := range doc.Report.Diagnostics {
		fmt.Fprintf(&md, "- [%s] `%s` @0x%X：%s\n  - 证据：`%s`\n",
			strings.ToUpper(string(d.Level)), d.Code, d.Offset, d.Message, d.Evidence)
	}

	fmt.Fprintf(&md, "\n## 对象候选（字节证据）\n\n")
	objNums := make([]int, 0, len(g.Objects))
	for n := range g.Objects {
		objNums = append(objNums, n)
	}
	sort.Ints(objNums)
	for _, n := range objNums {
		ov := g.Objects[n]
		revs := make([]int, 0, len(ov.Revisions))
		for r := range ov.Revisions {
			revs = append(revs, r)
		}
		sort.Ints(revs)
		for _, r := range revs {
			for _, rc := range ov.Revisions[r] {
				c := rc.Candidate
				chosen := "　"
				if rc.Chosen {
					chosen = "✓"
				}
				free := ""
				if c.Free {
					free = " [FREE gen " + itoa(c.Gen) + "]"
				}
				fmt.Fprintf(&md, "- %s 对象 %d · 修订 %d · 候选 #%d[%s]%s\n",
					chosen, n, r, c.ID, c.Kind, free)
				fmt.Fprintf(&md, "  - 有效性：%v；字节范围：[0x%X,0x%X)；%s\n",
					c.Valid, c.Offset, c.EndOffset, c.Reason)
				fmt.Fprintf(&md, "  - 证据：%s\n", c.Evidence)
				if c.Stream {
					fmt.Fprintf(&md, "  - 流：filters=%v 核验长度=%d 展开=%v（%d 字节）%s\n",
						c.Filters, c.StreamLen, c.Expanded, c.ExpandedLen, c.ExpandNote)
				}
				if c.IsCompressed() {
					fmt.Fprintf(&md, "  - 压缩于 ObjStm %d（候选 #%d）内层[%d,%d)\n",
						c.ContainerObj, c.ContainerCand, c.InnerOff, c.InnerOff+c.InnerLen)
				}
				// 写出对象原始字节
				if !c.Free && !c.IsCompressed() && c.Valid && c.Offset >= 0 &&
					c.EndOffset > c.Offset && c.EndOffset <= len(data) {
					fname := fmt.Sprintf("objects/obj%d_rev%d_cand%d.bin", n, r, c.ID)
					if err := put(fname, data[c.Offset:c.EndOffset]); err != nil {
						return nil, err
					}
				}
			}
		}
	}
	fmt.Fprintf(&md, "\n> 本清单与对象字节均直接取自导入时按字节保存的原件，未经过任何 PDF 重写。\n")

	if err := put("REVIEW.md", md.Bytes()); err != nil {
		return nil, err
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return &ExportBundle{Manifest: md.Bytes(), Zip: zipBuf.Bytes()}, nil
}

func note(s string) string {
	if strings.TrimSpace(s) == "" {
		return ""
	}
	return " — " + s
}
