package brain

import (
	"slices"
	"strings"
	"testing"
	"time"
)

// queryWords keeps words of any script, and splits scripts written
// without spaces into character pairs.
func TestQueryWordsAnyScript(t *testing.T) {
	for _, tc := range []struct {
		q    string
		want []string
	}{
		{"walk me through, the DAY", []string{"walk", "through", "the", "day"}},
		{"今天最开始", []string{"今天", "天最", "最开", "开始"}},
		{"db-codex还在等吗?", []string{"codex", "还在", "在等", "等吗"}},
		{"我 说了", []string{"我", "说了"}},
		{"привет мир", []string{"привет", "мир"}},
		{"テストは", []string{"テス", "スト", "トは"}},
	} {
		if got := queryWords(tc.q); !slices.Equal(got, tc.want) {
			t.Errorf("queryWords(%q) = %q, want %q", tc.q, got, tc.want)
		}
	}
}

func TestRecordLineFormat(t *testing.T) {
	now := time.Date(2026, 10, 8, 15, 0, 0, 0, time.UTC)
	if got := recordLine(time.Time{}, CompactionInfo{}, now); got != "" {
		t.Errorf("empty record: %q", got)
	}
	yesterday := now.Add(-20 * time.Hour)
	if got := recordLine(yesterday, CompactionInfo{}, now); !strings.Contains(got, "started at Wed 2026-10-07 19:00:00") {
		t.Errorf("another day: %q", got)
	}
}
