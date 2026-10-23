import SheepdogKit
import SwiftUI

/// A worker's question: who is asking, the summary, the command (three
/// lines, tap for all) and the agent's own option labels, verbatim. It
/// never folds. Floating above the capsule on the Conversation, or inside
/// the agent's card on the Agents page (`embedded`).
///
/// One accent per screen: only the screen's first question has the filled
/// primary (`primary`); the others are outlined in amber.
struct ApprovalCardView: View {
    let card: ApprovalFlow.Card
    /// Inside an Agents card, which draws the frame and the name.
    var embedded = false
    var primary = true
    /// Identifier prefix: `approval` for the voice's question, else
    /// `question.<alias>`.
    var idPrefix = "approval"
    /// The header's right side: "4 min · 1 of 2".
    var meta: String?
    let answer: (Int) -> Void

    @State private var commandExpanded = false

    private var pending: PendingAction { card.pending }
    private var agent: String { pending.agent?.isEmpty == false ? pending.agent! : "An agent" }

    var body: some View {
        VStack(alignment: .leading, spacing: 10) {
            if !embedded { header }
            Text(pending.displaySummary)
                .font(.body.weight(.semibold))
                .foregroundStyle(Theme.ink)
                .fixedSize(horizontal: false, vertical: true)
                .accessibilityIdentifier("\(idPrefix).summary")
            if let command = pending.command?.trimmingCharacters(in: .whitespacesAndNewlines), !command.isEmpty {
                // Up to three lines, never cut in the middle (where the
                // dangerous part of a command usually is); a tap shows all.
                Button {
                    withAnimation(Theme.ease) { commandExpanded.toggle() }
                } label: {
                    Text(command)
                        .font(.subheadline.monospaced())
                        .foregroundStyle(Theme.ink)
                        .multilineTextAlignment(.leading)
                        .lineLimit(commandExpanded ? nil : 3)
                        .truncationMode(.tail)
                        .fixedSize(horizontal: false, vertical: true)
                        .padding(.horizontal, 10)
                        .padding(.vertical, 7)
                        .frame(maxWidth: .infinity, minHeight: 32, alignment: .leading)
                        .background(Theme.fill, in: RoundedRectangle(cornerRadius: Theme.Radius.small, style: .continuous))
                }
                .buttonStyle(.plain)
                .accessibilityLabel("Command: \(command)")
                .accessibilityHint(commandExpanded ? "" : "Shows the whole command")
                .accessibilityIdentifier("\(idPrefix).command")
            }
            phaseView
        }
        .padding(.horizontal, embedded ? 12 : 14)
        .padding(.top, 12)
        .padding(.bottom, embedded ? 12 : 14)
        .background(
            RoundedRectangle(cornerRadius: embedded ? 12 : Theme.Radius.large, style: .continuous)
                .fill(embedded ? Theme.accentSoft : Theme.card)
                .shadow(color: .black.opacity(embedded ? 0 : 0.12), radius: 12, y: 8)
        )
        .overlay {
            if !embedded {
                RoundedRectangle(cornerRadius: Theme.Radius.large, style: .continuous)
                    .strokeBorder(borderTint, lineWidth: primary ? 1.5 : 1)
            }
        }
        .accessibilityElement(children: .contain)
        .accessibilityIdentifier("\(idPrefix).card")
    }

    private var borderTint: Color {
        switch card.phase {
        case .answered: return Theme.done
        case .gone: return Theme.line
        case .open, .sending: return primary ? Theme.blocked : Theme.line
        }
    }

    /// The canvas header: a state dot, who asks, and how long ago (and
    /// "1 of 2" with several). It wraps at the accessibility sizes.
    private var header: some View {
        ViewThatFits(in: .horizontal) {
            HStack(spacing: 7) { headerName; Spacer(minLength: 8); headerMeta }
            VStack(alignment: .leading, spacing: 2) { headerName; headerMeta }
        }
        .font(.footnote)
        .accessibilityElement(children: .ignore)
        .accessibilityLabel(card.isSettled ? "\(agent) asked" : "\(agent) needs your answer" + (meta.map { ", \($0)" } ?? ""))
        .accessibilityAddTraits(.isHeader)
    }

    private var headerName: some View {
        HStack(spacing: 7) {
            Circle()
                .fill(card.isSettled ? Theme.done : Theme.blocked)
                .frame(width: 7, height: 7)
            Text(agent)
                .fontWeight(.semibold)
                .foregroundStyle(Theme.ink)
                .fixedSize(horizontal: false, vertical: true)
        }
    }

    @ViewBuilder private var headerMeta: some View {
        if let meta {
            Text(meta)
                .foregroundStyle(Theme.muted)
                .monospacedDigit()
                .contentTransition(.numericText())
                .animation(Theme.ease, value: meta)
        }
    }

    @ViewBuilder
    private var phaseView: some View {
        switch card.phase {
        case let .open(error):
            buttons(sending: nil)
            if let error {
                Label(error, systemImage: "exclamationmark.triangle.fill")
                    .font(.caption)
                    .foregroundStyle(Theme.danger)
                    .fixedSize(horizontal: false, vertical: true)
            }
        case let .sending(option):
            buttons(sending: option)
        case let .answered(outcome, spoken):
            VStack(alignment: .leading, spacing: 4) {
                Label(outcome, systemImage: "checkmark.circle.fill")
                    .font(.subheadline.weight(.semibold))
                    .foregroundStyle(Theme.done)
                    .accessibilityIdentifier("\(idPrefix).answered")
                if let spoken {
                    Text(spoken)
                        .font(.footnote)
                        .foregroundStyle(Theme.muted)
                }
            }
            .transition(.opacity)
        case let .gone(message):
            Label(message, systemImage: "exclamationmark.circle")
                .font(.subheadline)
                .foregroundStyle(Theme.muted)
                .fixedSize(horizontal: false, vertical: true)
                .accessibilityIdentifier("\(idPrefix).gone")
        }
    }

