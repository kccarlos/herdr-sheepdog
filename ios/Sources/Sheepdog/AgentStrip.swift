import SheepdogKit
import SwiftUI

/// The flock, one chip per agent: alias, a state dot, and the pending
/// question or the last result underneath.
struct AgentStrip: View {
    let agents: [Agent]
    let question: (Agent) -> String?
    let onSelect: (Agent) -> Void

    var body: some View {
        if agents.isEmpty {
            EmptyFlock()
        } else {
            ScrollView(.horizontal, showsIndicators: false) {
                HStack(spacing: 10) {
                    ForEach(agents) { agent in
                        Button {
                            onSelect(agent)
                        } label: {
                            AgentChip(agent: agent, subtitle: agent.subtitle(pendingQuestion: question(agent)))
                        }
                        .buttonStyle(.plain)
                        .accessibilityIdentifier("agent.chip.\(agent.alias)")
                    }
                }
                .padding(.horizontal, 16)
                .padding(.vertical, 2)
            }
            .scrollClipDisabled()
        }
    }
}

struct AgentChip: View {
    let agent: Agent
    let subtitle: String?

    private var isBlocked: Bool { agent.state == .blocked }
    private var tint: Color { Theme.color(for: agent.state) }

    var body: some View {
        VStack(alignment: .leading, spacing: 4) {
            HStack(spacing: 6) {
                Circle()
                    .fill(tint)
                    .frame(width: 9, height: 9)
                Text(agent.alias)
                    .font(.subheadline.weight(.semibold))
                    .lineLimit(1)
                if isBlocked {
                    Image(systemName: "hand.raised.fill")
                        .font(.caption2)
                        .foregroundStyle(tint)
                }
            }
            Text(subtitle ?? Theme.word(for: agent.state))
                .font(.caption)
                .foregroundStyle(.secondary)
                .lineLimit(1)
                .truncationMode(.tail)
        }
        .padding(.horizontal, 12)
        .padding(.vertical, 9)
        .frame(minWidth: 132, maxWidth: 210, alignment: .leading)
        .background(
            RoundedRectangle(cornerRadius: 14, style: .continuous)
                .fill(isBlocked ? tint.opacity(0.14) : Color(.secondarySystemBackground))
        )
        .overlay(
            RoundedRectangle(cornerRadius: 14, style: .continuous)
                .strokeBorder(isBlocked ? tint.opacity(0.55) : Color.clear, lineWidth: 1.5)
        )
        .accessibilityElement(children: .ignore)
        .accessibilityLabel("\(agent.alias), \(Theme.word(for: agent.state))")
        .accessibilityValue(subtitle ?? "")
        .accessibilityHint("Shows what this agent is doing")
        .accessibilityAddTraits(.isButton)
    }
}

struct EmptyFlock: View {
    var body: some View {
        HStack(spacing: 10) {
            Image(systemName: "pawprint")
                .foregroundStyle(.secondary)
            Text("No agents in the flock yet — start one in herdr.")
                .font(.footnote)
                .foregroundStyle(.secondary)
            Spacer(minLength: 0)
        }
        .padding(.horizontal, 14)
        .padding(.vertical, 12)
        .background(
            RoundedRectangle(cornerRadius: 14, style: .continuous)
                .fill(Color(.secondarySystemBackground))
        )
        .padding(.horizontal, 16)
    }
}

/// The sheet behind a chip: everything the daemon knows about one agent.
struct AgentSheet: View {
    let agent: Agent
    let question: String?
    @Environment(\.dismiss) private var dismiss

    var body: some View {
        NavigationStack {
            List {
                Section {
                    LabeledContent("State") {
                        HStack(spacing: 6) {
                            Circle().fill(Theme.color(for: agent.state)).frame(width: 9, height: 9)
                            Text(Theme.word(for: agent.state))
                        }
                    }
                    if let since = agent.stateSince {
                        LabeledContent("Since", value: since.formatted(date: .omitted, time: .shortened))
                    }
                    if let kind = agent.kind, !kind.isEmpty {
                        LabeledContent("Kind", value: kind)
                    }
                    if let workspace = agent.workspaceLabel, !workspace.isEmpty {
                        LabeledContent("Workspace", value: workspace)
                    }
                }

                if let question = question ?? agent.lastBlock?.question, !question.isEmpty {
                    Section("Waiting on you") {
                        Text(question)
                        if let options = agent.lastBlock?.options, !options.isEmpty {
                            ForEach(Array(options.enumerated()), id: \.offset) { index, option in
                                Label(option.label, systemImage: "\(index + 1).circle")
                                    .font(.callout)
                            }
                        }
                    }
                }

                if let task = agent.currentTask, !task.isEmpty {
                    Section("Current task") { Text(task) }
                }
                if let result = agent.lastResult?.summary, !result.isEmpty {
                    Section("Last result") { Text(result) }
                } else if let last = agent.lastTask, !last.isEmpty {
                    Section("Last task") { Text(last) }
                }
            }
            .navigationTitle(agent.alias)
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .confirmationAction) {
                    Button("Done") { dismiss() }
                }
            }
        }
        .presentationDetents([.medium, .large])
        .presentationDragIndicator(.visible)
    }
}
