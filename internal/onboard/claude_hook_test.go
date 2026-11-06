package onboard

import (
	"context"
	"encoding/json"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/kccarlos/herdr-sheepdog/internal/config"
)

func TestClaudeHookMergeBackupDoctorAndRemove(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	binDir := filepath.Join(t.TempDir(), "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(binDir, "sheepdog")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	// 1. Fresh home without ~/.claude/settings.json: creates it (0600), no .bak.
	note, err := UpdateClaudeHook(home, bin, false)
	if err != nil || note == "" || strings.Contains(note, ".bak") {
		t.Fatalf("fresh install: note=%q err=%v", note, err)
	}
	settingsPath := filepath.Join(home, ".claude", "settings.json")
	st, err := os.Stat(settingsPath)
	if err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("settings.json stat: %v mode=%v", err, st)
	}
	if _, err := os.Stat(settingsPath + ".bak"); !os.IsNotExist(err) {
		t.Fatalf("unexpected .bak on fresh install: %v", err)
	}

	// 2. Existing settings.json with other keys, PreToolUse and another PermissionRequest matcher.
	orig := "{\n  \"model\": \"opus\",\n  \"hooks\": {\n    \"PreToolUse\": [\n      {\"hooks\": [{\"type\": \"command\", \"command\": \"my-linter\"}]}\n    ],\n    \"PermissionRequest\": [\n      {\"matcher\": \"Write\", \"hooks\": [{\"type\": \"command\", \"command\": \"audit-write\"}]}\n    ]\n  }\n}\n"
	if err := os.WriteFile(settingsPath, []byte(orig), 0o600); err != nil {
		t.Fatal(err)
	}
	note, err = UpdateClaudeHook(home, bin, false)
	if err != nil || !strings.Contains(note, ".bak") {
		t.Fatalf("merge install: note=%q err=%v", note, err)
	}
	bak, err := os.ReadFile(settingsPath + ".bak")
	if err != nil || string(bak) != orig {
		t.Fatalf("backup = %q (%v), want %q", bak, err, orig)
	}

	// Re-running with the same binary is a no-op and keeps the original backup.
	note, err = UpdateClaudeHook(home, bin, false)
	if err != nil || note != "" {
		t.Fatalf("idempotent install: note=%q err=%v", note, err)
	}
	if bak2, _ := os.ReadFile(settingsPath + ".bak"); string(bak2) != orig {
		t.Fatalf("idempotent run overwrote backup: %q", bak2)
	}

	// Verify merged JSON preserves model, PreToolUse, and audit-write.
	mergedRaw, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatal(err)
	}
	var merged map[string]any
	if err := json.Unmarshal(mergedRaw, &merged); err != nil {
		t.Fatalf("merged json invalid: %v\n%s", err, mergedRaw)
	}
	if merged["model"] != "opus" || !strings.Contains(string(mergedRaw), "my-linter") || !strings.Contains(string(mergedRaw), "audit-write") {
		t.Fatalf("merged json lost existing entries:\n%s", mergedRaw)
	}
	wantCmd := bin + " hook claude-permission"
	if !strings.Contains(string(mergedRaw), wantCmd) {
		t.Fatalf("merged json missing command %q:\n%s", wantCmd, mergedRaw)
	}
	if !strings.Contains(string(mergedRaw), `"async": true`) {
		t.Fatalf("merged json missing async:true:\n%s", mergedRaw)
	}

	// 3. Detect + Doctor states.
	probe := Probe{
		GOOS: "darwin", Home: home,
		Getenv: func(string) string { return "" },
		LookPath: func(name string) (string, error) {
			if name == "claude" {
				return "/fake/claude", nil
			}
			return "", fs.ErrNotExist
		},
		Stat: func(name string) (fs.FileInfo, error) {
			if strings.HasPrefix(name, home) || strings.HasPrefix(name, binDir) {
				return os.Stat(name)
			}
			return nil, fs.ErrNotExist
		},
		ReadFile:      os.ReadFile,
		SocketAnswers: func(string) bool { return false },
	}
	cfg := config.Default()
	cfg.Herdr = testHerdr()
	cfg.Push.Mode = config.PushModeOff
	cfg.Web.Port = -1
	cfg.Hooks.ClaudePermission = true

	env := Detect(cfg, probe, testPanel(nil), nil)
	if !env.Claude || env.ClaudeHook != bin {
		t.Fatalf("Detect: Claude=%v ClaudeHook=%q, want true %q", env.Claude, env.ClaudeHook, bin)
	}
	checkOf := func(c config.Config, e Environment) Result {
		for _, r := range Doctor(context.Background(), c, DoctorOptions{Environment: &e, Home: home}) {
			if r.Name == "claude hook" {
				return r
			}
		}
		return Result{}
	}
	if r := checkOf(cfg, env); r.Status != OK {
		t.Errorf("doctor installed = %+v, want OK", r)
	}
	missingBinEnv := env
	missingBinEnv.ClaudeHook = filepath.Join(home, "gone", "sheepdog")
	if r := checkOf(cfg, missingBinEnv); r.Status != Warn || !strings.Contains(r.Fix, "sheepdog setup") {
		t.Errorf("doctor missing binary = %+v, want Warn", r)
	}
	noHookEnv := env
	noHookEnv.ClaudeHook = ""
	if r := checkOf(cfg, noHookEnv); r.Status != Warn || !strings.Contains(r.Fix, "sheepdog setup") {
		t.Errorf("doctor missing hook = %+v, want Warn", r)
	}
	offCfg := cfg
	offCfg.Hooks.ClaudePermission = false
	if r := checkOf(offCfg, env); r.Status != Skip {
		t.Errorf("doctor opted out = %+v, want Skip", r)
	}

	// 4. Dry-run remove leaves file untouched; real remove strips only Sheepdog's entry.
	note, err = UpdateClaudeHook(home, "", true)
	if err != nil || !strings.Contains(note, "would remove") {
		t.Fatalf("dry-run remove: note=%q err=%v", note, err)
	}
	if afterDry, _ := os.ReadFile(settingsPath); string(afterDry) != string(mergedRaw) {
		t.Fatal("dry-run modified settings.json")
	}
	note, err = UpdateClaudeHook(home, "", false)
	if err != nil || !strings.Contains(note, "removed") {
		t.Fatalf("remove: note=%q err=%v", note, err)
	}
	afterRaw, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(afterRaw), "claude-permission") {
		t.Fatalf("sheepdog hook still present:\n%s", afterRaw)
	}
	if !strings.Contains(string(afterRaw), "my-linter") || !strings.Contains(string(afterRaw), "audit-write") || !strings.Contains(string(afterRaw), `"opus"`) {
		t.Fatalf("remove touched other entries:\n%s", afterRaw)
	}
}

