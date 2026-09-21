// Package web serves the single-page probe UI and its JSON API.
package web

import (
	"embed"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"strconv"
	"strings"

	"pagevein/internal/app"
	"pagevein/internal/pdf"
)

//go:embed static/*
var staticFS embed.FS

type Server struct {
	app *app.App
	mux *http.ServeMux
}

func New(a *app.App) *Server {
	s := &Server{app: a, mux: http.NewServeMux()}
	s.routes()
	return s
}

func (s *Server) Handler() http.Handler { return s.mux }

func subStatic() fs.FS {
	sub, err := fs.Sub(staticFS, "static")
	if err != nil {
		panic(err)
	}
	return sub
}

func (s *Server) routes() {
	s.mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServerFS(subStatic())))
	s.mux.HandleFunc("GET /", s.index)
	s.mux.HandleFunc("GET /api/documents", s.listDocs)
	s.mux.HandleFunc("POST /api/documents", s.upload)
	s.mux.HandleFunc("GET /api/documents/{id}", s.getDoc)
	s.mux.HandleFunc("POST /api/documents/{id}/decide", s.decide)
	s.mux.HandleFunc("POST /api/documents/{id}/branches", s.createBranch)
	s.mux.HandleFunc("GET /api/documents/{id}/objects/{num}/{gen}", s.object)
	s.mux.HandleFunc("GET /api/documents/{id}/candidates/{cid}/bytes", s.candBytes)
	s.mux.HandleFunc("GET /api/documents/{id}/export", s.exportBundle)
}

func (s *Server) index(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	b, err := staticFS.ReadFile("static/index.html")
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(b)
}

func writeJSON(w http.ResponseWriter, v any, code int) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, map[string]string{"error": msg}, code)
}

func (s *Server) listDocs(w http.ResponseWriter, r *http.Request) {
	docs, err := s.app.ListDocuments()
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	type item struct {
		ID         string `json:"id"`
		Name       string `json:"name"`
		Size       int64  `json:"size"`
		ImportedAt string `json:"importedAt"`
		SHA256     string `json:"sha256"`
	}
	out := make([]item, 0, len(docs))
	for _, d := range docs {
		out = append(out, item{d.ID, d.Name, d.Size, d.ImportedAt, d.SHA256})
	}
	writeJSON(w, out, 200)
}

func (s *Server) upload(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseMultipartForm(256 << 20); err != nil {
		writeErr(w, 400, "invalid multipart form: "+err.Error())
		return
	}
	file, hdr, err := r.FormFile("file")
	if err != nil {
		writeErr(w, 400, "missing form field 'file'")
		return
	}
	defer file.Close()
	content, err := io.ReadAll(io.LimitReader(file, 512<<20))
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	id, err := s.app.Import(hdr.Filename, content)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, map[string]string{"id": id}, 201)
}

func (s *Server) getDoc(w http.ResponseWriter, r *http.Request) {
	res, err := s.app.GetResult(r.PathValue("id"))
	if err != nil {
		writeErr(w, 404, err.Error())
		return
	}
	writeJSON(w, buildViewModel(res, s.app.Objects(res)), 200)
}

type decisionVM struct {
	CandidateID string `json:"candidateId"`
	Action      string `json:"action"`
	Note        string `json:"note"`
}

type branchVM struct {
	ID                string `json:"id"`
	ParentID          string `json:"parentId"`
	Label             string `json:"label"`
	Num               int    `json:"num"`
	Gen               int    `json:"gen"`
	ChosenCandidateID string `json:"chosenCandidateId"`
	RejectedIDs       string `json:"rejectedIds"`
	CreatedAt         string `json:"createdAt"`
}

type candidateVM = pdf.Candidate

type objectVM struct {
	Num       int              `json:"num"`
	Gen       int              `json:"gen"`
	Contested bool             `json:"contested"`
	Versions  []*pdf.Candidate `json:"versions"`
}

type viewModel struct {
	DocumentID   string             `json:"documentId"`
	Name         string             `json:"name"`
	Size         int64              `json:"size"`
	SHA256       string             `json:"sha256"`
	ImportedAt   string             `json:"importedAt"`
	ScannedAt    string             `json:"scannedAt"`
	Revisions    []pdf.Revision     `json:"revisions"`
	Objects      []objectVM         `json:"objects"`
	References   []pdf.Reference    `json:"references"`
	Diagnostics  []pdf.Diagnostic   `json:"diagnostics"`
	TrailingData []pdf.TrailingBlob `json:"trailingData"`
	Limits       pdf.Limits         `json:"limits"`
	Decisions    []decisionVM       `json:"decisions"`
	Branches     []branchVM         `json:"branches"`
}

func orEmptyRev(x []pdf.Revision) []pdf.Revision {
	if x == nil {
		return []pdf.Revision{}
	}
	return x
}
func orEmptyRef(x []pdf.Reference) []pdf.Reference {
	if x == nil {
		return []pdf.Reference{}
	}
	return x
}
func orEmptyDiag(x []pdf.Diagnostic) []pdf.Diagnostic {
	if x == nil {
		return []pdf.Diagnostic{}
	}
	return x
}
func orEmptyTrail(x []pdf.TrailingBlob) []pdf.TrailingBlob {
	if x == nil {
		return []pdf.TrailingBlob{}
	}
	return x
}

