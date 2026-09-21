package store

import (
	"testing"

	"pagepulse/internal/pdf"
)

func TestImportBranchConfirmRestart(t *testing.T) {
	dir := t.TempDir()

	// 第一次“启动”：导入 + 创建分支 + 在分支上确认启发式候选
	data := pdf.FixtureDuplicateCandidates()
	run := func() (*Store, int64, int64) {
		st, err := Open(dir)
		if err != nil {
			t.Fatal(err)
		}
		docs, err := st.ListDocuments()
		if err != nil {
			t.Fatal(err)
		}
		var docID int64
		if len(docs) == 0 {
			rep := pdf.ScanFile(data, "dup.pdf", pdf.DefaultLimits())
			d, err := st.Import("dup.pdf", data, rep)
			if err != nil {
				t.Fatal(err)
			}
			docID = d.ID
		} else {
			docID = docs[0].ID
		}
		def, err := st.DefaultBranchID(docID)
		if err != nil {
			t.Fatal(err)
		}
		return st, docID, def
	}

	st, docID, def := run()

	// 默认分支：对象4 rev1 应存在冲突（声明 + 启发式两个有效候选）
	g, err := st.Resolve(docID, def)
	if err != nil {
		t.Fatal(err)
	}
	if !contains(g.Conflicts, "4:1") {
		t.Fatalf("默认分支应把 4:1 标记为竞争冲突, got conflicts=%v", g.Conflicts)
	}

	// 找启发式候选
	var heurID int
	doc, _ := st.GetDocument(docID)
	for _, c := range doc.Report.Candidates {
		if c.ObjNum == 4 && c.Revision == 1 && c.Kind == pdf.CandHeuristic {
			heurID = c.ID
		}
	}
	if heurID == 0 {
		t.Fatal("未找到启发式候选")
	}

	b, err := st.CreateBranch(docID, "采用重复定义", &def, "测试分支")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.ChooseCandidate(docID, b.ID, 4, 1, heurID, "人工采信启发式候选"); err != nil {
		t.Fatal(err)
	}
	st.Close()

	// 第二次“启动”：重新打开，分支与确认必须一致；旧默认分支仍可复现
	st2, docID2, def2 := run()
	defer st2.Close()
	if docID2 != docID {
		t.Fatalf("重启后文档 ID 变化: %d != %d", docID2, docID)
	}
	gDefault, err := st2.Resolve(docID2, def2)
	if err != nil {
		t.Fatal(err)
	}
	if !contains(gDefault.Conflicts, "4:1") {
		t.Fatal("旧默认分支重启后应仍保持冲突状态（未采信启发式）")
	}
	gBranch, err := st2.Resolve(docID2, b.ID)
	if err != nil {
		t.Fatal(err)
	}
	chosen := gBranch.EffectiveValueAt(4, 1)
	if chosen == nil || chosen.ID != heurID || chosen.Kind != pdf.CandHeuristic {
		t.Fatalf("分支重启后应选启发式候选 #%d, got %+v", heurID, chosen)
	}
	if contains(gBranch.Conflicts, "4:1") {
		t.Fatal("已确认分支不应再报告 4:1 冲突")
	}
	choices, err := st2.ListChoices(docID2, b.ID)
	if err != nil || len(choices) != 1 || choices[0].CandID != heurID {
		t.Fatalf("分支确认持久化错误: %+v", choices)
	}

	// 原件仍按字节存在且与导入一致
	d, _ := st2.GetDocument(docID2)
	orig, err := st2.ReadOriginal(d)
	if err != nil || len(orig) != len(data) {
		t.Fatalf("原件字节丢失或变化: len=%d want=%d", len(orig), len(data))
	}
}

func contains(ss []string, target string) bool {
	for _, s := range ss {
		if s == target {
			return true
		}
	}
	return false
}
