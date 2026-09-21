// Package store persists documents, scan results, user decisions and
// interpretation branches in SQLite. The original PDF bytes are always kept on
// disk unchanged; the database only records metadata and evidence.
package store

import (
	"database/sql"
	"fmt"

	_ "modernc.org/sqlite"
)

type Store struct {
	db *sql.DB
}

func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	st := &Store{db: db}
	if err := st.migrate(); err != nil {
		return nil, err
	}
	return st, nil
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) migrate() error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS documents (
			id TEXT PRIMARY KEY,
			name TEXT NOT NULL,
			size INTEGER NOT NULL,
			sha256 TEXT NOT NULL,
			path TEXT NOT NULL,
			imported_at TEXT NOT NULL,
			scanned_at TEXT NOT NULL,
			scan_json TEXT NOT NULL
		);`,
		`CREATE TABLE IF NOT EXISTS decisions (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			doc_id TEXT NOT NULL,
			candidate_id TEXT NOT NULL,
			action TEXT NOT NULL,
			note TEXT NOT NULL DEFAULT '',
			created_at TEXT NOT NULL,
			UNIQUE(doc_id, candidate_id)
		);`,
		`CREATE TABLE IF NOT EXISTS branches (
			id TEXT PRIMARY KEY,
			doc_id TEXT NOT NULL,
			parent_id TEXT NOT NULL DEFAULT '',
			label TEXT NOT NULL,
			num INTEGER NOT NULL,
			gen INTEGER NOT NULL,
			chosen_candidate_id TEXT NOT NULL,
			rejected_candidate_ids TEXT NOT NULL,
			created_at TEXT NOT NULL
		);`,
	}
	for _, q := range stmts {
		if _, err := s.db.Exec(q); err != nil {
			return fmt.Errorf("migrate: %w", err)
		}
	}
	return nil
}
