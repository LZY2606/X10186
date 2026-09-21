package main

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"

	"yemai/internal/forensic"
	"yemai/internal/store"
)

func (s *Server) handleList(w http.ResponseWriter, r *http.Request) {
	docs, err := s.st.ListDocuments()
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, docs, 200)
}

func (s *Server) handleUpload(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseMultipartForm(256 << 20); err != nil {
		writeErr(w, 400, "invalid multipart form: "+err.Error())
		return
	}
	file, fh, err := r.FormFile("file")
	if err != nil {
		writeErr(w, 400, "missing form field 'file'")
		return
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, 512<<20))
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	sc := forensic.NewScanner(s.lim)
	rep := sc.Scan(fh.Filename, data)
	doc, err := s.st.SaveDocument(fh.Filename, data, rep)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, doc, 201)
}

func (s *Server) loadDoc(w http.ResponseWriter, r *http.Request) (*store.DocumentRow, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeErr(w, 400, "bad document id")
		return nil, false
	}
	doc, err := s.st.GetDocument(id, true)
	if err != nil {
		writeErr(w, 404, "document not found")
		return nil, false
	}
	return doc, true
}

func (s *Server) handleGet(w http.ResponseWriter, r *http.Request) {
	doc, ok := s.loadDoc(w, r)
	if !ok {
		return
	}
	branches, _ := s.st.Branches(doc.ID)
	writeJSON(w, map[string]any{"document": doc, "branches": branches}, 200)
}

func (s *Server) handleBranches(w http.ResponseWriter, r *http.Request) {
	doc, ok := s.loadDoc(w, r)
	if !ok {
		return
	}
	bs, err := s.st.Branches(doc.ID)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, bs, 200)
}

func (s *Server) handleFork(w http.ResponseWriter, r *http.Request) {
	doc, ok := s.loadDoc(w, r)
	if !ok {
		return
	}
	var req struct {
		ParentID int64  `json:"parent_id"`
		Name     string `json:"name"`
		Note     string `json:"note"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	if strings.TrimSpace(req.Name) == "" {
		writeErr(w, 400, "branch name required")
		return
	}
	if req.ParentID == 0 {
		req.ParentID, _ = s.st.DefaultBranch(doc.ID)
	}
	b, err := s.st.ForkBranch(doc.ID, req.ParentID, req.Name, req.Note)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, b, 201)
}

func (s *Server) handleDecisions(w http.ResponseWriter, r *http.Request) {
	bid, err := strconv.ParseInt(r.PathValue("bid"), 10, 64)
	if err != nil {
		writeErr(w, 400, "bad branch id")
		return
	}
	eff, err := s.st.EffectiveDecisions(bid)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	own, err := s.st.Decisions(bid)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, map[string]any{"effective": eff, "own": own}, 200)
}

func (s *Server) handlePutDecision(w http.ResponseWriter, r *http.Request) {
	bid, err := strconv.ParseInt(r.PathValue("bid"), 10, 64)
	if err != nil {
		writeErr(w, 400, "bad branch id")
		return
	}
	var req struct {
		DocID       int64  `json:"doc_id"`
		Revision    int    `json:"revision"`
		ObjNum      int    `json:"obj_num"`
		ObjGen      int    `json:"obj_gen"`
		CandidateID string `json:"candidate_id"`
		Note        string `json:"note"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	doc, err := s.st.GetDocument(req.DocID, true)
	if err != nil {
		writeErr(w, 404, "document not found")
		return
	}
	if !candidateExists(doc.Report, req.CandidateID, req.Revision, req.ObjNum, req.ObjGen) {
		writeErr(w, 400, "candidate does not belong to this object/revision slot")
		return
	}
	if err := s.st.PutDecision(bid, req.DocID, req.Revision, req.ObjNum, req.ObjGen, req.CandidateID, req.Note); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, map[string]string{"status": "confirmed"}, 200)
}

