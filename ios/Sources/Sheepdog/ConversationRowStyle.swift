import SheepdogKit
import SwiftUI

// The conversation's visual system (docs/dev/design/conversation-timeline.md,
// section 12). Shape tells the kinds apart and colour confirms: a bubble is
// talk, a round well is an outcome, a square well is a thing to open, a
// hairline is context. Every row kind gets its look from the primitives in
// this file; the row views in ConversationRows.swift only choose which.

// MARK: - Environment

private struct LedgerPositionKey: EnvironmentKey {
    static let defaultValue = LedgerPosition.single
}

extension EnvironmentValues {
    /// Where the ledger line being drawn sits in its run.
    var ledgerPosition: LedgerPosition {
        get { self[LedgerPositionKey.self] }
        set { self[LedgerPositionKey.self] = newValue }
    }
}

extension Notification.Name {
    /// A pointer in the conversation was tapped: the floating question
    /// pulses its border once.
    static let pulseQuestion = Notification.Name("sheepdog.pulseQuestion")
}

// MARK: - Dialogue

/// Sizes its one child to at most `fraction` of the width it is offered and
/// puts it at the leading or trailing edge: a bubble that hugs its text and
/// never spans the row, without a `GeometryReader` per row.
struct BubbleWidth: Layout {
    var fraction: CGFloat
    var trailing: Bool

    func sizeThatFits(proposal: ProposedViewSize, subviews: Subviews, cache: inout ()) -> CGSize {
        guard let child = subviews.first else { return .zero }
        guard let width = proposal.width else { return child.sizeThatFits(.unspecified) }
        let size = child.sizeThatFits(ProposedViewSize(width: width * fraction, height: nil))
        return CGSize(width: width, height: size.height)
    }

    func placeSubviews(in bounds: CGRect, proposal: ProposedViewSize, subviews: Subviews, cache: inout ()) {
        guard let child = subviews.first else { return }
        let size = child.sizeThatFits(ProposedViewSize(width: bounds.width * fraction, height: nil))
        let x = trailing ? bounds.maxX - size.width : bounds.minX
        child.place(at: CGPoint(x: x, y: bounds.minY), anchor: .topLeading, proposal: ProposedViewSize(size))
    }
}

/// The dialogue bubble's shape: 20 pt corners and one tight 6 pt corner on
/// the side of whoever speaks.
func bubbleShape(you: Bool) -> UnevenRoundedRectangle {
    UnevenRoundedRectangle(
        topLeadingRadius: 20,
        bottomLeadingRadius: you ? 20 : 6,
        bottomTrailingRadius: you ? 6 : 20,
        topTrailingRadius: 20,
        style: .continuous
    )
}

// MARK: - The ledger

/// How one ledger line's well looks.
struct LedgerLook {
    var symbol: String
    var glyph: Color
    var fill: Color
    /// A square well (an object you open) instead of a round one.
    var square = false
    /// A hollow ring instead of a fill (closed by itself).
    var ring: Color?
    /// A dashed outline instead of a fill (a card that is gone).
    var dashed = false
    /// Three wells offset behind each other: a group of updates.
    var stacked = false

    /// An agent said something: blue news.
    static let news = LedgerLook(symbol: "terminal.fill", glyph: Theme.working, fill: Theme.workingSoft)
    /// News that arrived while you were away: the same blue, with a moon.
    static let missed = LedgerLook(symbol: "moon.zzz.fill", glyph: Theme.working, fill: Theme.workingSoft)
    static let approved = LedgerLook(symbol: "checkmark", glyph: Theme.done, fill: Theme.doneSoft)
    /// Declining is your call, not an error: neutral, no red.
    static let declined = LedgerLook(symbol: "xmark", glyph: Theme.ink, fill: Theme.fill)
    /// Closed by itself: hollow, a clock, no cross.
    static let closed = LedgerLook(symbol: "clock", glyph: Theme.muted, fill: .clear, ring: Theme.line)
    static func card(_ symbol: String) -> LedgerLook {
        LedgerLook(symbol: symbol, glyph: Theme.ink, fill: Theme.fill, square: true)
    }
    static let cardGone = LedgerLook(symbol: "rectangle.on.rectangle", glyph: Theme.muted, fill: .clear, square: true, dashed: true)
    static func group(missed: Bool) -> LedgerLook {
        var look: LedgerLook = missed ? .missed : .news
        look.stacked = true
        return look
    }
    static let held = LedgerLook(symbol: "hourglass", glyph: Theme.muted, fill: Theme.fill)
    static let dropped = LedgerLook(symbol: "bell.slash", glyph: Theme.muted, fill: Theme.fill)
}

