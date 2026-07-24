package server

import (
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/tachodril/claude-deck/internal/model"
	"github.com/tachodril/claude-deck/internal/store"
	"github.com/tachodril/claude-deck/internal/tmuxview"
)

// deckSessions projects stored sessions into what pane-binding needs.
func deckSessions(sessions []model.Session) []tmuxview.DeckSession {
	out := make([]tmuxview.DeckSession, 0, len(sessions))
	for _, s := range sessions {
		title := s.Title
		if title == "" {
			title = s.AiTitle
		}
		out = append(out, tmuxview.DeckSession{
			ID: s.ID, Cwd: s.Cwd, Title: title,
			CreatedAt: s.CreatedAt, LastUsedAt: s.LastUsedAt,
		})
	}
	return out
}

// topology returns the local tmux tree, briefly cached: /api/sessions polls a
// few times a second when the dashboard is open, and a tmux+ps round trip per
// poll would be wasteful.
func (s *Server) topology(sessions []model.Session) *tmuxview.Topology {
	s.tmuxMu.Lock()
	defer s.tmuxMu.Unlock()
	if s.tmuxTop != nil && time.Since(s.tmuxAt) < 3*time.Second {
		return s.tmuxTop
	}
	s.tmuxTop = tmuxview.Snapshot(deckSessions(sessions), false)
	s.tmuxAt = time.Now()
	return s.tmuxTop
}

// paneBinding is one located claude session: exactly which pane it is in.
type paneBinding struct {
	loc    string // "kosaten1:2.3 · claude-sessions"
	pid    string
	bypass bool
	exact  bool // argv/title methods identify the session; cwd-* only guess
}

// tmuxBindings flattens the local topology into sessionID -> pane location.
func tmuxBindings(top *tmuxview.Topology) map[string]paneBinding {
	m := map[string]paneBinding{}
	if top == nil || top.Err != "" {
		return m
	}
	for _, sess := range top.Sessions {
		for _, w := range sess.Windows {
			for _, p := range w.Panes {
				if p.Claude == nil || p.Claude.SessionID == "" {
					continue
				}
				loc := sess.Name + ":" + strconv.Itoa(w.Index) + "." + strconv.Itoa(p.Index)
				if w.Name != "" {
					loc += " · " + w.Name
				}
				m[p.Claude.SessionID] = paneBinding{
					loc: loc, pid: p.Claude.PID, bypass: p.Claude.Bypass,
					exact: p.Claude.Method == "argv" || p.Claude.Method == "title",
				}
			}
		}
	}
	return m
}

// handleTmux serves the full topology. ?follow=1 descends into ssh panes to
// show nested tmux servers on other machines (slower: it dials them).
// Every explicit fetch is a deliberate look at reality, so it is recorded in
// the temporal model — with follow, that includes the remote layers.
func (s *Server) handleTmux(w http.ResponseWriter, r *http.Request) {
	sessions, _ := s.st.All()
	top := tmuxview.Snapshot(deckSessions(sessions), r.URL.Query().Get("follow") == "1")
	_ = s.st.RecordTmuxObservation(top)
	writeJSON(w, top)
}

// handleTmuxHistory answers temporal questions from the SCD2 model:
//   ?pane=%7                 → that pane's version history (the pane is the
//                              durable face; these rows are what it showed)
//   ?at=2026-07-22T20:00:00Z → the whole world as it was at that instant
//   ?pane=%7&at=…            → what that pane showed at that instant
// at accepts RFC3339 or epoch milliseconds. Optional &host= (default all).
func (s *Server) handleTmuxHistory(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	var at int64
	if v := q.Get("at"); v != "" {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			at = t.UnixMilli()
		} else if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			at = n
		} else {
			http.Error(w, "at must be RFC3339 or epoch ms", 400)
			return
		}
	}
	limit, _ := strconv.Atoi(q.Get("limit"))
	rows, err := s.st.TmuxHistory(q.Get("host"), q.Get("pane"), at, limit)
	if err != nil {
		writeJSON(w, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, map[string]any{
		"as_of_last_observation": s.st.LastTmuxObservation(),
		"states":                 rows,
	})
}

// handleTmuxExec runs one tmux command against any layer. chain [] is the
// local server; ["user@host"] the tmux behind that ssh pane; and so on deeper.
func (s *Server) handleTmuxExec(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", 405)
		return
	}
	var req struct {
		Chain []string `json:"chain"`
		Args  []string `json:"args"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || len(req.Args) == 0 {
		http.Error(w, "bad request", 400)
		return
	}
	out, err := tmuxview.Exec(req.Chain, req.Args...)
	if err != nil {
		writeJSON(w, map[string]any{"ok": false, "error": err.Error(), "output": out})
		return
	}
	writeJSON(w, map[string]any{"ok": true, "output": out})
}

// handleTmuxRestore rebuilds tmux from the latest (or a named) snapshot.
func (s *Server) handleTmuxRestore(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", 405)
		return
	}
	var req struct{ Path string }
	json.NewDecoder(r.Body).Decode(&req)
	path := req.Path
	if path == "" {
		var err error
		if path, err = tmuxview.LatestSnapshot(s.claudeDir); err != nil {
			writeJSON(w, map[string]any{"ok": false, "error": "no snapshot yet"})
			return
		}
	}
	created, skipped, err := tmuxview.Restore(path)
	resp := map[string]any{"ok": err == nil, "created": created, "skipped": skipped}
	if err != nil {
		resp["error"] = err.Error()
	}
	writeJSON(w, resp)
}

// handleSearch is full-text search over every prompt ever sent.
func (s *Server) handleSearch(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query().Get("q")
	if q == "" {
		writeJSON(w, []any{})
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	hits, err := s.st.Search(q, limit)
	if err != nil {
		writeJSON(w, map[string]any{"error": err.Error()})
		return
	}
	if hits == nil {
		hits = []store.Hit{} // never null: the UI iterates
	}
	writeJSON(w, hits)
}
