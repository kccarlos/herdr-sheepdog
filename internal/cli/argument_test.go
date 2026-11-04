package cli

import (
	"context"
	"errors"
	"testing"
)

// {question} and {session} are argv elements of the read-only CLIs: one
// that starts with "-" would be parsed as a flag and could switch the
// sandbox off, so Run refuses it before anything starts: the result
// carries no argv, because the child never ran.
func TestRunRefusesFlagLikeQuestionAndSession(t *testing.T) {
	bin := fakeBinPath(t)
	r := newTestRunner(t, Config{CodexBin: bin, ClaudeBin: bin}, Detection{Codex: true, Claude: true})
	ws := t.TempDir()

	for _, tc := range []struct {
		name string
		q    Query
	}{
		{"question", Query{Question: "--sandbox=danger-full-access"}},
		{"question after trim", Query{Question: "  -c sandbox_mode=danger-full-access"}},
		{"session", Query{Question: "why?", Session: "--dangerously-bypass-approvals-and-sandbox"}},
		{"session with a space", Query{Question: "why?", Session: "abc def"}},
		{"session with a slash", Query{Question: "why?", Session: "../abc"}},
		{"fork session", Query{Question: "why?", Session: "-c", Fork: true}},
	} {
		for _, backend := range []Backend{Codex, Claude} {
			t.Run(tc.name+"/"+string(backend), func(t *testing.T) {
				q := tc.q
				q.Backend, q.Workspace = backend, ws
				res, err := r.Run(context.Background(), q)
				if !errors.Is(err, ErrBadArgument) {
					t.Fatalf("err = %v, want ErrBadArgument", err)
				}
				if len(res.Argv) != 0 {
					t.Errorf("argv = %q: the child must not start", res.Argv)
				}
			})
		}
	}

	// Real ids and ordinary questions still run.
	for _, session := range []string{"", "sess-1", "019a1b2c-3d4e-7f50-8a9b-0c1d2e3f4a5b"} {
		q := Query{Backend: Claude, Workspace: ws, Question: "what is 2 - 1?", Session: session}
		if _, err := r.Run(context.Background(), q); errors.Is(err, ErrBadArgument) {
			t.Errorf("session %q refused: %v", session, err)
		}
	}
}
