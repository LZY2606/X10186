package main

import (
	"os"

	"yemai/internal/forensic"
	"yemai/internal/store"
)

// readExpandedMember re-derives decoded member bytes from the byte-exact
// original by re-running the verified scanner and slicing the member range.
func (s *Server) readExpandedMember(doc *store.DocumentRow, c *forensic.Candidate) ([]byte, error) {
	data, err := os.ReadFile(s.st.OriginalPath(doc))
	if err != nil {
		return nil, err
	}
	sc := forensic.NewScanner(s.lim)
	rep := sc.Scan(doc.FileName, data)
	for _, cc := range rep.Candidates {
		if cc.Num == c.Num && cc.Gen == c.Gen && cc.Compressed &&
			cc.Revision == c.Revision && cc.HeaderOK && len(cc.MemberRaw) > 0 {
			return cc.MemberRaw, nil
		}
	}
	return nil, os.ErrNotExist
}