/// The 28 pt well on a ledger line's leading edge (it scales a little with
/// the text and is gone at the accessibility sizes).
struct LedgerWell: View {
    let look: LedgerLook
    @ScaledMetric(relativeTo: .subheadline) private var size: CGFloat = 28

    private var side: CGFloat { min(size, 38) }

    var body: some View {
        ZStack(alignment: .leading) {
            if look.stacked {
                disc.opacity(0.55).offset(x: 10)
                disc.opacity(0.8).offset(x: 5)
            }
            disc.overlay {
                Image(systemName: look.symbol)
                    .font(.system(size: 15, weight: .semibold))
                    .foregroundStyle(look.glyph)
            }
        }
        .frame(width: look.stacked ? side + 10 : side, height: side, alignment: .leading)
        .accessibilityHidden(true)
    }

    private var disc: some View {
        let shape = RoundedRectangle(cornerRadius: look.square ? 8 : side / 2, style: .continuous)
        return shape
            .fill(look.fill)
            .overlay {
                if let ring = look.ring {
                    shape.strokeBorder(ring, lineWidth: 1.5)
                } else if look.dashed {
                    shape.strokeBorder(Theme.dash, style: StrokeStyle(lineWidth: 1.5, dash: [3, 2.5]))
                } else if look.stacked {
                    shape.strokeBorder(Theme.card, lineWidth: 2)
                }
            }
            .frame(width: side, height: side)
    }
}

/// How far a ledger line's text column (and what is inset to it) sits from
/// the card's edge: the well and its gap, nothing at the accessibility
/// sizes. `well` is the view's own `@ScaledMetric` well size.
enum LedgerInset {
    static func width(well: CGFloat, accessibility: Bool) -> CGFloat {
        accessibility ? 0 : min(well, 38) + 11
    }
}

/// What a ledger line's chevron does: nothing, down or up with its state,
/// only when the line is cut short, or a right chevron (it opens a sheet).
enum LedgerDisclosure {
    case none
    case always
    case ifTruncated
    case push
}

/// The card around a ledger line (or a run of them): 14 pt corners, one
/// hairline border; a line joined above or below drops that corner and
/// border and gets a hairline inset to the text column instead. `dashed` is
/// the held-push look of the technical view.
struct LedgerCard<Content: View>: View {
    var dashed = false
    @ViewBuilder var content: Content

    @Environment(\.ledgerPosition) private var position
    @Environment(\.dynamicTypeSize) private var dynamicTypeSize
    @ScaledMetric(relativeTo: .subheadline) private var well: CGFloat = 28

    var body: some View {
        let top: CGFloat = position.joinsAbove ? 0 : Theme.Radius.card
        let bottom: CGFloat = position.joinsBelow ? 0 : Theme.Radius.card
        let shape = UnevenRoundedRectangle(
            topLeadingRadius: top, bottomLeadingRadius: bottom, bottomTrailingRadius: bottom, topTrailingRadius: top,
            style: .continuous
        )
        content
            .padding(.horizontal, 12)
            .padding(.vertical, 10)
            .frame(maxWidth: .infinity, minHeight: 44, alignment: .leading)
            .background { if !dashed { shape.fill(Theme.card) } }
            .overlay {
                if dashed {
                    shape.strokeBorder(Theme.dash, style: StrokeStyle(lineWidth: 1, dash: [4, 3]))
                } else {
                    shape.strokeBorder(Theme.line, lineWidth: 1)
                        .mask { Rectangle().padding(.top, position.joinsAbove ? 1 : 0).padding(.bottom, position.joinsBelow ? 1 : 0) }
                }
            }
            .overlay(alignment: .top) {
                if position.joinsAbove {
                    Rectangle()
                        .fill(Theme.line)
                        .frame(height: 1)
                        .padding(.leading, 12 + LedgerInset.width(well: well, accessibility: dynamicTypeSize.isAccessibilitySize))
                }
            }
            .contentShape(shape)
    }
}

