package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/tachodril/claude-deck/internal/tmuxview"
)

// The feed turns agents from pollers into subscribers: the tmux witness
// pushes events here, and any number of clients (Claude sessions, scripts)
// hold an SSE connection to /api/tmux/feed. Structural events additionally
// trigger a debounced SCD observation, so tmux_pane_states converges within
// ~a second of reality instead of a minute.

// startWitness wires the control-mode witness into the hub. Called once.
func (s *Server) startWitness() {
	var debounce *time.Timer
	tmuxview.StartWitness(func(e tmuxview.Event) {
		s.broadcast(e)
		if !e.Structural() {
			return
		}
		s.feedMu.Lock()
		if debounce != nil {
			debounce.Stop()
		}
		debounce = time.AfterFunc(500*time.Millisecond, func() {
			sessions, err := s.st.All()
			if err != nil {
				return
			}
			top := tmuxview.Snapshot(deckSessions(sessions), false)
			_ = s.st.RecordTmuxObservation(top)
			s.tmuxMu.Lock() // the world changed; drop the short-lived cache
			s.tmuxTop, s.tmuxAt = top, time.Now()
			s.tmuxMu.Unlock()
		})
		s.feedMu.Unlock()
	})
}

func (s *Server) broadcast(e tmuxview.Event) {
	data, err := json.Marshal(e)
	if err != nil {
		return
	}
	s.feedMu.Lock()
	defer s.feedMu.Unlock()
	for ch := range s.feedSubs {
		select {
		case ch <- data:
		default: // slow consumer: drop the event, never block the witness
		}
	}
}

// handleTmuxFeed is the SSE stream. Each event is one JSON line:
//
//	curl -N localhost:7420/api/tmux/feed
//	data: {"at":1784900000000,"kind":"output","arg":"%3","bytes":128}
func (s *Server) handleTmuxFeed(w http.ResponseWriter, r *http.Request) {
	fl, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", 500)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	ch := make(chan []byte, 256)
	s.feedMu.Lock()
	s.feedSubs[ch] = true
	s.feedMu.Unlock()
	defer func() {
		s.feedMu.Lock()
		delete(s.feedSubs, ch)
		s.feedMu.Unlock()
	}()

	fmt.Fprintf(w, ": connected %d\n\n", time.Now().UnixMilli())
	fl.Flush()
	ping := time.NewTicker(30 * time.Second)
	defer ping.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-ping.C:
			fmt.Fprint(w, ": ping\n\n")
			fl.Flush()
		case data := <-ch:
			fmt.Fprintf(w, "data: %s\n\n", data)
			fl.Flush()
		}
	}
}
