import SheepdogKit
import SwiftUI

/// The Flock pane: one card per agent with everything the daemon knows
/// about it — state and how long, the task, the question it is waiting on
/// (answerable right here), the last result as the shepherd heard it, and
/// what the worker itself last wrote. Mirrors `/api/state`'s agents (the
/// same list as `/api/agents`), refreshed over `/events`.
struct FlockView: View {
    /// Whether the agents that are not running (`unknown`, `exited`) are
    /// offered at all, folded into "Not running (N)" at the bottom. Set to
    /// false to hide them entirely.
    static let showsNotRunning = true

    let flock: Flock
    /// The approval card, when a worker is asking: shown inline on that
    /// agent's card instead of pinned above the controls.
    let approval: ApprovalFlow.Card?
    /// A notification tap: scroll to this agent's card, then forget it.
    @Binding var focusedPaneID: String?
    let answer: (Int) -> Void

    /// Closed by default: the running agents are what matters.
    @State private var showNotRunning = false

    var body: some View {
        ScrollViewReader { proxy in
            ScrollView {
                LazyVStack(spacing: 12) {
                    if flock.isEmpty {
                        EmptyFlock()
                            .padding(.top, 12)
                            .padding(.horizontal, -16)
                    } else if flock.running.isEmpty {
                        Text("No agent is running right now.")
                            .font(.footnote)
                            .foregroundStyle(.secondary)
                            .frame(maxWidth: .infinity, alignment: .leading)
                            .padding(.vertical, 8)
                    }
                    ForEach(flock.running) { agent in
                        card(agent)
                    }
                    if Self.showsNotRunning, !flock.notRunning.isEmpty {
                        notRunningRow
                        if showNotRunning {
                            ForEach(flock.notRunning) { agent in
                                card(agent)
                            }
                        }
                    }
                }
                .padding(.horizontal, 16)
                .padding(.vertical, 12)
            }
            .onAppear { scrollToFocus(proxy) }
            .onChange(of: focusedPaneID) { _, _ in scrollToFocus(proxy) }
        }
        .accessibilityIdentifier("flock.list")
    }

    private func card(_ agent: Agent) -> some View {
        AgentCard(
            agent: agent,
            approval: approval?.pending.paneID == agent.paneID ? approval : nil,
            answer: answer
        )
        .id(agent.paneID)
    }

    private var notRunningRow: some View {
        Button {
            withAnimation(.easeInOut(duration: 0.2)) { showNotRunning.toggle() }
        } label: {
            HStack {
                Text("Not running (\(flock.notRunning.count))")
                    .font(.subheadline.weight(.medium))
                Spacer()
                Image(systemName: "chevron.right")
                    .font(.footnote.weight(.semibold))
                    .rotationEffect(.degrees(showNotRunning ? 90 : 0))
            }
            .foregroundStyle(.secondary)
            .frame(minHeight: 44)
            .contentShape(Rectangle())
        }
        .buttonStyle(.plain)
        .accessibilityValue(showNotRunning ? "Shown" : "Hidden")
        .accessibilityHint("Agents whose pane closed or that herdr no longer reports")
        .accessibilityIdentifier("flock.notRunning")
    }

    private func scrollToFocus(_ proxy: ScrollViewProxy) {
        guard let id = focusedPaneID else { return }
        if flock.notRunning.contains(where: { $0.paneID == id }) { showNotRunning = true }
        DispatchQueue.main.async {
            withAnimation { proxy.scrollTo(id, anchor: .top) }
            focusedPaneID = nil
        }
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
                ApprovalCardView(card: approval, embedded: true, answer: answer)
            } else if let block = agent.lastBlock, let question = block.question?.trimmedOrNil {
                labelled(block.answered == true ? "Last question (answered)" : "Asks", question, systemImage: "hand.raised")
                    .foregroundStyle(block.answered == true ? .secondary : .primary)
            }

            if let result = agent.lastResult, let spoken = result.spoken?.trimmedOrNil {
                VStack(alignment: .leading, spacing: 4) {
                    labelled("Last result", spoken, systemImage: "flag.checkered")
                    if let detail = result.detail?.trimmedOrNil {
                        DisclosureGroup("Detail") {
                            MarkdownText(detail)
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

    @Environment(\.dynamicTypeSize) private var dynamicTypeSize

    @ViewBuilder
    private var header: some View {
        // At the accessibility sizes the name, kind and state no longer fit
        // on one line: stacked, nothing is cut to "au…".
        if dynamicTypeSize.isAccessibilitySize {
            VStack(alignment: .leading, spacing: 4) {
                HStack(spacing: 8) { dot; name }
                stateText
                kindTag
            }
        } else {
            HStack(alignment: .firstTextBaseline, spacing: 8) {
                dot
                name
                kindTag
                Spacer(minLength: 4)
                stateText
            }
        }
    }

    private var dot: some View { Circle().fill(tint).frame(width: 10, height: 10) }

    private var name: some View {
        Text(agent.alias)
            .font(.headline)
            .lineLimit(1)
    }

    @ViewBuilder
    private var kindTag: some View {
        if let kind = agent.kind?.trimmedOrNil {
            Text(kind)
                .font(.caption2.weight(.medium))
                .foregroundStyle(.secondary)
                .padding(.horizontal, 6)
                .padding(.vertical, 2)
                .background(Color(.tertiarySystemFill), in: Capsule())
        }
    }

    private var stateText: some View {
        // Re-evaluated twice a minute, so "4 min" keeps up on its own.
        TimelineView(.periodic(from: .now, by: 30)) { context in
            let since = agent.timeInState(at: context.date)
            Text(since.map { "\(Theme.word(for: agent.state)) · \($0)" } ?? Theme.word(for: agent.state))
                .font(.caption.weight(.semibold))
                .foregroundStyle(Theme.textColor(for: agent.state))
                .accessibilityIdentifier("flock.agent.\(agent.alias).state")
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
                MarkdownText(message)
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
            MarkdownText(text)
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
