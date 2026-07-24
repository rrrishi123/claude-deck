package tmuxview

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// SnapshotFile is what gets written to disk: enough to rebuild every local
// tmux session — window names, exact pane geometry (tmux layout strings are
// replayable), each pane's cwd, and which Claude session to resume where.
//
// Only the local layer is persisted. A nested tmux on another machine
// survives that machine's own lifecycle; after a local reboot the ssh pane is
// recreated and the remote tmux is simply re-entered, nothing to restore.
type SnapshotFile struct {
	TakenAt  string        `json:"taken_at"`
	Sessions []SessSnap    `json:"sessions"`
	Hash     string        `json:"hash"` // topology fingerprint, for change detection
}

type SessSnap struct {
	Name    string     `json:"name"`
	Windows []WinSnap  `json:"windows"`
}

type WinSnap struct {
	Index  int        `json:"index"`
	Name   string     `json:"name"`
	Layout string     `json:"layout"`
	Panes  []PaneSnap `json:"panes"`
}

type PaneSnap struct {
	Index   int    `json:"index"`
	Cwd     string `json:"cwd"`
	Cmd     string `json:"cmd"` // what was running, informational
	Claude  string `json:"claude,omitempty"` // session uuid to --resume
	Bypass  bool   `json:"bypass,omitempty"`
	SSHDest string `json:"ssh_dest,omitempty"`
	// Options are the pane's @-prefixed user options (@pane_label, @agent, …).
	// They are pane-dimension attributes like any other: captured in every
	// snapshot and re-applied on restore.
	Options map[string]string `json:"options,omitempty"`
}

// SnapshotDir is where snapshots live, under ~/.claude.
func SnapshotDir(claudeDir string) string { return filepath.Join(claudeDir, "tmux-snapshots") }

// WriteSnapshot captures the local layer and writes latest.json, plus a
// timestamped copy when the topology actually changed since the last write.
// Returns the path written and whether it was a change.
func WriteSnapshot(claudeDir string, deck []DeckSession) (string, bool, error) {
	// local layer only; no ssh dialing from the background loop
	return WriteSnapshotFrom(claudeDir, Snapshot(deck, false))
}

// paneOptions reads every pane's @-prefixed user options in one shell pass.
func paneOptions() map[string]map[string]string {
	out, err := runShell(nil, localOrRemoteTmux(nil)+` list-panes -a -F '#{pane_id}' | while read p; do `+
		localOrRemoteTmux(nil)+` show-options -p -t "$p" | sed "s|^|$p |"; done`)
	if err != nil {
		return nil
	}
	m := map[string]map[string]string{}
	for _, line := range strings.Split(out, "\n") {
		f := strings.SplitN(line, " ", 3)
		if len(f) < 3 || !strings.HasPrefix(f[1], "@") {
			continue
		}
		val := f[2]
		// show-options quotes values containing spaces; strip one quote layer
		if len(val) >= 2 && val[0] == '"' && val[len(val)-1] == '"' {
			val = strings.ReplaceAll(val[1:len(val)-1], `\"`, `"`)
		}
		if m[f[0]] == nil {
			m[f[0]] = map[string]string{}
		}
		m[f[0]][f[1]] = val
	}
	return m
}

// WriteSnapshotFrom persists an already-taken topology, so callers that need
// the topology for other purposes (temporal recording) observe reality once.
func WriteSnapshotFrom(claudeDir string, top *Topology) (string, bool, error) {
	if top.Err != "" {
		return "", false, fmt.Errorf("tmux: %s", top.Err)
	}
	snap := fromTopology(top, paneOptions())
	dir := SnapshotDir(claudeDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", false, err
	}
	latest := filepath.Join(dir, "latest.json")

	changed := true
	if prev, err := os.ReadFile(latest); err == nil {
		var old SnapshotFile
		if json.Unmarshal(prev, &old) == nil && old.Hash == snap.Hash {
			changed = false
		}
	}
	snap.TakenAt = time.Now().Format(time.RFC3339)
	data, _ := json.MarshalIndent(snap, "", "  ")
	if err := os.WriteFile(latest, data, 0o644); err != nil {
		return "", false, err
	}
	if changed {
		stamped := filepath.Join(dir, "snap-"+time.Now().Format("20060102-150405")+".json")
		_ = os.WriteFile(stamped, data, 0o644)
		prune(dir, 100)
	}
	return latest, changed, nil
}

func fromTopology(top *Topology, opts map[string]map[string]string) SnapshotFile {
	var sf SnapshotFile
	h := sha256.New()
	for _, s := range top.Sessions {
		ss := SessSnap{Name: s.Name}
		for _, w := range s.Windows {
			ws := WinSnap{Index: w.Index, Name: w.Name, Layout: w.Layout}
			for _, p := range w.Panes {
				ps := PaneSnap{Index: p.Index, Cwd: p.Cwd, Cmd: p.Cmd, SSHDest: p.SSHDest, Options: opts[p.ID]}
				if p.Claude != nil && p.Claude.SessionID != "" {
					ps.Claude = p.Claude.SessionID
					ps.Bypass = p.Claude.Bypass
				}
				ws.Panes = append(ws.Panes, ps)
				fmt.Fprintf(h, "%s|%d|%s|%s|%s|%v\n", s.Name, w.Index, w.Name, p.Cwd, ps.Claude, ps.Options)
			}
			ss.Windows = append(ss.Windows, ws)
		}
		sf.Sessions = append(sf.Sessions, ss)
	}
	sf.Hash = hex.EncodeToString(h.Sum(nil)[:12])
	return sf
}

