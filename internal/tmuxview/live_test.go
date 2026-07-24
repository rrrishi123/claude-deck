package tmuxview

import (
	"encoding/json"
	"os"
	"testing"
)

// Live tests run only when TMUXVIEW_LIVE is set: they need a real tmux server
// (and TMUXVIEW_SSH set to a reachable host for the remote-layer test).
func TestLiveLocalLayer(t *testing.T) {
	if os.Getenv("TMUXVIEW_LIVE") == "" {
		t.Skip("set TMUXVIEW_LIVE=1 to run against the real tmux server")
	}
	top := readLayer(nil, nil)
	if top.Err != "" {
		t.Fatalf("local layer error: %s", top.Err)
	}
	if len(top.Sessions) == 0 {
		t.Fatal("expected at least one session")
	}
	b, _ := json.MarshalIndent(top, "", " ")
	t.Logf("local: %s", b)
}

func TestLiveRemoteLayer(t *testing.T) {
	host := os.Getenv("TMUXVIEW_SSH")
	if os.Getenv("TMUXVIEW_LIVE") == "" || host == "" {
		t.Skip("set TMUXVIEW_LIVE=1 and TMUXVIEW_SSH=<host> to run")
	}
	top := readLayer([]string{host}, nil)
	b, _ := json.MarshalIndent(top, "", " ")
	t.Logf("remote raw: err=%q %s", top.Err, b)
	if top.Err != "" {
		t.Fatalf("remote layer error: %s", top.Err)
	}
	if len(top.Sessions) == 0 {
		t.Fatal("expected at least one remote session")
	}
}
