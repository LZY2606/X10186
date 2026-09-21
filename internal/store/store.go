package store

// store.go: SQLite 持久层。保存原始文件路径、扫描报告、解释分支与人工确认。
// 原始 PDF 永不重写；确认只追加选择记录，旧分支始终可复现。

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"

	"pagepulse/internal/pdf"
)

// Store 页脉探针的数据存储。
type Store struct {
	db  *sql.DB
	dir string
}

// Document 一条导入记录。
type Document struct {
	ID         int64       `json:"id"`
	Name       string      `json:"name"`
	Hash       string      `json:"hash"`
	Size       int         `json:"size"`
	OrigPath   string      `json:"origPath"`
	ImportedAt time.Time   `json:"importedAt"`
	Report     *pdf.Report `json:"report,omitempty"`
}

const schema = `
CREATE TABLE IF NOT EXISTS documents (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	name TEXT NOT NULL,
	hash TEXT NOT NULL,
	size INTEGER NOT NULL,
	orig_path TEXT NOT NULL,
	imported_at TEXT NOT NULL,
	report_json TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS branches (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	doc_id INTEGER NOT NULL,
	name TEXT NOT NULL,
	parent_id INTEGER,
	created_at TEXT NOT NULL,
	note TEXT DEFAULT '',
	UNIQUE(doc_id, name)
);
CREATE TABLE IF NOT EXISTS choices (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	doc_id INTEGER NOT NULL,
	branch_id INTEGER NOT NULL,
	obj_num INTEGER NOT NULL,
	revision INTEGER NOT NULL,
	cand_id INTEGER NOT NULL,
	created_at TEXT NOT NULL,
	note TEXT DEFAULT ''
);
`

// Open 打开（必要时创建）数据库与原件目录。
func Open(dataDir string) (*Store, error) {
	if err := os.MkdirAll(filepath.Join(dataDir, "originals"), 0o755); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", filepath.Join(dataDir, "pagepulse.db"))
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, err
	}
	return &Store{db: db, dir: dataDir}, nil
}

// Close 关闭数据库。
func (st *Store) Close() error { return st.db.Close() }

// Import 保存原件字节并持久化扫描报告，同时建立默认分支。永不重写原件。
func (st *Store) Import(name string, data []byte, report *pdf.Report) (*Document, error) {
	hash := sha256Hex(data)
	origPath := filepath.Join(st.dir, "originals", hash[:16]+"_"+sanitize(name))
	if err := os.WriteFile(origPath, data, 0o644); err != nil {
		return nil, err
	}
	// 落盘后把原件设为只读，从文件系统层面防止探针自身误改/重写。
	if err := os.Chmod(origPath, 0o444); err != nil {
		return nil, err
	}
	reportJSON, err := json.Marshal(report)
	if err != nil {
		return nil, err
	}
	ctx := context.Background()
	tx, err := st.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx,
		`INSERT INTO documents(name,hash,size,orig_path,imported_at,report_json) VALUES(?,?,?,?,?,?)`,
		name, hash, len(data), origPath, time.Now().UTC().Format(time.RFC3339), string(reportJSON))
	if err != nil {
		return nil, err
	}
	docID, _ := res.LastInsertId()
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO branches(doc_id,name,parent_id,created_at,note) VALUES(?,?,?,?,?)`,
		docID, "default", nil, time.Now().UTC().Format(time.RFC3339), "导入时自动选择：按声明找到的有效候选"); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return &Document{ID: docID, Name: name, Hash: hash, Size: len(data), OrigPath: origPath, Report: report}, nil
}

// ListDocuments 列出全部导入（不含大报告字段）。
func (st *Store) ListDocuments() ([]*Document, error) {
	rows, err := st.db.Query(`SELECT id,name,hash,size,orig_path,imported_at FROM documents ORDER BY id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Document
	for rows.Next() {
		d := &Document{}
		var ts string
		if err := rows.Scan(&d.ID, &d.Name, &d.Hash, &d.Size, &d.OrigPath, &ts); err != nil {
			return nil, err
		}
		d.ImportedAt, _ = time.Parse(time.RFC3339, ts)
		out = append(out, d)
	}
	return out, rows.Err()
}

// GetDocument 读取文档与报告。
func (st *Store) GetDocument(id int64) (*Document, error) {
	d := &Document{}
	var ts, reportJSON string
	err := st.db.QueryRow(
		`SELECT id,name,hash,size,orig_path,imported_at,report_json FROM documents WHERE id=?`, id).
		Scan(&d.ID, &d.Name, &d.Hash, &d.Size, &d.OrigPath, &ts, &reportJSON)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("document %d not found", id)
	}
	if err != nil {
		return nil, err
	}
	d.ImportedAt, _ = time.Parse(time.RFC3339, ts)
	d.Report = &pdf.Report{}
	if err := json.Unmarshal([]byte(reportJSON), d.Report); err != nil {
		return nil, err
	}
	return d, nil
}

