import SheepdogKit
import SwiftUI

/// A worker's question: who is asking, the summary, the command (three
/// lines, tap for all) and the agent's options as full-width rows (label,
/// then its description; the agent's recommendation as a tag). Floating
/// above the capsule on the Conversation, in the question sheet, or inside
/// the agent's card on the Agents page (`embedded`).
///
/// No option is filled: all look the same until one is tapped, so none
/// reads as already chosen. The screen's first question (`primary`) has
/// the amber border; the others a plain one.
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
    /// A question from another computer (several computers only): the
    /// card says where it is and that a tap answers there.
    var remote: RemoteQuestion?
    /// Talk to that computer.
    var talkTo: (() -> Void)?
    /// How many lines of the question show before a fade and "More"; nil
    /// for all of it (the sheet, the Agents page).
    var questionLines: Int?
    let answer: (Int) -> Void

    @State private var commandExpanded = false
    @State private var questionExpanded = false
    /// The question's height cut to `questionLines`, and in full: "More"
    /// shows only when they differ.
    @State private var questionShown: CGFloat = 0
    @State private var questionFull: CGFloat = 0
    @ScaledMetric(relativeTo: .body) private var fadeHeight: CGFloat = 22
    @Environment(\.accessibilityReduceMotion) private var reduceMotion

    private var pending: PendingAction { card.pending }
    private var agent: String { pending.agent?.isEmpty == false ? pending.agent! : "An agent" }
    private var questionCut: Bool { questionLines != nil && !questionExpanded && questionFull > questionShown + 1 }

    var body: some View {
        VStack(alignment: .leading, spacing: 10) {
            if let remote, !embedded {
                Label(remote.label, systemImage: "desktopcomputer")
                    .font(.caption.weight(.semibold))
                    .foregroundStyle(Theme.muted)
                    .fixedSize(horizontal: false, vertical: true)
                    .accessibilityLabel("On \(remote.label)")
                    .accessibilityIdentifier("computerLabel.question.\(remote.computerID)")
            }
            if !embedded { header }
            question
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
            if let remote, !card.isSettled {
                remoteFooter(remote)
            }
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

    /// The question from its first word: cut to `questionLines` with a fade
    /// and "More" when it is longer, never clipped from the top.
    @ViewBuilder private var question: some View {
        VStack(alignment: .leading, spacing: 0) {
            questionText
                .lineLimit(questionLines.map { questionExpanded ? nil : $0 } ?? nil)
                .onGeometryChange(for: CGFloat.self) { $0.size.height.rounded() } action: { questionShown = $0 }
                .background(alignment: .top) {
                    if questionLines != nil {
                        // The same text uncut, hidden: tells a cut question
                        // from one that just fits.
                        questionText
                            .hidden()
                            .accessibilityHidden(true)
                            .onGeometryChange(for: CGFloat.self) { $0.size.height.rounded() } action: { questionFull = $0 }
                    }
                }
                .mask {
                    VStack(spacing: 0) {
                        Rectangle()
                        if questionCut {
                            LinearGradient(colors: [.black, .clear], startPoint: .top, endPoint: .bottom)
                                .frame(height: fadeHeight)
                        }
                    }
                }
                .accessibilityIdentifier("\(idPrefix).summary")
            if questionCut || questionExpanded {
                Button {
                    withAnimation(Theme.ease(reduceMotion: reduceMotion)) { questionExpanded.toggle() }
                } label: {
                    Text(questionExpanded ? "Less" : "More")
                        .font(.subheadline.weight(.semibold))
                        .foregroundStyle(Theme.blockedText)
                        .frame(minWidth: 44, minHeight: 44, alignment: .leading)
                        .contentShape(Rectangle())
                }
                .buttonStyle(.plain)
                .accessibilityHint(questionExpanded ? "Shows less of the question" : "Shows the whole question")
                .accessibilityIdentifier("\(idPrefix).more")
            }
        }
    }

    private var questionText: some View {
        Text(pending.displaySummary)
            .font(.body.weight(.semibold))
            .foregroundStyle(Theme.ink)
            .multilineTextAlignment(.leading)
            .frame(maxWidth: .infinity, alignment: .leading)
            .fixedSize(horizontal: false, vertical: true)
    }

    /// "Answers by tap go to Home Mac." and Talk to Home Mac: the voice
    /// answers only on the current computer.
    private func remoteFooter(_ remote: RemoteQuestion) -> some View {
        ViewThatFits(in: .horizontal) {
            HStack(spacing: 8) {
                remoteNote(remote)
                Spacer(minLength: 4)
                talkButton(remote)
            }
            VStack(alignment: .leading, spacing: 4) {
                remoteNote(remote)
                talkButton(remote)
            }
        }
    }

    private func remoteNote(_ remote: RemoteQuestion) -> some View {
        Text("Answers by tap go to \(remote.computerName).")
            .font(.footnote)
            .foregroundStyle(Theme.muted)
            .fixedSize(horizontal: false, vertical: true)
            .accessibilityIdentifier("computerLabel.answersGo.\(remote.computerID)")
    }

    @ViewBuilder private func talkButton(_ remote: RemoteQuestion) -> some View {
        if let talkTo {
            Button(action: talkTo) {
                Text("Talk to \(remote.computerName)")
                    .font(.footnote.weight(.semibold))
                    .foregroundStyle(Theme.ink)
                    .fixedSize(horizontal: false, vertical: true)
                    .frame(minHeight: 44)
                    .contentShape(Rectangle())
            }
            .buttonStyle(.plain)
            .accessibilityIdentifier("computerLabel.talkTo.\(remote.computerID)")
        }
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

    /// The options as full-width rows, in the screen's order; none stands
    /// out until it is tapped.
    private func buttons(sending: PendingOption?) -> some View {
        VStack(spacing: 8) {
            ForEach(pending.options ?? [], id: \.index) { option in
                OptionRow(
                    option: option,
                    sending: sending?.index == option.index,
                    enabled: sending == nil,
                    identifier: "\(idPrefix).option.\(option.index)"
                ) {
                    answer(option.index)
                }
            }
        }
    }
}

/// One option: a rounded row as wide as the card, at least 44 pt tall and
/// as tall as its text. The label in semibold, a "Recommended" tag after
/// it when the agent recommends it, and the description below in smaller,
/// muted text; nothing is cut. A "No…" option's label is red.
private struct OptionRow: View {
    let option: PendingOption
    let sending: Bool
    let enabled: Bool
    let identifier: String
    let action: () -> Void

    var body: some View {
        Button(action: action) {
            HStack(alignment: .firstTextBaseline, spacing: 10) {
                VStack(alignment: .leading, spacing: 3) {
                    title
                    if let detail = option.displayDetail {
                        Text(detail)
                            .font(.subheadline)
                            .foregroundStyle(Theme.muted)
                            .fixedSize(horizontal: false, vertical: true)
                    }
                }
                .multilineTextAlignment(.leading)
                .frame(maxWidth: .infinity, alignment: .leading)
                if sending { ProgressView().controlSize(.small).tint(Theme.ink) }
            }
            .padding(.horizontal, 14)
            .padding(.vertical, 10)
            .frame(maxWidth: .infinity, minHeight: 44, alignment: .leading)
            .contentShape(RoundedRectangle(cornerRadius: Theme.Radius.card, style: .continuous))
        }
        .buttonStyle(OptionRowStyle(sending: sending))
        .opacity(enabled || sending ? 1 : 0.5)
        .disabled(!enabled)
        .accessibilityLabel(option.accessibilityLabel)
        .accessibilityHint(sending ? "Sending" : "")
        .accessibilityIdentifier(identifier)
    }

    /// The label, and the tag after it on the same line when both fit,
    /// else under it.
    @ViewBuilder private var title: some View {
        if option.isRecommended {
            ViewThatFits(in: .horizontal) {
                HStack(alignment: .firstTextBaseline, spacing: 8) {
                    label.fixedSize()
                    RecommendedTag()
                }
                VStack(alignment: .leading, spacing: 4) {
                    label
                    RecommendedTag()
                }
            }
        } else {
            label
        }
    }

    private var label: some View {
        Text(option.displayLabel)
            .font(.body.weight(.semibold))
            .foregroundStyle(option.isNegative ? Theme.danger : Theme.ink)
            .fixedSize(horizontal: false, vertical: true)
    }
}

/// "Recommended": a small tag, never a fill, so the option does not read
/// as chosen.
private struct RecommendedTag: View {
    var body: some View {
        Text("Recommended")
            .font(.caption.weight(.semibold))
            .foregroundStyle(Theme.blockedText)
            .padding(.horizontal, 7)
            .padding(.vertical, 2)
            .background(Capsule().fill(Theme.accentSoft))
            .fixedSize()
            .accessibilityHidden(true)
    }
}

/// The row's frame: the card's fill with a hairline, a darker fill while
/// pressed, the amber line while its answer is sending.
private struct OptionRowStyle: ButtonStyle {
    var sending: Bool

    func makeBody(configuration: Configuration) -> some View {
        let shape = RoundedRectangle(cornerRadius: Theme.Radius.card, style: .continuous)
        configuration.label
            .background(shape.fill(configuration.isPressed ? Theme.fill : Theme.card))
            .overlay(shape.strokeBorder(sending ? Theme.blocked : Theme.line, lineWidth: 1))
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
