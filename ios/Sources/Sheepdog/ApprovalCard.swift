import SheepdogKit
import SwiftUI

/// A worker's approval prompt, pinned above the controls where the thumb
/// is: who is asking, what, the command, and one button per option.
struct ApprovalCardView: View {
    let card: ApprovalFlow.Card
    /// On an agent's Agents card, which already draws the frame: no second
    /// border, background or shadow (review B11).
    var embedded = false
    let answer: (Int) -> Void

    @State private var commandExpanded = false

    private var pending: PendingAction { card.pending }
    private var agent: String { pending.agent?.isEmpty == false ? pending.agent! : "An agent" }

    var body: some View {
        VStack(alignment: .leading, spacing: 10) {
            header
            Text(pending.displaySummary)
                .font(.callout)
                .fixedSize(horizontal: false, vertical: true)
                .accessibilityIdentifier("approval.summary")
            if let command = pending.command?.trimmingCharacters(in: .whitespacesAndNewlines), !command.isEmpty {
                // Up to three lines, never cut in the middle (where the
                // dangerous part of a command usually is); a tap shows all.
                Button {
                    withAnimation(.easeInOut(duration: 0.15)) { commandExpanded.toggle() }
                } label: {
                    Text(command)
                        .font(.footnote.monospaced())
                        .foregroundStyle(Color(.label))
                        .multilineTextAlignment(.leading)
                        .lineLimit(commandExpanded ? nil : 3)
                        .truncationMode(.tail)
                        .fixedSize(horizontal: false, vertical: true)
                        .padding(.horizontal, 8)
                        .padding(.vertical, 6)
                        .frame(maxWidth: .infinity, minHeight: 32, alignment: .leading)
                        .background(Color(.tertiarySystemFill), in: RoundedRectangle(cornerRadius: 7, style: .continuous))
                }
                .buttonStyle(.plain)
                .accessibilityLabel("Command: \(command)")
                .accessibilityHint(commandExpanded ? "" : "Shows the whole command")
                .accessibilityIdentifier("approval.command")
            }
            phaseView
        }
        .padding(embedded ? 0 : 14)
        .background {
            if !embedded {
                RoundedRectangle(cornerRadius: 18, style: .continuous)
                    .fill(Color(.secondarySystemGroupedBackground))
                    .shadow(color: .black.opacity(0.10), radius: 10, y: 2)
            }
        }
        .overlay {
            if !embedded {
                RoundedRectangle(cornerRadius: 18, style: .continuous)
                    .strokeBorder(borderTint.opacity(0.55), lineWidth: 1.25)
            }
        }
        .accessibilityElement(children: .contain)
        .accessibilityIdentifier("approval.card")
    }

    private var borderTint: Color {
        switch card.phase {
        case .answered: return Theme.done
        case .gone: return Color(.systemGray)
        case .open, .sending: return Theme.blocked
        }
    }

    private var header: some View {
        HStack(spacing: 8) {
            Image(systemName: "hand.raised.fill")
                .foregroundStyle(Theme.blocked)
            Text(card.isSettled ? "\(agent) asked" : "\(agent) needs your answer")
                .font(.subheadline.weight(.semibold))
            Spacer(minLength: 0)
        }
        .accessibilityElement(children: .combine)
        .accessibilityAddTraits(.isHeader)
    }

    @ViewBuilder
    private var phaseView: some View {
        switch card.phase {
        case let .open(error):
            buttons(sending: nil)
            if let error {
                Label(error, systemImage: "exclamationmark.triangle.fill")
                    .font(.caption)
                    .foregroundStyle(Color(.systemRed))
                    .fixedSize(horizontal: false, vertical: true)
            }
        case let .sending(option):
            buttons(sending: option)
        case let .answered(outcome, spoken):
            VStack(alignment: .leading, spacing: 4) {
                Label(outcome, systemImage: "checkmark.circle.fill")
                    .font(.subheadline.weight(.semibold))
                    .foregroundStyle(Theme.done)
                    .accessibilityIdentifier("approval.answered")
                if let spoken {
                    Text(spoken)
                        .font(.footnote)
                        .foregroundStyle(.secondary)
                }
            }
            .transition(.opacity)
        case let .gone(message):
            Label(message, systemImage: "exclamationmark.circle")
                .font(.subheadline)
                .foregroundStyle(.secondary)
                .fixedSize(horizontal: false, vertical: true)
                .accessibilityIdentifier("approval.gone")
        }
    }

    private func buttons(sending: PendingOption?) -> some View {
        VStack(spacing: 8) {
            ForEach(pending.options ?? [], id: \.index) { option in
                OptionButton(
                    option: option,
                    sending: sending?.index == option.index,
                    enabled: sending == nil
                ) {
                    answer(option.index)
                }
            }
        }
    }
}

/// One option: the default prominent, a "No…" destructive, the rest plain.
private struct OptionButton: View {
    let option: PendingOption
    let sending: Bool
    let enabled: Bool
    let action: () -> Void

    var body: some View {
        let label = HStack(spacing: 8) {
            if sending { ProgressView().controlSize(.small) }
            // Never truncated: "Yes, and don't ask again for …" is exactly
            // the option that must be read in full.
            Text(option.label)
                .multilineTextAlignment(.center)
                .fixedSize(horizontal: false, vertical: true)
        }
        .font(.subheadline.weight(option.isDefault ? .semibold : .regular))
        .frame(maxWidth: .infinity, minHeight: 36)

        Group {
            if option.isDefault && !option.isNegative {
                // Ink on the orange fill (the system's white is 2:1).
                Button(action: action) { label.foregroundStyle(Color("OnAccent")) }
                    .buttonStyle(.borderedProminent)
            } else if option.isNegative {
                Button(role: .destructive, action: action) { label }
                    .buttonStyle(.bordered)
            } else {
                Button(action: action) { label }
                    .buttonStyle(.bordered)
                    .tint(Theme.primaryText)
            }
        }
        .controlSize(.large)
        .disabled(!enabled)
        .accessibilityLabel(option.accessibilityLabel)
        .accessibilityHint(sending ? "Sending" : "")
        .accessibilityIdentifier("approval.option.\(option.index)")
    }
}
