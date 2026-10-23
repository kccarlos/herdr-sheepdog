import Foundation

/// The order the open questions float above the capsule
/// (app-redesign.md 1.1): the current computer's first, then the rest by
/// age, oldest first. `questions` arrives oldest first from `/api/state`;
/// the order within each group is kept. Only the first gets the filled
/// primary button.
public enum QuestionOrder {
    public static func ordered<Q>(_ questions: [Q], isCurrentComputer: (Q) -> Bool) -> [Q] {
        questions.filter(isCurrentComputer) + questions.filter { !isCurrentComputer($0) }
    }

    /// The questions the conversation floats: every open one, plus the
    /// voice's own while it shows how it ended (answered, gone), in front.
    public static func floating(open: [PendingAction], voice: PendingAction?, voiceSettled: Bool) -> [PendingAction] {
        guard let voice, voiceSettled, !open.contains(where: { $0.id == voice.id }) else { return open }
        return [voice] + open
    }

    /// The rows that fold to a pointer while their question floats: the
    /// union over every open question (`QuestionFocus`).
    public static func pointerRowIDs(rows: [TimelineRow], open: [PendingAction]) -> Set<String> {
        open.reduce(into: Set<String>()) { ids, q in
            ids.formUnion(QuestionFocus.pointerRowIDs(rows: rows, pending: q))
        }
    }
}
