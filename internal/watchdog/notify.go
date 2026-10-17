package watchdog

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/STAIxBWLB/dotfiles/internal/exec"
)

// Notifier fans an alert out to the configured channels. All channels are
// independently optional: the macOS notification only fires on darwin when
// enabled, the ntfy POST is skipped when the topic URL is empty, and the
// Telegram POST is skipped when the channel is disabled or no credentials
// resolve.
type Notifier struct {
	Settings NotifySettings
	Runner   *exec.Runner // osascript delivery
	HTTP     *http.Client // ntfy and telegram delivery; nil uses a default client
	GOOS     string       // injected so tests can pose as either platform
}

// NewNotifier builds a Notifier for the current platform.
func NewNotifier(s NotifySettings, runner *exec.Runner, goos string) *Notifier {
	return &Notifier{Settings: s, Runner: runner, GOOS: goos}
}

// Notify delivers one alert. Every configured channel is attempted even
// when an earlier one fails — a dead macOS or ntfy path must not starve the
// remaining channels — and all channel failures are joined into the returned
// error: an alert path that fails silently is the incident this feature
// exists to prevent.
func (n *Notifier) Notify(ctx context.Context, level, msg string) error {
	var errs []error
	if n.Settings.MacOS && n.GOOS == "darwin" {
		if err := n.notifyMacOS(ctx, level, msg); err != nil {
			errs = append(errs, fmt.Errorf("macOS notification: %w", err))
		}
	}
	if n.Settings.NtfyURL != "" {
		if err := n.notifyNtfy(ctx, level, msg); err != nil {
			errs = append(errs, fmt.Errorf("ntfy notification: %w", err))
		}
	}
	if n.Settings.Telegram.Enabled {
		if err := n.notifyTelegram(ctx, level, msg); err != nil {
			errs = append(errs, fmt.Errorf("telegram notification: %w", err))
		}
	}
	return errors.Join(errs...)
}

func (n *Notifier) notifyMacOS(ctx context.Context, level, msg string) error {
	if n.Runner == nil {
		return fmt.Errorf("no runner configured")
	}
	script := fmt.Sprintf(`display notification "%s" with title "dot watchdog" subtitle "%s"`,
		appleScriptEscape(msg), appleScriptEscape(level))
	_, err := n.Runner.Run(ctx, "osascript", "-e", script)
	return err
}

func (n *Notifier) notifyNtfy(ctx context.Context, level, msg string) error {
	client := n.HTTP
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, n.Settings.NtfyURL, strings.NewReader(msg))
	if err != nil {
		return err
	}
	req.Header.Set("Title", "dot watchdog")
	req.Header.Set("Priority", ntfyPriority(level))
	req.Header.Set("Tags", ntfyTag(level))
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	if resp.StatusCode >= 300 {
		return fmt.Errorf("ntfy returned %s", resp.Status)
	}
	return nil
}

// ntfyPriority maps alert levels onto ntfy's 1-5 named priorities.
func ntfyPriority(level string) string {
	switch level {
	case "critical":
		return "urgent"
	case "warn":
		return "high"
	default:
		return "default"
	}
}

func ntfyTag(level string) string {
	switch level {
	case "critical":
		return "rotating_light"
	case "warn":
		return "warning"
	default:
		return "information_source"
	}
}

// appleScriptEscape makes a string literal safe inside an AppleScript
// double-quoted string.
func appleScriptEscape(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	return strings.ReplaceAll(s, `"`, `\"`)
}
