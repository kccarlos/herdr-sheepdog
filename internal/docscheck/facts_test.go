package docscheck

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestDocFacts checks that user and contract docs stay aligned with the
// daemon's storage layout, auth/TLS model, setup flow, and routes.
func TestDocFacts(t *testing.T) {
	root := repoRoot(t)
	read := func(rel string) string {
		t.Helper()
		b, err := os.ReadFile(filepath.Join(root, rel))
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	mustContain := func(rel, doc string, wants ...string) {
		t.Helper()
		for _, w := range wants {
			if !strings.Contains(doc, w) {
				t.Errorf("%s is missing %q", rel, w)
			}
		}
	}
	mustNotContain := func(rel, doc string, bads ...string) {
		t.Helper()
		for _, b := range bads {
			if strings.Contains(doc, b) {
				t.Errorf("%s still contains stale %q", rel, b)
			}
		}
	}

	// Finding #2 & #9: CONFIG.md storage table, env vars, and init prose.
	cfgDoc := read("CONFIG.md")
	mustContain("CONFIG.md", cfgDoc,
		"cards.jsonl",
		"wrapups.json",
		"tsnet.crt",
		"ANTHROPIC_API_KEY",
		"OPENAI_API_KEY",
	)
	mustNotContain("CONFIG.md", cfgDoc,
		"A new an existing",
		"gets a long random web token",
	)

	// Finding #2 & #3/F1: docs/privacy.md storage table and file browser.
	privDoc := read("docs/privacy.md")
	mustContain("docs/privacy.md", privDoc,
		"cards.jsonl",
		"wrapups.json",
		"env-file.json",
		"tsnet.crt",
		"Files",
	)

	// Finding #3/F1: docs/remote-access.md mentions the read-only file browser when web.shell is off,
	// and documents proxy/Funnel loopback rules.
	remDoc := read("docs/remote-access.md")
	mustContain("docs/remote-access.md", remDoc, "file browser", "tailscale funnel reset")

	secDoc := read("SECURITY.md")
	mustContain("SECURITY.md", secDoc, "X-Forwarded-*", "Tailscale Funnel")
	mustNotContain("SECURITY.md", secDoc, "It has **no TLS**")

	// Finding #4, #6, #7: docs/quick-start.md and docs/models.md.
	qsDoc := read("docs/quick-start.md")
	mustContain("docs/quick-start.md", qsDoc, "Free quick start")
	mustNotContain("docs/quick-start.md", qsDoc, "install.sh | sh\nsheepdog setup")

	modDoc := read("docs/models.md")
	mustContain("docs/models.md", modDoc, "Free quick start")

	// Finding #5: docs/safety.md network bullet.
	safeDoc := read("docs/safety.md")
	mustContain("docs/safety.md", safeDoc, "TLS and paired devices")
	mustNotContain("docs/safety.md", safeDoc, "a token for anything else, no TLS")

	// Finding #8: docs/commands.md doctor entry.
	cmdDoc := read("docs/commands.md")
	mustContain("docs/commands.md", cmdDoc, "project folders")
	mustNotContain("docs/commands.md", cmdDoc, "port, bind, token, push")

	// Finding #3/F1, #10, F2, F3: docs/iphone-app.md.
	iosDoc := read("docs/iphone-app.md")
	mustContain("docs/iphone-app.md", iosDoc,
		"Save a command",
		"Mark this moment",
		"start over",
		"⌘K",
		"Control Center",
		"**Files**",
	)
	mustNotContain("docs/iphone-app.md", iosDoc, "Add a command…")

	// Finding F3: docs/desktop.md spoken reset.
	deskDoc := read("docs/desktop.md")
	mustContain("docs/desktop.md", deskDoc, "start over")

	// Finding F2, F4: docs/troubleshooting.md.
	trDoc := read("docs/troubleshooting.md")
	mustContain("docs/troubleshooting.md", trDoc,
		"Mark this moment",
		"gate=off",
	)

	// Finding #11: docs/dev/ws-contract.md.
	wsDoc := read("docs/dev/ws-contract.md")
	mustContain("docs/dev/ws-contract.md", wsDoc,
		"term_unavailable",
		"/api/brain/models",
		"/api/brain/model",
		"/api/utterance",
		"/api/pending/cancel",
		"maxRemoteConns",
		"not a tracked agent",
		"hidden for safety",
	)
	mustNotContain("docs/dev/ws-contract.md", wsDoc,
		"ConstantTimeCompare",
		"There is no rate limiting",
	)
}
