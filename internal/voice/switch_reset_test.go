package voice

import (
	"context"
	"testing"
)

// fakeSessReset is a fakeSess that also implements the optional Reset
// ("New conversation").
type fakeSessReset struct {
	*fakeSess
	resets []string
}

func (f *fakeSessReset) Reset(reason string) error {
	f.resets = append(f.resets, reason)
	return nil
}

// Switcher.Reset forwards to the current session when it supports it.
func TestSwitcherResetForwardsToCurrentSession(t *testing.T) {
	r := &fakeSessReset{fakeSess: newFake("gemini")}
	sw, err := NewSwitcher([]Provider{
		{ProviderInfo: ProviderInfo{ID: "gemini", Available: true}, New: func() (Session, error) { return r, nil }},
	}, "gemini", nil)
	if err != nil {
		t.Fatal(err)
	}
	// Before Start there is no current session: a harmless no-op.
	if err := sw.Reset("early"); err != nil {
		t.Fatalf("Reset before Start: %v", err)
	}
	if err := sw.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := sw.Reset("new_conversation"); err != nil {
		t.Fatal(err)
	}
	if len(r.resets) != 1 || r.resets[0] != "new_conversation" {
		t.Fatalf("resets = %v", r.resets)
	}
}

// A session that does not implement Reset makes it a no-op, not an error.
func TestSwitcherResetNoopWithoutSupport(t *testing.T) {
	g := newFake("gemini")
	sw, err := NewSwitcher([]Provider{
		{ProviderInfo: ProviderInfo{ID: "gemini", Available: true}, New: func() (Session, error) { return g, nil }},
	}, "gemini", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := sw.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := sw.Reset("x"); err != nil {
		t.Fatalf("Reset without support: %v", err)
	}
}
