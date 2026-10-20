package brain

import (
	"strings"
	"testing"
)

func TestClipMarkdown(t *testing.T) {
	md := strings.Repeat("line of text\n", 1000)
	got := clipMarkdown(md, ShowMarkdownBytes)
	if len(got) > ShowMarkdownBytes || !strings.HasSuffix(got, "…(cut)") {
		t.Errorf("clipped to %d bytes, ends %q", len(got), got[len(got)-12:])
	}
	if clipMarkdown("short", ShowMarkdownBytes) != "short" {
		t.Error("short markdown changed")
	}
}
