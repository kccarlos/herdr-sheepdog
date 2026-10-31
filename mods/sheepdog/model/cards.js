// What the orchestrator put on the screen with its `show` tool: the `card` of a
// `card` item of GET /api/conversation (docs/dev/ws-contract.md "Cards"). It
// ports the words of ScreenCard (ios/Sources/SheepdogKit/Cards.swift). Display
// only: an approval card carries the question read-only, never a way to answer
// it. Pure. No herdr imports.

const trimmed = (s) => (s ?? "").trim();

// The card a history item opens, or null: not a card item, or the daemon no
// longer stores its card.
export const cardOf = (item) => (item?.kind === "card" && item.card ? item.card : null);

// The agent's name as the voice says it.
export const agentName = (card) => trimmed(card.spoken) || trimmed(card.agent) || null;

// What kind of card it is, in a word or two.
export function kindWord(card) {
  switch (card.kind) {
    case "agent": return "Agent window";
    case "excerpt": return "Screen";
    case "approval": return "Question";
    default: return "Card";
  }
}

// The heading: the title, else a plain one for the kind.
export function displayTitle(card) {
  const title = trimmed(card.title);
  if (title) return title;
  const who = agentName(card);
  switch (card.kind) {
    case "agent": return who ? `${who}'s window` : "Agent window";
    case "excerpt": return who ? `${who}'s screen` : "Agent screen";
    case "approval": return who ? `${who} is asking` : "An agent is asking";
    default: return "Card";
  }
}

// The agent this card is about, as herdr-app knows it: the card's `pane_id` is
// the terminal address of the agent's pane (with herdr-core, the hc terminal
// address), which is what herdr-app's `agent.focus` takes. Null when the card
// is not about one.
export const focusTarget = (card) => trimmed(card.pane_id) || null;

// The command that shows that agent in herdr-app, in place of the phone's agent
// window.
export const focusCommand = (card) => (focusTarget(card) ? { name: "agent.focus", args: { terminal: focusTarget(card) } } : null);
