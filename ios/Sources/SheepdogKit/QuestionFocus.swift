import Foundation

/// One blocked agent, shown once. While a question is pinned on the
/// approval card, the conversation's own copies of it — the approval item,
/// the push that announced it, and the voice's sentence restating it — fold
/// to a one-line pointer to the card, so a single question no longer fills
/// the screen four times over (review B1). Pure: the rows go back to full
/// size the moment the card settles.
public enum QuestionFocus {
    /// The ids of the rows to show as a pointer, for the open question
    /// `pending` (nil: nothing is pinned, nothing folds).
    public static func pointerRowIDs(rows: [TimelineRow], pending: PendingAction?) -> Set<String> {
        guard let pending, pending.isAnswerPrompt else { return [] }
        let alias = pending.agent?.trimmed.lowercased() ?? ""
        let summary = pending.displaySummary.trimmed.lowercased()

        func mentions(_ text: String) -> Bool {
            let t = text.lowercased()
            if !summary.isEmpty, t.trimmed == summary { return true }
            return !alias.isEmpty && t.contains(alias)
        }

        // Where the question starts: its approval item, else (history not
        // synced yet) after the user last spoke.
        let approvalIndex = rows.lastIndex { row in
            if case let .item(item) = row { return item.kind == .approval && item.pendingID == pending.id }
            return false
        }
        let lastShepherd = rows.lastIndex { row in
            switch row {
            case let .item(item): return item.kind == .shepherd
            case let .live(bubble): return bubble.speaker == .shepherd
            default: return false
            }
        }
        var ids: Set<String> = []
        let start: Int
        if let approvalIndex {
            ids.insert(rows[approvalIndex].id)
            // The announcing push can land a moment before the item.
            start = max(0, approvalIndex - 2)
        } else {
            start = (lastShepherd ?? -1) + 1
        }
        guard start < rows.count else { return ids }

        var foldedDog = false
        for index in start ..< rows.count {
            let row = rows[index]
            switch row {
            case let .item(item):
                switch item.kind {
                case .push:
                    let agentMatches = item.agent.map { !alias.isEmpty && $0.trimmed.lowercased() == alias } ?? false
                    if agentMatches || mentions(item.text) { ids.insert(row.id) }
                case .dog:
                    if !foldedDog, index > (approvalIndex ?? -1), mentions(item.text) {
                        ids.insert(row.id)
                        foldedDog = true
                    }
                case .shepherd:
                    // The user answered or moved on: what follows is a
                    // new exchange.
                    if index > (approvalIndex ?? start) { return ids }
                default:
                    break
                }
            case let .live(bubble):
                switch (bubble.speaker, bubble.kind) {
                case (.system, .push):
                    if mentions(bubble.text) { ids.insert(row.id) }
                case (.sheepdog, _):
                    if !foldedDog, index > (approvalIndex ?? -1), mentions(bubble.text) {
                        ids.insert(row.id)
                        foldedDog = true
                    }
                case (.shepherd, _):
                    if index > (approvalIndex ?? start) { return ids }
                default:
                    break
                }
            default:
                break
            }
        }
        return ids
    }
}
