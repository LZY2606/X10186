package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"yemai/internal/forensic"
)

// ErrNotFound indicates a missing row.
var ErrNotFound = errors.New("not found")

// Store persists documents, reports and review decisions in SQLite while
// keeping the byte-exact original under the project data directory.
type Store struct {
	db  *sql.DB
	dir string
}

// DocumentRow is one imported PDF.
type DocumentRow struct {
	ID         int64            `json:"id"`
	FileName   string           `json:"file_name"`
	Size       int64            `json:"size"`
	SHA256     string           `json:"sha256"`
	Revisions  int              `json:"revisions"`
	Candidates int              `json:"candidates"`
	UploadedAt string           `json:"uploaded_at"`
	Report     *forensic.Report `json:"report,omitempty"`
	OrigPath   string           `json:"-"`
}

// BranchRow is one interpretation line. id=1 is the immutable "as-found" line.
type BranchRow struct {
	ID        int64         `json:"id"`
	DocID     int64         `json:"doc_id"`
	ParentID  sql.NullInt64 `json:"parent_id"`
	Name      string        `json:"name"`
	CreatedAt string        `json:"created_at"`
	Note      string        `json:"note"`
}

// DecisionRow records a user-confirmed candidate choice at a revision.
type DecisionRow struct {
	ID          int64  `json:"id"`
	BranchID    int64  `json:"branch_id"`
	DocID       int64  `json:"doc_id"`
	Revision    int    `json:"revision"`
	ObjNum      int    `json:"obj_num"`
	ObjGen      int    `json:"obj_gen"`
	CandidateID string `json:"candidate_id"`
	Note        string `json:"note"`
	CreatedAt   string `json:"created_at"`
}

// Open creates dataDir if needed and runs migrations.
func Open(dataDir string) (*Store, error) {
	if err := os.MkdirAll(filepath.Join(dataDir, "originals"), 0o755); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Join(dataDir, "objects"), 0o755); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite3", filepath.Join(dataDir, "yemai.db"))
	if err != nil {
		return nil, err
	}
	if _, err := db.ExecContext(context.Background(), `PRAGMA foreign_keys=ON; PRAGMA busy_timeout=5000;`); err != nil {
		db.Close()
		return nil, err
	}
	if _, err := db.ExecContext(context.Background(), schema); err != nil {
		db.Close()
		return nil, err
	}
	return &Store{db: db, dir: dataDir}, nil
}

func (s *Store) Close() error { return s.db.Close() }

const schema = `
CREATE TABLE IF NOT EXISTS documents (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  file_name TEXT NOT NULL,
  size INTEGER NOT NULL,
  sha256 TEXT NOT NULL,
  uploaded_at TEXT NOT NULL,
  orig_path TEXT NOT NULL,
  report_json TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS branches (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  doc_id INTEGER NOT NULL REFERENCES documents(id) ON DELETE CASCADE,
  parent_id INTEGER REFERENCES branches(id),
  name TEXT NOT NULL,
  note TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS decisions (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  branch_id INTEGER NOT NULL REFERENCES branches(id) ON DELETE CASCADE,
  doc_id INTEGER NOT NULL REFERENCES documents(id) ON DELETE CASCADE,
  revision INTEGER NOT NULL,
  obj_num INTEGER NOT NULL,
  obj_gen INTEGER NOT NULL,
  candidate_id TEXT NOT NULL,
  note TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL,
  UNIQUE(branch_id, revision, obj_num, obj_gen)
);
CREATE INDEX IF NOT EXISTS idx_dec_branch ON decisions(branch_id);
`

