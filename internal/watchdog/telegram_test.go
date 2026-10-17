package watchdog

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/STAIxBWLB/dotfiles/internal/config"
)

func TestLoadTelegramEnv_MissingFileIsNotConfigured(t *testing.T) {
	t.Setenv("TELEGRAM_BOT_TOKEN", "")
	t.Setenv("TELEGRAM_CHAT_ID", "")
	_, _, ok, err := LoadTelegramEnv(filepath.Join(t.TempDir(), "telegram.env"))
	if err != nil {
		t.Fatalf("missing env file must not be an error: %v", err)
	}
	if ok {
		t.Fatal("missing env file with no process env must report not-configured")
	}
}

func TestLoadTelegramEnv_ParsesFileForms(t *testing.T) {
	t.Setenv("TELEGRAM_BOT_TOKEN", "")
	t.Setenv("TELEGRAM_CHAT_ID", "")
	for name, tc := range map[string]struct {
		content   string
		wantToken string
		wantChat  string
	}{
		"plain": {
			content:   "TELEGRAM_BOT_TOKEN=abc123\nTELEGRAM_CHAT_ID=42\n",
			wantToken: "abc123", wantChat: "42",
		},
		"comments and blanks": {
			content:   "# rotated 2026-09\n\nTELEGRAM_BOT_TOKEN=tok\n# TELEGRAM_CHAT_ID=old\nTELEGRAM_CHAT_ID=-100\n",
			wantToken: "tok", wantChat: "-100",
		},
		"export prefix": {
			content:   "export TELEGRAM_BOT_TOKEN=tok\nexport TELEGRAM_CHAT_ID=@dotops\n",
			wantToken: "tok", wantChat: "@dotops",
		},
		"quoted values": {
			content:   "TELEGRAM_BOT_TOKEN=\"tok with space\"\nTELEGRAM_CHAT_ID='99'\n",
			wantToken: "tok with space", wantChat: "99",
		},
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "telegram.env")
			if err := os.WriteFile(path, []byte(tc.content), 0o600); err != nil {
				t.Fatal(err)
			}
			token, chatID, ok, err := LoadTelegramEnv(path)
			if err != nil {
				t.Fatalf("LoadTelegramEnv: %v", err)
			}
			if !ok || token != tc.wantToken || chatID != tc.wantChat {
				t.Errorf("LoadTelegramEnv = (%q, %q, %v), want (%q, %q, true)", token, chatID, ok, tc.wantToken, tc.wantChat)
			}
		})
	}
}

