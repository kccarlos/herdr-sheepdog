package ctxcap

// Link ids are "<kind>:<id>": call:<voice tool call id>, push:<push id>,
// evt:<worker event id>. The supervisor mints push and event ids and
// puts them on a push's tags; the voice providers turn the tags into the
// push item's links (LinksFromTags), and the orchestrator's messages carry the
// same strings (brain.Message.Link).

// The push tags that carry link ids.
const (
	TagPushID  = "push_id"
	TagEventID = "event_id"
	TagCallID  = "call_id"
)

// CallLink, PushLink and EventLink build a link id ("" for an empty id).
func CallLink(id string) string  { return link("call:", id) }
func PushLink(id string) string  { return link("push:", id) }
func EventLink(id string) string { return link("evt:", id) }

func link(prefix, id string) string {
	if id == "" {
		return ""
	}
	return prefix + id
}

// LinksFromTags returns the link ids a push's tags carry.
func LinksFromTags(tags map[string]any) []string {
	var out []string
	for _, k := range []struct{ tag, prefix string }{{TagPushID, "push:"}, {TagEventID, "evt:"}, {TagCallID, "call:"}} {
		if id, ok := tags[k.tag].(string); ok && id != "" {
			out = append(out, k.prefix+id)
		}
	}
	return out
}
