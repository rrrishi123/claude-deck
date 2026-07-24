package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/tachodril/claude-deck/internal/ingest"
	"github.com/tachodril/claude-deck/internal/server"
	"github.com/tachodril/claude-deck/internal/store"
	"github.com/tachodril/claude-deck/internal/tmuxview"
)

// version is overridable at build time via -ldflags "-X main.version=v1.2.3".
var version = "dev"

func main() {
	port := flag.String("port", "7420", "port to serve on")
	noOpen := flag.Bool("no-open", false, "do not auto-open the browser")
	showVer := flag.Bool("version", false, "print version and exit")
	installSvc := flag.Bool("install-service", false, "run ClaudeDeck always-on (auto-start at login), then exit")
	uninstallSvc := flag.Bool("uninstall-service", false, "remove the always-on service, then exit")
	tmuxSnap := flag.Bool("tmux-snapshot", false, "save the tmux topology (sessions/windows/panes + claude bindings) now, then exit")
	tmuxRestore := flag.String("tmux-restore", "", "rebuild tmux from a snapshot file ('latest' for the newest); resumes claude sessions in their panes")
	flag.Parse()
	if *showVer {
		fmt.Println("claude-deck", version)
		return
	}
	if *tmuxSnap || *tmuxRestore != "" {
		if err := tmuxCLI(*tmuxSnap, *tmuxRestore); err != nil {
			log.Fatal(err)
		}
		return
	}
	if *installSvc {
		if err := installService(*port); err != nil {
			log.Fatal(err)
		}
		return
	}
	if *uninstallSvc {
		if err := uninstallService(); err != nil {
			log.Fatal(err)
		}
		return
	}

	home, err := os.UserHomeDir()
	if err != nil {
		log.Fatal(err)
	}
	claudeDir := filepath.Join(home, ".claude")

	st, err := store.Open(filepath.Join(claudeDir, "claude-deck.db"))
	if err != nil {
		log.Fatalf("store: %v", err)
	}
	defer st.Close()

	log.Println("ingesting sessions from", claudeDir, "…")
	n, err := ingest.Run(claudeDir, st)
	if err != nil {
		log.Printf("ingest warning: %v", err)
	}
	log.Printf("ingested %d sessions", n)

	// Background resurrect: snapshot the tmux topology every minute so a crash
	// or power loss never costs the layout again. Timestamped copies pile up
	// only when something actually changed.
	go func() {
		for {
			// One observation of reality per tick, used twice: the restore
			// snapshot on disk and the temporal (SCD2) record in the store.
			top := tmuxview.Snapshot(deckFromStore(st), false)
			_, _, _ = tmuxview.WriteSnapshotFrom(claudeDir, top) // quiet: "no tmux server" is normal
			_ = st.RecordTmuxObservation(top)
			time.Sleep(time.Minute)
		}
	}()

	url := "http://localhost:" + *port
	log.Printf("ClaudeDeck %s → %s", version, url)
	// Auto-open the browser only for interactive runs (not under launchd/pipes).
	if !*noOpen && interactive() {
		go func() {
			time.Sleep(500 * time.Millisecond)
			_ = exec.Command("open", url).Start()
		}()
	}
	if err := server.New(st, claudeDir).Listen(":" + *port); err != nil {
		log.Fatal(err)
	}
}

func interactive() bool {
	fi, err := os.Stdout.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}

// deckFromStore loads sessions for pane binding; nil on any error is fine
// (bindings degrade to argv-only, which covers resumed sessions).
func deckFromStore(st *store.Store) []tmuxview.DeckSession {
	sessions, err := st.All()
	if err != nil {
		return nil
	}
	out := make([]tmuxview.DeckSession, 0, len(sessions))
	for _, s := range sessions {
		title := s.Title
		if title == "" {
			title = s.AiTitle
		}
		out = append(out, tmuxview.DeckSession{
			ID: s.ID, Cwd: s.Cwd, Title: title, CreatedAt: s.CreatedAt, LastUsedAt: s.LastUsedAt,
		})
	}
	return out
}

// tmuxCLI implements --tmux-snapshot and --tmux-restore.
func tmuxCLI(snap bool, restore string) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	claudeDir := filepath.Join(home, ".claude")
	if snap {
		st, err := store.Open(filepath.Join(claudeDir, "claude-deck.db"))
		var deck []tmuxview.DeckSession
		if err == nil {
			deck = deckFromStore(st)
			st.Close()
		}
		path, changed, err := tmuxview.WriteSnapshot(claudeDir, deck)
		if err != nil {
			return err
		}
		status := "unchanged since last snapshot"
		if changed {
			status = "topology changed, timestamped copy kept"
		}
		fmt.Printf("✓ %s (%s)\n  %s\n", path, status, tmuxview.Describe(path))
		return nil
	}
	path := restore
	if path == "latest" {
		if path, err = tmuxview.LatestSnapshot(claudeDir); err != nil {
			return fmt.Errorf("no snapshot found in %s — is the deck service running?", tmuxview.SnapshotDir(claudeDir))
		}
	}
	fmt.Printf("restoring: %s\n", tmuxview.Describe(path))
	created, skipped, err := tmuxview.Restore(path)
	for _, s := range created {
		fmt.Printf("✓ created session %q\n", s)
	}
	for _, s := range skipped {
		fmt.Printf("• session %q already exists, left untouched\n", s)
	}
	return err
}
