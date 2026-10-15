package main

import (
	"runtime/debug"
	"testing"
)

func TestStampFromBuildInfo(t *testing.T) {
	bi := &debug.BuildInfo{
		Main: debug.Module{Version: "v0.2.0"},
		Settings: []debug.BuildSetting{
			{Key: "vcs.revision", Value: "0123456789abcdef"},
			{Key: "vcs.time", Value: "2026-10-15T10:00:00Z"},
			{Key: "vcs.modified", Value: "true"},
		},
	}
	v, c, d := stampFromBuildInfo(bi, "dev", "none", "unknown")
	if v != "v0.2.0" || c != "0123456-dirty" || d != "2026-10-15T10:00:00Z" {
		t.Fatalf("got %q %q %q", v, c, d)
	}
	// ldflags values win.
	v, c, d = stampFromBuildInfo(bi, "v1.0.0", "abc", "2026-01-01")
	if v != "v1.0.0" || c != "abc" || d != "2026-01-01" {
		t.Fatalf("ldflags overridden: %q %q %q", v, c, d)
	}
	// "(devel)" and no VCS data keep the placeholders.
	v, c, d = stampFromBuildInfo(&debug.BuildInfo{Main: debug.Module{Version: "(devel)"}}, "dev", "none", "unknown")
	if v != "dev" || c != "none" || d != "unknown" {
		t.Fatalf("placeholders changed: %q %q %q", v, c, d)
	}
}
