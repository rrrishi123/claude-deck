package server

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/tachodril/claude-deck/internal/environment"
	"github.com/tachodril/claude-deck/internal/ingest"
	"github.com/tachodril/claude-deck/internal/live"
	"github.com/tachodril/claude-deck/internal/model"
	"github.com/tachodril/claude-deck/internal/prompts"
	"github.com/tachodril/claude-deck/internal/store"
	"github.com/tachodril/claude-deck/internal/terminal"
	"github.com/tachodril/claude-deck/internal/tmuxview"
	"github.com/tachodril/claude-deck/web"
)

type Server struct {
	st         *store.Store
	claudeDir  string
	mu         sync.Mutex
	lastIngest time.Time
	ingesting  bool
	envMu      sync.Mutex
	env        environment.Stats
	envAt      time.Time
	promptsMu  sync.Mutex
	waiting    map[string]*model.Prompt // cwd -> prompt the session is blocked on
	lastReq    time.Time
	tmuxMu     sync.Mutex
	tmuxTop    *tmuxview.Topology // short-lived cache, see topology()
	tmuxAt     time.Time
	feedMu     sync.Mutex
	feedSubs   map[chan []byte]bool // SSE subscribers of the witness feed
}

func New(st *store.Store, claudeDir string) *Server {
	return &Server{st: st, claudeDir: claudeDir, lastIngest: time.Now(), feedSubs: map[chan []byte]bool{}}
}

// maybeReingest refreshes the DB from ~/.claude at most every 15s, in the
// background, so stored fields (last_used_at, prompts, tokens) stay current
// while the dashboard is open — and costs nothing when nobody's watching.
func (s *Server) maybeReingest() {
	s.mu.Lock()
	if s.ingesting || time.Since(s.lastIngest) < 15*time.Second {
		s.mu.Unlock()
		return
	}
	s.ingesting = true
	s.mu.Unlock()
	go func() {
		ingest.Run(s.claudeDir, s.st)
		s.mu.Lock()
		s.lastIngest = time.Now()
		s.ingesting = false
		s.mu.Unlock()
	}()
}

// watchPrompts periodically reads the terminal tail of each running session and
// records which are blocked on a permission prompt. It only works while the
// dashboard is open (a recent request), so it costs nothing when nobody watches.
func (s *Server) watchPrompts() {
	for {
		time.Sleep(6 * time.Second)
		s.mu.Lock()
		active := !s.lastReq.IsZero() && time.Since(s.lastReq) < 20*time.Second
		s.mu.Unlock()
		if active {
			s.scanPrompts()
		}
	}
}

func (s *Server) scanPrompts() {
	running := map[string]bool{}
	for cwd := range live.ClaudeProcs() {
		running[cwd] = true
	}
	cwdByTty := map[string]string{}
	ttys := make([]string, 0, len(running))
	for cwd := range running {
		if tty := live.TtyForCwd(cwd); tty != "" {
			cwdByTty[tty] = cwd
			ttys = append(ttys, tty)
		}
	}
	found := map[string]*model.Prompt{}
	for tty, tail := range terminal.ReadTails(ttys) {
		if p := prompts.Detect(tail); p != nil {
			found[cwdByTty[tty]] = p
		}
	}
	s.promptsMu.Lock()
	s.waiting = found
	s.promptsMu.Unlock()
}

func (s *Server) Listen(addr string) error {
	go s.watchPrompts()
	s.startWitness()
	mux := http.NewServeMux()
	mux.HandleFunc("/api/sessions", s.handleSessions)
	mux.HandleFunc("/api/stats", s.handleStats)
	mux.HandleFunc("/api/analytics", s.handleAnalytics)
	mux.HandleFunc("/api/environment", s.handleEnvironment)
	mux.HandleFunc("/api/meta", s.handleMeta)
	mux.HandleFunc("/api/action", s.handleAction)
	mux.HandleFunc("/api/tmux", s.handleTmux)
	mux.HandleFunc("/api/tmux/exec", s.handleTmuxExec)
	mux.HandleFunc("/api/tmux/restore", s.handleTmuxRestore)
	mux.HandleFunc("/api/tmux/history", s.handleTmuxHistory)
	mux.HandleFunc("/api/tmux/feed", s.handleTmuxFeed)
	mux.HandleFunc("/api/search", s.handleSearch)
	sub, _ := fs.Sub(web.FS, "static")
	fileServer := http.FileServer(http.FS(sub))
	mux.Handle("/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Hashed assets are immutable; never cache index.html so a rebuilt UI is
		// picked up on a normal reload (no more "hard-refresh to see changes").
		if strings.Contains(r.URL.Path, "/assets/") {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		} else {
			w.Header().Set("Cache-Control", "no-cache")
		}
		fileServer.ServeHTTP(w, r)
	}))
	return http.ListenAndServe(addr, mux)
}

