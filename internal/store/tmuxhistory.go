package store

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/tachodril/claude-deck/internal/tmuxview"
)

// Temporal model of the tmux world, so "what runs where" is never answered
// with a stale overwrite.
//
// A pane is the durable entity (dimension) — its stable tmux ID is the face
// that persists. What it shows — command, cwd, window, bound Claude session —
// are slowly-changing attributes, kept as SCD Type 2 rows with validity
// intervals: closed and re-opened on change, never updated in place. The one
// exception is the pane title, which churns constantly (Claude's spinner
// lives in it), so it is Type 1: refreshed on the current row only.
//
// Consistency is split deliberately:
//   - ACID inside the store: each observation is one transaction (close old
//     rows + open new + record the observation, atomically — WAL journal).
//   - BASE between store and reality: the store is a mirror of tmux and lags
//     it by up to the observation interval. That staleness is explicit, not
//     hidden: tmux_observations records every look, so "unchanged since T"
//     and "unobserved since T" are different answers, and every topology
//     response carries its as_of.
//
// Layers are host-scoped: an unfollowed remote layer is simply unobserved —
// its rows are left open, never closed by a local-only observation.

const tmuxSchema = `
CREATE TABLE IF NOT EXISTS tmux_panes (
  host       TEXT,
  pane_id    TEXT,
  first_seen INTEGER,
  last_seen  INTEGER,
  closed_at  INTEGER,
  PRIMARY KEY (host, pane_id)
);
CREATE TABLE IF NOT EXISTS tmux_pane_states (
  id            INTEGER PRIMARY KEY AUTOINCREMENT,
  host          TEXT,
  pane_id       TEXT,
  session_name  TEXT,
  window_id     TEXT,
  window_index  INTEGER,
  window_name   TEXT,
  pane_index    INTEGER,
  cmd           TEXT,
  cwd           TEXT,
  title         TEXT,
  claude_session_id TEXT,
  valid_from    INTEGER,
  valid_to      INTEGER,
  is_current    INTEGER DEFAULT 1
);
CREATE INDEX IF NOT EXISTS idx_tps_current ON tmux_pane_states(host, pane_id, is_current);
CREATE INDEX IF NOT EXISTS idx_tps_interval ON tmux_pane_states(valid_from, valid_to);
CREATE TABLE IF NOT EXISTS tmux_observations (
  observed_at   INTEGER PRIMARY KEY,
  hosts         TEXT,
  topology_hash TEXT
);`

// paneObs is one pane as seen in one observation.
type paneObs struct {
	host, paneID, sessName, winID, winName, cmd, cwd, title, claudeID string
	winIndex, paneIndex                                               int
}

// scdKey is the set of attributes whose change closes a version. Title is
// excluded on purpose (Type 1 — spinner churn would version-explode).
func (p paneObs) scdKey() string {
	return fmt.Sprintf("%s|%s|%d|%s|%d|%s|%s|%s",
		p.sessName, p.winID, p.winIndex, p.winName, p.paneIndex, p.cmd, p.cwd, p.claudeID)
}

