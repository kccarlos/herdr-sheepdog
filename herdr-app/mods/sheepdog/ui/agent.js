// Agent details, opened from an agent's name in the timeline. The values come
// from GET /api/agents, using the state fields the phone's detail view shows.

import { h_flex, v_flex } from "gpui-base";
import { errorText, mutedText, overlayButton, overlayFrame, valueBlock } from "./overlay-widgets.js";

function questionAndAnswer(t, agent) {
  if (!agent.lastQuestion) return mutedText(t, "No question has been recorded yet.");
  const answer = agent.answered ? (agent.lastAnswer || "Answered in the agent's terminal") : "Not answered yet";
  return valueBlock(t, agent.answered ? "Last question and answer" : "Last question", `${agent.lastQuestion} — ${answer}`);
}

export function agentOverlay(t, { name, form, onClose }) {
  const agent = form.agent;
  const rows = [];
  if (form.loading) rows.push(mutedText(t, "Loading the agent…"));
  if (form.error) rows.push(errorText(t, form.error));
  if (!form.loading && !form.error && !agent) rows.push(mutedText(t, `No agent named ${name} is running now.`));
  if (agent) {
    rows.push(
      valueBlock(t, "Agent", [agent.name, agent.kind, agent.state].filter(Boolean).join(" · ")),
      valueBlock(t, "Working on", agent.worksOn || "Nothing right now."),
      valueBlock(t, "Last result", agent.lastResult || "No finished result yet."),
    );
    if (agent.lastResultDetail) rows.push(valueBlock(t, "Result detail", agent.lastResultDetail));
    rows.push(questionAndAnswer(t, agent));
    rows.push(valueBlock(t, "Space", agent.space || "Not reported"));
  }
  const body = v_flex()
    .id("sheepdog-agent-details")
    .flex_1()
    .min_h(0)
    .w_full()
    .overflow_y_scrollbar()
    .p(16)
    .gap(14)
    .children(rows);
  return overlayFrame(t, {
    id: "sheepdog-agent-details",
    title: name || "Agent details",
    subtitle: "What the daemon knows now",
    onClose,
    body,
    footer: h_flex().w_full().px(14).py(8).justify_end().border_t(1).border_color(t.border).child(overlayButton(t, "agent-details-done", "Done", onClose, { primary: true })),
  });
}