// withStatus loads sessions and overlays live status. Sessions found in tmux
// panes are identified exactly (per-pane binding), so many sessions running in
// the same directory each show as running — the old cwd heuristic only marks
// the most-recently-used one and stays as the fallback for non-tmux terminals.
func (s *Server) withStatus() ([]model.Session, error) {
	s.maybeReingest()
	s.mu.Lock()
	s.lastReq = time.Now()
	s.mu.Unlock()
	sessions, err := s.st.All()
	if err != nil {
		return nil, err
	}
	procs := live.ClaudeProcs()
	binds := tmuxBindings(s.topology(sessions))
	boundPid := map[string]bool{}
	for _, b := range binds {
		boundPid[b.pid] = true
	}
	// cwd heuristic operates on whatever tmux didn't already account for
	running := map[string]bool{}
	for cwd, pids := range procs {
		for _, pid := range pids {
			if !boundPid[pid] {
				running[cwd] = true
			}
		}
	}
	newest := map[string]int64{}
	for _, ss := range sessions {
		if _, tmuxBound := binds[ss.ID]; tmuxBound {
			continue
		}
		if running[ss.Cwd] && ss.LastUsedAt > newest[ss.Cwd] {
			newest[ss.Cwd] = ss.LastUsedAt
		}
	}
	tpaths := transcriptPaths(s.claudeDir)
	s.promptsMu.Lock()
	waiting := s.waiting
	s.promptsMu.Unlock()
	usageCache := map[string][2]float64{}
	bypassCache := map[string]bool{}
	for i := range sessions {
		s := &sessions[i]
		b, tmuxBound := binds[s.ID]
		switch {
		case tmuxBound:
			s.Status = "running"
			s.TmuxLoc = b.loc
			s.CPU, s.MemMB = live.UsageForPids([]string{b.pid})
			s.Bypass = b.bypass
		case running[s.Cwd] && s.LastUsedAt == newest[s.Cwd]:
			s.Status = "running"
			if u, ok := usageCache[s.Cwd]; ok {
				s.CPU, s.MemMB = u[0], u[1]
				s.Bypass = bypassCache[s.Cwd]
			} else if pids := procs[s.Cwd]; len(pids) > 0 {
				s.CPU, s.MemMB = live.UsageForPids(pids)
				s.Bypass = live.Bypassed(pids)
				usageCache[s.Cwd] = [2]float64{s.CPU, s.MemMB}
				bypassCache[s.Cwd] = s.Bypass
			}
		default:
			s.Status = "ended"
			continue
		}
		s.Working = s.CPU > workingCPU
		if !s.Working {
			if fi, err := os.Stat(tpaths[s.ID]); err == nil {
				s.Working = time.Since(fi.ModTime()) < workingWindow
			}
		}
		if p := waiting[s.Cwd]; p != nil {
			s.Waiting = true
			s.Prompt = p
			s.Working = false
		}
	}
	return sessions, nil
}

// A running session is "working" if it's burning CPU (an idle Claude sits near
// 0%, blocked on input) or its transcript was written very recently.
const (
	workingCPU    = 10.0
	workingWindow = 15 * time.Second
)

// transcriptPaths maps session id -> its transcript file path (one glob).
func transcriptPaths(claudeDir string) map[string]string {
	m := map[string]string{}
	files, _ := filepath.Glob(filepath.Join(claudeDir, "projects", "*", "*.jsonl"))
	for _, fp := range files {
		m[strings.TrimSuffix(filepath.Base(fp), ".jsonl")] = fp
	}
	return m
}

func (s *Server) handleSessions(w http.ResponseWriter, r *http.Request) {
	sessions, err := s.withStatus()
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	writeJSON(w, sessions)
}

