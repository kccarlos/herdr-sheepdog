// The orchestrator's replies that carry suggested next steps, and whether the
// newest one can still be tapped (docs/dev/design/conversation-timeline.md,
// section 14). The options are not in GET /api/conversation: they are in the
// `voice.tool_result` event of the reply, so the panel folds those events (the
// one place it reads an event's payload) like BrainActivity.fold, and
// `liveSuggestions` ports ConversationTimeline.suggestedReplies' rule. Pure.
// No herdr imports.

import { daemonNotice } from "./classify.js";

// How many replies are kept.
export const CAPACITY = 50;

// Folds one daemon event (`id` is its event id) into `replies`, the replies
// seen so far in id order, and returns the new list (the same one when the
// event changes nothing). Only the orchestrator's `to_supervisor` results are
// replies; a cancelled one is not.
export function foldReply(replies, event, id) {
  if (event.type !== "voice.tool_result") return replies;
  const p = event.payload ?? {};
  if (p.name !== "to_supervisor" || p.cancelled === true || !(id > 0)) return replies;
  const pending = p.pending === true;
  const spoken = typeof p.spoken === "string" ? p.spoken.trim() : "";
  if (!spoken && !pending) return replies;
  const options = Array.isArray(p.options) ? p.options.filter((o) => typeof o === "string" && o.trim()).map((o) => o.trim()) : [];
  const reply = { id, options, pending };
  const at = replies.findIndex((r) => r.id === id);
  if (at >= 0) {
    const old = replies[at];
    if (old.pending === pending && old.options.length === options.length && old.options.every((o, i) => o === options[i])) return replies;
    return replies.map((r, i) => (i === at ? reply : r));
  }
  const next = [...replies, reply].sort((a, b) => a.id - b.id);
  return next.length > CAPACITY ? next.slice(next.length - CAPACITY) : next;
}

// A context loss the voice would not recover from: the question it asked is
// forgotten.
const ENDS = new Set(["reset", "restarted", "contextLost", "freshReconnect", "voiceRestarted"]);

// The one reply whose options are live: { replyId, options }, or null. It is
// the newest reply, when after it there is none of: the user's words (said,
// typed, a tapped option, one still on its way); a newer reply, with options
// or not; a new conversation, a daemon restart or the voice losing its
// context. The voice saying the reply, agent updates and questions do not end
// it. A pending reply has no options yet. `items` are the conversation's (and
// the outbox's pending ones), `replies` what foldReply kept.
export function liveSuggestions(items, replies) {
  const events = [...items.map((item) => ({ id: item.id, item })), ...replies.map((reply) => ({ id: reply.id, reply }))].sort((a, b) => a.id - b.id);
  let live = null;
  for (const e of events) {
    if (e.reply) live = !e.reply.pending && e.reply.options.length ? { replyId: e.reply.id, options: e.reply.options } : null;
    else if (e.item.kind === "shepherd" || ENDS.has(daemonNotice(e.item))) live = null;
  }
  return live;
}
