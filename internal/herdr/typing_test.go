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

func TestTypedInputPlain(t *testing.T) {
	for _, tc := range []struct {
		name, screen string
		typed, known bool
	}{
		{"claude empty", "────────\n❯\n────────\n  status", false, true},
		{"claude empty nbsp", "────────\n❯\u00a0\n────────\n  status", false, true},
		{"claude placeholder", "────────\n❯\u00a0Try \"create a util logging.py that...\"\n────────\n  status", false, true},
		{"claude wrapped placeholder", "────────\n❯\u00a0Try \"create a util\n  logging.py that...\"\n────────\n  status", false, true},
		{"claude truncated placeholder", "────────\n❯\u00a0Try \"create a util…\n────────\n  status", false, true},
		{"claude queue hint", "────────\n❯\u00a0Press up to edit queued messages\n────────\n  status", false, true},
		{"claude queue edit hint", "────────\n❯\u00a0Press Enter to edit the selected message, or up again for history\n────────\n  status", false, true},
		{"claude subagent hint", "────────\n❯\u00a0Message @researcher…\n────────\n  status", false, true},
		{"claude typed", "────────\n❯\u00a0halftyped\n────────\n  status", true, true},
		{"claude boxed paragraphs", "────────\n❯ \n\n  paragraph\n────────\n  status", true, true},
		{"single rule typed", "❯ unfinished words\n────────", true, true},
		{"codex placeholder", "› Ask Codex to do anything\n\n  gpt-5.6-luna max · ~/repo", false, true},
		{"codex followup placeholder", "› Ask a follow-up question\n\n  gpt-5.6-luna max · ~/repo", false, true},
		{"codex typed", "› halftyped\n\n  gpt-5.6-luna max · ~/repo", true, true},
		{"codex wrapped typed", "› line 1\n  line 2\n\n  gpt-5.6-luna max · ~/repo", true, true},
		{"bare empty prompt", "❯ ", false, true},
		{"bare unboxed text unclear", "❯ claude", false, false},
		{"history", "❯ old command\nworking\n\nstatus", false, false},
		{"history separated", "› old command\n\nworking\n  status", false, false},
		{"single rule history", "❯ old command\nworking on it\n────────\nstatus", false, false},
		{"no prompt", "no prompt here", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			typed, known := TypedInputPlain(tc.screen)
			if typed != tc.typed || known != tc.known {
				t.Errorf("got (%v, %v), want (%v, %v)", typed, known, tc.typed, tc.known)
			}
		})
	}
}