// SaveDocument writes the original bytes (byte-exact) and the report.
func (s *Store) SaveDocument(fileName string, data []byte, r *forensic.Report) (*DocumentRow, error) {
	now := time.Now().UTC().Format(time.RFC3339)
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	res, err := tx.Exec(
		`INSERT INTO documents(file_name,size,sha256,uploaded_at,orig_path,report_json) VALUES(?,?,?,?,?,?)`,
		fileName, len(data), r.SHA256, now, "", "")
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	origName := fmt.Sprintf("%d-%s", id, safeName(fileName))
	origPath := filepath.Join("originals", origName)
	absOrig := filepath.Join(s.dir, origPath)
	if err := os.WriteFile(absOrig, data, 0o644); err != nil {
		return nil, err
	}
	jb, err := json.Marshal(r)
	if err != nil {
		return nil, err
	}
	if _, err := tx.Exec(`UPDATE documents SET orig_path=?, report_json=? WHERE id=?`,
		origPath, string(jb), id); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(
		`INSERT INTO branches(doc_id,parent_id,name,note,created_at) VALUES(?,NULL,?,?,?)`,
		id, "默认分支（按声明）", "initial as-found interpretation", now); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return s.GetDocument(id, true)
}

func safeName(n string) string {
	out := make([]rune, 0, len(n))
	for _, c := range n {
		if c == '/' || c == '\\' || c == 0 {
			out = append(out, '_')
		} else {
			out = append(out, c)
		}
	}
	if len(out) == 0 {
		return "file.pdf"
	}
	return string(out)
}

