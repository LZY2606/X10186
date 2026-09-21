package pdf

// pages.go: 以最新修订 Root 为起点解析页树（信息性展示，带循环保护）。

func (s *scanner) resolvePages() {
	if len(s.sections) == 0 {
		return
	}
	latest := s.sections[0]
	if latest.rootNum == 0 {
		return
	}
	root := s.effective(latest.rootNum, latest.rootGen, len(s.sections)-1)
	if root == nil {
		return
	}
	pages := root.Parsed.DictGet("Pages")
	if pages == nil || pages.Kind != "ref" {
		return
	}
	pagesNode := s.effective(pages.RefNum, pages.RefGen, root.Revision)
	if pagesNode == nil {
		return
	}
	visited := map[int]bool{}
	idx := 0
	var walk func(c *Candidate)
	walk = func(c *Candidate) {
		if c == nil || c.Parsed == nil || visited[c.ObjNum] {
			return
		}
		visited[c.ObjNum] = true
		typ := c.Parsed.DictGet("Type")
		if typ != nil && typ.Kind == "name" && typ.Text == "Page" {
			s.rep.Pages = append(s.rep.Pages, &PageInfo{
				Index: idx, ObjNum: c.ObjNum, Gen: c.Gen, Revision: c.Revision,
			})
			idx++
			return
		}
		kids := c.Parsed.DictGet("Kids")
		if kids == nil || kids.Kind != "array" {
			return
		}
		for _, k := range kids.Items {
			if k.Kind != "ref" {
				continue
			}
			child := s.effective(k.RefNum, k.RefGen, c.Revision)
			walk(child)
		}
	}
	walk(pagesNode)
}

// effective 选取不晚于 atRev 的最新有效候选（启发式不自动当选）。
func (s *scanner) effective(obj, gen, atRev int) *Candidate {
	var best *Candidate
	for _, c := range s.byObj[obj] {
		if !c.Valid || c.Kind != CandDeclared || c.Free {
			continue
		}
		if c.Revision <= atRev && (best == nil || c.Revision > best.Revision) {
			best = c
		}
	}
	return best
}
