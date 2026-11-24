package herdr

import "testing"

func TestTypedInputUnclearWithoutPromptStyling(t *testing.T) {
	for _, screen := range []string{
		"❯ Try asking something\n────────",
		"\x1b[31mold output\x1b[0m\n❯ Try asking something\n────────",
		"❯ \n────────\nold output",
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
		{"dim placeholder", "❯ \x1b[2mTry asking something\x1b[0m\n────────", false, true},
		{"typed before dim", "❯ fix \x1b[2mthe tests\x1b[0m\n────────", true, true},
		{"wrapped codex ruled", "\x1b[0m› hello\n  world\n────────", true, true},
		{"truecolor typed after history", "previous ❯ text\n❯ \x1b[38;2;10;20;30mtyped\x1b[0m", true, true},
		{"nbsp", "────────\n\x1b[0m❯\u00a0typed\n────────\nstatus", true, true},
		{"empty", "────────\n\x1b[0m❯\n────────\nstatus", false, true},
		{"history", "\x1b[0m❯ old command\nworking\n\nstatus", false, false},
		{"history separated", "\x1b[0m› old command\n\nworking\n  status", false, false},
		{"live after history", "\x1b[0m❯ old command\n────────\n\x1b[0m❯\u00a0\x1b[2mplaceholder\x1b[0m\n────────\nstatus", false, true},
		{"single rule history", "\x1b[0m❯ old command\nworking on it\n────────\nstatus", false, false},
		{"boxed paragraphs", "────────\n\x1b[0m❯ \n\n  paragraph\n────────\nstatus", true, true},
		{"wrapped codex", "\x1b[0m› line 1\n  line 2\n  line 3\n\n  footer", true, true},
		{"colon color dim", "\x1b[2m\x1b[38:5:240m❯ placeholder\n────────", false, true},
		{"private csi", "\x1b[0m\x1b[>4;2m❯ typed\n────────", true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, b := TypedInput(tc.screen)
			if a != tc.typed || b != tc.known {
				t.Errorf("got %v %v", a, b)
			}
		})
	}
}