func TestLoadTelegramEnv_IncompleteFileIsNotConfigured(t *testing.T) {
	t.Setenv("TELEGRAM_BOT_TOKEN", "")
	t.Setenv("TELEGRAM_CHAT_ID", "")
	path := filepath.Join(t.TempDir(), "telegram.env")
	if err := os.WriteFile(path, []byte("TELEGRAM_BOT_TOKEN=tok\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, ok, err := LoadTelegramEnv(path); err != nil || ok {
		t.Errorf("token-only file = (ok=%v, err=%v), want (false, nil)", ok, err)
	}
}

func TestLoadTelegramEnv_MalformedLineFails(t *testing.T) {
	t.Setenv("TELEGRAM_BOT_TOKEN", "")
	t.Setenv("TELEGRAM_CHAT_ID", "")
	path := filepath.Join(t.TempDir(), "telegram.env")
	if err := os.WriteFile(path, []byte("TELEGRAM_BOT_TOKEN\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := LoadTelegramEnv(path); err == nil {
		t.Fatal("a line without '=' must fail, not silently parse as empty")
	} else if strings.Contains(err.Error(), "TELEGRAM_BOT_TOKEN") {
		t.Fatalf("the parse error must not echo the offending line (it may hold a mistyped credential): %v", err)
	}
}

func TestLoadTelegramEnv_ProcessEnvWins(t *testing.T) {
	t.Setenv("TELEGRAM_BOT_TOKEN", "env-token")
	t.Setenv("TELEGRAM_CHAT_ID", "env-chat")
	path := filepath.Join(t.TempDir(), "telegram.env")
	if err := os.WriteFile(path, []byte("TELEGRAM_BOT_TOKEN=file-token\nTELEGRAM_CHAT_ID=file-chat\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	token, chatID, ok, err := LoadTelegramEnv(path)
	if err != nil || !ok {
		t.Fatalf("LoadTelegramEnv: ok=%v err=%v", ok, err)
	}
	if token != "env-token" || chatID != "env-chat" {
		t.Errorf("process env must override the file, got (%q, %q)", token, chatID)
	}
	// The environment alone also configures the channel when no file exists.
	token, chatID, ok, err = LoadTelegramEnv(filepath.Join(t.TempDir(), "missing.env"))
	if err != nil || !ok || token != "env-token" || chatID != "env-chat" {
		t.Errorf("env-only configuration = (%q, %q, %v, %v)", token, chatID, ok, err)
	}
}

func TestNotifier_TelegramSendMessage(t *testing.T) {
	t.Setenv("TELEGRAM_BOT_TOKEN", "")
	t.Setenv("TELEGRAM_CHAT_ID", "")
	var gotPath, gotContentType string
	var gotBody telegramMessage
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotContentType = r.Header.Get("Content-Type")
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &gotBody)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	envPath := filepath.Join(t.TempDir(), "telegram.env")
	if err := os.WriteFile(envPath, []byte("TELEGRAM_BOT_TOKEN=tok123\nTELEGRAM_CHAT_ID=-100200\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	n := &Notifier{
		Settings: NotifySettings{Telegram: TelegramSettings{Enabled: true, EnvPath: envPath, APIBase: srv.URL}},
		GOOS:     "linux",
	}
	if err := n.Notify(context.Background(), "critical", "refusal loop"); err != nil {
		t.Fatalf("Notify: %v", err)
	}
	if gotPath != "/bottok123/sendMessage" {
		t.Errorf("request path = %q, want the token in /bot<token>/sendMessage", gotPath)
	}
	if gotContentType != "application/json" {
		t.Errorf("content type = %q", gotContentType)
	}
	if gotBody.ChatID != float64(-100200) { // JSON numbers decode as float64
		t.Errorf("chat_id = %#v, want a numeric -100200", gotBody.ChatID)
	}
	if gotBody.Text != "[critical] refusal loop" {
		t.Errorf("text = %q", gotBody.Text)
	}
	if !gotBody.DisableWebPagePreview {
		t.Error("disable_web_page_preview must be true")
	}
}

func TestNotifier_TelegramInfoLevelHasNoPrefix(t *testing.T) {
	t.Setenv("TELEGRAM_BOT_TOKEN", "tok")
	t.Setenv("TELEGRAM_CHAT_ID", "1")
	var gotBody telegramMessage
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &gotBody)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	n := &Notifier{
		Settings: NotifySettings{Telegram: TelegramSettings{Enabled: true, EnvPath: filepath.Join(t.TempDir(), "missing.env"), APIBase: srv.URL}},
		GOOS:     "linux",
	}
	if err := n.Notify(context.Background(), "info", "recovered"); err != nil {
		t.Fatalf("Notify: %v", err)
	}
	if gotBody.Text != "recovered" {
		t.Errorf("info text = %q, want the bare message", gotBody.Text)
	}
	if gotBody.ChatID != float64(1) {
		t.Errorf("chat_id = %#v", gotBody.ChatID)
	}
}

func TestNotifier_TelegramStringChatID(t *testing.T) {
	t.Setenv("TELEGRAM_BOT_TOKEN", "tok")
	t.Setenv("TELEGRAM_CHAT_ID", "@dotops")
	var gotBody telegramMessage
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &gotBody)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	n := &Notifier{
		Settings: NotifySettings{Telegram: TelegramSettings{Enabled: true, EnvPath: filepath.Join(t.TempDir(), "missing.env"), APIBase: srv.URL}},
		GOOS:     "linux",
	}
	if err := n.Notify(context.Background(), "warn", "x"); err != nil {
		t.Fatalf("Notify: %v", err)
	}
	if gotBody.ChatID != "@dotops" {
		t.Errorf("a non-numeric chat id must stay a string, got %#v", gotBody.ChatID)
	}
}

func TestNotifier_TelegramFailureSurfaces(t *testing.T) {
	t.Setenv("TELEGRAM_BOT_TOKEN", "tok")
	t.Setenv("TELEGRAM_CHAT_ID", "1")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()
	n := &Notifier{
		Settings: NotifySettings{Telegram: TelegramSettings{Enabled: true, EnvPath: filepath.Join(t.TempDir(), "missing.env"), APIBase: srv.URL}},
		GOOS:     "linux",
	}
	err := n.Notify(context.Background(), "warn", "x")
	if err == nil || !strings.Contains(err.Error(), "telegram") {
		t.Fatalf("a 5xx from Telegram must be an error naming the channel: %v", err)
	}
}

func TestNotifier_TelegramNetworkErrorRedactsToken(t *testing.T) {
	t.Setenv("TELEGRAM_BOT_TOKEN", "secret-token-123")
	t.Setenv("TELEGRAM_CHAT_ID", "1")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	srv.Close() // guarantee a connection error whose url.Error carries the token URL
	n := &Notifier{
		Settings: NotifySettings{Telegram: TelegramSettings{Enabled: true, EnvPath: filepath.Join(t.TempDir(), "missing.env"), APIBase: srv.URL}},
		GOOS:     "linux",
	}
	err := n.Notify(context.Background(), "warn", "x")
	if err == nil {
		t.Fatal("a refused connection must be an error")
	}
	if strings.Contains(err.Error(), "secret-token-123") {
		t.Fatalf("the bot token must never appear in a logged error: %v", err)
	}
}

func TestNotifier_TelegramEnabledButUnconfiguredIsNoOp(t *testing.T) {
	t.Setenv("TELEGRAM_BOT_TOKEN", "")
	t.Setenv("TELEGRAM_CHAT_ID", "")
	n := &Notifier{
		Settings: NotifySettings{Telegram: TelegramSettings{Enabled: true, EnvPath: filepath.Join(t.TempDir(), "missing.env"), APIBase: "http://127.0.0.1:1"}},
		GOOS:     "linux",
	}
	if err := n.Notify(context.Background(), "critical", "x"); err != nil {
		t.Fatalf("enabled-without-credentials must be a clean no-op, got %v", err)
	}
}

func TestResolveNotify_TelegramDefaults(t *testing.T) {
	settings := ResolveNotify(config.WatchdogNotifyConfig{Telegram: config.WatchdogTelegramConfig{Enabled: true}}, t.TempDir())
	if settings.Telegram.APIBase != DefaultTelegramAPIBase {
		t.Errorf("APIBase = %q, want %q", settings.Telegram.APIBase, DefaultTelegramAPIBase)
	}
	if settings.Telegram.ReminderInterval != DefaultTelegramReminderInterval {
		t.Errorf("ReminderInterval = %v, want %v", settings.Telegram.ReminderInterval, DefaultTelegramReminderInterval)
	}
	if !filepath.IsAbs(settings.Telegram.EnvPath) {
		t.Errorf("EnvPath must resolve absolute, got %q", settings.Telegram.EnvPath)
	}
	// A disabled channel keeps its knobs at zero: no phantom reminder cadence.
	off := ResolveNotify(config.WatchdogNotifyConfig{}, t.TempDir())
	if off.Telegram.ReminderInterval != 0 || off.Telegram.Enabled {
		t.Errorf("disabled telegram = %#v", off.Telegram)
	}
}
