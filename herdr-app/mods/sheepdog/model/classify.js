// The panel's copy of the phone's classification table: one daemon item in,
// its category and what the timeline does with it out. It ports
// ConversationTimeline.classify, DaemonNotice and the settled-question words
// (ios/Sources/SheepdogKit: ConversationClassification, ConversationContext,
// ConversationHistory). The cases both sides must agree on are one file,
// ios/Tests/Fixtures/timeline-classification.json; a change to either
// classifier lands with a change to it. No herdr imports: node tests this.

// The categories an item can have (docs/dev/design/conversation-timeline.md,
// section 4). "screen" is the phone's folded group, which nothing here makes.
export const CATEGORIES = {
  dialogue: { code: "DLG", title: "Dialogue" },
  needsYou: { code: "ASK", title: "Needs you" },
  outcome: { code: "UPD", title: "Agent update" },
  context: { code: "CTX", title: "Context" },
  system: { code: "SYS", title: "System" },
  screen: { code: "SCR", title: "Screen" },
};

const KINDS = new Set(["shepherd", "dog", "push", "approval", "card", "notice"]);

// The `subkind` of the row the context pass makes for a context loss the user
// would notice. Made here, never sent by the daemon.
export const VOICE_RESTARTED = "voice_restarted";

// The daemon's `subkind` tags on a notice (docs/dev/ws-contract.md "History
// and resume").
const SUBKINDS = new Map([
  ["start", "restarted"], ["reset", "reset"], ["context_lost", "contextLost"],
  ["fresh", "freshReconnect"], ["resumed", "resumedReconnect"], ["closed", "closed"],
]);

const trimmed = (s) => (s ?? "").trim();
const plain = (category) => ({ category, shownByDefault: true, folds: false, bridgesRuns: false, ledger: false });

// A day heading: where you are, so context.
export const DAY_CLASS = plain("context");

// A kind this build does not know reads as "unknown": a quiet notice.
export const kindOf = (item) => (KINDS.has(item.kind) ? item.kind : "unknown");

// A push that interrupts is the one that must stand out.
export const needsUser = (item) => kindOf(item) === "push" && item.urgency === "interrupt";

// What a notice is, from the daemon's `subkind` first and its words only when
// the subkind is absent or one this build does not know (an older daemon).
export function daemonNotice(item) {
  const kind = kindOf(item);
  if (kind !== "notice" && kind !== "unknown") return "other";
  if (kind === "notice" && item.subkind === "reset") return "reset";
  if (item.subkind === VOICE_RESTARTED) return "voiceRestarted";
  const tagged = SUBKINDS.get(item.subkind);
  if (tagged) return tagged;
  const text = trimmed(item.text).replace(/\.+$/, "");
  switch (text) {
    case "Sheepdog started": return "restarted";
    case "Voice context lost": return "contextLost";
    case "Voice reconnected with a fresh session; earlier context was lost": return "freshReconnect";
    case "Voice reconnected; the conversation continues": return "resumedReconnect";
    default: return text.startsWith("Voice session closed") ? "closed" : "other";
  }
}

// The one table of item kind to category, and so of what is shown, folded and
// hidden. `ledger`: drawn as a line of the ledger, consecutive ones in one
// card. `folds`: may fold into an "N agent updates" row (the panel does not
// fold yet; the flag is the table's). `bridgesRuns`: a run of updates
// continues across it.
export function classify(item) {
  switch (kindOf(item)) {
    case "shepherd":
    case "dog":
      return plain("dialogue");
    case "push":
      return needsUser(item) ? plain("needsYou") : { ...plain("outcome"), folds: true, ledger: true };
    case "approval":
      // Open: a question. Settled: what happened to it.
      return approvalOutcome(item) == null ? plain("needsYou") : { ...plain("outcome"), ledger: true };
    case "card":
      return plain("outcome");
    default:
      switch (daemonNotice(item)) {
        case "reset":
        case "voiceRestarted":
          return plain("context");
        case "restarted":
          return { ...plain("context"), bridgesRuns: true };
        // The daemon's reconnects, context losses and shutdowns.
        default:
          return { ...plain("system"), shownByDefault: false };
      }
  }
}

// An approval option's label is a decline when it says no.
function isNegative(label) {
  const l = trimmed(label).toLowerCase();
  return l === "no" || l.startsWith("no,") || l.startsWith("no ") || l.startsWith("no.") || l.startsWith("deny") || l.startsWith("reject");
}

// `via` of an answered approval, for people: a tap, or the voice.
export function describeVia(via) {
  switch (via) {
    case "tap": return "by tap";
    case "fast_path":
    case "brain":
    case "confirm":
    case "voice": return "by voice";
    default: return via ? `via ${via}` : null;
  }
}

// An approval's answered state in plain words: "You approved — Yes, proceed ·
// by tap". Null while it is open, and for anything that is not an approval.
export function approvalOutcome(item) {
  if (kindOf(item) !== "approval") return null;
  switch (item.resolution ?? "open") {
    case "open":
      return null;
    case "confirmed": {
      const label = trimmed(item.answer);
      const how = describeVia(item.via);
      let out = label ? `${isNegative(label) ? "You declined" : "You approved"} — ${label}` : "Answered";
      if (how) out += ` · ${how}`;
      return out;
    }
    case "expired":
      return "Expired without an answer";
    case "replaced":
      return "Replaced by a newer question";
    default:
      // Cancelled, or a resolution this build does not know. The daemon does
      // not say why the prompt left the screen.
      return "Closed";
  }
}

// The question's first few words, without who is asking: "Codex is asking
// allow Computer Use to use Finder." becomes "allow Computer Use to use
// Finder". Null when nothing is left.
function questionTopic(item) {
  let words = trimmed(item.text).replace(/[.?!:]+$/, "");
  const agent = trimmed(item.agent);
  if (agent && words.toLowerCase().startsWith(agent.toLowerCase())) {
    words = words.slice(agent.length);
    const leads = [" is asking to ", " is asking ", " asks to ", " asks ", " wants to ", " needs to ", " needs "];
    const lead = leads.find((l) => words.toLowerCase().startsWith(l));
    if (lead) words = words.slice(lead.length);
  }
  words = words.trim();
  if (!words) return null;
  const limit = 36;
  const chars = Array.from(words);
  if (chars.length <= limit) return words;
  const cut = chars.slice(0, limit).join("");
  const at = cut.lastIndexOf(" ");
  return `${at >= 0 ? cut.slice(0, at) : cut}…`;
}

// "Codex's question (allow Computer Use to use Finder)": whose question, and
// its first few words.
export function questionTitle(item) {
  const agent = trimmed(item.agent);
  const who = agent ? `${agent}'s question` : "A question";
  const topic = questionTopic(item);
  return topic ? `${who} (${topic})` : who;
}

// How a question that ended unanswered ended, in a word. Null for an open or
// answered one.
function endedWord(item) {
  if (kindOf(item) !== "approval") return null;
  switch (item.resolution ?? "open") {
    case "open":
    case "confirmed": return null;
    case "expired": return "expired";
    case "replaced": return "replaced";
    default: return "closed";
  }
}

// The settled question's line: "Codex's question (Finder access) — closed", or
// the answer, "You approved — Yes · by tap". Null while it is open.
export function settledLine(item) {
  const outcome = approvalOutcome(item);
  if (outcome == null) return null;
  const ended = endedWord(item);
  return ended ? `${questionTitle(item)} — ${ended}` : outcome;
}
