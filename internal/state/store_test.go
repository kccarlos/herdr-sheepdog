package state

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/herdr"
)

var t0 = time.Date(2026, 9, 17, 16, 55, 0, 0, time.UTC)

func tr(pane string, from, to herdr.State, at time.Time) herdr.Transition {
	return herdr.Transition{PaneID: pane, From: from, To: to, At: at, Agent: "codex", WorkspaceID: "w5", WorkspaceLabel: "sheepdog-recon", Source: "socket"}
}

func mustApply(t *testing.T, s *Store, x herdr.Transition) (Agent, bool) {
	t.Helper()
	a, changed, err := s.Apply(x)
	if err != nil {
		t.Fatalf("Apply(%+v): %v", x, err)
	}
	return a, changed
}

func TestApplySequenceBuildsTable(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "state.json"), Options{})
	if err != nil {
		t.Fatal(err)
	}

	a, changed := mustApply(t, s, tr("w5:p1", "", herdr.StateIdle, t0))
	if !changed || a.Alias != "sheepdog-recon-codex" || !a.AliasAuto || a.Kind != "codex" || a.State != herdr.StateIdle || !a.StateSince.Equal(t0) {
		t.Fatalf("after discovery: %+v", a)
	}

	a, _ = mustApply(t, s, tr("w5:p1", herdr.StateIdle, herdr.StateWorking, t0.Add(10*time.Second)))
	if a.State != herdr.StateWorking || !a.StateSince.Equal(t0.Add(10*time.Second)) {
		t.Fatalf("after working: %+v", a)
	}

	// Same state again (e.g. snapshot re-emit on restart): nothing changes,
	// state_since is kept.
	a, changed = mustApply(t, s, tr("w5:p1", herdr.StateWorking, herdr.StateWorking, t0.Add(20*time.Second)))
	if changed || !a.StateSince.Equal(t0.Add(10*time.Second)) {
		t.Fatalf("repeat state changed=%v since=%v", changed, a.StateSince)
	}

	a, _ = mustApply(t, s, tr("w5:p1", herdr.StateWorking, herdr.StateBlocked, t0.Add(30*time.Second)))
	if a.LastBlock == nil || a.LastBlock.Answered || !a.LastBlock.SeenAt.Equal(t0.Add(30*time.Second)) {
		t.Fatalf("blocked should record last_block: %+v", a.LastBlock)
	}
	a, _ = mustApply(t, s, tr("w5:p1", herdr.StateBlocked, herdr.StateDone, t0.Add(40*time.Second)))
	if a.State != herdr.StateDone || a.LastBlock == nil || !a.LastBlock.Answered {
		t.Fatalf("leaving blocked should mark answered: %+v", a)
	}

	// Second agent in another workspace.
	x := tr("w7:p1", "", herdr.StateWorking, t0)
	x.Agent, x.WorkspaceID, x.WorkspaceLabel, x.Name = "claude", "w7", "Auth Service", "auth"
	a, _ = mustApply(t, s, x)
	if a.Alias != "auth-service-auth" {
		t.Errorf("alias with name = %q", a.Alias)
	}
	if l := s.List(); len(l) != 2 || l[0].PaneID != "w5:p1" || l[1].PaneID != "w7:p1" {
		t.Errorf("List = %+v", l)
	}
	if got, ok := s.ByAlias("auth-service-auth"); !ok || got.PaneID != "w7:p1" {
		t.Errorf("ByAlias = %+v, %v", got, ok)
	}

	// Closed pane is dropped along with its pending action.
	if err := s.SetPending(PendingAction{ID: "p1", PaneID: "w5:p1", Kind: PendingAnswerPrompt, CreatedAt: t0, ExpiresAt: t0.Add(2 * time.Minute)}); err != nil {
		t.Fatal(err)
	}
	c := tr("w5:p1", herdr.StateDone, herdr.StateUnknown, t0.Add(time.Minute))
	c.Closed = true
	if _, changed = mustApply(t, s, c); !changed {
		t.Error("closing a known pane should change the table")
	}
	if _, ok := s.Get("w5:p1"); ok {
		t.Error("closed pane still present")
	}
	if _, ok := s.Pending(); ok {
		t.Error("pending action for closed pane should be cleared")
	}
	if _, changed = mustApply(t, s, c); changed {
		t.Error("closing an unknown pane should be a no-op")
	}
}

func TestPersistenceRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nested", "state.json")
	s, err := Open(path, Options{})
	if err != nil {
		t.Fatal(err)
	}
	mustApply(t, s, tr("w5:p1", "", herdr.StateWorking, t0))
	mustApply(t, s, tr("w5:p1", herdr.StateWorking, herdr.StateBlocked, t0.Add(5*time.Second)))
	if err := s.SetAlias("w5:p1", "Auth!"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetPending(PendingAction{ID: "p1", PaneID: "w5:p1", Kind: PendingAnswerPrompt, SpokenSummary: "auth asks to create hello.txt", CreatedAt: t0, ExpiresAt: t0.Add(2 * time.Minute)}); err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateSession(func(ss *Session) { ss.VoiceConnected = true; ss.LastBriefing = "one agent"; ss.LastBriefingAt = &t0 }); err != nil {
		t.Fatal(err)
	}

	// No temp files left behind, file is private.
	entries, _ := os.ReadDir(filepath.Dir(path))
	if len(entries) != 1 || entries[0].Name() != "state.json" {
		t.Errorf("dir contents = %v", entries)
	}
	if fi, err := os.Stat(path); err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("state file mode = %v, err %v", fi.Mode(), err)
	}

	// "Kill" the daemon: open a fresh store on the same file.
	s2, err := Open(path, Options{})
	if err != nil {
		t.Fatal(err)
	}
	a, ok := s2.Get("w5:p1")
	if !ok || a.Alias != "auth" || a.AliasAuto || a.State != herdr.StateBlocked || !a.StateSince.Equal(t0.Add(5*time.Second)) || a.Kind != "codex" || a.WorkspaceLabel != "sheepdog-recon" {
		t.Fatalf("restored row = %+v", a)
	}
	if a.LastBlock == nil || !a.LastBlock.SeenAt.Equal(t0.Add(5*time.Second)) {
		t.Errorf("last_block not restored: %+v", a.LastBlock)
	}
	p, ok := s2.Pending()
	if !ok || p.ID != "p1" || p.SpokenSummary != "auth asks to create hello.txt" || !p.ExpiresAt.Equal(t0.Add(2*time.Minute)) {
		t.Errorf("pending restored = %+v, %v", p, ok)
	}
	if ss := s2.Snapshot().Session; !ss.VoiceConnected || ss.LastBriefing != "one agent" || ss.LastBriefingAt == nil || !ss.LastBriefingAt.Equal(t0) {
		t.Errorf("session restored = %+v", ss)
	}
	// Re-applying the restart bootstrap transition keeps state_since.
	a, changed := mustApply(t, s2, tr("w5:p1", "", herdr.StateBlocked, t0.Add(time.Hour)))
	if changed || !a.StateSince.Equal(t0.Add(5*time.Second)) {
		t.Errorf("bootstrap after restart changed=%v since=%v", changed, a.StateSince)
	}
	if err := s2.ClearPending(); err != nil {
		t.Fatal(err)
	}
	if _, ok := s2.Pending(); ok {
		t.Error("ClearPending did not clear")
	}

	// The file is plain, indented JSON with the documented keys.
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"agents", "session"} {
		if _, ok := doc[k]; !ok {
			t.Errorf("missing top-level key %q in %s", k, raw)
		}
	}
	if _, ok := doc["pending_action"]; ok {
		t.Errorf("cleared pending_action still serialised: %s", raw)
	}
}

