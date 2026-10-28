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
    /// The composer and keyboard are up: a lower cap, so some of the
    /// conversation stays in view.
    var compact = false
    /// A card from another computer: its label (by the card's id).
    var remote: (String) -> RemoteQuestion? = { _ in nil }
    /// Talk to a computer (by id).
    var talkTo: (String) -> Void = { _ in }
    let answer: (String, Int) -> Void

    @State private var contentHeight: CGFloat = 0
    /// The full question sheet, opened from the one-line bar.
    @State private var showSheet = false
    /// One pulse of the border when a pointer in the conversation is tapped.
    @State private var pulsing = false
    @Environment(\.dynamicTypeSize) private var dynamicTypeSize
    @Environment(\.accessibilityReduceMotion) private var reduceMotion

    /// Half the screen; a little less at the accessibility sizes, where
    /// the capsule above the home indicator is taller too.
    private var maxHeight: CGFloat {
        let screen = UIApplication.shared.connectedScenes.compactMap { ($0 as? UIWindowScene)?.screen.bounds.height }.first ?? 800
        if compact { return screen * 0.22 }
        return screen * (dynamicTypeSize.isAccessibilitySize ? 0.38 : 0.5)
    }

    /// At the largest text sizes the cards do not fit above the capsule: one
    /// bar, "Codex asked" and "Answer", opens the full question in a sheet.
    private var collapsed: Bool { dynamicTypeSize >= .accessibility3 }

    var body: some View {
        Group {
            if collapsed {
                bar
            } else {
                stack(announce: true)
                    .overlay {
                        RoundedRectangle(cornerRadius: Theme.Radius.large, style: .continuous)
                            .strokeBorder(Theme.blocked, lineWidth: 3)
                            .opacity(pulsing ? 1 : 0)
                            .allowsHitTesting(false)
                    }
            }
        }
        .onReceive(NotificationCenter.default.publisher(for: .pulseQuestion)) { _ in
            if collapsed {
                showSheet = true
            } else if !reduceMotion {
                withAnimation(.easeOut(duration: 0.2)) { pulsing = true }
                withAnimation(.easeIn(duration: 0.5).delay(0.3)) { pulsing = false }
            }
        }
        .onChange(of: cards.isEmpty) { _, empty in if empty { showSheet = false } }
    }

    /// "Codex asked" and the button that opens the question.
    private var bar: some View {
        let first = cards.first?.pending
        let title = cards.count > 1 ? "\(cards.count) questions" : "\(first?.agent ?? "An agent") asked"
        return VStack(alignment: .leading, spacing: 10) {
            Text(title)
                .font(.title3.weight(.semibold))
                .foregroundStyle(Theme.ink)
                .fixedSize(horizontal: false, vertical: true)
            Button { showSheet = true } label: {
                Text("Answer")
                    .font(.title3.weight(.semibold))
                    .foregroundStyle(Theme.onAccent)
                    .frame(maxWidth: .infinity, minHeight: 56)
                    .background(Capsule().fill(Color.accentColor))
            }
            .buttonStyle(.plain)
            .accessibilityHint("Opens the full question")
            .accessibilityIdentifier("questions.answer")
        }
        .padding(.horizontal, 14)
        .padding(.vertical, 12)
        .background(RoundedRectangle(cornerRadius: Theme.Radius.large, style: .continuous).fill(Theme.card))
        .overlay(RoundedRectangle(cornerRadius: Theme.Radius.large, style: .continuous).strokeBorder(Theme.blocked, lineWidth: 1.5))
        .accessibilityElement(children: .contain)
        .accessibilityLabel("\(title). Needs your answer.")
        .onAppear {
            AccessibilityNotification.Announcement("\(first?.agent ?? "An agent") needs your answer.").post()
        }
        .sheet(isPresented: $showSheet) {
            ScrollView { stackCards(announce: false).padding(.horizontal, 12).padding(.top, 16) }
                .background(Theme.surface)
                .presentationDetents([.large])
                .presentationDragIndicator(.visible)
        }
        .accessibilityIdentifier("questions.floating")
    }

    private func stack(announce: Bool) -> some View {
        ScrollView {
            stackCards(announce: announce)
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

    private func stackCards(announce: Bool) -> some View {
        VStack(spacing: 8) {
            ForEach(Array(cards.enumerated()), id: \.element.pending.id) { index, card in
                ApprovalCardView(
                    card: card,
                    primary: index == 0,
                    idPrefix: idPrefix(card),
                    meta: meta(card, index: index),
                    remote: remote(card.pending.id),
                    talkTo: remote(card.pending.id).map { r in { talkTo(r.computerID) } },
                    answer: { answer(card.pending.id, $0) }
                )
                // Rises from the capsule; collapses into its settled
                // row in the conversation when answered.
                .matchedInMotion(id: card.pending.id)
                .transition(reduceMotion ? .opacity : .move(edge: .bottom).combined(with: .opacity))
                .onAppear {
                    if announce {
                        AccessibilityNotification.Announcement("\(card.pending.agent ?? "An agent") needs your answer. \(card.pending.displaySummary)").post()
                    }
                }
            }
        }
        .padding(.vertical, 4)
    }

    private func idPrefix(_ card: ApprovalFlow.Card) -> String {
        if card.pending.id == voiceQuestionID { return "approval" }
        if let r = remote(card.pending.id) { return "question.\(r.computerID).\(r.pending.agent ?? r.pending.paneID)" }
        return "question.\(card.pending.agent ?? card.pending.paneID)"
    }

    /// "4 min · 1 of 2".
    private func meta(_ card: ApprovalFlow.Card, index: Int) -> String? {
        let parts = [time(card.pending), cards.count > 1 ? "\(index + 1) of \(cards.count)" : nil].compactMap { $0 }
        return parts.isEmpty ? nil : parts.joined(separator: " · ")
    }
}