func TestClaudeHookSpacesSymlinksAndConcurrency(t *testing.T) {
	// 1. Binary path containing spaces: shell-quoted + async:true, executes via /bin/sh -c, Detect + Doctor OK, remove clean.
	home := t.TempDir()
	t.Setenv("HOME", home)
	spaceRoot := filepath.Join(t.TempDir(), "Work Disk", "sheepdog bin")
	if err := os.MkdirAll(spaceRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(t.TempDir(), "ran.txt")
	bin := filepath.Join(spaceRoot, "sheepdog")
	script := "#!/bin/sh\nprintf '%s' \"$*\" > '" + marker + "'\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	settingsPath := filepath.Join(home, ".claude", "settings.json")
	if err := os.MkdirAll(filepath.Dir(settingsPath), 0o700); err != nil {
		t.Fatal(err)
	}
	// Legacy unquoted path with spaces or missing async: Doctor warns and UpdateClaudeHook repairs it.
	legacy := `{"hooks":{"PermissionRequest":[{"hooks":[{"type":"command","command":"` + bin + ` hook claude-permission"}]}]}}`
	if err := os.WriteFile(settingsPath, []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	probe := Probe{
		GOOS: "darwin", Home: home,
		Getenv:        func(string) string { return "" },
		LookPath:      func(name string) (string, error) { return "/fake/claude", nil },
		Stat:          os.Stat,
		ReadFile:      os.ReadFile,
		SocketAnswers: func(string) bool { return false },
	}
	cfg := config.Default()
	cfg.Herdr = testHerdr()
	cfg.Push.Mode = config.PushModeOff
	cfg.Web.Port = -1
	cfg.Hooks.ClaudePermission = true

	envLegacy := Detect(cfg, probe, testPanel(nil), nil)
	var legacyRes Result
	for _, r := range Doctor(context.Background(), cfg, DoctorOptions{Environment: &envLegacy, Home: home}) {
		if r.Name == "claude hook" {
			legacyRes = r
		}
	}
	if legacyRes.Status != Warn {
		t.Fatalf("doctor on legacy unquoted/sync hook = %+v, want Warn", legacyRes)
	}

	note, err := UpdateClaudeHook(home, bin, false)
	if err != nil || note == "" {
		t.Fatalf("repair install: note=%q err=%v", note, err)
	}
	raw, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatal(err)
	}
	var root struct {
		Hooks struct {
			PermissionRequest []struct {
				Hooks []struct {
					Type    string `json:"type"`
					Command string `json:"command"`
					Async   bool   `json:"async"`
				} `json:"hooks"`
			} `json:"PermissionRequest"`
		} `json:"hooks"`
	}
	if err := json.Unmarshal(raw, &root); err != nil {
		t.Fatalf("unmarshal: %v\n%s", err, raw)
	}
	if len(root.Hooks.PermissionRequest) != 1 || len(root.Hooks.PermissionRequest[0].Hooks) != 1 {
		t.Fatalf("unexpected hooks shape:\n%s", raw)
	}
	h := root.Hooks.PermissionRequest[0].Hooks[0]
	if !h.Async {
		t.Fatalf("expected async:true, got %+v", h)
	}
	if out, err := exec.Command("/bin/sh", "-c", h.Command).CombinedOutput(); err != nil {
		t.Fatalf("sh -c %q failed: %v (%s)", h.Command, err, out)
	}
	if got, _ := os.ReadFile(marker); string(got) != "hook claude-permission" {
		t.Fatalf("hook args = %q, want %q", got, "hook claude-permission")
	}

	envOK := Detect(cfg, probe, testPanel(nil), nil)
	if envOK.ClaudeHook != bin {
		t.Fatalf("Detect ClaudeHook = %q, want %q", envOK.ClaudeHook, bin)
	}
	for _, r := range Doctor(context.Background(), cfg, DoctorOptions{Environment: &envOK, Home: home}) {
		if r.Name == "claude hook" && r.Status != OK {
			t.Fatalf("doctor after repair = %+v, want OK", r)
		}
	}
	if _, err := UpdateClaudeHook(home, "", false); err != nil {
		t.Fatalf("remove quoted hook: %v", err)
	}
	if after, _ := os.ReadFile(settingsPath); strings.Contains(string(after), "claude-permission") {
		t.Fatalf("quoted hook not removed:\n%s", after)
	}

	// 2. Symlinks on settings.json or settings.json.bak are refused without touching target.
	symHome := t.TempDir()
	if err := os.MkdirAll(filepath.Join(symHome, ".claude"), 0o700); err != nil {
		t.Fatal(err)
	}
	canary := filepath.Join(t.TempDir(), "canary.txt")
	if err := os.WriteFile(canary, []byte("keep-me"), 0o600); err != nil {
		t.Fatal(err)
	}
	symSettings := filepath.Join(symHome, ".claude", "settings.json")
	if err := os.Symlink(canary, symSettings); err != nil {
		t.Fatal(err)
	}
	if _, err := UpdateClaudeHook(symHome, bin, false); err == nil {
		t.Fatal("expected error when settings.json is a symlink")
	}
	if got, _ := os.ReadFile(canary); string(got) != "keep-me" {
		t.Fatalf("symlink target modified: %q", got)
	}
	_ = os.Remove(symSettings)
	if err := os.WriteFile(symSettings, []byte(`{"model":"sonnet"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(canary, symSettings+".bak"); err != nil {
		t.Fatal(err)
	}
	if _, err := UpdateClaudeHook(symHome, bin, false); err == nil {
		t.Fatal("expected error when settings.json.bak is a symlink")
	}
	if got, _ := os.ReadFile(canary); string(got) != "keep-me" {
		t.Fatalf("backup symlink target modified: %q", got)
	}

	// 3. Concurrent UpdateClaudeHook calls produce valid 0600 JSON and preserve unrelated keys.
	concHome := t.TempDir()
	concSettings := filepath.Join(concHome, ".claude", "settings.json")
	if err := os.MkdirAll(filepath.Dir(concSettings), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(concSettings, []byte(`{"model":"opus"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := UpdateClaudeHook(concHome, bin, false); err != nil {
				t.Errorf("concurrent UpdateClaudeHook: %v", err)
			}
		}()
	}
	wg.Wait()
	concRaw, err := os.ReadFile(concSettings)
	if err != nil {
		t.Fatal(err)
	}
	var concMap map[string]any
	if err := json.Unmarshal(concRaw, &concMap); err != nil || concMap["model"] != "opus" {
		t.Fatalf("concurrent settings corrupted: err=%v\n%s", err, concRaw)
	}
}
