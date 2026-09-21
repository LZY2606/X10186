package store

// resolve.go: 把“扫描候选 + 分支确认”解析成某个解释分支下的对象版本图。
// 规则确定性、纯函数，重启后结果一致；绝不按最晚偏移自动决定竞争。

import (
	"sort"

	"pagepulse/internal/pdf"
)

// ResolvedCandidate 是分支视角下的候选（带是否被选中标记）。
type ResolvedCandidate struct {
	*pdf.Candidate
	Chosen bool `json:"chosen"`
}

// ObjectView 一个对象在各修订的版本视图。
type ObjectView struct {
	ObjNum    int                          `json:"objNum"`
	Revisions map[int][]*ResolvedCandidate `json:"revisions"`
}

// ResolvedGraph 分支下的解析结果。
type ResolvedGraph struct {
	DocID    int64               `json:"docId"`
	BranchID int64               `json:"branchId"`
	Objects  map[int]*ObjectView `json:"objects"`
	Choices  []*Choice           `json:"choices"`
	// conflictKeys 仍需用户确认的竞争键 "obj:rev"
	Conflicts []string `json:"conflicts"`
}

// Resolve 计算分支下的有效版本。选择优先级：
//  1. 分支上对 (obj,rev) 的最新人工确认；
//  2. 无确认时：唯一的“按声明找到且有效”候选自动当选；
//     若存在多个竞争候选，则不自动决定，标记为冲突。
func (st *Store) Resolve(docID, branchID int64) (*ResolvedGraph, error) {
	doc, err := st.GetDocument(docID)
	if err != nil {
		return nil, err
	}
	if err := st.branchExists(docID, branchID); err != nil {
		return nil, err
	}
	choices, err := st.ListChoices(docID, branchID)
	if err != nil {
		return nil, err
	}
	// 同一键最新确认生效
	chosen := map[[2]int]int{} // (obj,rev)->candID
	for _, c := range choices {
		chosen[[2]int{c.ObjNum, c.Revision}] = c.CandID
	}

	g := &ResolvedGraph{
		DocID: docID, BranchID: branchID,
		Objects: map[int]*ObjectView{}, Choices: choices,
	}
	byObjRev := map[[2]int][]*pdf.Candidate{}
	for _, cand := range doc.Report.Candidates {
		key := [2]int{cand.ObjNum, cand.Revision}
		byObjRev[key] = append(byObjRev[key], cand)
		ov := g.Objects[cand.ObjNum]
		if ov == nil {
			ov = &ObjectView{ObjNum: cand.ObjNum, Revisions: map[int][]*ResolvedCandidate{}}
			g.Objects[cand.ObjNum] = ov
		}
	}
	keys := make([][2]int, 0, len(byObjRev))
	for k := range byObjRev {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i][0] != keys[j][0] {
			return keys[i][0] < keys[j][0]
		}
		return keys[i][1] < keys[j][1]
	})
	for _, key := range keys {
		cands := byObjRev[key]
		view := g.Objects[key[0]]
		var rc []*ResolvedCandidate
		var validDeclared, validAny []*pdf.Candidate
		for _, c := range cands {
			if c.Valid {
				validAny = append(validAny, c)
				if c.Kind == pdf.CandDeclared && !c.Free {
					validDeclared = append(validDeclared, c)
				}
			}
			rc = append(rc, &ResolvedCandidate{Candidate: c})
		}
		// 人工确认
		if cid, ok := chosen[key]; ok {
			for _, r := range rc {
				r.Chosen = r.ID == cid
			}
		} else {
			// 默认自动：恰好一个声明有效候选
			if len(validDeclared) == 1 && len(validAny) == 1 {
				for _, r := range rc {
					if r.ID == validDeclared[0].ID {
						r.Chosen = true
					}
				}
			} else if len(validAny) > 1 {
				// 存在启发式竞争或多声明竞争，不自动决定
				g.Conflicts = append(g.Conflicts, conflictKey(key[0], key[1]))
			}
		}
		view.Revisions[key[1]] = rc
	}
	sort.Strings(g.Conflicts)
	return g, nil
}

func conflictKey(obj, rev int) string {
	return itoa(obj) + ":" + itoa(rev)
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}

// EffectiveValueAt 返回对象在分支下、不晚于 atRev 的最新被选有效候选。
func (g *ResolvedGraph) EffectiveValueAt(obj, atRev int) *pdf.Candidate {
	ov := g.Objects[obj]
	if ov == nil {
		return nil
	}
	revs := make([]int, 0, len(ov.Revisions))
	for r := range ov.Revisions {
		revs = append(revs, r)
	}
	sort.Ints(revs)
	var best *pdf.Candidate
	for _, r := range revs {
		if r > atRev {
			continue
		}
		for _, rc := range ov.Revisions[r] {
			if rc.Chosen && rc.Valid && !rc.Free {
				best = rc.Candidate
			}
		}
	}
	return best
}
