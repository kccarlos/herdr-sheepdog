package supervisor

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kccarlos/herdr-sheepdog/internal/brain"
	"github.com/kccarlos/herdr-sheepdog/internal/herdr"
	"github.com/kccarlos/herdr-sheepdog/internal/push"
	"github.com/kccarlos/herdr-sheepdog/internal/redact"
	"github.com/kccarlos/herdr-sheepdog/internal/state"
)

type captureNotifier struct {
	got []push.Notification
}

func (c *captureNotifier) Notify(n push.Notification) {
	c.got = append(c.got, n)
}

func TestRedactionPersistenceBoundaries(t *testing.T) {
	secret := "custom-secret-123456789"
	notifs := &captureNotifier{}
	opt := func(c *Config) {
		c.Redactor = redact.New(secret)
		c.Notifier = notifs
	}
	h := newHarness(t, opt)
	h.addAgent("w5:p1", "auth", "codex", herdr.StateIdle)
	h.srv.SetScreen("w5:p1", "output "+secret)
	for _, req := range []brain.CardRequest{{Kind: brain.ShowCard, Title: secret, Markdown: secret}, {Kind: brain.ShowExcerpt, PaneID: "w5:p1"}} {
		card, err := h.sup.buildCard(context.Background(), req)
		if err != nil {
			t.Fatal(err)
		}
		h.sup.storeCard(card)
		b, _ := json.Marshal(card)
		if strings.Contains(string(b), secret) {
			t.Error("card contains secret")
		}
	}
	if err := h.sup.appendNote(secret); err != nil {
		t.Fatal(err)
	}
	if _, err := h.sup.createPromise("w5:p1", brain.PromiseRequest{Text: secret, Event: brain.PromiseEventDone}); err != nil {
		t.Fatal(err)
	}
	row, _ := h.store.Get("w5:p1")
	h.sup.announceBlock(context.Background(), row, strings.ReplaceAll(fixture(t, "codex-approval-prompt.screen.txt"), "hello from codex.", secret))
	for _, name := range []string{"state.json", "notes.jsonl", "cards.jsonl"} {
		b, err := os.ReadFile(filepath.Join(h.dir, name))
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(b), secret) {
			t.Errorf("%s contains secret", name)
		}
	}
	p, ok := h.store.Pending()
	if !ok || !strings.Contains(p.Command, "printf") || !strings.Contains(p.Command, "[REDACTED]") {
		t.Errorf("approval lost context: %+v", p)
	}
	h.sup.notify(announcement{row: row, event: brain.PromiseEventBlocked, eventID: "evt-red-1", pending: p.ID}, nil, "auth asks to run "+secret)
	if len(notifs.got) == 0 {
		t.Fatal("expected push notification")
	}
	for _, n := range notifs.got {
		b, _ := json.Marshal(n)
		if strings.Contains(string(b), secret) {
			t.Errorf("push notification leaked secret: %s", b)
		}
	}
}

func TestInvestigationRedactionPersistence(t *testing.T) {
	secret := "custom-secret-123456789"
	for _, failure := range []bool{false, true} {
		t.Run(map[bool]string{false: "result", true: "error"}[failure], func(t *testing.T) {
			f := &fakeInvestigator{answer: secret}
			if failure {
				f.err = errors.New(secret)
			}
			h := newInvestigationHarness(t, f, func(c *Config) { c.Redactor = redact.New(secret) })
			job := start(t, h, "check", "", "check")
			waitFor(t, func() bool {
				inv, ok := h.sup.investigation(job.Job)
				return ok && inv.Status != state.InvestigationRunning
			}, "investigation completion")
			b, err := os.ReadFile(filepath.Join(h.dir, "state.json"))
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(b), secret) {
				t.Error("investigation secret persisted")
			}
		})
	}
}