// ReadOriginal 返回原件字节（从磁盘原件，而不是重存 PDF）。
func (st *Store) ReadOriginal(d *Document) ([]byte, error) {
	return os.ReadFile(d.OrigPath)
}

// Branch 解释分支。
type Branch struct {
	ID        int64  `json:"id"`
	DocID     int64  `json:"docId"`
	Name      string `json:"name"`
	ParentID  *int64 `json:"parentId"`
	CreatedAt string `json:"createdAt"`
	Note      string `json:"note"`
}

// ListBranches 列出文档的分支。
func (st *Store) ListBranches(docID int64) ([]*Branch, error) {
	rows, err := st.db.Query(
		`SELECT id,doc_id,name,parent_id,created_at,note FROM branches WHERE doc_id=? ORDER BY id`, docID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Branch
	for rows.Next() {
		b := &Branch{}
		if err := rows.Scan(&b.ID, &b.DocID, &b.Name, &b.ParentID, &b.CreatedAt, &b.Note); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// Choice 一次人工确认。
type Choice struct {
	ID        int64  `json:"id"`
	BranchID  int64  `json:"branchId"`
	ObjNum    int    `json:"objNum"`
	Revision  int    `json:"revision"`
	CandID    int    `json:"candId"`
	CreatedAt string `json:"createdAt"`
	Note      string `json:"note"`
}

// ListChoices 列出分支上的确认。
func (st *Store) ListChoices(docID, branchID int64) ([]*Choice, error) {
	rows, err := st.db.Query(
		`SELECT id,branch_id,obj_num,revision,cand_id,created_at,note FROM choices
		 WHERE doc_id=? AND branch_id=? ORDER BY id`, docID, branchID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Choice
	for rows.Next() {
		c := &Choice{}
		if err := rows.Scan(&c.ID, &c.BranchID, &c.ObjNum, &c.Revision, &c.CandID, &c.CreatedAt, &c.Note); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// DefaultBranchID 返回文档默认分支 ID。
func (st *Store) DefaultBranchID(docID int64) (int64, error) {
	var id int64
	err := st.db.QueryRow(`SELECT id FROM branches WHERE doc_id=? AND name='default'`, docID).Scan(&id)
	return id, err
}

// branchExists 校验分支归属。
func (st *Store) branchExists(docID, branchID int64) error {
	var got int64
	err := st.db.QueryRow(`SELECT doc_id FROM branches WHERE id=?`, branchID).Scan(&got)
	if err == sql.ErrNoRows {
		return fmt.Errorf("branch %d not found", branchID)
	}
	if err != nil {
		return err
	}
	if got != docID {
		return fmt.Errorf("branch %d 不属于文档 %d", branchID, docID)
	}
	return nil
}

// ChooseCandidate 在分支上确认某对象在某修订使用哪个候选。
// 重复确认同一键会追加新记录（解释分支是追加式时间线）。
func (st *Store) ChooseCandidate(docID, branchID int64, objNum, revision, candID int, note string) (*Choice, error) {
	if err := st.branchExists(docID, branchID); err != nil {
		return nil, err
	}
	if !st.candidateExists(docID, candID) {
		return nil, fmt.Errorf("candidate %d 不存在", candID)
	}
	now := time.Now().UTC().Format(time.RFC3339)
	res, err := st.db.Exec(
		`INSERT INTO choices(doc_id,branch_id,obj_num,revision,cand_id,created_at,note)
		 VALUES(?,?,?,?,?,?,?)`, docID, branchID, objNum, revision, candID, now, note)
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	return &Choice{ID: id, BranchID: branchID, ObjNum: objNum, Revision: revision, CandID: candID, CreatedAt: now, Note: note}, nil
}

func (st *Store) candidateExists(docID int64, candID int) bool {
	d, err := st.GetDocument(docID)
	if err != nil {
		return false
	}
	for _, c := range d.Report.Candidates {
		if c.ID == candID {
			return true
		}
	}
	return false
}

// CreateBranch 从父分支复制其全部确认，形成可复现的解释分支。
func (st *Store) CreateBranch(docID int64, name string, parentID *int64, note string) (*Branch, error) {
	if parentID != nil {
		if err := st.branchExists(docID, *parentID); err != nil {
			return nil, err
		}
	}
	now := time.Now().UTC().Format(time.RFC3339)
	tx, err := st.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	res, err := tx.Exec(
		`INSERT INTO branches(doc_id,name,parent_id,created_at,note) VALUES(?,?,?,?,?)`,
		docID, name, parentID, now, note)
	if err != nil {
		return nil, err
	}
	newID, _ := res.LastInsertId()
	if parentID != nil {
		if _, err := tx.Exec(
			`INSERT INTO choices(doc_id,branch_id,obj_num,revision,cand_id,created_at,note)
			 SELECT ?, ?, obj_num,revision,cand_id,created_at,note FROM choices
			 WHERE doc_id=? AND branch_id=?`, newID, newID, docID, *parentID); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return &Branch{ID: newID, DocID: docID, Name: name, ParentID: parentID, CreatedAt: now, Note: note}, nil
}
