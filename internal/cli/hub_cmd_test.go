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
	t.Setenv("TMUX_PANE", "")
	t.Run("another session gets guidance only", func(t *testing.T) {
		calls := fakeHubTmux(t, "other\t"+root, "")
		msg, err := focusSlot("proj", root, "/s", hubSlots["console"])
		if err != nil || !strings.Contains(msg, "tmux switch-client -t proj") {
			t.Fatalf("msg %q err %v", msg, err)
		}
		if len(*calls) != 1 {
			t.Fatalf("only the session read is allowed, got %v", *calls)
		}
	})
	t.Run("same name at another root gets guidance only", func(t *testing.T) {
		fakeHubTmux(t, "proj\t"+t.TempDir(), "")
		if msg, _ := focusSlot("proj", root, "/s", hubSlots["console"]); !strings.Contains(msg, "is not the proj workspace") {
			t.Fatalf("msg %q", msg)
		}
	})
	t.Run("an existing AHUB slot is focused", func(t *testing.T) {
		calls := fakeHubTmux(t, "proj\t"+root, "%1\t@1\t\n%2\t@1\thub\n%3\t@2\tverify")
		if _, err := focusSlot("proj", root, "/s", hubSlots["console"]); err != nil {
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
		msg, err := focusSlot("proj", root, "/s", hubSlots["console"])
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

// git and files reuse the tools.sh launchers in a new owned window, and a key
// binding's run-shell reads the caller's session through $TMUX_PANE (#331).
func TestFocusSlotTools(t *testing.T) {
	root := t.TempDir()
	var ran []string
	old := hubRunTool
	t.Cleanup(func() { hubRunTool = old })
	hubRunTool = func(script, fn, pane, dir string) error {
		ran = append(ran, strings.Join([]string{script, fn, pane, dir}, " "))
		return nil
	}

	t.Setenv("TMUX_PANE", "%4")
	calls := fakeHubTmux(t, "proj\t"+root, "%1\t@1\thub\n%2\t@2\tverify")
	msg, err := focusSlot("proj", root, "/s", hubSlots["git"])
	if err != nil || !strings.Contains(msg, "Added a 'git' window") {
		t.Fatalf("msg %q err %v", msg, err)
	}
	got := strings.Join(*calls, "\n")
	for _, want := range []string{"display-message -p -t %4", "new-window -t =proj: -n git -c " + root, "set-option -p -t %9 @dot_slot git"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in\n%s", want, got)
		}
	}
	if strings.Contains(got, "send-keys") || len(ran) != 1 || ran[0] != "/s/tools.sh run_lazygit %9 "+root {
		t.Fatalf("the new window must run only the tools.sh launcher: calls\n%s\nran %v", got, ran)
	}

	ran = nil
	calls = fakeHubTmux(t, "proj\t"+root, "%1\t@1\thub\n%5\t@3\tfiles")
	if _, err := focusSlot("proj", root, "/s", hubSlots["files"]); err != nil {
		t.Fatal(err)
	}
	got = strings.Join(*calls, "\n")
	if !strings.Contains(got, "select-pane -t %5") || strings.Contains(got, "new-window") || len(ran) != 0 {
		t.Fatalf("an existing files window must only be focused:\n%s\nran %v", got, ran)
	}

	fakeHubTmux(t, "other\t"+root, "")
	if msg, _ := focusSlot("proj", root, "/s", hubSlots["git"]); !strings.Contains(msg, "is not the proj workspace") || strings.Contains(msg, "ahub --project") {
		t.Fatalf("guidance for git elsewhere = %q", msg)
	}
}
