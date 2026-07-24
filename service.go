package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

const agentLabel = "com.claudedeck.agent"

// installService writes a launchd agent that runs this binary always-on
// (auto-start at login, restart on crash) and loads it now.
func installService(port string) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	plistDir := filepath.Join(home, "Library", "LaunchAgents")
	logPath := filepath.Join(home, "Library", "Logs", "claude-deck.log")
	os.MkdirAll(plistDir, 0o755)
	os.MkdirAll(filepath.Dir(logPath), 0o755)
	plistPath := filepath.Join(plistDir, agentLabel+".plist")

	plist := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
  <key>Label</key><string>%s</string>
  <key>ProgramArguments</key><array><string>%s</string><string>--port</string><string>%s</string><string>--no-open</string></array>
  <key>RunAtLoad</key><true/><key>KeepAlive</key><true/>
  <key>EnvironmentVariables</key><dict><key>PATH</key><string>/usr/bin:/bin:/usr/sbin:/sbin:/usr/local/bin:/opt/homebrew/bin</string></dict>
  <key>StandardOutPath</key><string>%s</string><key>StandardErrorPath</key><string>%s</string>
</dict></plist>
`, agentLabel, exe, port, logPath, logPath)

	if err := os.WriteFile(plistPath, []byte(plist), 0o644); err != nil {
		return err
	}
	_ = exec.Command("launchctl", "unload", plistPath).Run()
	if out, err := exec.Command("launchctl", "load", "-w", plistPath).CombinedOutput(); err != nil {
		return fmt.Errorf("launchctl load failed: %v: %s", err, out)
	}
	fmt.Printf("✓ ClaudeDeck is now always-on at http://localhost:%s\n", port)
	fmt.Printf("  it auto-starts at login and restarts if it crashes.\n")
	fmt.Printf("  logs: %s\n", logPath)
	fmt.Printf("  turn it off with:  claude-deck --uninstall-service\n")
	return nil
}

// uninstallService stops and removes the launchd agent. Data is left untouched.
func uninstallService() error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	plistPath := filepath.Join(home, "Library", "LaunchAgents", agentLabel+".plist")
	_ = exec.Command("launchctl", "bootout", fmt.Sprintf("gui/%d/%s", os.Getuid(), agentLabel)).Run()
	_ = exec.Command("launchctl", "unload", plistPath).Run()
	os.Remove(plistPath)
	fmt.Println("✓ ClaudeDeck service removed (your data in ~/.claude/claude-deck.db is untouched).")
	return nil
}
