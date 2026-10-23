import SheepdogKit
import SwiftUI

/// The agent strip on the Conversation (canvas "ConversationOne"): one
/// single-line chip per running agent, a state dot and its alias, blocked
/// agents first in the soft accent. Tapping one opens its agent window.
/// What a blocked agent needs is the chip's VoiceOver value; the question
/// itself floats above the capsule.
struct AgentStrip: View {
    let agents: [Agent]
    /// The open question, if any: a blocked agent's chip names what it needs.
    let pending: PendingAction?
    /// How many computers are paired, and the computer a chip's agent runs
    /// on (by pane id) when it is not the current one. With one computer
    /// no chip ever names one.
    var computers = 1
    var computerOf: (Agent) -> String? = { _ in nil }

    var body: some View {
        if agents.isEmpty {
            EmptyFlock()
        } else {
            ScrollView(.horizontal, showsIndicators: false) {
                HStack(spacing: 6) {
                    ForEach(agents) { agent in
                        // The same agent window an Agents card opens.
                        NavigationLink(value: AgentWindowRoute(paneID: agent.paneID)) {
                            AgentChip(
                                agent: agent,
                                subtitle: agent.chipSubtitle(pending: pending),
                                computer: AgentStripLabel.computer(
                                    state: agent.state,
                                    agentComputer: computerOf(agent),
                                    pairedComputers: computers
                                )
                            )
                        }
                        .buttonStyle(ChipButtonStyle())
                        .zoomSource(agent.paneID)
                        .accessibilityIdentifier("agent.chip.\(agent.alias)")
                    }
                }
                .padding(.horizontal, 16)
            }
            .scrollClipDisabled()
        }
    }
}

/// One chip: 32 pt tall, drawn inside a 44 pt tap target. The text never
/// truncates: the strip scrolls sideways instead, and at the accessibility
/// sizes the chip grows taller with its text.
struct AgentChip: View {
    let agent: Agent
    let subtitle: String?
    /// Another computer's name (several computers only).
    var computer: String?

    private var isBlocked: Bool { agent.state == .blocked }

    var body: some View {
        HStack(spacing: 6) {
            Circle()
                .fill(Theme.color(for: agent.state))
                .frame(width: 7, height: 7)
                .doneFlash(agent.state)
                .animation(Theme.ease, value: agent.state)
                .accessibilityHidden(true)
            Text(agent.alias)
                .font(.subheadline.weight(.semibold))
                .foregroundStyle(Theme.ink)
            if let computer {
                Text(computer)
                    .font(.caption)
                    .foregroundStyle(Theme.primaryText)
            }
        }
        .lineLimit(1)
        .fixedSize()
        .padding(.horizontal, 11)
        .frame(minHeight: 32)
        .background(Capsule().fill(isBlocked ? Theme.accentSoft : Theme.card))
        .overlay(Capsule().strokeBorder(isBlocked ? Theme.blocked : Theme.line, lineWidth: 1))
        // The 44 pt target around the 32 pt pill.
        .padding(.vertical, 6)
        .contentShape(Rectangle())
        .accessibilityElement(children: .ignore)
        .accessibilityLabel([agent.alias, computer, Theme.word(for: agent.state)].compactMap { $0 }.joined(separator: ", "))
        .accessibilityValue(subtitle ?? "")
        .accessibilityHint("Opens this agent's window")
        .accessibilityAddTraits(.isButton)
    }
}

/// Dims a little under the finger.
private struct ChipButtonStyle: ButtonStyle {
    func makeBody(configuration: Configuration) -> some View {
        configuration.label
            .opacity(configuration.isPressed ? 0.6 : 1)
    }
}

struct EmptyFlock: View {
    var body: some View {
        HStack(spacing: 10) {
            Image(systemName: "pawprint")
                .foregroundStyle(Theme.muted)
                .accessibilityHidden(true)
            Text("No agents yet — start one in herdr.")
                .font(.footnote)
                .foregroundStyle(Theme.muted)
                .fixedSize(horizontal: false, vertical: true)
            Spacer(minLength: 0)
        }
        .padding(.horizontal, 14)
        .frame(minHeight: 44)
        .background(Capsule().fill(Theme.card))
        .overlay(Capsule().strokeBorder(Theme.line, lineWidth: 1))
        .padding(.horizontal, 16)
        .padding(.vertical, 4)
    }
}
