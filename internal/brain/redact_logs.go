package brain

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/kccarlos/herdr-sheepdog/internal/ctxcap"
	"github.com/kccarlos/herdr-sheepdog/internal/redact"
)

// RedactResult reports what RedactLogs inspected and rewrote.
type RedactResult struct {
	CheckedFiles int
	ChangedFiles int
	ChangedLines int
}

// credentialKeyRe matches structured JSON map keys that hold secrets,
// mirroring the key=value credential pattern in redact.Redactor for decoded
// JSON maps where the key and value are separate strings.
var credentialKeyRe = regexp.MustCompile(`(?i)(?:^|[^0-9A-Za-z])(?:api[_-]?key|(?:access|auth|refresh|id|bearer)[_-]?token|client[_-]?secret|password|passwd)$`)

const (
	minCredentialValLen = 6
	maxFailedJSONProbes = 4096
)

func isCredentialKey(k string) bool {
	return credentialKeyRe.MatchString(k)
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
	return RedactLogsContext(context.Background(), dataDir, r)
}

// RedactLogsContext is RedactLogs with cancellation support: if ctx is
// cancelled before a file is renamed, that file and all remaining files are
// left untouched and no temporary files remain.
func RedactLogsContext(ctx context.Context, dataDir string, r *redact.Redactor) (RedactResult, error) {
	var res RedactResult
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return res, err
	}
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
		if err := ctx.Err(); err != nil {
			return res, err
		}
		res.CheckedFiles++
		lines, err := redactJSONLFile(ctx, p, r)
		if err != nil {
			return res, err
		}
		if lines > 0 {
			res.ChangedFiles++
			res.ChangedLines += lines
		}
	}
	if res.ChangedFiles > 0 {
		syncDir(brainDir)
	}
	return res, nil
}

func syncDir(dir string) {
	d, err := os.Open(dir)
	if err != nil {
		return
	}
	_ = d.Sync()
	_ = d.Close()
}

