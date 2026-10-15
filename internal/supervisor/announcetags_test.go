package supervisor

import (
	"testing"

	"github.com/kccarlos/herdr-sheepdog/internal/voice"
)

// An idle line that needs the user carries needs_user, so a voice queue
// holding it releases it ahead of plain news; plain idle news stays untagged.
func TestAnnounceTagsIdleNeedsUser(t *testing.T) {
	if tags := announceTags(voice.UrgencyIdle, false, true, ""); tags["needs_user"] != true {
		t.Errorf("idle line that needs the user: tags = %v, want needs_user", tags)
	}
	if tags := announceTags(voice.UrgencyIdle, false, false, ""); tags != nil {
		t.Errorf("plain idle news: tags = %v, want none", tags)
	}
	if tags := announceTags(voice.UrgencySilent, false, true, "x"); tags != nil {
		t.Errorf("silent context: tags = %v, want none", tags)
	}
	if tags := announceTags(voice.UrgencyInterrupt, false, true, ""); tags != nil {
		t.Errorf("interrupt without a pending action keeps the old rule: tags = %v", tags)
	}
}