func candidateExists(rep *forensic.Report, cid string, rev, num, gen int) bool {
	for _, c := range rep.Candidates {
		if c.ID == cid && c.Revision == rev && c.Num == num && c.Gen == gen {
			return true
		}
	}
	return false
}

// hexResponse is a 16-byte-per-line hex/ASCII dump of original file bytes.
type hexResponse struct {
	Start int64     `json:"start"`
	End   int64     `json:"end"`
	Lines []hexLine `json:"lines"`
}

type hexLine struct {
	Offset int64  `json:"offset"`
	Hex    string `json:"hex"`
	ASCII  string `json:"ascii"`
}

func (s *Server) handleHex(w http.ResponseWriter, r *http.Request) {
	doc, ok := s.loadDoc(w, r)
	if !ok {
		return
	}
	data, err := os.ReadFile(s.st.OriginalPath(doc))
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	start, _ := strconv.ParseInt(r.URL.Query().Get("start"), 10, 64)
	length, _ := strconv.ParseInt(r.URL.Query().Get("length"), 10, 64)
	if length <= 0 || length > 4096 {
		length = 256
	}
	if start < 0 {
		start = 0
	}
	if start > int64(len(data)) {
		start = int64(len(data))
	}
	end := start + length
	if end > int64(len(data)) {
		end = int64(len(data))
	}
	resp := hexResponse{Start: start, End: end}
	for p := start; p < end; {
		chunk := data[p:min64(p+16, end)]
		hexpairs := make([]string, len(chunk))
		ascii := make([]byte, len(chunk))
		for i, b := range chunk {
			hexpairs[i] = fmt.Sprintf("%02x", b)
			if b >= 32 && b < 127 {
				ascii[i] = b
			} else {
				ascii[i] = '.'
			}
		}
		resp.Lines = append(resp.Lines, hexLine{
			Offset: p, Hex: strings.Join(hexpairs, " "), ASCII: string(ascii),
		})
		p += int64(len(chunk))
	}
	writeJSON(w, resp, 200)
}

func (s *Server) handleCandidate(w http.ResponseWriter, r *http.Request) {
	doc, ok := s.loadDoc(w, r)
	if !ok {
		return
	}
	cid := r.PathValue("cid")
	for _, c := range doc.Report.Candidates {
		if c.ID == cid {
			writeJSON(w, c, 200)
			return
		}
	}
	writeErr(w, 404, "candidate not found")
}

func (s *Server) handleObjectBytes(w http.ResponseWriter, r *http.Request) {
	doc, ok := s.loadDoc(w, r)
	if !ok {
		return
	}
	cid := r.PathValue("cid")
	for _, c := range doc.Report.Candidates {
		if c.ID != cid {
			continue
		}
		name := fmt.Sprintf("obj-%d-%d-rev%d-%s.bin", c.Num, c.Gen, c.Revision, c.Origin)
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
		if c.Compressed {
			// Decoded member bytes are evidence only when all checks passed.
			if !c.HeaderOK || c.ByteRange.End <= c.ByteRange.Start {
				writeErr(w, 409, "compressed member was not verified/expanded")
				return
			}
			raw, err := s.readExpandedMember(doc, c)
			if err != nil {
				writeErr(w, 500, err.Error())
				return
			}
			w.Header().Set("X-Evidence-Source", "decoded-object-stream")
			w.Write(raw)
			return
		}
		data, err := os.ReadFile(s.st.OriginalPath(doc))
		if err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		if c.ByteRange.Start < 0 || c.ByteRange.End > int64(len(data)) || c.ByteRange.End < c.ByteRange.Start {
			writeErr(w, 409, "candidate has no valid original byte range")
			return
		}
		w.Header().Set("X-Evidence-Source", "original-bytes")
		w.Write(data[c.ByteRange.Start:c.ByteRange.End])
		return
	}
	writeErr(w, 404, "candidate not found")
}

func min64(a, b int64) int64 {
	if a < b {
		return a
	}
	return b
}

var _ = sort.Strings
var _ = hex.EncodedLen
