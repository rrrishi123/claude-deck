package tmuxview

import (
	"bufio"
	"os/exec"
	"strings"
	"time"
)

// The witness is a long-lived `tmux -C` control-mode client: tmux pushes
// notifications (%output, %window-add, %layout-change, …) the moment they
// happen, turning the deck from a 60-second sampler into a streaming
// observer. Polling remains as reconciliation; the witness closes the gap
// between samples.
//
// Scope note, stated honestly: a control-mode client receives %output only
// for panes of the session it is attached to (structural notifications are
// server-wide). With one session — the common case — coverage is total.
// Remote layers are not witnessed; they stay polled.

// Event is one witness notification, normalized.
type Event struct {
	At    int64  `json:"at"`              // epoch ms, when the witness saw it
	Kind  string `json:"kind"`            // "output", "window-add", "layout-change", …
	Arg   string `json:"arg,omitempty"`   // pane/window/session id when the notification names one
	Bytes int    `json:"bytes,omitempty"` // payload size for output events (content itself is not forwarded)
}

// Structural reports whether this event changes the topology (and so should
// trigger an SCD observation) as opposed to being pane activity.
func (e Event) Structural() bool {
	switch e.Kind {
	case "output", "extended-output":
		return false
	}
	return true
}

// witnessKinds is the allowlist of notifications worth forwarding.
var witnessKinds = map[string]bool{
	"output": true, "extended-output": true,
	"window-add": true, "window-close": true, "unlinked-window-add": true,
	"window-renamed": true, "layout-change": true, "window-pane-changed": true,
	"session-changed": true, "session-renamed": true, "sessions-changed": true,
	"session-window-changed": true, "pane-mode-changed": true,
}

// StartWitness attaches a control-mode client and streams events to onEvent
// from a goroutine. If the client dies (server restart, no server yet), it
// retries forever with backoff — the witness outlives tmux servers.
func StartWitness(onEvent func(Event)) {
	go func() {
		for {
			runWitnessOnce(onEvent)
			time.Sleep(5 * time.Second)
		}
	}()
}

func runWitnessOnce(onEvent func(Event)) {
	cmd := exec.Command(tmuxBin(), "-C", "attach-session")
	stdin, err := cmd.StdinPipe() // held open: closing it detaches the client
	if err != nil {
		return
	}
	defer stdin.Close()
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return
	}
	if err := cmd.Start(); err != nil {
		return
	}
	defer cmd.Wait()

	sc := bufio.NewScanner(stdout)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		line := sc.Text()
		if len(line) < 2 || line[0] != '%' {
			continue // command replies (%begin/%end blocks), not notifications
		}
		kind, rest, _ := strings.Cut(line[1:], " ")
		if !witnessKinds[kind] {
			continue
		}
		e := Event{At: time.Now().UnixMilli(), Kind: kind}
		if arg, payload, ok := strings.Cut(rest, " "); ok {
			e.Arg = arg
			if kind == "output" || kind == "extended-output" {
				e.Bytes = len(payload) // size only; content stays in the pane
			}
		} else {
			e.Arg = rest
		}
		onEvent(e)
	}
}
