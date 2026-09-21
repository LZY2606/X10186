// Package app orchestrates scanning, persistence, user decisions/branches and
// export for the page-vein probe. It never rewrites or "repairs" a PDF.
package app

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"pagevein/internal/pdf"
	"pagevein/internal/store"
)

type App struct {
	st      *store.Store
	dataDir string
	mu      sync.Mutex
}

func New(st *store.Store, dataDir string) (*App, error) {
	if err := os.MkdirAll(filepath.Join(dataDir, "originals"), 0o755); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Join(dataDir, "exports"), 0o755); err != nil {
		return nil, err
	}
	return &App{st: st, dataDir: dataDir}, nil
}

func newID(prefix string) string {
	var b [12]byte
	_, _ = rand.Read(b[:])
	return prefix + "_" + hex.EncodeToString(b[:])
}

// Import stores the bytes verbatim, scans them, and persists the result.
func (a *App) Import(name string, content []byte) (string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()

	sum := sha256.Sum256(content)
	id := newID("doc")
	origName := id + ".pdf"
	origPath := filepath.Join(a.dataDir, "originals", origName)
	if err := os.WriteFile(origPath, content, 0o644); err != nil {
		return "", err
	}
	sc := pdf.NewScanner(content, pdf.DefaultLimits())
	result := sc.Scan()
	raw, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return "", err
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	doc := store.Document{
		ID: id, Name: name, Size: int64(len(content)),
		SHA256: hex.EncodeToString(sum[:]), Path: origPath,
		ImportedAt: now, ScannedAt: result.ScannedAt, ScanJSON: raw,
	}
	if err := a.st.InsertDocument(doc); err != nil {
		return "", err
	}
	return id, nil
}

// Result is the scan plus persisted decisions and branches.
type Result struct {
	Document  store.Document   `json:"-"`
	Scan      *pdf.ScanResult  `json:"scan"`
	Decisions []store.Decision `json:"decisions"`
	Branches  []store.Branch   `json:"branches"`
}

func (a *App) load(id string) (*Result, error) {
	doc, err := a.st.GetDocument(id)
	if err != nil {
		return nil, err
	}
	if doc == nil {
		return nil, fmt.Errorf("document not found")
	}
	var scan pdf.ScanResult
	if err := json.Unmarshal(doc.ScanJSON, &scan); err != nil {
		return nil, err
	}
	decs, err := a.st.ListDecisions(id)
	if err != nil {
		return nil, err
	}
	branches, err := a.st.ListBranches(id)
	if err != nil {
		return nil, err
	}
	applyDecisions(&scan, decs)
	return &Result{Document: *doc, Scan: &scan, Decisions: decs, Branches: branches}, nil
}

func applyDecisions(scan *pdf.ScanResult, decs []store.Decision) {
	byCand := map[string]pdf.CandidateStatus{}
	for _, d := range decs {
		byCand[d.CandidateID] = pdf.CandidateStatus(d.Action)
	}
	for _, c := range scan.Candidates {
		if st, ok := byCand[c.ID]; ok {
			c.Status = st
		}
	}
}

func (a *App) GetResult(id string) (*Result, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.load(id)
}

func (a *App) ListDocuments() ([]store.Document, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.st.ListDocuments()
}