func (s *Server) handleStats(w http.ResponseWriter, r *http.Request) {
	sessions, err := s.withStatus()
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	stats := struct {
		Total     int   `json:"total"`
		Running   int   `json:"running"`
		Prompts   int   `json:"prompts"`
		Projects  int   `json:"projects"`
		TokensIn  int64 `json:"tokens_in"`
		TokensOut int64 `json:"tokens_out"`
	}{}
	projs := map[string]bool{}
	for _, ss := range sessions {
		stats.Total++
		stats.Prompts += ss.PromptCount
		stats.TokensIn += ss.TokensIn
		stats.TokensOut += ss.TokensOut
		if ss.Status == "running" {
			stats.Running++
		}
		if ss.Cwd != "" {
			projs[ss.Cwd] = true
		}
	}
	stats.Projects = len(projs)
	writeJSON(w, stats)
}

func (s *Server) handleAnalytics(w http.ResponseWriter, r *http.Request) {
	s.maybeReingest()
	sessions, err := s.st.All()
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	days, _ := s.st.Daily()

	type agg struct {
		Key      string `json:"key"`
		Sessions int    `json:"sessions"`
		Prompts  int    `json:"prompts"`
		Tokens   int64  `json:"tokens"`
	}
	byProj := map[string]*agg{}
	byModel := map[string]*agg{}
	bump := func(m map[string]*agg, key string, s model.Session) {
		if key == "" {
			return
		}
		a := m[key]
		if a == nil {
			a = &agg{Key: key}
			m[key] = a
		}
		a.Sessions++
		a.Prompts += s.PromptCount
		a.Tokens += s.TokensIn + s.TokensOut
	}
	for _, ss := range sessions {
		bump(byProj, ss.Project, ss)
		model := ss.Model
		if model == "" {
			model = "unknown"
		}
		bump(byModel, model, ss)
	}
	topN := func(m map[string]*agg, sortByTokens bool, n int) []agg {
		list := make([]agg, 0, len(m))
		for _, a := range m {
			list = append(list, *a)
		}
		sort.Slice(list, func(i, j int) bool {
			if sortByTokens {
				return list[i].Tokens > list[j].Tokens
			}
			return list[i].Prompts > list[j].Prompts
		})
		if n > 0 && len(list) > n {
			list = list[:n]
		}
		return list
	}
	writeJSON(w, map[string]any{
		"daily":       days,
		"topProjects": topN(byProj, false, 12),
		"models":      topN(byModel, true, 0),
	})
}

// handleEnvironment serves skills + subagent usage, scanned from ~/.claude and
// cached for 30s (the scan reads every transcript, so we don't do it per poll).
func (s *Server) handleEnvironment(w http.ResponseWriter, r *http.Request) {
	s.envMu.Lock()
	defer s.envMu.Unlock()
	if s.envAt.IsZero() || time.Since(s.envAt) > 30*time.Second {
		s.env = environment.Scan(s.claudeDir)
		s.envAt = time.Now()
	}
	writeJSON(w, s.env)
}

func (s *Server) handleMeta(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", 405)
		return
	}
	var req struct {
		ID       string `json:"id"`
		Favorite bool   `json:"favorite"`
		Tags     string `json:"tags"`
		Notes    string `json:"notes"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.ID == "" {
		http.Error(w, "bad request", 400)
		return
	}
	if err := s.st.SetMeta(req.ID, req.Favorite, req.Tags, req.Notes); err != nil {
		writeJSON(w, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}

func (s *Server) handleAction(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", 405)
		return
	}
	var req struct{ Action, ID, Cwd, Text, Perm string }
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	var err error
	switch req.Action {
	case "focus":
		err = terminal.Focus(req.Cwd)
	case "resume":
		err = terminal.Resume(req.Cwd, req.ID, permFlag(req.Perm))
	case "new":
		err = terminal.NewSession(req.Cwd, req.Text, permFlag(req.Perm))
	case "kill":
		err = terminal.Kill(req.Cwd)
	case "reveal":
		err = terminal.Reveal(req.Cwd)
	case "send":
		err = terminal.SendText(req.Cwd, req.Text)
	case "message":
		err = terminal.SendMessage(req.Cwd, req.Text)
	case "bypass":
		err = terminal.RestartWithFlags(req.Cwd, req.ID, "--dangerously-skip-permissions")
	case "unbypass":
		err = terminal.RestartWithFlags(req.Cwd, req.ID, "--permission-mode default")
	default:
		err = fmt.Errorf("unknown action %q", req.Action)
	}
	if err != nil {
		writeJSON(w, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}

// permFlag maps the UI's permission choice to a claude launch flag.
func permFlag(perm string) string {
	switch perm {
	case "skip":
		return "--dangerously-skip-permissions"
	case "ask":
		return "--permission-mode default"
	default:
		return ""
	}
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}