// prune keeps the newest n timestamped snapshots.
func prune(dir string, n int) {
	entries, _ := filepath.Glob(filepath.Join(dir, "snap-*.json"))
	if len(entries) <= n {
		return
	}
	sort.Strings(entries)
	for _, e := range entries[:len(entries)-n] {
		os.Remove(e)
	}
}

// Restore rebuilds tmux sessions from a snapshot file. Existing sessions with
// the same name are left untouched (restore is additive, never destructive) —
// their names are returned in skipped. Claude panes get their
// `claude --resume <uuid>` typed for them.
func Restore(path string) (created, skipped []string, err error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, err
	}
	var sf SnapshotFile
	if err := json.Unmarshal(data, &sf); err != nil {
		return nil, nil, err
	}
	for _, s := range sf.Sessions {
		if len(s.Windows) == 0 {
			continue
		}
		if _, e := Exec(nil, "has-session", "-t", "="+s.Name); e == nil {
			skipped = append(skipped, s.Name)
			continue
		}
		if err := restoreSession(s); err != nil {
			return created, skipped, fmt.Errorf("session %q: %w", s.Name, err)
		}
		created = append(created, s.Name)
	}
	return created, skipped, nil
}

func restoreSession(s SessSnap) error {
	first := s.Windows[0]
	firstCwd := paneCwd(first, 0)
	if _, err := Exec(nil, "new-session", "-d", "-s", s.Name, "-c", firstCwd, "-n", first.Name); err != nil {
		return err
	}
	for wi, w := range s.Windows {
		target := s.Name + ":" + strconv.Itoa(w.Index)
		if wi > 0 {
			if _, err := Exec(nil, "new-window", "-d", "-t", target, "-c", paneCwd(w, 0), "-n", w.Name); err != nil {
				return err
			}
		} else if first.Index != 0 {
			// the first window was created at the server's base-index; move it home
			Exec(nil, "move-window", "-s", s.Name+":^", "-t", target)
		}
		// No -d: focus follows each new pane, so the next split appends after it
		// and pane order matches the snapshot (with -d every split would insert
		// right after pane 1, reversing the middle).
		for pi := 1; pi < len(w.Panes); pi++ {
			if _, err := Exec(nil, "split-window", "-t", target, "-c", paneCwd(w, pi)); err != nil {
				return err
			}
		}
		if len(w.Panes) > 1 && w.Layout != "" {
			Exec(nil, "select-layout", "-t", target, w.Layout)
		}
		for pi, p := range w.Panes {
			pt := target + "." + strconv.Itoa(paneIndexAt(w, pi))
			// user dimensions travel with the pane: re-apply @-options first
			for name, val := range p.Options {
				Exec(nil, "set-option", "-p", "-t", pt, name, val)
			}
			if p.Claude == "" {
				continue
			}
			cmd := "claude --resume " + p.Claude
			if p.Bypass {
				cmd += " --dangerously-skip-permissions"
			}
			Exec(nil, "send-keys", "-t", pt, cmd, "Enter")
		}
	}
	return nil
}

// paneCwd returns pane i's cwd with a home-dir fallback.
func paneCwd(w WinSnap, i int) string {
	if i < len(w.Panes) && w.Panes[i].Cwd != "" {
		return w.Panes[i].Cwd
	}
	home, _ := os.UserHomeDir()
	return home
}

// paneIndexAt maps the i-th recreated pane to its tmux pane index. Panes are
// split in order, so display indexes follow the server's pane-base-index; use
// the saved index when the count matches, else fall back to base+offset.
func paneIndexAt(w WinSnap, i int) int {
	if i < len(w.Panes) {
		return w.Panes[i].Index
	}
	return i
}

// LatestSnapshot returns the path of latest.json if it exists.
func LatestSnapshot(claudeDir string) (string, error) {
	p := filepath.Join(SnapshotDir(claudeDir), "latest.json")
	if _, err := os.Stat(p); err != nil {
		return "", err
	}
	return p, nil
}

// Describe renders a one-line summary of a snapshot for CLI output.
func Describe(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	var sf SnapshotFile
	if json.Unmarshal(data, &sf) != nil {
		return ""
	}
	wins, panes, claudes := 0, 0, 0
	for _, s := range sf.Sessions {
		wins += len(s.Windows)
		for _, w := range s.Windows {
			panes += len(w.Panes)
			for _, p := range w.Panes {
				if p.Claude != "" {
					claudes++
				}
			}
		}
	}
	return fmt.Sprintf("%d session(s), %d window(s), %d pane(s), %d claude session(s) — taken %s",
		len(sf.Sessions), wins, panes, claudes, sf.TakenAt)
}
