import SheepdogKit
import SwiftUI

/// A worker's approval prompt, pinned above the controls where the thumb
/// is: who is asking, what, the command, and one button per option.
struct ApprovalCardView: View {
    let card: ApprovalFlow.Card
    let answer: (Int) -> Void

    private var pending: PendingAction { card.pending }
    private var agent: String { pending.agent?.isEmpty == false ? pending.agent! : "An agent" }

    var body: some View {
        VStack(alignment: .leading, spacing: 10) {
            header
            Text(pending.displaySummary)
                .font(.callout)
                .lineLimit(4)
                .fixedSize(horizontal: false, vertical: true)
                .accessibilityIdentifier("approval.summary")
            if let command = pending.command?.trimmingCharacters(in: .whitespacesAndNewlines), !command.isEmpty {
                Text(command)
                    .font(.footnote.monospaced())
                    .lineLimit(1)
                    .truncationMode(.middle)
                    .padding(.horizontal, 8)
                    .padding(.vertical, 5)
                    .frame(maxWidth: .infinity, alignment: .leading)
                    .background(Color(.tertiarySystemFill), in: RoundedRectangle(cornerRadius: 7, style: .continuous))
                    .accessibilityLabel("Command: \(command)")
            }
            phaseView
        }
        .padding(14)
        .background(
            RoundedRectangle(cornerRadius: 18, style: .continuous)
                .fill(Color(.secondarySystemGroupedBackground))
                .shadow(color: .black.opacity(0.10), radius: 10, y: 2)
        )
        .overlay(
            RoundedRectangle(cornerRadius: 18, style: .continuous)
                .strokeBorder(borderTint.opacity(0.55), lineWidth: 1.25)
        )
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
            Text(option.label)
                .lineLimit(2)
                .multilineTextAlignment(.center)
        }
        .font(.subheadline.weight(option.isDefault ? .semibold : .regular))
        .frame(maxWidth: .infinity, minHeight: 36)

        Group {
            if option.isDefault && !option.isNegative {
                Button(action: action) { label }
                    .buttonStyle(.borderedProminent)
            } else if option.isNegative {
                Button(role: .destructive, action: action) { label }
                    .buttonStyle(.bordered)
            } else {
                Button(action: action) { label }
                    .buttonStyle(.bordered)
            }
        }
        .controlSize(.large)
        .disabled(!enabled)
        .accessibilityLabel(option.accessibilityLabel)
        .accessibilityHint(sending ? "Sending" : "")
        .accessibilityIdentifier("approval.option.\(option.index)")
    }
}
