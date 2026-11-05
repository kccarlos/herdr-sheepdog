package brain

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

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

func TestRedactLogsStructuredKeysAndEmbeddedJSON(t *testing.T) {
	dataDir := t.TempDir()
	brainDir := filepath.Join(dataDir, TranscriptDir)
	if err := os.MkdirAll(brainDir, 0o700); err != nil {
		t.Fatal(err)
	}

	cfgSecret := `my"quoted\secret&key123`
	r := redact.New(cfgSecret)

	// Transcript lines whose ONLY secrets are structured map keys (no sk-/AIza shapes).
	nestedArgs, err := json.Marshal(map[string]any{
		"access_token":   "plain-access-token-222222",
		"refresh_token":  "plain-refresh-token-333333",
		"client_secret":  "plain-client-secret-444444",
		"password":       "plain-password-value-555555",
		"OPENAI_API_KEY": "plain-env-key-value-666666",
		"x-api-key":      []string{"plain-header-key-777777"},
		"key":            "prompt:sys",
		"tokens":         42,
	})
	if err != nil {
		t.Fatal(err)
	}
	rawMsg, err := json.Marshal(map[string]any{
		"role": "assistant",
		"tool_calls": []map[string]any{{
			"id":       "call_1",
			"type":     "function",
			"function": map[string]any{"name": "run_cli", "arguments": string(nestedArgs)},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}

	ts := time.Date(2026, 9, 18, 17, 0, 0, 0, time.UTC)
	msg := Message{
		TS:       ts,
		Role:     RoleAssistant,
		Kind:     KindToolCall,
		Provider: "openai",
		Model:    "gpt-test",
		ToolCall: &ToolCall{
			ID:   "call_1",
			Name: "send_keys",
			Args: map[string]any{
				"api_key": "plain-credential-value-111111",
				"keys":    []string{"y", "enter"},
			},
		},
		Raw: rawMsg,
	}
	msgBytes, err := json.Marshal(msg)
	if err != nil {
		t.Fatal(err)
	}
	trPath := filepath.Join(brainDir, "20260918T170000Z.jsonl")
	if err := os.WriteFile(trPath, append(msgBytes, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}

	// BrainStep with renderGemini / renderOpenAI style sections:
	// - embedded JSON inside "→ send_keys({...})\n\n" with JSON-escaped cfgSecret and api_key
	// - section starting with '{' and ending with "\n\n" whose trailing newlines must survive
	sendArgsJSON, err := json.Marshal(map[string]string{
		"cmd":     "export K=" + cfgSecret,
		"api_key": "plain-inline-api-key-888888",
	})
	if err != nil {
		t.Fatal(err)
	}
	var bd ctxcap.Builder
	bd.Add(ctxcap.Section{Key: "prompt:sys", Label: "system", Class: ctxcap.ClassSystem}, "=== SYSTEM ===\nYou are the orchestrator.\n\n")
	bd.Add(ctxcap.Section{Key: "seq:1", Label: "tool call", Class: ctxcap.ClassToolCall, Seq: 1}, "### assistant\n→ send_keys("+string(sendArgsJSON)+")\n\n")
	bd.Add(ctxcap.Section{Key: "seq:2", Label: "reply", Class: ctxcap.ClassBrain, Seq: 2}, "{\"spoken\":\"All clear.\",\"auth_token\":\"plain-reply-token-999999\"}\n\n")
	bd.Add(ctxcap.Section{Key: "seq:3", Label: "tool result", Class: ctxcap.ClassToolResult, Seq: 3}, "### tool\n← read_screen({\"output\":\"ok\",\"id_token\":\"plain-id-token-000000\"})\n\n")
	bodyBytes, err := json.Marshal(map[string]any{
		"headers": map[string]any{"x-goog-api-key": "plain-goog-key-abcdef"},
		"schema":  map[string]any{"password": map[string]any{"type": "string", "description": "user password"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	snap := bd.Snapshot(bodyBytes)
	step := ctxcap.BrainStep{
		Step: 1, Run: "20260918T170000Z", Segment: "20260918T170000Z",
		Turn: 1, TurnStep: 1, Kind: "utterance", Provider: "gemini", Model: "gemini-test",
		TS: ts, Format: "gemini", WindowTokens: 32000, Tokens: snap.Tokens(),
		Usage:    &ctxcap.Usage{PromptTokens: 120, CompletionTokens: 30},
		Snapshot: snap,
	}
	stepBytes, err := json.Marshal(step)
	if err != nil {
		t.Fatal(err)
	}
	reqPath := filepath.Join(brainDir, "20260918T170000Z.requests.jsonl")
	if err := os.WriteFile(reqPath, append(stepBytes, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}

	res, err := RedactLogs(dataDir, r)
	if err != nil {
		t.Fatalf("RedactLogs: %v", err)
	}
	if res.ChangedFiles != 2 || res.ChangedLines != 2 {
		t.Fatalf("RedactLogs = %+v, want ChangedFiles=2 ChangedLines=2", res)
	}

	for _, p := range []string{trPath, reqPath} {
		raw, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		for _, leak := range []string{
			"plain-credential-value-111111",
			"plain-access-token-222222",
			"plain-refresh-token-333333",
			"plain-client-secret-444444",
			"plain-password-value-555555",
			"plain-env-key-value-666666",
			"plain-header-key-777777",
			"plain-inline-api-key-888888",
			"plain-reply-token-999999",
			"plain-id-token-000000",
			"plain-goog-key-abcdef",
			"quoted",
			"key123",
		} {
			if strings.Contains(string(raw), leak) {
				t.Errorf("%s still contains %q:\n%s", filepath.Base(p), leak, raw)
			}
		}
	}

	// Non-credential keys ("key", "tokens", "keys", schema map) must survive intact.
	gotMsgs, err := ReadTranscript(trPath)
	if err != nil {
		t.Fatalf("ReadTranscript: %v", err)
	}
	if got := gotMsgs[0].ToolCall.Args["api_key"]; got != redact.Mask {
		t.Errorf("ToolCall.Args[api_key] = %v, want %q", got, redact.Mask)
	}
	if keys, ok := gotMsgs[0].ToolCall.Args["keys"].([]any); !ok || len(keys) != 2 || keys[0] != "y" {
		t.Errorf("ToolCall.Args[keys] = %v, want [y enter]", gotMsgs[0].ToolCall.Args["keys"])
	}

	reqRaw, err := os.ReadFile(reqPath)
	if err != nil {
		t.Fatal(err)
	}
	var gotStep ctxcap.BrainStep
	if err := json.Unmarshal(bytes.TrimSpace(reqRaw), &gotStep); err != nil {
		t.Fatalf("unmarshal BrainStep: %v", err)
	}
	if err := gotStep.CheckCoverage(); err != nil {
		t.Fatalf("CheckCoverage: %v", err)
	}
	secs := gotStep.WithTexts()
	if !strings.HasSuffix(secs[2].Text, "\n\n") {
		t.Errorf("section[2] lost trailing newlines: %q", secs[2].Text)
	}
	if !bytes.Contains(gotStep.Body, []byte(`"user password"`)) {
		t.Errorf("schema object under 'password' key was clobbered: %s", gotStep.Body)
	}
	if gotStep.WindowTokens != 32000 || gotStep.Usage.PromptTokens != 120 {
		t.Errorf("token counts clobbered: window=%d prompt=%d", gotStep.WindowTokens, gotStep.Usage.PromptTokens)
	}

	// Idempotent on second run.
	res2, err := RedactLogs(dataDir, r)
	if err != nil {
		t.Fatalf("second RedactLogs: %v", err)
	}
	if res2.ChangedFiles != 0 || res2.ChangedLines != 0 {
		t.Errorf("second RedactLogs = %+v, want 0 changes", res2)
	}
}

func TestRedactLogsCrossBoundaryUTF8AndCollapse(t *testing.T) {
	dataDir := t.TempDir()
	brainDir := filepath.Join(dataDir, TranscriptDir)
	if err := os.MkdirAll(brainDir, 0o700); err != nil {
		t.Fatal(err)
	}

	// Case 1: cross-boundary secret surrounded by multi-byte UTF-8 runes.
	var bd1 ctxcap.Builder
	sec0 := "=== SYSTEM ===\n→ 状态 · "
	sec3 := "← 工具结果 · 正常\n"
	bd1.Add(ctxcap.Section{Key: "s0", Label: "s0", Class: ctxcap.ClassSystem}, sec0)
	bd1.Add(ctxcap.Section{Key: "s1", Label: "s1", Class: ctxcap.ClassShepherd}, "前缀 — sk-cross")
	bd1.Add(ctxcap.Section{Key: "s2", Label: "s2", Class: ctxcap.ClassShepherd}, "1234567890abcdef · 结尾\n")
	bd1.Add(ctxcap.Section{Key: "s3", Label: "s3", Class: ctxcap.ClassToolResult}, sec3)
	snap1 := bd1.Snapshot([]byte(`{}`))
	step1 := ctxcap.BrainStep{Step: 1, Run: "r1", Segment: "r1", Provider: "gemini", Model: "m", Format: "gemini", Tokens: snap1.Tokens(), Snapshot: snap1}

	// Case 2: 15 1-byte sections spelling a single secret that shrinks to 10-byte "[REDACTED]"
	// so len(full) < len(newSecs).
	var bd2 ctxcap.Builder
	secret15 := "sk-1234567890ab"
	for i := 0; i < len(secret15); i++ {
		bd2.Add(ctxcap.Section{Key: "c", Label: "c", Class: ctxcap.ClassShepherd}, secret15[i:i+1])
	}
	snap2 := bd2.Snapshot([]byte(`{}`))
	step2 := ctxcap.BrainStep{Step: 2, Run: "r1", Segment: "r1", Provider: "gemini", Model: "m", Format: "gemini", Tokens: snap2.Tokens(), Snapshot: snap2}

	var buf bytes.Buffer
	for _, st := range []ctxcap.BrainStep{step1, step2} {
		b, err := json.Marshal(st)
		if err != nil {
			t.Fatal(err)
		}
		buf.Write(b)
		buf.WriteByte('\n')
	}
	reqPath := filepath.Join(brainDir, "20260918T180000Z.requests.jsonl")
	if err := os.WriteFile(reqPath, buf.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := RedactLogs(dataDir, redact.New()); err != nil {
		t.Fatalf("RedactLogs: %v", err)
	}

	lines := bytes.Split(bytes.TrimSpace(mustReadFile(t, reqPath)), []byte("\n"))
	if len(lines) != 2 {
		t.Fatalf("got %d lines, want 2", len(lines))
	}
	for idx, line := range lines {
		if strings.Contains(string(line), "1234567890") {
			t.Errorf("line %d still contains secret: %s", idx, line)
		}
		var got ctxcap.BrainStep
		if err := json.Unmarshal(line, &got); err != nil {
			t.Fatalf("line %d unmarshal: %v", idx, err)
		}
		if err := got.CheckCoverage(); err != nil {
			t.Errorf("line %d CheckCoverage: %v (len(text)=%d, sections=%+v)", idx, err, len(got.Text), got.Sections)
		}
		if !utf8.ValidString(got.Text) {
			t.Errorf("line %d Text invalid UTF-8: %q", idx, got.Text)
		}
		for si, sec := range got.WithTexts() {
			if !utf8.ValidString(sec.Text) {
				t.Errorf("line %d section %d invalid UTF-8: %q", idx, si, sec.Text)
			}
		}
		if idx == 0 {
			wt := got.WithTexts()
			if wt[0].Text != sec0 {
				t.Errorf("section 0 changed: got %q, want %q", wt[0].Text, sec0)
			}
			if wt[len(wt)-1].Text != sec3 {
				t.Errorf("last section changed: got %q, want %q", wt[len(wt)-1].Text, sec3)
			}
		}
	}
}

func TestRedactLogsOddInputsPreserveReadability(t *testing.T) {
	dataDir := t.TempDir()
	brainDir := filepath.Join(dataDir, TranscriptDir)
	if err := os.MkdirAll(brainDir, 0o700); err != nil {
		t.Fatal(err)
	}

	// 1. Concatenated JSON objects on one line (missing newline before a crash).
	concatPath := filepath.Join(brainDir, "20260918T190000Z.jsonl")
	m1, _ := json.Marshal(Message{Role: RoleUser, Kind: KindNote, Content: "first", ToolCall: &ToolCall{ID: "1", Name: "x", Args: map[string]any{"api_key": "concat-secret-111111"}}})
	m2, _ := json.Marshal(Message{Role: RoleAssistant, Kind: KindReply, Content: `{"spoken":"ok","password":"concat-secret-222222"}`})
	if err := os.WriteFile(concatPath, append(append(m1, m2...), '\n'), 0o600); err != nil {
		t.Fatal(err)
	}

	// 2. Empty file and odd non-int tokens field + truncated crash tail without trailing newline.
	emptyPath := filepath.Join(brainDir, "20260918T190100Z.jsonl")
	if err := os.WriteFile(emptyPath, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	oddPath := filepath.Join(brainDir, "20260918T190200Z.requests.jsonl")
	oddContent := "{\"text\":\"hello sk-odd1234567890abcdef\",\"sections\":[{\"key\":\"a\",\"label\":\"a\",\"class\":\"system\",\"start\":0,\"end\":28,\"tokens\":7}],\"tokens\":\"not-an-int\"}\r\n" +
		"{\"truncated\":\"sk-tail1234567890abcdef"
	if err := os.WriteFile(oddPath, []byte(oddContent), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := RedactLogs(dataDir, redact.New()); err != nil {
		t.Fatalf("RedactLogs: %v", err)
	}

	// Concatenated line stays readable by ReadTranscript with secrets masked.
	msgs, err := ReadTranscript(concatPath)
	if err != nil {
		t.Fatalf("ReadTranscript(concatPath): %v", err)
	}
	if len(msgs) != 2 {
		t.Fatalf("len(msgs) = %d, want 2", len(msgs))
	}
	concatRaw := string(mustReadFile(t, concatPath))
	for _, leak := range []string{"concat-secret-111111", "concat-secret-222222"} {
		if strings.Contains(concatRaw, leak) {
			t.Errorf("concatPath leaked %q: %s", leak, concatRaw)
		}
	}

	// Empty file stays empty.
	if got := mustReadFile(t, emptyPath); len(got) != 0 {
		t.Errorf("empty file got %d bytes", len(got))
	}

	// Non-int "tokens" field was not clobbered, and truncated tail was redacted.
	oddLines := strings.Split(strings.TrimSpace(string(mustReadFile(t, oddPath))), "\n")
	if len(oddLines) != 2 {
		t.Fatalf("oddLines = %d, want 2", len(oddLines))
	}
	var oddObj map[string]any
	if err := json.Unmarshal([]byte(strings.TrimRight(oddLines[0], "\r")), &oddObj); err != nil {
		t.Fatalf("unmarshal oddLines[0]: %v", err)
	}
	if oddObj["tokens"] != "not-an-int" {
		t.Errorf("non-int tokens clobbered: got %v", oddObj["tokens"])
	}
	if strings.Contains(oddLines[1], "sk-tail") || !strings.Contains(oddLines[1], redact.Mask) {
		t.Errorf("truncated tail not redacted: %q", oddLines[1])
	}
}

func TestRedactLogsInterruptedContextLeavesFilesIntact(t *testing.T) {
	dataDir := t.TempDir()
	brainDir := filepath.Join(dataDir, TranscriptDir)
	if err := os.MkdirAll(brainDir, 0o700); err != nil {
		t.Fatal(err)
	}
	orig := `{"ts":"2026-09-18T20:00:00Z","role":"user","kind":"note","content":"key sk-interrupt1234567890"}` + "\n"
	p := filepath.Join(brainDir, "20260918T200000Z.jsonl")
	if err := os.WriteFile(p, []byte(orig), 0o600); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := RedactLogsContext(ctx, dataDir, redact.New())
	if err == nil {
		t.Fatal("expected error from cancelled context, got nil")
	}
	if got := string(mustReadFile(t, p)); got != orig {
		t.Errorf("file modified despite cancelled context: %q", got)
	}
	ents, err := os.ReadDir(brainDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range ents {
		if strings.HasPrefix(e.Name(), ".") || strings.HasSuffix(e.Name(), ".tmp") {
			t.Errorf("leftover temp file: %s", e.Name())
		}
	}
}

func mustReadFile(t *testing.T, p string) []byte {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