    private func buttons(sending: PendingOption?) -> some View {
        let options = pending.options ?? []
        // The option that gets the accent: the prompt's default, else the
        // first.
        let lead = options.first(where: \.isDefault)?.index ?? options.first?.index
        return OptionFlow(spacing: 8) {
            ForEach(options, id: \.index) { option in
                OptionButton(
                    option: option,
                    style: OptionButton.Style(lead: option.index == lead, primary: primary, negative: option.isNegative),
                    sending: sending?.index == option.index,
                    enabled: sending == nil,
                    fullRow: option.label.count > 18,
                    identifier: "\(idPrefix).option.\(option.index)"
                ) {
                    answer(option.index)
                }
                .layoutValue(key: FullRow.self, value: option.label.count > 18)
            }
        }
    }
}

/// One option, a pill at least 44 pt tall: the lead option filled with the
/// accent (ink text) on the screen's first question, else outlined in
/// amber; a "No…" option in red; the rest plain. The label is never cut.
private struct OptionButton: View {
    struct Style {
        var lead: Bool
        var primary: Bool
        var negative: Bool
    }

    let option: PendingOption
    let style: Style
    let sending: Bool
    let enabled: Bool
    var fullRow = false
    let identifier: String
    let action: () -> Void

    private var filled: Bool { style.lead && style.primary && !style.negative }

    var body: some View {
        Button(action: action) {
            HStack(spacing: 8) {
                if sending { ProgressView().controlSize(.small).tint(filled ? Theme.onAccent : Theme.ink) }
                Text(option.label)
                    .multilineTextAlignment(.leading)
                    .fixedSize(horizontal: false, vertical: true)
            }
            .font(.body.weight(style.lead ? .semibold : .regular))
            .foregroundStyle(filled ? Theme.onAccent : (style.negative ? Theme.danger : Theme.ink))
            .padding(.horizontal, 16)
            .padding(.vertical, 10)
            .frame(maxWidth: fullRow ? .infinity : nil, minHeight: 44, alignment: .leading)
            .background(Capsule().fill(filled ? Color.accentColor : Theme.card))
            .overlay {
                if !filled {
                    Capsule().strokeBorder(style.lead ? Theme.blocked : Theme.line, lineWidth: 1)
                }
            }
            .contentShape(Capsule())
        }
        .buttonStyle(.plain)
        .opacity(enabled || sending ? 1 : 0.5)
        .disabled(!enabled)
        .accessibilityLabel(option.accessibilityLabel)
        .accessibilityHint(sending ? "Sending" : "")
        .accessibilityIdentifier(identifier)
    }
}

/// Marks a view that takes a whole row of an `OptionFlow`.
struct FullRow: LayoutValueKey {
    static let defaultValue = false
}

/// Options side by side while they fit, wrapping to the next row; a long
/// label (`FullRow`) takes a row of its own and stretches across it.
struct OptionFlow: Layout {
    var spacing: CGFloat = 8

    func sizeThatFits(proposal: ProposedViewSize, subviews: Subviews, cache: inout ()) -> CGSize {
        let width = proposal.width ?? 320
        let rows = arrange(width: width, subviews: subviews)
        let height = rows.reduce(0) { $0 + $1.height } + spacing * CGFloat(max(0, rows.count - 1))
        return CGSize(width: width, height: height)
    }

    func placeSubviews(in bounds: CGRect, proposal: ProposedViewSize, subviews: Subviews, cache: inout ()) {
        var y = bounds.minY
        for row in arrange(width: bounds.width, subviews: subviews) {
            var x = bounds.minX
            for item in row.items {
                subviews[item.index].place(at: CGPoint(x: x, y: y), proposal: ProposedViewSize(width: item.width, height: row.height))
                x += item.width + spacing
            }
            y += row.height + spacing
        }
    }

    private struct Row {
        var items: [(index: Int, width: CGFloat)] = []
        var height: CGFloat = 0
        var used: CGFloat = 0
    }

    private func arrange(width: CGFloat, subviews: Subviews) -> [Row] {
        var rows: [Row] = []
        var row = Row()
        for (index, subview) in subviews.enumerated() {
            if subview[FullRow.self] {
                if !row.items.isEmpty { rows.append(row); row = Row() }
                let h = subview.sizeThatFits(ProposedViewSize(width: width, height: nil)).height
                rows.append(Row(items: [(index, width)], height: h, used: width))
                continue
            }
            let ideal = subview.sizeThatFits(.unspecified)
            let w = min(ideal.width, width)
            if !row.items.isEmpty, row.used + spacing + w > width {
                rows.append(row)
                row = Row()
            }
            let h = subview.sizeThatFits(ProposedViewSize(width: w, height: nil)).height
            row.items.append((index, w))
            row.used += (row.items.count > 1 ? spacing : 0) + w
            row.height = max(row.height, h)
        }
        if !row.items.isEmpty { rows.append(row) }
        return rows
    }
}
