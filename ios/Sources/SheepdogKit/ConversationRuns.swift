import CoreGraphics
import Foundation

/// Where a ledger line sits in its run: consecutive agent outcomes share
/// one card (docs/dev/design/conversation-timeline.md, section 12), so a
/// line joined above or below drops that corner and its border.
public struct LedgerPosition: Equatable, Sendable {
    public var joinsAbove: Bool
    public var joinsBelow: Bool

    public init(joinsAbove: Bool = false, joinsBelow: Bool = false) {
        self.joinsAbove = joinsAbove
        self.joinsBelow = joinsBelow
    }

    public static let single = LedgerPosition()
}

extension TimelineRow {
    /// The user's own words (a bubble on the right).
    public var isFromUser: Bool {
        switch self {
        case let .item(item): return item.kind == .shepherd
        case let .live(bubble): return bubble.speaker == .shepherd
        default: return false
        }
    }

    /// A card shown on the screen: an object of its own, never in a run.
    public var isCardShown: Bool {
        switch self {
        case .card: return true
        case let .item(item): return item.kind == .card
        default: return false
        }
    }
}

extension ConversationTimeline {
    /// The position of every ledger line (by display id) in its run. A
    /// row in `excluding` (a question folded to its pointer) is not one;
    /// neither is anything that is not a ledger line, which ends the run.
    /// A folded group that ends in a restart divider ends the run too.
    public static func ledgerPositions(_ rows: [ConversationDisplayRow], excluding: Set<String> = []) -> [String: LedgerPosition] {
        func isLedger(_ i: Int) -> Bool {
            rows.indices.contains(i) && rows[i].joinsLedger && !excluding.contains(rows[i].id)
        }
        func endsInLedger(_ i: Int) -> Bool {
            if case let .updates(_, members) = rows[i] { return members.last.map { $0.classification.ledger } ?? true }
            return true
        }
        var out: [String: LedgerPosition] = [:]
        for i in rows.indices where isLedger(i) {
            out[rows[i].id] = LedgerPosition(
                joinsAbove: isLedger(i - 1) && endsInLedger(i - 1),
                joinsBelow: isLedger(i + 1) && endsInLedger(i)
            )
        }
        return out
    }

    /// The voice (or the orchestrator) answered after `since`: what
    /// "Catch me up" waits for.
    public static func voiceAnswered(_ rows: [TimelineRow], since: Date) -> Bool {
        rows.contains { row in
            switch row {
            case let .item(item): return item.kind == .dog && item.ts >= since
            case let .live(bubble): return bubble.speaker == .sheepdog && bubble.at >= since
            case let .brain(reply, _): return reply.ts >= since
            default: return false
            }
        }
    }

    /// The space above `row` under `previous`: 6 within one exchange, 14
    /// from a divider to dialogue, 18 between different categories (and
    /// when a new exchange starts), 0 inside a ledger run, 8 between a
    /// run and a card. The technical view is a log: 6 everywhere.
    public static func gap(from previous: ConversationDisplayRow, to row: ConversationDisplayRow, joined: Bool = false, technical: Bool = false) -> CGFloat {
        if technical { return 6 }
        if joined { return 0 }
        func shownCard(_ r: ConversationDisplayRow) -> Bool {
            if case let .row(row) = r { return row.isCardShown }
            return false
        }
        func user(_ r: ConversationDisplayRow) -> Bool {
            if case let .row(row) = r { return row.isFromUser }
            return false
        }
        let (a, b) = (previous.category, row.category)
        if shownCard(previous) || shownCard(row) {
            return (previous.joinsLedger || row.joinsLedger || shownCard(previous) && shownCard(row)) ? 8 : 18
        }
        if a == .context { return b == .dialogue ? 14 : 8 }
        if a == .dialogue, b == .dialogue { return !user(previous) && user(row) ? 18 : 6 }
        if a == .dialogue, b == .needsYou { return 4 }
        return 18
    }
}

extension QuestionFocus {
    /// One pointer per question: of the rows folded to a pointer (the
    /// approval, the push that announced it, the voice's restatement), the
    /// one that is drawn, by row id, with who asked. The approval item
    /// wins and an agent is listed once. Without an approval item (the
    /// history is not synced yet) the first folded row stands for it.
    public static func pointerLines(rows: [TimelineRow], pointerIDs: Set<String>) -> [String: String] {
        guard !pointerIDs.isEmpty else { return [:] }
        func asker(_ agent: String?) -> String { agent?.trimmed.nonEmpty ?? "An agent" }
        var lines: [String: String] = [:]
        var seen: Set<String> = []
        for row in rows where pointerIDs.contains(row.id) {
            guard case let .item(item) = row, item.kind == .approval else { continue }
            if seen.insert(asker(item.agent)).inserted { lines[row.id] = asker(item.agent) }
        }
        if lines.isEmpty, let first = rows.first(where: { pointerIDs.contains($0.id) }) {
            if case let .item(item) = first { lines[first.id] = asker(item.agent) } else { lines[first.id] = asker(nil) }
        }
        return lines
    }
}
