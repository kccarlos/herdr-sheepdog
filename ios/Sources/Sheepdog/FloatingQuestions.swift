import SheepdogKit
import SwiftUI

/// The open questions floating above the capsule (canvas
/// "ConversationOne"): one card per question, in `QuestionOrder`, the
/// first with the filled primary button and the others outlined in amber.
/// The stack is as tall as its cards up to a cap, then scrolls inside;
/// it grows with the text size, and the conversation scrolls under it.
struct FloatingQuestions: View {
    let cards: [ApprovalFlow.Card]
    /// The question the voice is on: its buttons keep the `approval.*`
    /// identifiers, the others are `question.<alias>.*`.
    var voiceQuestionID: String?
    /// "4 min": how long its agent has waited.
    var time: (PendingAction) -> String? = { _ in nil }
    let answer: (String, Int) -> Void

    @State private var contentHeight: CGFloat = 0
    @Environment(\.dynamicTypeSize) private var dynamicTypeSize

    /// Half the screen; a little less at the accessibility sizes, where
    /// the capsule above the home indicator is taller too.
    private var maxHeight: CGFloat {
        let screen = UIApplication.shared.connectedScenes.compactMap { ($0 as? UIWindowScene)?.screen.bounds.height }.first ?? 800
        return screen * (dynamicTypeSize.isAccessibilitySize ? 0.38 : 0.5)
    }

    var body: some View {
        ScrollView {
            VStack(spacing: 8) {
                ForEach(Array(cards.enumerated()), id: \.element.pending.id) { index, card in
                    ApprovalCardView(
                        card: card,
                        primary: index == 0,
                        idPrefix: idPrefix(card),
                        meta: meta(card, index: index),
                        answer: { answer(card.pending.id, $0) }
                    )
                    .onAppear {
                        AccessibilityNotification.Announcement("\(card.pending.agent ?? "An agent") needs your answer. \(card.pending.displaySummary)").post()
                    }
                }
            }
            .padding(.vertical, 4)
            .onGeometryChange(for: CGFloat.self) { $0.size.height } action: { height in
                // Whole points only: a sub-point wobble must not relayout.
                let rounded = height.rounded(.up)
                if rounded != contentHeight { contentHeight = rounded }
            }
        }
        .scrollBounceBehavior(.basedOnSize)
        .scrollIndicators(contentHeight > maxHeight ? .visible : .hidden)
        .frame(height: min(max(contentHeight, 1), maxHeight))
        .accessibilityIdentifier("questions.floating")
    }

    private func idPrefix(_ card: ApprovalFlow.Card) -> String {
        if card.pending.id == voiceQuestionID { return "approval" }
        return "question.\(card.pending.agent ?? card.pending.paneID)"
    }

    /// "4 min · 1 of 2".
    private func meta(_ card: ApprovalFlow.Card, index: Int) -> String? {
        let parts = [time(card.pending), cards.count > 1 ? "\(index + 1) of \(cards.count)" : nil].compactMap { $0 }
        return parts.isEmpty ? nil : parts.joined(separator: " · ")
    }
}
