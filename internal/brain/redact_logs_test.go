package brain

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/ctxcap"
	"github.com/kccarlos/herdr-sheepdog/internal/redact"
)

func TestRedactLogs(t *testing.T) {
	dataDir := t.TempDir()
	brainDir := filepath.Join(dataDir, TranscriptDir)
	if err := os.MkdirAll(brainDir, 0o700); err != nil {
		t.Fatal(err)
	}

	// Include quotes, backslashes and ampersands in the configured secret so any
	// naive byte-level replacement would corrupt JSON escaping.
	cfgSecret := `my"quoted\secret&key123`
	skSecret := "sk-proj-1234567890abcdefghijkl"
	pemSecret := "-----BEGIN PRIVATE KEY-----\nMIIEvQIBADANBgkqhkiG9w0BAQEFAASCBKcw\n-----END PRIVATE KEY-----"
	r := redact.New(cfgSecret)

	nestedArgs, err := json.Marshal(map[string]string{
		"cmd": "export K=" + cfgSecret + " && echo " + skSecret,
	})
	if err != nil {
		t.Fatal(err)
	}
	rawMsg, err := json.Marshal(map[string]any{
		"role": "assistant",
		"tool_calls": []map[string]any{{
			"id":   "call_1",
			"type": "function",
			"function": map[string]any{
				"name":      "send_keys",
				"arguments": string(nestedArgs),
			},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}

	ts := time.Date(2026, 9, 18, 16, 28, 4, 0, time.UTC)
	msgs := []Message{
		{
			TS:      ts,
			Role:    RoleUser,
			Kind:    KindRequest,
			Content: "check <worker> with " + cfgSecret + "\n" + pemSecret,
			Words:   "use " + skSecret,
		},
		{
			TS:       ts.Add(time.Second),
			Role:     RoleAssistant,
			Kind:     KindToolCall,
			Provider: "openai",
			Model:    "gpt-test",
			ToolCall: &ToolCall{
				ID:   "call_1",
				Name: "send_keys",
				Args: map[string]any{"keys": "Bearer eyJhbGciOiJIUzI1NiJ9.supersecret"},
			},
			Raw: rawMsg,
		},
		{
			TS:      ts.Add(2 * time.Second),
			Role:    RoleUser,
			Kind:    KindNote,
			Content: "plain note with no secret",
		},
	}
	trPath := filepath.Join(brainDir, "20260918T162804Z.jsonl")
	var trBuf bytes.Buffer
	for _, m := range msgs {
		m.Tokens = EstimateTokens(m.Content)
		b, err := json.Marshal(m)
		if err != nil {
			t.Fatal(err)
		}
		trBuf.Write(b)
		trBuf.WriteByte('\n')
	}
	if err := os.WriteFile(trPath, trBuf.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}

	// Build a BrainStep with contiguous Snapshot sections whose middle section
	// shrinks when the secret is masked.
	var bd ctxcap.Builder
	bd.Add(ctxcap.Section{Key: "prompt:sys", Label: "system", Class: ctxcap.ClassSystem}, "=== SYSTEM ===\nYou are the orchestrator.\n")
	bd.Add(ctxcap.Section{Key: "seq:1", Label: "user", Class: ctxcap.ClassShepherd, Seq: 1}, "=== USER ===\nKey: "+skSecret+" and "+cfgSecret+"\n")
	bd.Add(ctxcap.Section{Key: "tool:read", Label: "tools", Class: ctxcap.ClassTools}, "=== TOOLS ===\nread_screen <pane>\n")
	reqBody, err := json.Marshal(map[string]any{
		"messages": []map[string]any{
			{"role": "user", "content": "Key: " + skSecret + " and " + cfgSecret},
			{"role": "assistant", "tool_calls": []map[string]any{{
				"id":       "call_1",
				"function": map[string]any{"name": "send_keys", "arguments": string(nestedArgs)},
			}}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	snap := bd.Snapshot(reqBody)
	step := ctxcap.BrainStep{
		Step:         1,
		Run:          "20260918T162804Z",
		Segment:      "20260918T162804Z",
		Turn:         1,
		TurnStep:     1,
		Kind:         "utterance",
		Provider:     "openai",
		Model:        "gpt-test",
		TS:           ts,
		Format:       "openai",
		WindowTokens: 32000,
		Tokens:       snap.Tokens(),
		Error:        "upstream 401 for https://api.example.invalid/v1?key=AIzaSyD1234567890abcdefghijklmnop",
		Snapshot:     snap,
	}
	stepBytes, err := json.Marshal(step)
	if err != nil {
		t.Fatal(err)
	}
	reqPath := filepath.Join(brainDir, "20260918T162804Z.requests.jsonl")
	if err := os.WriteFile(reqPath, append(stepBytes, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}

	// Clean file: must not be rewritten.
	cleanPath := filepath.Join(brainDir, "20260919T100000Z.jsonl")
	cleanLine := `{"ts":"2026-09-19T10:00:00Z","role":"user","kind":"note","content":"all clean","tokens":3}` + "\n"
	if err := os.WriteFile(cleanPath, []byte(cleanLine), 0o600); err != nil {
		t.Fatal(err)
	}
	oldTime := time.Date(2026, 9, 19, 10, 0, 0, 0, time.UTC)
	if err := os.Chtimes(cleanPath, oldTime, oldTime); err != nil {
		t.Fatal(err)
	}

	// Symlink inside brain/ pointing outside must be ignored.
	outside := filepath.Join(t.TempDir(), "outside.jsonl")
	if err := os.WriteFile(outside, []byte(`{"secret":"`+skSecret+`"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(brainDir, "20260920T000000Z.jsonl")); err != nil {
		t.Fatal(err)
	}

	res, err := RedactLogs(dataDir, r)
	if err != nil {
		t.Fatalf("RedactLogs: %v", err)
	}
	if res.CheckedFiles != 3 || res.ChangedFiles != 2 || res.ChangedLines != 3 {
		t.Fatalf("RedactLogs = %+v, want CheckedFiles=3 ChangedFiles=2 ChangedLines=3", res)
	}

	// Verify no temp files left behind and permissions are 0600.
	ents, err := os.ReadDir(brainDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range ents {
		if strings.HasPrefix(e.Name(), ".") || strings.HasSuffix(e.Name(), ".tmp") {
			t.Errorf("leftover temp file: %s", e.Name())
		}
	}
	for _, p := range []string{trPath, reqPath} {
		st, err := os.Stat(p)
		if err != nil {
			t.Fatal(err)
		}
		if st.Mode().Perm() != 0o600 {
			t.Errorf("%s mode = %v, want 0600", p, st.Mode().Perm())
		}
		raw, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		for _, leak := range []string{cfgSecret, `quoted\\secret`, skSecret, "MIIEvQIBADAN", "supersecret", "AIzaSyD1234567890"} {
			if strings.Contains(string(raw), leak) {
				t.Errorf("%s still contains %q:\n%s", filepath.Base(p), leak, raw)
			}
		}
	}

	// Verify transcript JSON remains valid, including Raw and nested arguments JSON.
	gotMsgs, err := ReadTranscript(trPath)
	if err != nil {
		t.Fatalf("ReadTranscript: %v", err)
	}
	if len(gotMsgs) != 3 {
		t.Fatalf("len(gotMsgs) = %d, want 3", len(gotMsgs))
	}
	if !strings.Contains(gotMsgs[0].Content, "<worker>") || !strings.Contains(gotMsgs[0].Content, redact.Mask) {
		t.Errorf("msg[0].Content = %q", gotMsgs[0].Content)
	}
	if !json.Valid(gotMsgs[1].Raw) {
		t.Fatalf("msg[1].Raw invalid JSON: %s", gotMsgs[1].Raw)
	}
	var decodedRaw struct {
		ToolCalls []struct {
			Function struct {
				Arguments string `json:"arguments"`
			} `json:"function"`
		} `json:"tool_calls"`
	}
	if err := json.Unmarshal(gotMsgs[1].Raw, &decodedRaw); err != nil {
		t.Fatalf("unmarshal msg[1].Raw: %v", err)
	}
	if len(decodedRaw.ToolCalls) != 1 || !json.Valid([]byte(decodedRaw.ToolCalls[0].Function.Arguments)) {
		t.Fatalf("nested arguments invalid JSON: %q", decodedRaw.ToolCalls)
	}

	// Verify request log JSON and Snapshot section coverage remain valid.
	reqRaw, err := os.ReadFile(reqPath)
	if err != nil {
		t.Fatal(err)
	}
	var gotStep ctxcap.BrainStep
	if err := json.Unmarshal(bytes.TrimSpace(reqRaw), &gotStep); err != nil {
		t.Fatalf("unmarshal BrainStep: %v", err)
	}
	if err := gotStep.CheckCoverage(); err != nil {
		t.Fatalf("CheckCoverage: %v (sections=%+v, text=%q)", err, gotStep.Sections, gotStep.Text)
	}
	withTexts := gotStep.WithTexts()
	if len(withTexts) != 3 || withTexts[2].Text != "=== TOOLS ===\nread_screen <pane>\n" {
		t.Errorf("section[2] text = %q, want intact tools section", withTexts[2].Text)
	}
	if !json.Valid(gotStep.Body) {
		t.Errorf("gotStep.Body invalid JSON: %s", gotStep.Body)
	}
	bl, err := ctxcap.OpenBrainLog(brainDir, "20260921T000000Z", nil)
	if err != nil {
		t.Fatalf("OpenBrainLog: %v", err)
	}
	_ = bl.Close()
	if bl.Len() != 1 {
		t.Errorf("OpenBrainLog loaded %d steps, want 1", bl.Len())
	}

	// Clean file untouched; symlink target untouched.
	if st, err := os.Stat(cleanPath); err != nil || !st.ModTime().Equal(oldTime) {
		t.Errorf("clean file mtime changed: %v %v", st, err)
	}
	if outBytes, _ := os.ReadFile(outside); !strings.Contains(string(outBytes), skSecret) {
		t.Errorf("symlink target outside brain/ was modified")
	}

	// Second run is a no-op.
	res2, err := RedactLogs(dataDir, r)
	if err != nil {
		t.Fatalf("second RedactLogs: %v", err)
	}
	if res2.ChangedFiles != 0 || res2.ChangedLines != 0 {
		t.Errorf("second RedactLogs = %+v, want 0 changes", res2)
	}
}
