package cli

import (
	"strings"
	"testing"
)

// fakeHubTmux scripts tmux for focusHub: the current session line and the
// session's panes, recording every call.
func fakeHubTmux(t *testing.T, current, panes string) *[]string {
	t.Helper()
	var calls []string
	old := hubTmux
	t.Cleanup(func() { hubTmux = old })
	hubTmux = func(args ...string) (string, error) {
		calls = append(calls, strings.Join(args, " "))
		switch args[0] {
		case "display-message":
			return current, nil
		case "list-panes":
			return panes, nil
		case "new-window":
			return "%9", nil
		}
		return "", nil
	}
	return &calls
}

func TestFocusHub(t *testing.T) {
	root := t.TempDir()
	t.Run("another session gets guidance only", func(t *testing.T) {
		calls := fakeHubTmux(t, "other\t"+root, "")
		msg, err := focusHub("proj", root, "/s/ahub-pane.sh")
		if err != nil || !strings.Contains(msg, "tmux switch-client -t proj") {
			t.Fatalf("msg %q err %v", msg, err)
		}
		if len(*calls) != 1 {
			t.Fatalf("only the session read is allowed, got %v", *calls)
		}
	})
	t.Run("same name at another root gets guidance only", func(t *testing.T) {
		fakeHubTmux(t, "proj\t"+t.TempDir(), "")
		if msg, _ := focusHub("proj", root, "/s/ahub-pane.sh"); !strings.Contains(msg, "is not the proj workspace") {
			t.Fatalf("msg %q", msg)
		}
	})
	t.Run("an existing AHUB slot is focused", func(t *testing.T) {
		calls := fakeHubTmux(t, "proj\t"+root, "%1\t@1\t\n%2\t@1\thub\n%3\t@2\tverify")
		if _, err := focusHub("proj", root, "/s/ahub-pane.sh"); err != nil {
			t.Fatal(err)
		}
		got := strings.Join(*calls, "\n")
		for _, want := range []string{"wait-for -L dot-hub-proj", "select-window -t @1", "select-pane -t %2", "wait-for -U dot-hub-proj"} {
			if !strings.Contains(got, want) {
				t.Errorf("missing %q in\n%s", want, got)
			}
		}
		if strings.Contains(got, "new-window") || strings.Contains(got, "send-keys") {
			t.Errorf("an existing slot must not add a window or type into a pane:\n%s", got)
		}
	})
	t.Run("a legacy session gets one owned hub window", func(t *testing.T) {
		calls := fakeHubTmux(t, "proj\t"+root, "%1\t@1\t\n%2\t@1\t")
		msg, err := focusHub("proj", root, "/s/ahub-pane.sh")
		if err != nil || !strings.Contains(msg, "Added a 'hub' window") {
			t.Fatalf("msg %q err %v", msg, err)
		}
		got := strings.Join(*calls, "\n")
		for _, want := range []string{"new-window -t =proj: -n hub -c " + root, "set-option -p -t %9 @dot_slot hub", "send-keys -t %9 bash '/s/ahub-pane.sh' hub '" + root + "' Enter", "wait-for -U dot-hub-proj"} {
			if !strings.Contains(got, want) {
				t.Errorf("missing %q in\n%s", want, got)
			}
		}
	})
}
