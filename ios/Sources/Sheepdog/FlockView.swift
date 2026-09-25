import SheepdogKit
import SwiftUI

/// The Flock pane: one card per agent with everything the daemon knows
/// about it — state and how long, the task, the question it is waiting on
/// (answerable right here), the last result as the shepherd heard it, and
/// what the worker itself last wrote. Mirrors `/api/state`'s agents (the
/// same list as `/api/agents`), refreshed over `/events`.
struct FlockView: View {
    let agents: [Agent]
    /// The approval card, when a worker is asking: shown inline on that
    /// agent's card instead of pinned above the controls.
    let approval: ApprovalFlow.Card?
    let answer: (Int) -> Void

    var body: some View {
        ScrollView {
            LazyVStack(spacing: 12) {
                if agents.isEmpty {
                    EmptyFlock()
                        .padding(.top, 12)
                        .padding(.horizontal, -16)
                }
                ForEach(agents) { agent in
                    AgentCard(
                        agent: agent,
                        approval: approval?.pending.paneID == agent.paneID ? approval : nil,
                        answer: answer
                    )
                }
            }
            .padding(.horizontal, 16)
            .padding(.vertical, 12)
        }
        .accessibilityIdentifier("flock.list")
    }
}

/// One agent, in full.
struct AgentCard: View {
    let agent: Agent
    let approval: ApprovalFlow.Card?
    let answer: (Int) -> Void

    @State private var showWholeMessage = false

    private var tint: Color { Theme.color(for: agent.state) }
    private var isBlocked: Bool { agent.state == .blocked }

    var body: some View {
        VStack(alignment: .leading, spacing: 10) {
            header
            if let task = agent.currentTask?.trimmedOrNil {
                labelled("Working on", task, systemImage: "hammer")
            } else if let last = agent.lastTask?.trimmedOrNil {
                labelled("Last task", last, systemImage: "checkmark.circle")
            }

            if let approval {
                ApprovalCardView(card: approval, answer: answer)
            } else if let block = agent.lastBlock, let question = block.question?.trimmedOrNil {
                labelled(block.answered == true ? "Last question (answered)" : "Asks", question, systemImage: "hand.raised")
                    .foregroundStyle(block.answered == true ? .secondary : .primary)
            }

            if let result = agent.lastResult, let spoken = result.spoken?.trimmedOrNil {
                VStack(alignment: .leading, spacing: 4) {
                    labelled("Last result", spoken, systemImage: "flag.checkered")
                    if let detail = result.detail?.trimmedOrNil {
                        DisclosureGroup("Detail") {
                            Text(detail)
                                .font(.footnote)
                                .foregroundStyle(.secondary)
                                .frame(maxWidth: .infinity, alignment: .leading)
                                .textSelection(.enabled)
                        }
                        .font(.footnote)
                    }
                }
            }

            workerMessage
        }
        .padding(14)
        .frame(maxWidth: .infinity, alignment: .leading)
        .background(
            RoundedRectangle(cornerRadius: 16, style: .continuous)
                .fill(isBlocked ? tint.opacity(0.10) : Color(.secondarySystemBackground))
        )
        .overlay(
            RoundedRectangle(cornerRadius: 16, style: .continuous)
                .strokeBorder(isBlocked ? tint.opacity(0.5) : .clear, lineWidth: 1.25)
        )
        .accessibilityElement(children: .contain)
        .accessibilityIdentifier("flock.agent.\(agent.alias)")
    }

    private var header: some View {
        HStack(alignment: .firstTextBaseline, spacing: 8) {
            Circle().fill(tint).frame(width: 10, height: 10)
            Text(agent.alias)
                .font(.headline)
                .lineLimit(1)
            if let kind = agent.kind?.trimmedOrNil {
                Text(kind)
                    .font(.caption2.weight(.medium))
                    .foregroundStyle(.secondary)
                    .padding(.horizontal, 6)
                    .padding(.vertical, 2)
                    .background(Color(.tertiarySystemFill), in: Capsule())
            }
            Spacer(minLength: 4)
            // Re-evaluated twice a minute, so "4 min" keeps up on its own.
            TimelineView(.periodic(from: .now, by: 30)) { context in
                let since = agent.timeInState(at: context.date)
                Text(since.map { "\(Theme.word(for: agent.state)) · \($0)" } ?? Theme.word(for: agent.state))
                    .font(.caption.weight(.semibold))
                    .foregroundStyle(tint)
                    .accessibilityIdentifier("flock.agent.\(agent.alias).state")
            }
        }
    }

    /// What the worker last wrote for a human, from its own transcript
    /// (the daemon's work journal). Absent until the daemon has read one.
    @ViewBuilder
    private var workerMessage: some View {
        if let entry = agent.lastWorkerMessage, let message = entry.lastAssistant?.trimmedOrNil {
            VStack(alignment: .leading, spacing: 4) {
                HStack(spacing: 6) {
                    Image(systemName: "text.bubble")
                    Text(workerMessageTitle(entry))
                }
                .font(.caption.weight(.semibold))
                .foregroundStyle(.secondary)
                Text(message)
                    .font(.footnote)
                    .lineLimit(showWholeMessage ? nil : 4)
                    .fixedSize(horizontal: false, vertical: true)
                    .textSelection(.enabled)
                    .onTapGesture { withAnimation { showWholeMessage.toggle() } }
                if let footer = workerFooter(entry) {
                    Text(footer)
                        .font(.caption2)
                        .foregroundStyle(.tertiary)
                }
            }
            .padding(10)
            .frame(maxWidth: .infinity, alignment: .leading)
            .background(Color(.tertiarySystemBackground), in: RoundedRectangle(cornerRadius: 10, style: .continuous))
            .accessibilityElement(children: .combine)
            .accessibilityIdentifier("flock.agent.\(agent.alias).message")
        }
    }

    private func workerMessageTitle(_ entry: Agent.JournalEntry) -> String {
        var title = "\(agent.alias) wrote"
        if let event = entry.event?.trimmedOrNil { title += " when \(event)" }
        if let at = entry.at { title += " · \(at.formatted(date: .omitted, time: .shortened))" }
        return title
    }

    private func workerFooter(_ entry: Agent.JournalEntry) -> String? {
        var parts: [String] = []
        if let files = entry.filesTouched, !files.isEmpty { parts.append("edited " + files.joined(separator: ", ")) }
        if let tools = entry.tools, !tools.isEmpty {
            parts.append(tools.sorted { $0.value > $1.value }.map { "\($0.key) ×\($0.value)" }.joined(separator: " "))
        }
        return parts.isEmpty ? nil : parts.joined(separator: " · ")
    }

    private func labelled(_ title: String, _ text: String, systemImage: String) -> some View {
        VStack(alignment: .leading, spacing: 2) {
            Label(title, systemImage: systemImage)
                .font(.caption.weight(.semibold))
                .foregroundStyle(.secondary)
            Text(text)
                .font(.callout)
                .fixedSize(horizontal: false, vertical: true)
                .textSelection(.enabled)
        }
        .accessibilityElement(children: .combine)
    }
}

extension String {
    /// Trimmed, or nil when nothing is left.
    var trimmedOrNil: String? {
        let t = trimmingCharacters(in: .whitespacesAndNewlines)
        return t.isEmpty ? nil : t
    }
}