// Decide records a human confirmation/rejection for one candidate.
func (a *App) Decide(docID, candidateID, action, note string) error {
	if action != string(pdf.StatusConfirmed) && action != string(pdf.StatusRejected) {
		return fmt.Errorf("action must be confirmed or rejected")
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	res, err := a.load(docID)
	if err != nil {
		return err
	}
	if findCandidate(res.Scan, candidateID) == nil {
		return fmt.Errorf("candidate %s not found", candidateID)
	}
	return a.st.UpsertDecision(docID, store.Decision{
		CandidateID: candidateID, Action: action, Note: note,
		CreatedAt: time.Now().UTC().Format(time.RFC3339Nano),
	})
}

// CreateBranch forms an interpretation branch after the user chooses a candidate.
func (a *App) CreateBranch(docID string, num, gen int, chosenID, label, parentID string) (*store.Branch, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	res, err := a.load(docID)
	if err != nil {
		return nil, err
	}
	chosen := findCandidate(res.Scan, chosenID)
	if chosen == nil || chosen.Num != num || chosen.Gen != gen {
		return nil, fmt.Errorf("chosen candidate does not match object %d %d", num, gen)
	}
	if !chosen.Contested {
		return nil, fmt.Errorf("object %d %d has no competing candidates; a branch is not required", num, gen)
	}
	var rejected []string
	for _, c := range res.Scan.Candidates {
		if c.Num == num && c.Gen == gen && c.ID != chosenID && c.ByteStart >= 0 {
			rejected = append(rejected, c.ID)
		}
	}
	b := store.Branch{
		ID: newID("br"), DocID: docID, ParentID: parentID,
		Label: label, Num: num, Gen: gen, ChosenCandidateID: chosenID,
		RejectedCandidateIDs: joinCSV(rejected),
		CreatedAt:            time.Now().UTC().Format(time.RFC3339Nano),
	}
	if err := a.st.InsertBranch(b); err != nil {
		return nil, err
	}
	// the human choice is also recorded as a confirmed decision
	_ = a.st.UpsertDecision(docID, store.Decision{
		CandidateID: chosenID, Action: string(pdf.StatusConfirmed),
		Note:      "branch " + label,
		CreatedAt: time.Now().UTC().Format(time.RFC3339Nano),
	})
	for _, rid := range rejected {
		_ = a.st.UpsertDecision(docID, store.Decision{
			CandidateID: rid, Action: string(pdf.StatusRejected),
			Note:      "branch " + label,
			CreatedAt: time.Now().UTC().Format(time.RFC3339Nano),
		})
	}
	return &b, nil
}

func findCandidate(scan *pdf.ScanResult, id string) *pdf.Candidate {
	for _, c := range scan.Candidates {
		if c.ID == id {
			return c
		}
	}
	return nil
}

func joinCSV(xs []string) string {
	out := ""
	for i, x := range xs {
		if i > 0 {
			out += ","
		}
		out += x
	}
	return out
}

// OriginalBytes returns the verbatim stored file (never a re-saved PDF).
func (a *App) OriginalBytes(docID string) ([]byte, string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	doc, err := a.st.GetDocument(docID)
	if err != nil {
		return nil, "", err
	}
	if doc == nil {
		return nil, "", fmt.Errorf("document not found")
	}
	b, err := os.ReadFile(doc.Path)
	return b, doc.Name, err
}

// ObjectVersions groups competing versions of one object for the UI/API.
type ObjectVersionView struct {
	Num       int              `json:"num"`
	Gen       int              `json:"gen"`
	Contested bool             `json:"contested"`
	Versions  []*pdf.Candidate `json:"versions"`
}

func (a *App) Objects(res *Result) []ObjectVersionView {
	groups := map[string]*ObjectVersionView{}
	var order []string
	for _, c := range res.Scan.Candidates {
		key := fmt.Sprintf("%d:%d", c.Num, c.Gen)
		g, ok := groups[key]
		if !ok {
			g = &ObjectVersionView{Num: c.Num, Gen: c.Gen}
			groups[key] = g
			order = append(order, key)
		}
		g.Versions = append(g.Versions, c)
		g.Contested = g.Contested || c.Contested
	}
	sort.Slice(order, func(i, j int) bool {
		a := groups[order[i]]
		b := groups[order[j]]
		if a.Num != b.Num {
			return a.Num < b.Num
		}
		return a.Gen < b.Gen
	})
	out := make([]ObjectVersionView, 0, len(order))
	for _, k := range order {
		out = append(out, *groups[k])
	}
	return out
}
