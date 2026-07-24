package model

// Session is one Claude Code session, reconstructed from ~/.claude data.
// Timestamps are epoch milliseconds. Status is computed at read time.
type Session struct {
	ID           string `json:"id"`
	Cwd          string `json:"cwd"`
	Project      string `json:"project"`
	CreatedAt    int64  `json:"created_at"`
	LastUsedAt   int64  `json:"last_used_at"`
	DurationSecs int64  `json:"duration_secs"`
	PromptCount  int    `json:"prompt_count"`
	MessageCount int    `json:"message_count"`
	Model        string `json:"model"`
	GitBranch    string `json:"git_branch"`
	FirstPrompt  string `json:"first_prompt"`
	// Title is the session's custom name (Claude Code's rename feature); AiTitle
	// is the auto-generated summary. Used to tell same-directory sessions apart.
	Title       string `json:"title"`
	AiTitle     string `json:"ai_title"`
	TokensIn    int64  `json:"tokens_in"`
	TokensOut   int64  `json:"tokens_out"`
	CurrentTask string `json:"current_task"`
	Status      string `json:"status"` // running | ended (computed at read time)

	// user metadata (stored separately so re-ingest can't wipe it)
	Favorite bool   `json:"favorite"`
	Tags     string `json:"tags"`
	Notes    string `json:"notes"`

	// live resource usage (running sessions only, computed at read time)
	CPU   float64 `json:"cpu"`    // %CPU (can exceed 100 across cores)
	MemMB float64 `json:"mem_mb"` // resident memory in MB

	// Working is true when the session is actively processing right now,
	// inferred from very recent transcript writes (running sessions only).
	Working bool `json:"working"`

	// Waiting is true when the session is blocked on a permission/confirmation
	// prompt in its terminal; Prompt carries the question + choices when parsed.
	Waiting bool    `json:"waiting"`
	Prompt  *Prompt `json:"prompt,omitempty"`

	// Bypass is true when the running session was started with permission checks
	// skipped (--dangerously-skip-permissions).
	Bypass bool `json:"bypass"`

	// TmuxLoc is where this session lives in tmux ("kosaten1:2.3 · claude-sessions")
	// when it runs inside a tmux pane. Distinguishes many sessions sharing one
	// directory far better than the project name can.
	TmuxLoc string `json:"tmux_loc,omitempty"`
}

// PromptOption is one selectable choice in a terminal prompt. Key is the key to
// send to pick it (e.g. "1"); Label is the human text (e.g. "Yes").
type PromptOption struct {
	Key   string `json:"key"`
	Label string `json:"label"`
}

// Prompt is a permission/confirmation question a session is waiting on.
type Prompt struct {
	Question string         `json:"question"`
	Options  []PromptOption `json:"options"`
}

// Day is one bucket of the activity heatmap.
type Day struct {
	Day     string `json:"day"` // YYYY-MM-DD
	Prompts int    `json:"prompts"`
}
