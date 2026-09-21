package store

import "database/sql"

type Document struct {
	ID         string
	Name       string
	Size       int64
	SHA256     string
	Path       string
	ImportedAt string
	ScannedAt  string
	ScanJSON   []byte
}

type Decision struct {
	CandidateID string
	Action      string // confirmed | rejected
	Note        string
	CreatedAt   string
}

type Branch struct {
	ID                   string `json:"id"`
	DocID                string `json:"docId"`
	ParentID             string `json:"parentId"`
	Label                string `json:"label"`
	Num                  int    `json:"num"`
	Gen                  int    `json:"gen"`
	ChosenCandidateID    string `json:"chosenCandidateId"`
	RejectedCandidateIDs string `json:"rejectedIds"`
	CreatedAt            string `json:"createdAt"`
}

func (s *Store) InsertDocument(d Document) error {
	_, err := s.db.Exec(
		`INSERT INTO documents(id,name,size,sha256,path,imported_at,scanned_at,scan_json)
		 VALUES(?,?,?,?,?,?,?,?)`,
		d.ID, d.Name, d.Size, d.SHA256, d.Path, d.ImportedAt, d.ScannedAt, string(d.ScanJSON))
	return err
}

func (s *Store) ListDocuments() ([]Document, error) {
	rows, err := s.db.Query(
		`SELECT id,name,size,sha256,path,imported_at,scanned_at,scan_json FROM documents ORDER BY imported_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Document
	for rows.Next() {
		var d Document
		if err := rows.Scan(&d.ID, &d.Name, &d.Size, &d.SHA256, &d.Path, &d.ImportedAt, &d.ScannedAt, &d.ScanJSON); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

func (s *Store) GetDocument(id string) (*Document, error) {
	var d Document
	err := s.db.QueryRow(
		`SELECT id,name,size,sha256,path,imported_at,scanned_at,scan_json FROM documents WHERE id=?`, id).
		Scan(&d.ID, &d.Name, &d.Size, &d.SHA256, &d.Path, &d.ImportedAt, &d.ScannedAt, &d.ScanJSON)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &d, nil
}

func (s *Store) UpsertDecision(docID string, d Decision) error {
	_, err := s.db.Exec(
		`INSERT INTO decisions(doc_id,candidate_id,action,note,created_at)
		 VALUES(?,?,?,?,?)
		 ON CONFLICT(doc_id,candidate_id) DO UPDATE SET action=excluded.action, note=excluded.note, created_at=excluded.created_at`,
		docID, d.CandidateID, d.Action, d.Note, d.CreatedAt)
	return err
}

func (s *Store) ListDecisions(docID string) ([]Decision, error) {
	rows, err := s.db.Query(
		`SELECT candidate_id,action,note,created_at FROM decisions WHERE doc_id=? ORDER BY id`, docID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Decision
	for rows.Next() {
		var d Decision
		if err := rows.Scan(&d.CandidateID, &d.Action, &d.Note, &d.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

func (s *Store) InsertBranch(b Branch) error {
	_, err := s.db.Exec(
		`INSERT INTO branches(id,doc_id,parent_id,label,num,gen,chosen_candidate_id,rejected_candidate_ids,created_at)
		 VALUES(?,?,?,?,?,?,?,?,?)`,
		b.ID, b.DocID, b.ParentID, b.Label, b.Num, b.Gen, b.ChosenCandidateID, b.RejectedCandidateIDs, b.CreatedAt)
	return err
}

func (s *Store) ListBranches(docID string) ([]Branch, error) {
	rows, err := s.db.Query(
		`SELECT id,doc_id,parent_id,label,num,gen,chosen_candidate_id,rejected_candidate_ids,created_at
		 FROM branches WHERE doc_id=? ORDER BY created_at, id`, docID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Branch
	for rows.Next() {
		var b Branch
		if err := rows.Scan(&b.ID, &b.DocID, &b.ParentID, &b.Label, &b.Num, &b.Gen,
			&b.ChosenCandidateID, &b.RejectedCandidateIDs, &b.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}