/// One ledger line: the well (or, at the accessibility sizes, an inline
/// glyph), the words, the time and a chevron. The caller owns whether it is
/// open and what is under it.
struct LedgerLine: View {
    let look: LedgerLook
    var kicker: String?
    var kickerColor = Theme.ink
    /// Muted words before the text: "While you were away · ".
    var prefix: String?
    var text = ""
    var weight: Font.Weight = .regular
    var color = Theme.ink
    /// More lines of the same weight under the text (a group's newest).
    var previews: [String] = []
    /// A caption under the text.
    var sub: String?
    var time: String?
    var disclosure = LedgerDisclosure.none
    var isOpen = false
    /// Lines the text may take while closed; nil for as many as it needs.
    var collapsedLines: Int?

    @Environment(\.dynamicTypeSize) private var dynamicTypeSize

    private var ax: Bool { dynamicTypeSize.isAccessibilitySize }

    var body: some View {
        if disclosure == .ifTruncated, !ax {
            // The chevron appears only when the words do not fit one line.
            ViewThatFits(in: .horizontal) {
                content(fitsOneLine: true, chevron: nil)
                content(fitsOneLine: false, chevron: isOpen ? "chevron.up" : "chevron.down")
            }
        } else {
            content(fitsOneLine: false, chevron: chevronSymbol)
        }
    }

    private var chevronSymbol: String? {
        switch disclosure {
        case .none, .ifTruncated: return nil
        case .always: return isOpen ? "chevron.up" : "chevron.down"
        case .push: return "chevron.right"
        }
    }

    private func content(fitsOneLine: Bool, chevron: String?) -> some View {
        HStack(alignment: .top, spacing: 11) {
            if !ax { LedgerWell(look: look) }
            VStack(alignment: .leading, spacing: 2) {
                if let kicker {
                    Text(kicker)
                        .font(.footnote.weight(.semibold))
                        .foregroundStyle(kickerColor)
                        .contentTransition(.numericText())
                        .fixedSize(horizontal: false, vertical: true)
                }
                if !text.isEmpty {
                    if fitsOneLine {
                        words.fixedSize(horizontal: true, vertical: false)
                    } else {
                        words
                            .lineLimit(ax || isOpen ? nil : collapsedLines)
                            .truncationMode(.tail)
                            .fixedSize(horizontal: false, vertical: true)
                    }
                }
                ForEach(Array(previews.enumerated()), id: \.offset) { _, line in
                    Text(line)
                        .font(.subheadline)
                        .foregroundStyle(Theme.ink)
                        .lineLimit(ax ? nil : 1)
                        .truncationMode(.tail)
                }
                if let sub {
                    Text(sub)
                        .font(.caption)
                        .foregroundStyle(Theme.muted)
                        .lineLimit(ax || isOpen ? nil : 1)
                        .fixedSize(horizontal: false, vertical: true)
                }
                if ax, let time {
                    Text(time).font(.caption.monospacedDigit()).foregroundStyle(Theme.muted)
                }
            }
            .frame(maxWidth: fitsOneLine ? nil : .infinity, alignment: .leading)
            if !ax, let time {
                Text(time)
                    .font(.caption.monospacedDigit())
                    .foregroundStyle(Theme.muted)
                    .padding(.top, 2)
                    .fixedSize()
            }
            if let chevron {
                Image(systemName: chevron)
                    .font(.caption.weight(.semibold))
                    .foregroundStyle(Theme.chevron)
                    .padding(.top, 4)
                    .accessibilityHidden(true)
            }
        }
        .frame(maxWidth: fitsOneLine ? nil : .infinity, alignment: .leading)
    }

