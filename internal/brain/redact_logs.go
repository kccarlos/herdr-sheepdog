package brain

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/kccarlos/herdr-sheepdog/internal/ctxcap"
	"github.com/kccarlos/herdr-sheepdog/internal/redact"
)

// RedactResult reports what RedactLogs inspected and rewrote.
type RedactResult struct {
	CheckedFiles int
	ChangedFiles int
	ChangedLines int
}

// protectedBrainDir reports whether dir sits inside repository fixtures or
// recorded benchmark/eval data, which must never be rewritten.
func protectedBrainDir(dir string) bool {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return true
	}
	abs = filepath.ToSlash(abs) + "/"
	for _, p := range []string{"/docs/dev/bench/", "/docs/dev/eval/", "/docs/bench/", "/docs/eval/", "/testdata/"} {
		if strings.Contains(abs, p) {
			return true
		}
	}
	return false
}

// RedactLogs masks secrets in every regular *.jsonl file under
// <dataDir>/brain (both run transcripts and *.requests.jsonl logs),
// rewriting changed files atomically (mode 0600) while keeping every JSON
// line valid and preserving ctxcap.Snapshot section offsets.
func RedactLogs(dataDir string, r *redact.Redactor) (RedactResult, error) {
	var res RedactResult
	if dataDir == "" || protectedBrainDir(dataDir) {
		return res, nil
	}
	if r == nil {
		r = redact.New()
	}
	brainDir := filepath.Join(dataDir, TranscriptDir)
	ents, err := os.ReadDir(brainDir)
	if os.IsNotExist(err) {
		return res, nil
	}
	if err != nil {
		return res, err
	}
	var paths []string
	for _, e := range ents {
		name := e.Name()
		if !e.Type().IsRegular() || strings.HasPrefix(name, ".") || !strings.HasSuffix(name, ".jsonl") {
			continue
		}
		paths = append(paths, filepath.Join(brainDir, name))
	}
	sort.Strings(paths)
	for _, p := range paths {
		res.CheckedFiles++
		lines, err := redactJSONLFile(p, r)
		if err != nil {
			return res, err
		}
		if lines > 0 {
			res.ChangedFiles++
			res.ChangedLines += lines
		}
	}
	return res, nil
}

func redactJSONLFile(path string, r *redact.Redactor) (int, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	reader := bufio.NewReaderSize(f, 256<<10)
	var outLines [][]byte
	changedLines := 0
	for {
		raw, readErr := reader.ReadBytes('\n')
		if len(raw) > 0 {
			trimmed := bytes.TrimRight(raw, "\r\n")
			if len(bytes.TrimSpace(trimmed)) == 0 {
				outLines = append(outLines, append([]byte(nil), raw...))
			} else {
				redLine, changed, err := redactJSONLLine(trimmed, r)
				if err != nil {
					_ = f.Close()
					return 0, err
				}
				if changed {
					changedLines++
					outLines = append(outLines, append(redLine, '\n'))
				} else if raw[len(raw)-1] == '\n' {
					outLines = append(outLines, append([]byte(nil), raw...))
				} else {
					outLines = append(outLines, append(append([]byte(nil), raw...), '\n'))
				}
			}
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			_ = f.Close()
			return 0, readErr
		}
	}
	if err := f.Close(); err != nil {
		return 0, err
	}
	if changedLines == 0 {
		return 0, nil
	}

	tmp, err := os.CreateTemp(filepath.Dir(path), ".redact-*.tmp")
	if err != nil {
		return 0, err
	}
	tmpPath := tmp.Name()
	renamed := false
	defer func() {
		if !renamed {
			_ = tmp.Close()
			_ = os.Remove(tmpPath)
		}
	}()
	if err := tmp.Chmod(0o600); err != nil {
		return 0, err
	}
	w := bufio.NewWriterSize(tmp, 256<<10)
	for _, line := range outLines {
		if _, err := w.Write(line); err != nil {
			return 0, err
		}
	}
	if err := w.Flush(); err != nil {
		return 0, err
	}
	if err := tmp.Sync(); err != nil {
		return 0, err
	}
	if err := tmp.Close(); err != nil {
		return 0, err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return 0, err
	}
	renamed = true
	return changedLines, nil
}

func redactJSONLLine(line []byte, r *redact.Redactor) ([]byte, bool, error) {
	val, ok := decodeSingleJSON(line)
	if !ok {
		// Truncated or malformed crash tail: still mask any secret text.
		s := string(line)
		red := r.String(s)
		if red == s {
			return line, false, nil
		}
		return []byte(red), true, nil
	}
	var changed bool
	if m, isMap := val.(map[string]any); isMap {
		val, changed = redactTopObject(m, r)
	} else {
		val, changed = redactAny(val, r)
	}
	if !changed {
		return line, false, nil
	}
	encoded, err := encodeSingleLineJSON(val)
	if err != nil {
		return nil, false, err
	}
	return encoded, true, nil
}

func redactTopObject(m map[string]any, r *redact.Redactor) (map[string]any, bool) {
	if textStr, hasText := m["text"].(string); hasText {
		if secSlice, hasSecs := m["sections"].([]any); hasSecs && len(secSlice) > 0 {
			if out, ok, changed := redactSnapshotObject(m, textStr, secSlice, r); ok {
				return out, changed
			}
		}
	}
	origContent, hadContent := m["content"].(string)
	outAny, changed := redactAny(m, r)
	out := outAny.(map[string]any)
	if hadContent && changed {
		if newContent, ok := out["content"].(string); ok && newContent != origContent {
			if tok, ok := asInt(out["tokens"]); ok && tok > 0 {
				out["tokens"] = EstimateTokens(newContent)
			}
		}
	}
	return out, changed
}

