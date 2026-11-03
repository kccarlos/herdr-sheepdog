// The fields the phone's agent details show, resolved from GET /api/agents.
// Pure, so node tests it. No herdr imports.

const text = (value) => (typeof value === "string" ? value.trim() : "");

export function agentDetails(agents, name) {
  const wanted = text(name).toLowerCase();
  if (!wanted) return null;
  const agent = (Array.isArray(agents) ? agents : []).find((candidate) =>
    [candidate?.spoken, candidate?.alias, candidate?.name].some((value) => text(value).toLowerCase() === wanted),
  );
  if (!agent) return null;
  const block = agent.last_block;
  const result = agent.last_result;
  return {
    paneID: text(agent.pane_id),
    name: text(agent.spoken) || text(agent.alias) || text(agent.name) || wanted,
    alias: text(agent.alias),
    kind: text(agent.kind),
    state: text(agent.state),
    worksOn: text(agent.current_task) || text(agent.last_task) || text(agent.title),
    lastResult: text(result?.spoken),
    lastResultDetail: text(result?.detail),
    lastResultAt: text(result?.at),
    lastQuestion: text(block?.question),
    lastAnswer: text(block?.answer),
    answered: block?.answered === true,
    space: text(agent.workspace_label) || text(agent.workspace),
  };
}

// ---- counts for the status line --------------------------------------------
// How many agents are in each attention group, from /api/state's `agents`.
// The daemon sends `attention.group` (needs_you, working, done, not_running);
// an older daemon sends none, and the state decides, as the order in
// docs/dev/design/unread-pinned-mute.md section 5 does for the phone.
const GROUP_OF_STATE = { blocked: "needs_you", working: "working", idle: "done", done: "done" };
const KEY_OF_GROUP = { needs_you: "needsYou", working: "working", done: "done", not_running: "notRunning" };

export function attentionCounts(state) {
  const counts = { needsYou: 0, working: 0, done: 0, notRunning: 0 };
  for (const agent of Array.isArray(state?.agents) ? state.agents : []) {
    const given = agent?.attention?.group;
    const group = Object.hasOwn(KEY_OF_GROUP, given) ? given : (GROUP_OF_STATE[text(agent?.state)] ?? "not_running");
    counts[KEY_OF_GROUP[group]] += 1;
  }
  return counts;
}

// "1 needs you · 2 working · 1 done": the groups that have agents, in that
// order; "" when none do. Agents that are not running are left out.
export function attentionLine(counts) {
  const parts = [];
  if (counts?.needsYou > 0) parts.push(`${counts.needsYou} ${counts.needsYou === 1 ? "needs" : "need"} you`);
  if (counts?.working > 0) parts.push(`${counts.working} working`);
  if (counts?.done > 0) parts.push(`${counts.done} done`);
  return parts.join(" · ");
}
