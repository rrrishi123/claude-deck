package store

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
)

// Full-text search across every prompt ever sent, in any session.
//
// history.jsonl is append-only, so indexing is incremental: fts_state remembers
// the byte offset already indexed and each ingest only reads what's new. If the
// file shrank (rotation/manual edit), the index is rebuilt from zero.

const searchSchema = `
CREATE VIRTUAL TABLE IF NOT EXISTS prompts_fts USING fts5(
  session_id UNINDEXED, ts UNINDEXED, text
);
CREATE TABLE IF NOT EXISTS fts_state (key TEXT PRIMARY KEY, value INTEGER);`

type histLine struct {
	SessionID string `json:"sessionId"`
	Timestamp int64  `json:"timestamp"`
	Display   string `json:"display"`
}

// IndexHistory brings prompts_fts up to date with history.jsonl.
func (s *Store) IndexHistory(claudeDir string) error {
	if _, err := s.db.Exec(searchSchema); err != nil {
		return err
	}
	path := filepath.Join(claudeDir, "history.jsonl")
	fi, err := os.Stat(path)
	if err != nil {
		return err
	}
	var offset int64
	s.db.QueryRow(`SELECT value FROM fts_state WHERE key='history_offset'`).Scan(&offset)
	if offset > fi.Size() { // file shrank: rebuild
		if _, err := s.db.Exec(`DELETE FROM prompts_fts`); err != nil {
			return err
		}
		offset = 0
	}
	if offset == fi.Size() {
		return nil
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := f.Seek(offset, 0); err != nil {
		return err
	}

	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	stmt, err := tx.Prepare(`INSERT INTO prompts_fts (session_id, ts, text) VALUES (?,?,?)`)
	if err != nil {
		tx.Rollback()
		return err
	}
	defer stmt.Close()

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 64<<20)
	indexed := offset
	for sc.Scan() {
		lineLen := int64(len(sc.Bytes())) + 1 // +1 for the newline
		var h histLine
		if json.Unmarshal(sc.Bytes(), &h) == nil && h.SessionID != "" && h.Display != "" {
			if _, err := stmt.Exec(h.SessionID, h.Timestamp, h.Display); err != nil {
				tx.Rollback()
				return err
			}
		}
		indexed += lineLen
	}
	if _, err := tx.Exec(`INSERT INTO fts_state (key,value) VALUES ('history_offset',?)
		ON CONFLICT(key) DO UPDATE SET value=excluded.value`, indexed); err != nil {
		tx.Rollback()
		return err
	}
	return tx.Commit()
}

// Hit is one search result: a prompt (with highlighted snippet) plus enough of
// its session to render a useful row.
type Hit struct {
	SessionID string `json:"session_id"`
	Ts        int64  `json:"ts"`
	Snippet   string `json:"snippet"`
	Project   string `json:"project"`
	Cwd       string `json:"cwd"`
	Title     string `json:"title"`
	GitBranch string `json:"git_branch"`
}

// Search runs an FTS5 match over all prompts, best-ranked first.
func (s *Store) Search(query string, limit int) ([]Hit, error) {
	if _, err := s.db.Exec(searchSchema); err != nil {
		return nil, err
	}
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := s.db.Query(`
SELECT p.session_id, p.ts, snippet(prompts_fts, 2, '<mark>', '</mark>', '…', 18),
       COALESCE(s.project,''), COALESCE(s.cwd,''),
       COALESCE(NULLIF(s.title,''), NULLIF(s.ai_title,''), ''), COALESCE(s.git_branch,'')
FROM prompts_fts p
LEFT JOIN sessions s ON s.id = p.session_id
WHERE prompts_fts MATCH ?
ORDER BY bm25(prompts_fts)
LIMIT ?`, query, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Hit
	for rows.Next() {
		var h Hit
		if err := rows.Scan(&h.SessionID, &h.Ts, &h.Snippet, &h.Project, &h.Cwd, &h.Title, &h.GitBranch); err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	return out, rows.Err()
}
