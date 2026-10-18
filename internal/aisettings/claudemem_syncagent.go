package aisettings

import (
	"context"
	"errors"
	"fmt"
	"os"
	osexec "os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// The scheduled cross-machine sync: a user LaunchAgent that runs
// `dot ai memory sync --peer <target>` hourly, with an ssh reachability
// pre-check so an away peer costs a skipped run, not a failure.

// ClaudeMemSyncLaunchdLabel is the scheduled sync agent's label.
const ClaudeMemSyncLaunchdLabel = "com.dotfiles.claude-mem-sync"

// SyncAgentInterval is how often the scheduled sync runs.
const SyncAgentInterval = 3600

// SyncLaunchdPlistPath is where the sync agent plist lives.
func (m *ClaudeMemManager) SyncLaunchdPlistPath() string {
	return filepath.Join(m.HomeDir, "Library", "LaunchAgents", ClaudeMemSyncLaunchdLabel+".plist")
}

// SyncLogPath is the scheduled run's stdout/stderr log.
func (m *ClaudeMemManager) SyncLogPath() string {
	return filepath.Join(m.DataDir(), "logs", "claude-mem-sync.log")
}

// SyncAgentHardLimit is the wrapper's backstop in seconds: past it the run
// gets SIGTERM, then SIGKILL 30 s later. It sits above the command's own
// 10-minute --timeout, and launchd starts no new run while one is alive.
const SyncAgentHardLimit = 900

// RenderSyncAgentPlist renders the hourly sync agent. The invoked command
// starts with the ssh pre-check per the sync contract: an unreachable peer
// exits 0 before dot runs, so launchd never records a failure for a laptop
// that is simply away. DOT_SCHEDULED_RUN tells the sync command to apply
// the same policy to its own in-process probe. A watchdog enforces
// SyncAgentHardLimit; launchd reaps its leftover sleep with the job.
func RenderSyncAgentPlist(dotPath, peer, homeDir, logPath string) string {
	script := fmt.Sprintf(`ssh -o BatchMode=yes -o ConnectTimeout=10 %s true || exit 0
%s ai memory sync --peer %s & pid=$!
(sleep %d; kill -TERM $pid; sleep 30; kill -KILL $pid) 2>/dev/null & guard=$!
wait $pid; rc=$?
kill $guard 2>/dev/null
exit $rc`, shellQuote(peer), shellQuote(dotPath), shellQuote(peer), SyncAgentHardLimit)
	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key>
  <string>%s</string>
  <key>ProgramArguments</key>
  <array>
    <string>/bin/sh</string>
    <string>-c</string>
    <string>%s</string>
  </array>
  <key>EnvironmentVariables</key>
  <dict>
    <key>HOME</key>
    <string>%s</string>
    <key>PATH</key>
    <string>/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin</string>
    <key>DOT_SCHEDULED_RUN</key>
    <string>1</string>
  </dict>
  <key>StartInterval</key>
  <integer>%d</integer>
  <key>RunAtLoad</key>
  <true/>
  <key>StandardOutPath</key>
  <string>%s</string>
  <key>StandardErrorPath</key>
  <string>%s</string>
</dict>
</plist>
`, ClaudeMemSyncLaunchdLabel, xmlEscape(script), xmlEscape(homeDir), SyncAgentInterval, xmlEscape(logPath), xmlEscape(logPath))
}

// InstallSyncAgent writes and (re)loads the scheduled sync LaunchAgent.
// Idempotent; macOS-only.
func (m *ClaudeMemManager) InstallSyncAgent(ctx context.Context, peer string) error {
	if runtime.GOOS != "darwin" {
		return errors.New("claude-mem sync agent installation currently requires macOS launchd")
	}
	if strings.TrimSpace(peer) == "" {
		return errors.New("sync peer is empty")
	}
	for _, dir := range []string{filepath.Dir(m.SyncLaunchdPlistPath()), filepath.Dir(m.SyncLogPath())} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	plist := RenderSyncAgentPlist(m.DotPath, peer, m.HomeDir, m.SyncLogPath())
	if err := atomicWriteFile(m.SyncLaunchdPlistPath(), []byte(plist), 0o644); err != nil {
		return fmt.Errorf("write sync launch agent: %w", err)
	}
	domain := "gui/" + strconv.Itoa(os.Getuid())
	target := domain + "/" + ClaudeMemSyncLaunchdLabel
	_ = osexec.CommandContext(ctx, "launchctl", "bootout", target).Run()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if osexec.CommandContext(ctx, "launchctl", "print", target).Run() != nil {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if out, err := osexec.CommandContext(ctx, "launchctl", "bootstrap", domain, m.SyncLaunchdPlistPath()).CombinedOutput(); err != nil {
		return fmt.Errorf("bootstrap claude-mem sync agent: %w: %s", err, strings.TrimSpace(string(out)))
	}
	// Record the install so `dot ai memory status` knows the peer before the
	// first successful run; an existing record (a real sync) is never
	// overwritten.
	statePath := SyncStatePath(m.HomeDir)
	if peers, err := LoadSyncState(statePath); err == nil {
		if _, ok := peers[peer]; !ok {
			_ = SaveSyncState(statePath, peer, SyncStateEntry{LastResult: "agent installed, never run"})
		}
	}
	return nil
}
