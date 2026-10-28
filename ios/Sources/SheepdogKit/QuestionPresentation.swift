import Foundation

/// How the open questions float above the capsule: the cards themselves
/// (`inline`), or one bar, "codex asked · Answer", that opens them in a
/// sheet (`compact`). The rule lives here, in one function, and is
/// documented in docs/dev/design/conversation-timeline.md ("When a
/// question goes compact"). The view measures; this decides.
public enum QuestionPresentation: Equatable, Sendable {
    case inline
    case compact

    /// What the decision is made from, all measured by SwiftUI at the
    /// current width and text size.
    public struct Input: Equatable, Sendable {
        /// Each open card's natural height in the inline layout, its
        /// question cut to `QuestionPresentation.questionLines` lines (so
        /// "More", a spinner or an answer never changes it); 0 while a
        /// card is not measured yet.
        public var cardHeights: [Double]
        /// The gap between two stacked cards.
        public var spacing: Double
        /// The height the screen gives the conversation and everything
        /// over it: the host's safe area above the keyboard, measured on
        /// its root view, outside the floating stack, so the cards' own
        /// size never feeds back into it. 0 when not measured.
        public var area: Double
        /// Dynamic Type at accessibility 3 or larger.
        public var largestText: Bool
        /// The user is in the middle of it: the sheet is open, or an
        /// answer is in flight or just landed. Nothing changes under them.
        public var held: Bool

        public init(cardHeights: [Double], spacing: Double = 8, area: Double, largestText: Bool = false, held: Bool = false) {
            self.cardHeights = cardHeights
            self.spacing = spacing
            self.area = area
            self.largestText = largestText
            self.held = held
        }

        /// The stacked cards' natural height.
        public var natural: Double {
            cardHeights.reduce(0, +) + spacing * Double(max(0, cardHeights.count - 1))
        }
    }

    /// The share of `area` the inline cards may take. 0.4 of the safe area
    /// is about half of what is left once the top bar and the capsule are
    /// drawn, so the latest lines of the conversation stay in view; it
    /// keeps a three-option command approval inline on an iPhone in
    /// portrait at the default text size, and sends a question whose
    /// options carry descriptions to the sheet.
    public static let budgetShare = 0.4

    /// Hysteresis: back from compact to inline only once the cards fit in
    /// this share of the budget, so a size near the line (the keyboard,
    /// a rotation, a timer's text) does not flip the screen back and forth.
    public static let returnShare = 0.85

    /// The inline card shows this many lines of the question, then a fade
    /// and "More".
    public static let questionLines = 3

    /// The most height the inline cards may take in `area`.
    public static func budget(area: Double) -> Double { area * budgetShare }

    /// The presentation for `input`, given the one on screen (nil before
    /// the first decision).
    ///
    /// - Accessibility 3 and larger: always compact (the cards would cover
    ///   the screen and the bar is the larger target).
    /// - Held (the sheet is open, an answer in flight): what is on screen.
    /// - Not measured yet (no area, or a card without a height): what is
    ///   on screen, inline at first.
    /// - Otherwise inline when the stacked cards' natural height fits the
    ///   budget (or, coming back from compact, `returnShare` of it).
    public static func decide(_ input: Input, current: QuestionPresentation?) -> QuestionPresentation {
        if input.cardHeights.isEmpty { return current ?? .inline }
        if input.largestText { return .compact }
        if input.held, let current { return current }
        if input.area <= 0 || input.cardHeights.contains(where: { $0 <= 0 }) { return current ?? .inline }
        var limit = budget(area: input.area)
        if current == .compact { limit *= returnShare }
        return input.natural <= limit ? .inline : .compact
    }
}