func TestAliasDefaultsAndUniqueness(t *testing.T) {
	var warn bytes.Buffer
	s, err := Open("", Options{Aliases: map[string]string{"w9:p1": "auth"}, Warn: &warn})
	if err != nil {
		t.Fatal(err)
	}
	// Config alias wins and is pinned.
	x := tr("w9:p1", "", herdr.StateIdle, t0)
	a, _ := mustApply(t, s, x)
	if a.Alias != "auth" || a.AliasAuto {
		t.Errorf("config alias: %+v", a)
	}
	// Two agents of the same kind in the same workspace: -2 suffix.
	y := tr("w5:p1", "", herdr.StateIdle, t0)
	a, _ = mustApply(t, s, y)
	if a.Alias != "sheepdog-recon-codex" {
		t.Errorf("first derived = %q", a.Alias)
	}
	z := tr("w5:p2", "", herdr.StateIdle, t0)
	a, _ = mustApply(t, s, z)
	if a.Alias != "sheepdog-recon-codex-2" {
		t.Errorf("second derived = %q", a.Alias)
	}
	z3 := tr("w5:p3", "", herdr.StateIdle, t0)
	if a, _ = mustApply(t, s, z3); a.Alias != "sheepdog-recon-codex-3" {
		t.Errorf("third derived = %q", a.Alias)
	}
	if !strings.Contains(warn.String(), "already used") {
		t.Errorf("collision should be warned about: %q", warn.String())
	}
	// A derived alias is re-derived when identity details arrive later;
	// a bare pane with nothing known gets "agent".
	bare := herdr.Transition{PaneID: "w1:p1", To: herdr.StateUnknown, At: t0}
	if a, _ = mustApply(t, s, bare); a.Alias != "agent" {
		t.Errorf("bare alias = %q", a.Alias)
	}
	later := herdr.Transition{PaneID: "w1:p1", To: herdr.StateUnknown, At: t0, Agent: "claude", WorkspaceLabel: "Docs & Site"}
	if a, _ = mustApply(t, s, later); a.Alias != "docs-site-claude" {
		t.Errorf("re-derived alias = %q", a.Alias)
	}
	// SetAlias slugs, rejects collisions and empties, and pins.
	if err := s.SetAlias("w5:p2", "VPS One"); err != nil {
		t.Fatal(err)
	}
	if a, _ := s.Get("w5:p2"); a.Alias != "vps-one" || a.AliasAuto {
		t.Errorf("SetAlias result = %+v", a)
	}
	if err := s.SetAlias("w5:p3", "vps one"); err == nil {
		t.Error("collision should be rejected")
	}
	if err := s.SetAlias("w5:p3", "!!!"); err == nil {
		t.Error("empty slug should be rejected")
	}
	if err := s.SetAlias("w404:p1", "x"); err == nil {
		t.Error("unknown pane should be rejected")
	}
	if a, _ := s.Get("w5:p3"); a.Alias != "sheepdog-recon-codex-3" {
		t.Errorf("rejected SetAlias must not change alias: %q", a.Alias)
	}

	// Config aliases apply to rows loaded from disk.
	path := filepath.Join(t.TempDir(), "s.json")
	s1, _ := Open(path, Options{})
	mustApply(t, s1, tr("w5:p1", "", herdr.StateIdle, t0))
	s2, err := Open(path, Options{Aliases: map[string]string{"w5:p1": "auth"}})
	if err != nil {
		t.Fatal(err)
	}
	if a, _ := s2.Get("w5:p1"); a.Alias != "auth" || a.AliasAuto {
		t.Errorf("config alias not applied on load: %+v", a)
	}
}

func TestSlug(t *testing.T) {
	cases := map[string]string{
		"Auth Service":   "auth-service",
		"  --VPS_one-- ": "vps-one",
		"kvoice":         "kvoice",
		"héllo wörld":    "h-llo-w-rld",
		"!!!":            "",
		"w5:p1":          "w5-p1",
	}
	for in, want := range cases {
		if got := Slug(in); got != want {
			t.Errorf("Slug(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestCorruptFileStartsEmptyWithWarning(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	if err := os.WriteFile(path, []byte("{\"agents\": {\"w5:p1\": [broken"), 0o600); err != nil {
		t.Fatal(err)
	}
	var warn bytes.Buffer
	s, err := Open(path, Options{Warn: &warn})
	if err != nil {
		t.Fatalf("corrupt file must not fail Open: %v", err)
	}
	if len(s.List()) != 0 {
		t.Errorf("expected empty table, got %+v", s.List())
	}
	if !strings.Contains(warn.String(), "corrupt") {
		t.Errorf("expected a warning, got %q", warn.String())
	}
	if _, err := os.Stat(path + ".corrupt"); err != nil {
		t.Errorf("corrupt file should be moved aside: %v", err)
	}
	// The store is usable and writes a fresh file.
	mustApply(t, s, tr("w5:p1", "", herdr.StateIdle, t0))
	if _, err := os.Stat(path); err != nil {
		t.Errorf("fresh state file missing: %v", err)
	}

	// An unreadable path (a directory) is a hard error.
	if _, err := Open(dir, Options{}); err == nil {
		t.Error("directory as state path should fail")
	}
}
