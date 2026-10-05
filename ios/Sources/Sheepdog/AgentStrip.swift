import SheepdogKit
import SwiftUI

/// The flock, one chip per agent: alias, a state dot, and the pending
/// question or the last result underneath. Tapping one opens its agent
/// window.
struct AgentStrip: View {
    let agents: [Agent]
    /// The open question, if any: a blocked agent's chip names what it needs.
    let pending: PendingAction?

    var body: some View {
        if agents.isEmpty {
            EmptyFlock()
        } else {
            ScrollView(.horizontal, showsIndicators: false) {
                HStack(spacing: 10) {
                    ForEach(agents) { agent in
                        // The same agent window a Flock card opens.
                        NavigationLink(value: AgentWindowRoute(paneID: agent.paneID)) {
                            AgentChip(agent: agent, subtitle: agent.chipSubtitle(pending: pending))
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
        .accessibilityHint("Opens this agent's window")
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
