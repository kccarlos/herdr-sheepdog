package herdr

import "testing"

func TestHasTypedInput(t *testing.T) {
	for _, tc := range []struct {
		screen string
		want   bool
	}{
		{"❯ \x1b[2mTry asking something\x1b[0m\n────────", false},
		{"❯ fix \x1b[2mthe tests\x1b[0m\n────────", true},
		{"\x1b[0m› hello\n  world\n────────", true},
		{"❯ \n────────\nold output", false},
		{"previous ❯ text\n❯ \x1b[38;2;10;20;30mtyped\x1b[0m", true},
	} {
		if got := HasTypedInput(tc.screen); got != tc.want {
			t.Errorf("%q: got %v, want %v", tc.screen, got, tc.want)
		}
	}
}

func TestTypedInputUnclearWithoutPromptStyling(t *testing.T) {
	for _, screen := range []string{
		"❯ Try asking something\n────────",
		"\x1b[31mold output\x1b[0m\n❯ Try asking something\n────────",
		"no prompt here",
	} {
		if typed, known := TypedInput(screen); typed || known {
			t.Errorf("%q: typed=%v known=%v", screen, typed, known)
		}
	}
}

func TestTypedInputLiveRegion(t *testing.T) {
	for _, tc := range []struct {
		name, screen string
		typed, known bool
	}{
		{"nbsp", "────────\n\x1b[0m❯\u00a0typed\n────────\nstatus", true, true},
		{"empty", "────────\n\x1b[0m❯\n────────\nstatus", false, true},
		{"history", "\x1b[0m❯ old command\nworking\n\nstatus", false, false},
		{"history separated", "\x1b[0m› old command\n\nworking\n  status", false, false},
		{"live after history", "\x1b[0m❯ old command\n────────\n\x1b[0m❯\u00a0\x1b[2mplaceholder\x1b[0m\n────────\nstatus", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, b := TypedInput(tc.screen)
			if a != tc.typed || b != tc.known {
				t.Errorf("got %v %v", a, b)
			}
		})
	}
}
