package ingest

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/tachodril/claude-deck/internal/model"
	"github.com/tachodril/claude-deck/internal/store"
)

type histRec struct {
	SessionID string `json:"sessionId"`
	Project   string `json:"project"`
	Timestamp int64  `json:"timestamp"`
	Display   string `json:"display"`
}

type transRec struct {
	Type        string `json:"type"`
	Cwd         string `json:"cwd"`
	GitBranch   string `json:"gitBranch"`
	Timestamp   string `json:"timestamp"`
	CustomTitle string `json:"customTitle"`
	AiTitle     string `json:"aiTitle"`
	Message     struct {
		Model string `json:"model"`
		Usage struct {
			InputTokens  int64 `json:"input_tokens"`
			OutputTokens int64 `json:"output_tokens"`
		} `json:"usage"`
	} `json:"message"`
}

// Run rebuilds the session table from ~/.claude data. Returns count of sessions.
func Run(claudeDir string, st *store.Store) (int, error) {
	acc := map[string]*model.Session{}
	get := func(id string) *model.Session {
		s := acc[id]
		if s == nil {
			s = &model.Session{ID: id}
			acc[id] = s
		}
		return s
	}
	noteTime := func(s *model.Session, ms int64, prompt string) {
		if ms <= 0 {
			return
		}
		if s.CreatedAt == 0 || ms < s.CreatedAt {
			s.CreatedAt = ms
			if prompt != "" {
				s.FirstPrompt = prompt
			}
		}
		if ms > s.LastUsedAt {
			s.LastUsedAt = ms
		}
	}

	daily := map[string]int{}

	// 1) history.jsonl — per-prompt index (sessionId, cwd, epoch-ms ts, prompt)
	if f, err := os.Open(filepath.Join(claudeDir, "history.jsonl")); err == nil {
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 1<<20), 64<<20)
		for sc.Scan() {
			var h histRec
			if json.Unmarshal(sc.Bytes(), &h) != nil || h.SessionID == "" {
				continue
			}
			s := get(h.SessionID)
			s.PromptCount++
			if h.Project != "" && s.Cwd == "" {
				s.Cwd = h.Project
			}
			noteTime(s, h.Timestamp, h.Display)
			if h.Timestamp > 0 {
				day := time.UnixMilli(h.Timestamp).Format("2006-01-02")
				daily[day]++
			}
		}
		f.Close()
	}

	// 2) transcripts — model, branch, message count, token usage, precise times
	files, _ := filepath.Glob(filepath.Join(claudeDir, "projects", "*", "*.jsonl"))
	for _, fp := range files {
		id := strings.TrimSuffix(filepath.Base(fp), ".jsonl")
		f, err := os.Open(fp)
		if err != nil {
			continue
		}
		s := get(id)
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 1<<20), 64<<20)
		for sc.Scan() {
			var t transRec
			if json.Unmarshal(sc.Bytes(), &t) != nil {
				continue
			}
			if t.Type == "user" || t.Type == "assistant" {
				s.MessageCount++
			}
			if t.Cwd != "" && s.Cwd == "" {
				s.Cwd = t.Cwd
			}
			if t.GitBranch != "" && t.GitBranch != "HEAD" {
				s.GitBranch = t.GitBranch
			}
			if t.Message.Model != "" {
				s.Model = t.Message.Model
			}
			if t.CustomTitle != "" {
				s.Title = t.CustomTitle
			}
			if t.AiTitle != "" {
				s.AiTitle = t.AiTitle
			}
			s.TokensIn += t.Message.Usage.InputTokens
			s.TokensOut += t.Message.Usage.OutputTokens
			noteTime(s, parseISO(t.Timestamp), "")
		}
		f.Close()
	}

	out := make([]model.Session, 0, len(acc))
	for _, s := range acc {
		if s.Cwd != "" {
			s.Project = filepath.Base(s.Cwd)
			s.CurrentTask = readTask(s.Cwd)
		}
		if s.LastUsedAt > s.CreatedAt {
			s.DurationSecs = (s.LastUsedAt - s.CreatedAt) / 1000
		}
		out = append(out, *s)
	}
	if err := st.UpsertAll(out); err != nil {
		return 0, err
	}
	_ = st.SetDaily(daily)
	// Keep the full-text prompt index current (incremental; cheap).
	_ = st.IndexHistory(claudeDir)
	return len(out), nil
}

func parseISO(s string) int64 {
	if s == "" {
		return 0
	}
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return 0
	}
	return t.UnixMilli()
}

func readTask(cwd string) string {
	data, err := os.ReadFile(filepath.Join(cwd, ".claude", "context", "CURRENT_TASK.md"))
	if err != nil {
		return ""
	}
	inTask := false
	for _, ln := range strings.Split(string(data), "\n") {
		t := strings.TrimSpace(ln)
		if strings.HasPrefix(t, "## Task") {
			inTask = true
			continue
		}
		if inTask {
			if strings.HasPrefix(t, "## ") {
				break
			}
			if t == "" || strings.HasPrefix(t, ">") || strings.HasPrefix(t, "<") || strings.HasPrefix(t, "#") {
				continue
			}
			return t
		}
	}
	return ""
}