func redactJSONLFile(ctx context.Context, path string, r *redact.Redactor) (int, error) {
	fi, err := os.Lstat(path)
	if err != nil {
		return 0, err
	}
	if !fi.Mode().IsRegular() {
		return 0, nil
	}
	f, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	reader := bufio.NewReaderSize(f, 256<<10)
	var outLines [][]byte
	changedLines := 0
	for {
		if err := ctx.Err(); err != nil {
			_ = f.Close()
			return 0, err
		}
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
	if err := ctx.Err(); err != nil {
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
	if err := ctx.Err(); err != nil {
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
		// Truncated crash tail or concatenated JSON records: redact embedded
		// JSON structures as well as plain text without corrupting valid spans.
		s := string(line)
		red, changed := redactEmbeddedJSON(s, r)
		if !changed {
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
		if textChanged {
			newSec["start"] = newStart
			newSec["end"] = newEnd
			newSec["tokens"] = ctxcap.EstimateTokens(redPiece)
			if _, hasSecText := newSec["text"].(string); hasSecText {
				newSec["text"] = redPiece
			}
		}
		newSecs[i] = newSec
	}

	built := b.String()
	// Catch a secret that straddled a section boundary while snapping every
	// adjusted offset to valid UTF-8 rune boundaries.
	if full := r.String(built); full != built {
		textChanged = true
		changed = true
		newSecs = realignSectionsUTF8(newSecs, built, full)
		built = full
	}

	for k, v := range m {
		if k == "text" || k == "sections" {
			continue
		}
		rk, rv, entryChanged := redactMapEntry(k, v, r)
		if entryChanged {
			changed = true
		}
		out[rk] = rv
	}
	if textChanged {
		out["text"] = built
		if _, hasIntTok := asInt(out["tokens"]); hasIntTok {
			totalTokens := 0
			for _, elem := range newSecs {
				if sec, ok := elem.(map[string]any); ok {
					if tok, ok := asInt(sec["tokens"]); ok {
						totalTokens += tok
					}
				}
			}
			out["tokens"] = totalTokens
		}
	} else {
		out["text"] = textStr
	}
	out["sections"] = newSecs
	return out, true, changed
}

// realignSectionsUTF8 adjusts newSecs so [start, end) byte offsets partition
// full contiguously on valid UTF-8 rune boundaries, preserving unchanged
// prefix/suffix sections when possible and collapsing trailing sections if
// full has fewer runes than len(newSecs).
func realignSectionsUTF8(newSecs []any, built, full string) []any {
	if full == "" || len(newSecs) == 0 {
		return nil
	}
	runeOffsets := make([]int, 0, len(full)+1)
	for idx := range full {
		runeOffsets = append(runeOffsets, idx)
	}
	runeOffsets = append(runeOffsets, len(full))
	runeCount := len(runeOffsets) - 1
	if len(newSecs) > runeCount {
		newSecs = newSecs[:runeCount]
	}
	n := len(newSecs)
	if n == 1 {
		sec := newSecs[0].(map[string]any)
		sec["start"] = 0
		sec["end"] = len(full)
		sec["tokens"] = ctxcap.EstimateTokens(full)
		if _, hasSecText := sec["text"].(string); hasSecText {
			sec["text"] = full
		}
		return newSecs
	}

	commonPrefix := 0
	for commonPrefix < len(built) && commonPrefix < len(full) && built[commonPrefix] == full[commonPrefix] {
		commonPrefix++
	}
	for commonPrefix > 0 && commonPrefix < len(full) && !utf8.RuneStart(full[commonPrefix]) {
		commonPrefix--
	}
	commonSuffix := 0
	for commonSuffix < len(built)-commonPrefix && commonSuffix < len(full)-commonPrefix &&
		built[len(built)-1-commonSuffix] == full[len(full)-1-commonSuffix] {
		commonSuffix++
	}
	for commonSuffix > 0 && !utf8.RuneStart(full[len(full)-commonSuffix]) {
		commonSuffix--
	}

	prevRI := 0
	for i := range n {
		sec := newSecs[i].(map[string]any)
		var endRI int
		if i == n-1 {
			endRI = runeCount
		} else {
			oldEnd, _ := asInt(sec["end"])
			targetByte := oldEnd
			if oldEnd <= commonPrefix {
				targetByte = oldEnd
			} else if oldEnd >= len(built)-commonSuffix {
				targetByte = len(full) - (len(built) - oldEnd)
			}
			ri := sort.SearchInts(runeOffsets, targetByte)
			minRI := prevRI + 1
			maxRI := runeCount - (n - 1 - i)
			endRI = max(minRI, min(ri, maxRI))
		}
		startByte := runeOffsets[prevRI]
		endByte := runeOffsets[endRI]
		piece := full[startByte:endByte]
		sec["start"] = startByte
		sec["end"] = endByte
		sec["tokens"] = ctxcap.EstimateTokens(piece)
		if _, hasSecText := sec["text"].(string); hasSecText {
			sec["text"] = piece
		}
		prevRI = endRI
	}
	return newSecs
}

func redactMapEntry(k string, x any, r *redact.Redactor) (string, any, bool) {
	rk := r.String(k)
	changed := rk != k
	var rx any
	var xc bool
	if isCredentialKey(k) {
		rx, xc = redactCredentialValue(x, r)
	} else {
		rx, xc = redactAny(x, r)
	}
	if xc {
		changed = true
	}
	return rk, rx, changed
}

func redactCredentialValue(x any, r *redact.Redactor) (any, bool) {
	switch x := x.(type) {
	case string:
		trimmed := strings.TrimSpace(x)
		if trimmed == redact.Mask || len(trimmed) < minCredentialValLen {
			return redactString(x, r)
		}
		if (trimmed[0] == '{' && trimmed[len(trimmed)-1] == '}') || (trimmed[0] == '[' && trimmed[len(trimmed)-1] == ']') {
			if json.Valid([]byte(trimmed)) {
				return redactString(x, r)
			}
		}
		return redact.Mask, true
	case []any:
		out := make([]any, len(x))
		changed := false
		for i, elem := range x {
			re, ec := redactCredentialValue(elem, r)
			if ec {
				changed = true
			}
			out[i] = re
		}
		return out, changed
	case []string:
		out := make([]string, len(x))
		changed := false
		for i, elem := range x {
			re, ec := redactCredentialValue(elem, r)
			if ec {
				changed = true
			}
			out[i], _ = re.(string)
		}
		return out, changed
	default:
		return redactAny(x, r)
	}
}

func redactAny(v any, r *redact.Redactor) (any, bool) {
	switch v := v.(type) {
	case string:
		return redactString(v, r)
	case map[string]any:
		out := make(map[string]any, len(v))
		changed := false
		for k, x := range v {
			rk, rx, entryChanged := redactMapEntry(k, x, r)
			if entryChanged {
				changed = true
			}
			out[rk] = rx
		}
		return out, changed
	case map[string]string:
		out := make(map[string]string, len(v))
		changed := false
		for k, x := range v {
			rk, rx, entryChanged := redactMapEntry(k, x, r)
			if entryChanged {
				changed = true
			}
			out[rk], _ = rx.(string)
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
	case []string:
		out := make([]string, len(v))
		changed := false
		for i, x := range v {
			rx, xc := redactString(x, r)
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
			idx := strings.Index(s, trimmed)
			lead := s[:idx]
			trail := s[idx+len(trimmed):]
			redInner, innerChanged := redactAny(inner, r)
			if innerChanged {
				if encoded, err := encodeSingleLineJSON(redInner); err == nil {
					out := lead + string(encoded) + trail
					return out, out != s
				}
			}
			// Even if inner strings did not change, fall back only if result stays valid JSON.
			if after := r.String(trimmed); after != trimmed && json.Valid([]byte(after)) {
				out := lead + after + trail
				return out, true
			}
			return s, false
		}
	}
	return redactEmbeddedJSON(s, r)
}

// redactEmbeddedJSON scans s for embedded JSON objects and arrays (such as
// renderGemini's "→ send_keys({...})\n\n" or concatenated JSON objects) so
// structured credential keys and JSON-escaped secrets inside them are masked
// cleanly while non-JSON text around them is passed through r.String.
func redactEmbeddedJSON(s string, r *redact.Redactor) (string, bool) {
	if !strings.ContainsAny(s, "{[") {
		after := r.String(s)
		return after, after != s
	}
	var out strings.Builder
	changed := false
	plainStart := 0
	probes := 0
	for i := 0; i < len(s); {
		rel := strings.IndexAny(s[i:], "{[")
		if rel < 0 || probes >= maxFailedJSONProbes {
			break
		}
		cand := i + rel
		end, ok := scanJSONValueEnd(s, cand)
		if !ok {
			probes++
			i = cand + 1
			continue
		}
		inner, decoded := decodeSingleJSON([]byte(s[cand:end]))
		if !decoded {
			probes++
			i = cand + 1
			continue
		}
		if cand > plainStart {
			plain := s[plainStart:cand]
			redPlain := r.String(plain)
			if redPlain != plain {
				changed = true
			}
			out.WriteString(redPlain)
		}
		span := s[cand:end]
		var redInner any
		var innerChanged bool
		if m, isMap := inner.(map[string]any); isMap {
			redInner, innerChanged = redactTopObject(m, r)
		} else {
			redInner, innerChanged = redactAny(inner, r)
		}
		wroteSpan := false
		if innerChanged {
			if encoded, err := encodeSingleLineJSON(redInner); err == nil {
				encStr := string(encoded)
				if encStr != span {
					changed = true
				}
				out.WriteString(encStr)
				wroteSpan = true
			}
		}
		if !wroteSpan {
			if after := r.String(span); after != span && json.Valid([]byte(after)) {
				changed = true
				out.WriteString(after)
			} else {
				out.WriteString(span)
			}
		}
		plainStart = end
		i = end
	}
	if plainStart == 0 {
		after := r.String(s)
		return after, after != s
	}
	if plainStart < len(s) {
		tail := s[plainStart:]
		redTail := r.String(tail)
		if redTail != tail {
			changed = true
		}
		out.WriteString(redTail)
	}
	res := out.String()
	return res, changed && res != s
}

// scanJSONValueEnd finds the byte index just past a balanced JSON object or
// array starting at s[start], rejecting non-JSON code/prose early.
func scanJSONValueEnd(s string, start int) (int, bool) {
	if start >= len(s) {
		return 0, false
	}
	open := s[start]
	if open != '{' && open != '[' {
		return 0, false
	}
	// Quick structural check on the first non-whitespace token inside: a
	// non-empty JSON object must begin with a quoted key, and an empty {} or []
	// holds nothing to redact.
	first := start + 1
	for first < len(s) && isJSONSpace(s[first]) {
		first++
	}
	if first >= len(s) {
		return 0, false
	}
	if open == '{' && s[first] != '"' {
		return 0, false
	}
	if open == '[' && s[first] == ']' {
		return 0, false
	}

	stack := make([]byte, 1, 8)
	if open == '{' {
		stack[0] = '}'
	} else {
		stack[0] = ']'
	}
	inString := false
	escaped := false
	for j := start + 1; j < len(s); j++ {
		c := s[j]
		if inString {
			if escaped {
				escaped = false
				continue
			}
			if c == '\\' {
				escaped = true
				continue
			}
			if c == '"' {
				inString = false
				continue
			}
			if c < 0x20 {
				return 0, false
			}
			continue
		}
		switch c {
		case '"':
			inString = true
		case '{':
			stack = append(stack, '}')
		case '[':
			stack = append(stack, ']')
		case '}', ']':
			if stack[len(stack)-1] != c {
				return 0, false
			}
			stack = stack[:len(stack)-1]
			if len(stack) == 0 {
				if !json.Valid([]byte(s[start : j+1])) {
					return 0, false
				}
				return j + 1, true
			}
		default:
			if !isJSONOutsideStringByte(c) {
				return 0, false
			}
		}
	}
	return 0, false
}

func isJSONSpace(c byte) bool {
	return c == ' ' || c == '\t' || c == '\r' || c == '\n'
}

func isJSONOutsideStringByte(c byte) bool {
	if isJSONSpace(c) || (c >= '0' && c <= '9') {
		return true
	}
	switch c {
	case ':', ',', '-', '+', '.', 'e', 'E', 't', 'r', 'u', 'f', 'a', 'l', 's', 'n':
		return true
	default:
		return false
	}
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
	out := bytes.TrimSuffix(buf.Bytes(), []byte("\n"))
	if bytes.ContainsAny(out, "\r\n") || !json.Valid(out) {
		return nil, fmt.Errorf("brain: encoded JSON line is invalid")
	}
	return out, nil
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
