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