func buildViewModel(res *app.Result, objs []app.ObjectVersionView) *viewModel {
	vm := &viewModel{
		DocumentID:   res.Document.ID,
		Name:         res.Document.Name,
		Size:         res.Document.Size,
		SHA256:       res.Document.SHA256,
		ImportedAt:   res.Document.ImportedAt,
		ScannedAt:    res.Document.ScannedAt,
		Revisions:    orEmptyRev(res.Scan.Revisions),
		References:   orEmptyRef(res.Scan.References),
		Diagnostics:  orEmptyDiag(res.Scan.Diagnostics),
		TrailingData: orEmptyTrail(res.Scan.TrailingData),
		Limits:       res.Scan.Limits,
		Objects:      []objectVM{},
		Decisions:    []decisionVM{},
		Branches:     []branchVM{},
	}
	for _, o := range objs {
		vm.Objects = append(vm.Objects, objectVM{Num: o.Num, Gen: o.Gen, Contested: o.Contested, Versions: o.Versions})
	}
	for _, d := range res.Decisions {
		vm.Decisions = append(vm.Decisions, decisionVM{d.CandidateID, d.Action, d.Note})
	}
	for _, b := range res.Branches {
		vm.Branches = append(vm.Branches, branchVM{
			b.ID, b.ParentID, b.Label, b.Num, b.Gen, b.ChosenCandidateID,
			b.RejectedCandidateIDs, b.CreatedAt,
		})
	}
	return vm
}

func (s *Server) decide(w http.ResponseWriter, r *http.Request) {
	var body struct {
		CandidateID string `json:"candidateId"`
		Action      string `json:"action"`
		Note        string `json:"note"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	if err := s.app.Decide(r.PathValue("id"), body.CandidateID, body.Action, body.Note); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	writeJSON(w, map[string]bool{"ok": true}, 200)
}

func (s *Server) createBranch(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Num      int    `json:"num"`
		Gen      int    `json:"gen"`
		ChosenID string `json:"chosenCandidateId"`
		Label    string `json:"label"`
		ParentID string `json:"parentId"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	if strings.TrimSpace(body.Label) == "" {
		body.Label = fmt.Sprintf("分支 obj%d g%d", body.Num, body.Gen)
	}
	b, err := s.app.CreateBranch(r.PathValue("id"), body.Num, body.Gen, body.ChosenID, body.Label, body.ParentID)
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	writeJSON(w, b, 201)
}

func (s *Server) object(w http.ResponseWriter, r *http.Request) {
	num, err1 := strconv.Atoi(r.PathValue("num"))
	gen, err2 := strconv.Atoi(r.PathValue("gen"))
	if err1 != nil || err2 != nil {
		writeErr(w, 400, "bad object number/generation")
		return
	}
	res, err := s.app.GetResult(r.PathValue("id"))
	if err != nil {
		writeErr(w, 404, err.Error())
		return
	}
	var versions []*pdf.Candidate
	contested := false
	for _, o := range s.app.Objects(res) {
		if o.Num == num && o.Gen == gen {
			versions = o.Versions
			contested = o.Contested
		}
	}
	if versions == nil {
		writeErr(w, 404, "object not found")
		return
	}
	writeJSON(w, map[string]any{
		"num": num, "gen": gen, "contested": contested,
		"versions": versions, "referencedBy": app.ReferencedBy(res, num, gen),
	}, 200)
}

func (s *Server) candBytes(w http.ResponseWriter, r *http.Request) {
	res, err := s.app.GetResult(r.PathValue("id"))
	if err != nil {
		writeErr(w, 404, err.Error())
		return
	}
	cid := r.PathValue("cid")
	var cand *pdf.Candidate
	for _, c := range res.Scan.Candidates {
		if c.ID == cid {
			cand = c
		}
	}
	if cand == nil {
		writeErr(w, 404, "candidate not found")
		return
	}
	cb, err := s.app.CandidateBytes(res, cand)
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	mode := r.URL.Query().Get("format")
	if mode == "raw" {
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Write(cb.Original)
		return
	}
	writeJSON(w, map[string]any{
		"isCompressed": cb.IsCompressed,
		"note":         cb.Note,
		"originalHex":  app.HexDump(cb.Original, safeBase(cand), 256),
		"expandedHex":  hexOrEmpty(cb),
		"length":       len(cb.Original),
		"expandedLen":  len(cb.Expanded),
	}, 200)
}

func safeBase(c *pdf.Candidate) int {
	if c.HostNum > 0 {
		return c.HostStart
	}
	return c.ByteStart
}

func hexOrEmpty(cb *app.CandidateBytes) string {
	if cb == nil || cb.Expanded == nil {
		return ""
	}
	return app.HexDump(cb.Expanded, 0, 256)
}

func (s *Server) exportBundle(w http.ResponseWriter, r *http.Request) {
	res, err := s.app.GetResult(r.PathValue("id"))
	if err != nil {
		writeErr(w, 404, err.Error())
		return
	}
	f, err := s.app.BuildReviewBundle(res)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", `attachment; filename="review.zip"`)
	w.Write(f.Content)
}