// RecordTmuxObservation applies one topology observation to the temporal
// model in a single transaction. Only hosts present in the topology are
// touched; panes of observed hosts that vanished get their rows closed.
func (s *Store) RecordTmuxObservation(top *tmuxview.Topology) error {
	if top == nil || top.Err != "" {
		return nil // nothing observed; explicitly do not close anything
	}
	if _, err := s.db.Exec(tmuxSchema); err != nil {
		return err
	}
	now := time.Now().UnixMilli()

	var seen []paneObs
	hosts := map[string]bool{}
	h := sha256.New()
	var walk func(t *tmuxview.Topology)
	walk = func(t *tmuxview.Topology) {
		if t == nil || t.Err != "" {
			return
		}
		hosts[t.Host] = true
		for _, sess := range t.Sessions {
			for _, w := range sess.Windows {
				for _, p := range w.Panes {
					o := paneObs{
						host: t.Host, paneID: p.ID, sessName: sess.Name,
						winID: w.ID, winIndex: w.Index, winName: w.Name,
						paneIndex: p.Index, cmd: p.Cmd, cwd: p.Cwd, title: p.Title,
					}
					if p.Claude != nil {
						o.claudeID = p.Claude.SessionID
					}
					seen = append(seen, o)
					fmt.Fprintf(h, "%s|%s\n", o.host, o.scdKey())
					if p.Nested != nil {
						walk(p.Nested)
					}
				}
			}
		}
	}
	walk(top)

	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	seenIDs := map[string]bool{}
	for _, o := range seen {
		seenIDs[o.host+"\x00"+o.paneID] = true

		// dimension row: the face
		if _, err := tx.Exec(`
INSERT INTO tmux_panes (host,pane_id,first_seen,last_seen,closed_at) VALUES (?,?,?,?,NULL)
ON CONFLICT(host,pane_id) DO UPDATE SET last_seen=excluded.last_seen, closed_at=NULL`,
			o.host, o.paneID, now, now); err != nil {
			return err
		}

		// current SCD2 row, if any
		var curID int64
		var curKey string
		err := tx.QueryRow(`
SELECT id, session_name||'|'||window_id||'|'||window_index||'|'||window_name||'|'||pane_index||'|'||cmd||'|'||cwd||'|'||claude_session_id
FROM tmux_pane_states WHERE host=? AND pane_id=? AND is_current=1`, o.host, o.paneID).Scan(&curID, &curKey)
		switch {
		case err == nil && curKey == o.scdKey():
			// unchanged: Type 1 refresh of the churny title only
			if _, err := tx.Exec(`UPDATE tmux_pane_states SET title=? WHERE id=?`, o.title, curID); err != nil {
				return err
			}
			continue
		case err == nil:
			// changed: close the old version
			if _, err := tx.Exec(`UPDATE tmux_pane_states SET valid_to=?, is_current=0 WHERE id=?`, now, curID); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(`
INSERT INTO tmux_pane_states (host,pane_id,session_name,window_id,window_index,window_name,pane_index,cmd,cwd,title,claude_session_id,valid_from,valid_to,is_current)
VALUES (?,?,?,?,?,?,?,?,?,?,?,?,NULL,1)`,
			o.host, o.paneID, o.sessName, o.winID, o.winIndex, o.winName, o.paneIndex,
			o.cmd, o.cwd, o.title, o.claudeID, now); err != nil {
			return err
		}
	}

	// Close panes that an observed host no longer has. Unobserved hosts are
	// untouched: absence of observation is not absence.
	for host := range hosts {
		rows, err := tx.Query(`SELECT pane_id FROM tmux_pane_states WHERE host=? AND is_current=1`, host)
		if err != nil {
			return err
		}
		var gone []string
		for rows.Next() {
			var id string
			rows.Scan(&id)
			if !seenIDs[host+"\x00"+id] {
				gone = append(gone, id)
			}
		}
		rows.Close()
		for _, id := range gone {
			if _, err := tx.Exec(`UPDATE tmux_pane_states SET valid_to=?, is_current=0 WHERE host=? AND pane_id=? AND is_current=1`, now, host, id); err != nil {
				return err
			}
			if _, err := tx.Exec(`UPDATE tmux_panes SET closed_at=? WHERE host=? AND pane_id=?`, now, host, id); err != nil {
				return err
			}
		}
	}

	hostList := ""
	for host := range hosts {
		if hostList != "" {
			hostList += ","
		}
		hostList += host
	}
	if _, err := tx.Exec(`INSERT OR REPLACE INTO tmux_observations (observed_at,hosts,topology_hash) VALUES (?,?,?)`,
		now, hostList, hex.EncodeToString(h.Sum(nil)[:12])); err != nil {
		return err
	}
	return tx.Commit()
}

// PaneStateRow is one SCD2 version of a pane, for the history API.
type PaneStateRow struct {
	Host        string `json:"host"`
	PaneID      string `json:"pane_id"`
	SessionName string `json:"session_name"`
	WindowIndex int    `json:"window_index"`
	WindowName  string `json:"window_name"`
	PaneIndex   int    `json:"pane_index"`
	Cmd         string `json:"cmd"`
	Cwd         string `json:"cwd"`
	Title       string `json:"title"`
	ClaudeID    string `json:"claude_session_id,omitempty"`
	ValidFrom   int64  `json:"valid_from"`
	ValidTo     *int64 `json:"valid_to"` // null = still current
}

// TmuxHistory answers temporal questions. pane narrows to one pane's
// versions; at (epoch ms, 0 = ignore) narrows to versions valid at that
// instant — pane="" with at=T reconstructs the whole world as of T.
func (s *Store) TmuxHistory(host, pane string, at int64, limit int) ([]PaneStateRow, error) {
	if _, err := s.db.Exec(tmuxSchema); err != nil {
		return nil, err
	}
	if limit <= 0 || limit > 1000 {
		limit = 200
	}
	q := `SELECT host,pane_id,session_name,window_index,window_name,pane_index,cmd,cwd,title,claude_session_id,valid_from,valid_to
FROM tmux_pane_states WHERE 1=1`
	var args []any
	if host != "" {
		q, args = q+` AND host=?`, append(args, host)
	}
	if pane != "" {
		q, args = q+` AND pane_id=?`, append(args, pane)
	}
	if at > 0 {
		q, args = q+` AND valid_from<=? AND (valid_to IS NULL OR valid_to>?)`, append(args, at, at)
	}
	q, args = q+` ORDER BY valid_from DESC LIMIT ?`, append(args, limit)

	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PaneStateRow
	for rows.Next() {
		var r PaneStateRow
		if err := rows.Scan(&r.Host, &r.PaneID, &r.SessionName, &r.WindowIndex, &r.WindowName,
			&r.PaneIndex, &r.Cmd, &r.Cwd, &r.Title, &r.ClaudeID, &r.ValidFrom, &r.ValidTo); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// LastTmuxObservation returns when the world was last looked at (epoch ms),
// 0 if never — so callers can say "as of", not just "is".
func (s *Store) LastTmuxObservation() int64 {
	var t int64
	s.db.QueryRow(`SELECT MAX(observed_at) FROM tmux_observations`).Scan(&t)
	return t
}