// redactSnapshotObject redacts a ctxcap.BrainStep / Snapshot map section by
// section so Sections' [Start, End) byte offsets still cover Text contiguously.
func redactSnapshotObject(m map[string]any, textStr string, secSlice []any, r *redact.Redactor) (map[string]any, bool, bool) {
	bounds := make([][2]int, len(secSlice))
	pos := 0
	for i, elem := range secSlice {
		secMap, ok := elem.(map[string]any)
		if !ok {
			return nil, false, false
		}
		start, ok1 := asInt(secMap["start"])
		end, ok2 := asInt(secMap["end"])
		if !ok1 || !ok2 || start != pos || end <= start || end > len(textStr) {
			return nil, false, false
		}
		bounds[i] = [2]int{start, end}
		pos = end
	}
	if pos != len(textStr) {
		return nil, false, false
	}

	out := make(map[string]any, len(m))
	changed := false
	var b strings.Builder
	newSecs := make([]any, len(secSlice))
	totalTokens := 0
	textChanged := false
	for i, elem := range secSlice {
		secMap := elem.(map[string]any)
		newSecAny, secChanged := redactAny(secMap, r)
		newSec := newSecAny.(map[string]any)
		if secChanged {
			changed = true
		}
		piece := textStr[bounds[i][0]:bounds[i][1]]
		redPiece, pieceChanged := redactString(piece, r)
		if pieceChanged {
			textChanged = true
			changed = true
		}
		newStart := b.Len()
		b.WriteString(redPiece)
		newEnd := b.Len()
		newTok := ctxcap.EstimateTokens(redPiece)
		if textChanged {
			newSec["start"] = newStart
			newSec["end"] = newEnd
			newSec["tokens"] = newTok
		}
		totalTokens += newTok
		newSecs[i] = newSec
	}

	built := b.String()
	// Catch a secret that somehow straddled a section boundary.
	if full := r.String(built); full != built {
		textChanged = true
		changed = true
		built = full
		totalTokens = ctxcap.EstimateTokens(full)
		if len(newSecs) == 1 {
			sec := newSecs[0].(map[string]any)
			sec["start"] = 0
			sec["end"] = len(full)
			sec["tokens"] = totalTokens
		} else if len(full) >= len(newSecs) {
			// Collapse onto valid contiguous bounds if a cross-boundary match shifted length.
			cursor := 0
			for i := range newSecs {
				sec := newSecs[i].(map[string]any)
				oldEnd, _ := asInt(sec["end"])
				end := min(oldEnd, len(full)-(len(newSecs)-1-i))
				if i == len(newSecs)-1 || end <= cursor {
					if i == len(newSecs)-1 {
						end = len(full)
					} else {
						end = cursor + 1
					}
				}
				sec["start"] = cursor
				sec["end"] = end
				sec["tokens"] = ctxcap.EstimateTokens(full[cursor:end])
				cursor = end
			}
		}
	}

	for k, v := range m {
		if k == "text" || k == "sections" {
			continue
		}
		rk := r.String(k)
		if rk != k {
			changed = true
		}
		rv, vc := redactAny(v, r)
		if vc {
			changed = true
		}
		out[rk] = rv
	}
	if textChanged {
		out["text"] = built
		if _, hasTok := out["tokens"]; hasTok {
			out["tokens"] = totalTokens
		}
	} else {
		out["text"] = textStr
	}
	out["sections"] = newSecs
	return out, true, changed
}

func redactAny(v any, r *redact.Redactor) (any, bool) {
	switch v := v.(type) {
	case string:
		return redactString(v, r)
	case map[string]any:
		out := make(map[string]any, len(v))
		changed := false
		for k, x := range v {
			rk := r.String(k)
			if rk != k {
				changed = true
			}
			rx, xc := redactAny(x, r)
			if xc {
				changed = true
			}
			out[rk] = rx
		}
		return out, changed
	case []any:
		out := make([]any, len(v))
		changed := false
		for i, x := range v {
			rx, xc := redactAny(x, r)
			if xc {
				changed = true
			}
			out[i] = rx
		}
		return out, changed
	default:
		return v, false
	}
}

func redactString(s string, r *redact.Redactor) (string, bool) {
	trimmed := strings.TrimSpace(s)
	if len(trimmed) >= 2 && ((trimmed[0] == '{' && trimmed[len(trimmed)-1] == '}') || (trimmed[0] == '[' && trimmed[len(trimmed)-1] == ']')) {
		if inner, ok := decodeSingleJSON([]byte(trimmed)); ok {
			redInner, innerChanged := redactAny(inner, r)
			if innerChanged {
				if encoded, err := encodeSingleLineJSON(redInner); err == nil {
					return string(encoded), true
				}
			}
			// Even if inner strings did not change, fall back only if result stays valid JSON.
			if after := r.String(s); after != s && json.Valid([]byte(after)) {
				return after, true
			}
			return s, false
		}
	}
	after := r.String(s)
	return after, after != s
}

func decodeSingleJSON(b []byte) (any, bool) {
	if !json.Valid(b) {
		return nil, false
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, false
	}
	if dec.More() {
		return nil, false
	}
	var extra any
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		return nil, false
	}
	return v, true
}

func encodeSingleLineJSON(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n")), nil
}

func asInt(v any) (int, bool) {
	switch n := v.(type) {
	case int:
		return n, true
	case json.Number:
		i, err := strconv.Atoi(n.String())
		return i, err == nil
	case float64:
		return int(n), float64(int(n)) == n
	default:
		return 0, false
	}
}