// ListDocuments returns all imported documents without embedded reports.
func (s *Store) ListDocuments() ([]*DocumentRow, error) {
	rows, err := s.db.Query(
		`SELECT id,file_name,size,sha256,uploaded_at,orig_path FROM documents ORDER BY id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*DocumentRow
	for rows.Next() {
		d := &DocumentRow{}
		if err := rows.Scan(&d.ID, &d.FileName, &d.Size, &d.SHA256, &d.UploadedAt, &d.OrigPath); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// GetDocument loads one document; withReport includes the full scan.
func (s *Store) GetDocument(id int64, withReport bool) (*DocumentRow, error) {
	d := &DocumentRow{}
	var origPath, reportJSON string
	var err error
	if withReport {
		err = s.db.QueryRow(
			`SELECT id,file_name,size,sha256,uploaded_at,orig_path,report_json FROM documents WHERE id=?`,
			id).Scan(&d.ID, &d.FileName, &d.Size, &d.SHA256, &d.UploadedAt, &origPath, &reportJSON)
	} else {
		err = s.db.QueryRow(
			`SELECT id,file_name,size,sha256,uploaded_at,orig_path FROM documents WHERE id=?`,
			id).Scan(&d.ID, &d.FileName, &d.Size, &d.SHA256, &d.UploadedAt, &origPath)
	}
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	d.OrigPath = origPath
	if withReport {
		r := &forensic.Report{}
		if err := json.Unmarshal([]byte(reportJSON), r); err != nil {
			return nil, err
		}
		d.Report = r
		d.Revisions = len(r.Revisions)
		d.Candidates = len(r.Candidates)
	}
	return d, nil
}

// OriginalPath returns the absolute byte-exact copy path.
func (s *Store) OriginalPath(d *DocumentRow) string {
	if filepath.IsAbs(d.OrigPath) {
		return d.OrigPath
	}
	return filepath.Join(s.dir, d.OrigPath)
}

// ObjectDir returns the extracted-object directory for a document.
func (s *Store) ObjectDir(docID int64) string {
	return filepath.Join(s.dir, "objects", fmt.Sprintf("%d", docID))
}

// Branches lists a document's branches.
func (s *Store) Branches(docID int64) ([]*BranchRow, error) {
	rows, err := s.db.Query(
		`SELECT id,doc_id,parent_id,name,created_at,note FROM branches WHERE doc_id=? ORDER BY id`, docID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*BranchRow
	for rows.Next() {
		b := &BranchRow{}
		if err := rows.Scan(&b.ID, &b.DocID, &b.ParentID, &b.Name, &b.CreatedAt, &b.Note); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// DefaultBranch returns branch id 1 for a freshly imported document.
func (s *Store) DefaultBranch(docID int64) (int64, error) {
	var id int64
	err := s.db.QueryRow(`SELECT id FROM branches WHERE doc_id=? ORDER BY id LIMIT 1`, docID).Scan(&id)
	return id, err
}

// ForkBranch creates a new branch inheriting a parent's decisions. Decisions
// are copied; the parent stays reproducible forever.
func (s *Store) ForkBranch(docID, parentID int64, name, note string) (*BranchRow, error) {
	now := time.Now().UTC().Format(time.RFC3339)
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	res, err := tx.Exec(
		`INSERT INTO branches(doc_id,parent_id,name,note,created_at) VALUES(?,?,?,?,?)`,
		docID, parentID, name, note, now)
	if err != nil {
		return nil, err
	}
	newID, _ := res.LastInsertId()
	if _, err := tx.Exec(
		`INSERT INTO decisions(branch_id,doc_id,revision,obj_num,obj_gen,candidate_id,note,created_at)
		 SELECT ?,doc_id,revision,obj_num,obj_gen,candidate_id,note,? FROM decisions WHERE branch_id=?`,
		newID, now, parentID); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return &BranchRow{ID: newID, DocID: docID, ParentID: sql.NullInt64{Int64: parentID, Valid: true},
		Name: name, Note: note, CreatedAt: now}, nil
}

// PutDecision upserts a confirmed candidate choice for one object version.
func (s *Store) PutDecision(branchID, docID int64, rev, num, gen int, candID, note string) error {
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := s.db.Exec(
		`INSERT INTO decisions(branch_id,doc_id,revision,obj_num,obj_gen,candidate_id,note,created_at)
		 VALUES(?,?,?,?,?,?,?,?)
		 ON CONFLICT(branch_id,revision,obj_num,obj_gen)
		 DO UPDATE SET candidate_id=excluded.candidate_id, note=excluded.note, created_at=excluded.created_at`,
		branchID, docID, rev, num, gen, candID, note, now)
	return err
}

// Decisions lists the branch's own confirmed choices.
func (s *Store) Decisions(branchID int64) ([]*DecisionRow, error) {
	rows, err := s.db.Query(
		`SELECT id,branch_id,doc_id,revision,obj_num,obj_gen,candidate_id,note,created_at
		 FROM decisions WHERE branch_id=? ORDER BY revision,obj_num,obj_gen`, branchID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*DecisionRow
	for rows.Next() {
		d := &DecisionRow{}
		if err := rows.Scan(&d.ID, &d.BranchID, &d.DocID, &d.Revision, &d.ObjNum, &d.ObjGen,
			&d.CandidateID, &d.Note, &d.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// EffectiveDecisions walks branch ancestry and returns the final choice per
// (revision,obj); local choices override inherited ones.
func (s *Store) EffectiveDecisions(branchID int64) (map[string]*DecisionRow, error) {
	var chain []int64
	cur := branchID
	seen := map[int64]bool{}
	for cur != 0 {
		if seen[cur] { // defensive: corrupted parent cycle
			break
		}
		seen[cur] = true
		chain = append(chain, cur)
		var parent sql.NullInt64
		err := s.db.QueryRow(`SELECT parent_id FROM branches WHERE id=?`, cur).Scan(&parent)
		if errors.Is(err, sql.ErrNoRows) {
			break
		}
		if err != nil {
			return nil, err
		}
		if !parent.Valid {
			break
		}
		cur = parent.Int64
	}
	out := map[string]*DecisionRow{}
	// oldest ancestor first
	for i := len(chain) - 1; i >= 0; i-- {
		ds, err := s.Decisions(chain[i])
		if err != nil {
			return nil, err
		}
		for _, d := range ds {
			out[decisionKey(d.Revision, d.ObjNum, d.ObjGen)] = d
		}
	}
	return out, nil
}

func decisionKey(rev, num, gen int) string {
	return fmt.Sprintf("%d:%d:%d", rev, num, gen)
}
