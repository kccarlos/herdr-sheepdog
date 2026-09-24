package herdr_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/herdr"
	"github.com/kccarlos/herdr-sheepdog/internal/herdr/herdrtest"
)

// agent_session as the running herdr sends it (the recorded samples):
// kind "id" for Codex, Claude Code and agy alike.
func TestAgentSessionDecodedFromFixtures(t *testing.T) {
	cases := []struct {
		file string
		want herdr.AgentSession
	}{
		{"codex-approval-prompt.agent-get.json", herdr.AgentSession{Source: "herdr:codex", Agent: "codex", Kind: herdr.SessionKindID, Value: "01a0b1cb-a237-7722-8eea-8db9f1dd95b6"}},
		{"claude-permission-prompt.agent-get.json", herdr.AgentSession{Source: "herdr:claude", Agent: "claude", Kind: herdr.SessionKindID, Value: "366f482c-d489-4dda-9ba6-52d26f9069b5"}},
	}
	for _, c := range cases {
		data, err := os.ReadFile(filepath.Join(fixtureDir, c.file))
		if err != nil {
			t.Fatal(err)
		}
		var resp struct {
			Result struct {
				Agent herdr.AgentInfo `json:"agent"`
			} `json:"result"`
		}
		if err := json.Unmarshal(data, &resp); err != nil {
			t.Fatal(err)
		}
		if got := resp.Result.Agent.Session; got == nil || *got != c.want {
			t.Errorf("%s: session = %+v, want %+v", c.file, got, c.want)
		}
	}
	data, err := os.ReadFile(filepath.Join(fixtureDir, "agy-approval-prompt.agent-list.json"))
	if err != nil {
		t.Fatal(err)
	}
	var list struct {
		Result struct {
			Agents []herdr.AgentInfo `json:"agents"`
		} `json:"result"`
	}
	if err := json.Unmarshal(data, &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Result.Agents) == 0 || list.Result.Agents[0].Session == nil || list.Result.Agents[0].Session.Source != "herdr:antigravity_cli" {
		t.Errorf("agy session = %+v", list.Result.Agents)
	}
}

// The status events carry no session: the watcher keeps the snapshot's
// and puts it, with the cwd, on every transition.
func TestWatcherCarriesTheSession(t *testing.T) {
	srv := herdrtest.New(t)
	a := codexAgent()
	a.Session = &herdr.AgentSession{Source: "herdr:codex", Agent: "codex", Kind: herdr.SessionKindID, Value: "01a0b1cb"}
	srv.SetAgent(a)
	w, _, _ := startWatcher(t, srv, time.Hour, 1)
	ch := w.Transitions()
	tr := expectTransition(t, ch, "", herdr.StateIdle)
	if tr.Session == nil || tr.Session.Value != "01a0b1cb" || tr.Cwd != "/repo" {
		t.Errorf("bootstrap transition session %+v cwd %q", tr.Session, tr.Cwd)
	}
	srv.EmitStatus("w5:p1", herdr.StateWorking)
	tr = expectTransition(t, ch, herdr.StateIdle, herdr.StateWorking)
	if tr.Source != "socket" || tr.Session == nil || tr.Session.Kind != herdr.SessionKindID {
		t.Errorf("socket transition = %+v, session %+v", tr, tr.Session)
	}
	// Not logged: the session names a transcript on disk.
	b, _ := json.Marshal(tr)
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	if _, ok := m["session"]; ok || m["Session"] != nil {
		t.Errorf("transition JSON carries the session: %s", b)
	}
}