    /// The line itself: the glyph inline at the accessibility sizes, the
    /// muted prefix, then the words.
    private var words: Text {
        var out = Text("")
        if ax { out = out + Text(Image(systemName: look.symbol)).foregroundStyle(look.glyph) + Text(" ") }
        if let prefix { out = out + Text(prefix).foregroundStyle(Theme.muted).fontWeight(.regular) }
        return (out + Text(text).foregroundStyle(color)).font(.subheadline.weight(weight))
    }
}

/// A Fill block with a caption under an opened ledger line, inset to its
/// text column: "Claude asked · 12:56", "Why it closed".
struct LedgerBlocks: View {
    let blocks: [(label: String, text: String)]
    @Environment(\.dynamicTypeSize) private var dynamicTypeSize
    @ScaledMetric(relativeTo: .subheadline) private var well: CGFloat = 28

    var body: some View {
        VStack(alignment: .leading, spacing: 8) {
            ForEach(Array(blocks.enumerated()), id: \.offset) { _, block in
                VStack(alignment: .leading, spacing: 2) {
                    Text(block.label)
                        .font(.caption.weight(.semibold))
                        .foregroundStyle(Theme.mutedOnFill)
                    Text(block.text)
                        .font(.footnote)
                        .foregroundStyle(Theme.ink)
                        .textSelection(.enabled)
                }
                .frame(maxWidth: .infinity, alignment: .leading)
                .padding(.horizontal, 10)
                .padding(.vertical, 8)
                .background(Theme.fill, in: RoundedRectangle(cornerRadius: 10, style: .continuous))
                .accessibilityElement(children: .combine)
            }
        }
        .padding(.leading, LedgerInset.width(well: well, accessibility: dynamicTypeSize.isAccessibilitySize))
        .padding(.top, 8)
    }
}

// MARK: - Context

/// A hairline, a small glyph and a caption, centred: where you are, not
/// something that happened. No tap, no container.
struct ContextDivider: View {
    let title: String
    var symbol: String?

    var body: some View {
        HStack(spacing: 10) {
            line
            HStack(alignment: .firstTextBaseline, spacing: 5) {
                if let symbol {
                    Image(systemName: symbol)
                        .font(.caption)
                        .accessibilityHidden(true)
                }
                Text(title)
                    .multilineTextAlignment(.center)
            }
            .font(.caption.weight(.medium))
            .foregroundStyle(Theme.muted)
            .lineLimit(2)
            .layoutPriority(1)
            line
        }
        .frame(minHeight: 24)
        .padding(.vertical, 2)
        .accessibilityElement(children: .combine)
        .accessibilityIdentifier("divider.notice")
    }

    private var line: some View {
        Rectangle().fill(Theme.line).frame(height: 1).frame(maxWidth: .infinity)
    }
}

/// A plumbing line of the technical view: monospaced, muted, its own text
/// verbatim, a small glyph.
struct PlumbingLine: View {
    let text: String
    var symbol = "gearshape"

    var body: some View {
        HStack(alignment: .top, spacing: 7) {
            Image(systemName: symbol)
                .font(.caption)
                .padding(.top, 2)
                .accessibilityHidden(true)
            Text(text)
                .font(.footnote.monospaced())
                .fixedSize(horizontal: false, vertical: true)
            Spacer(minLength: 0)
        }
        .foregroundStyle(Theme.muted)
        .padding(.vertical, 3)
        .accessibilityElement(children: .combine)
        .accessibilityIdentifier("bubble.plumbing")
    }
}

// MARK: - Pills

/// The floating capsule over the list ("Jump to latest", the technical
/// view's tag): material, a hairline, a soft shadow.
struct FloatingPill: ViewModifier {
    func body(content: Content) -> some View {
        content
            .background(.regularMaterial, in: Capsule())
            .overlay(Capsule().strokeBorder(Theme.line, lineWidth: 1))
            .shadow(color: .black.opacity(0.12), radius: 7, y: 3)
    }
}

extension View {
    func floatingPill() -> some View { modifier(FloatingPill()) }
}
