import SheepdogKit
import SwiftUI

/// The open questions floating above the capsule (canvas
/// "ConversationOne"), in `QuestionOrder`. Inline, the cards themselves,
/// each question cut to three lines with "More"; compact, one bar,
/// "codex asked · Answer", that opens every card in full in a sheet.
/// `QuestionPresentation.decide` picks between them from the cards'
/// measured height and the screen's (docs/dev/design/conversation-timeline.md,
/// "When a question goes compact"). Either way the timeline keeps its
/// amber pointer row, and an answer goes through the same `answer` (the
/// ActionRunner path) as anywhere else.
struct FloatingQuestions: View {
    let cards: [ApprovalFlow.Card]
    /// The question the voice is on: its buttons keep the `approval.*`
    /// identifiers, the others are `question.<alias>.*`.
    var voiceQuestionID: String?
    /// "4 min": how long its agent has waited.
    var time: (PendingAction) -> String? = { _ in nil }
    /// The height the host's screen gives the conversation and what floats
    /// over it (`QuestionPresentation.Input.area`): measured on the host's
    /// root view, so it shrinks with the keyboard and never with the cards.
    var area: CGFloat
    /// A card from another computer: its label (by the card's id).
    var remote: (String) -> RemoteQuestion? = { _ in nil }
    /// Talk to a computer (by id).
    var talkTo: (String) -> Void = { _ in }
    let answer: (String, Int) -> Void

    /// Gap between stacked cards.
    private static let spacing: CGFloat = 8

    /// Each card's natural inline height, by question id.
    @State private var heights: [String: CGFloat] = [:]
    @State private var presentation: QuestionPresentation?
    @State private var contentHeight: CGFloat = 0
    /// The full question sheet, opened from the bar.
    @State private var showSheet = false
    /// One pulse of the border when a pointer in the conversation is tapped.
    @State private var pulsing = false
    /// Questions already announced to VoiceOver: a change of presentation
    /// does not announce them again.
    @State private var announced: Set<String> = []
    @Environment(\.dynamicTypeSize) private var dynamicTypeSize
    @Environment(\.accessibilityReduceMotion) private var reduceMotion

    private var input: QuestionPresentation.Input {
        QuestionPresentation.Input(
            cardHeights: cards.map { Double(heights[$0.pending.id] ?? 0) },
            spacing: Double(Self.spacing),
            area: Double(area),
            largestText: dynamicTypeSize >= .accessibility3,
            held: showSheet || cards.contains { !$0.acceptsTaps }
        )
    }

    private var compact: Bool { (presentation ?? .inline) == .compact }

    /// The inline stack's cap: the budget, so a card that fits never
    /// scrolls; "More" or an opened command may outgrow it, and then the
    /// stack scrolls from the top.
    private var cap: CGFloat {
        max(CGFloat(QuestionPresentation.budget(area: Double(area))), 120)
    }

    var body: some View {
        Group {
            if compact {
                bar
            } else {
                stack
                    .overlay {
                        RoundedRectangle(cornerRadius: Theme.Radius.large, style: .continuous)
                            .strokeBorder(Theme.blocked, lineWidth: 3)
                            .opacity(pulsing ? 1 : 0)
                            .allowsHitTesting(false)
                    }
            }
        }
        .frame(maxWidth: .infinity)
        .background(alignment: .top) { measuring }
        // On the container, not the bar: a change of presentation never
        // pulls the sheet out from under the user (and none happens while
        // it is open: `held`).
        .sheet(isPresented: $showSheet) { sheet }
        .onChange(of: input, initial: true) { _, input in
            let next = QuestionPresentation.decide(input, current: presentation)
            if next != presentation { presentation = next }
        }
        .onChange(of: cards.map(\.pending.id)) { _, ids in
            heights = heights.filter { ids.contains($0.key) }
            if ids.isEmpty { showSheet = false }
        }
        .onReceive(NotificationCenter.default.publisher(for: .pulseQuestion)) { _ in
            if compact {
                showSheet = true
            } else if !reduceMotion {
                withAnimation(.easeOut(duration: 0.2)) { pulsing = true }
                withAnimation(.easeIn(duration: 0.5).delay(0.3)) { pulsing = false }
            }
        }
    }

    // MARK: - Compact

    private var barTitle: String {
        cards.count > 1 ? "\(cards.count) questions" : "\(agent(cards.first)) asked"
    }

    /// The first line of the first question (with several, who asks it).
    private var barLine: String {
        guard let first = cards.first?.pending else { return "" }
        let line = first.displaySummary.split(whereSeparator: \.isNewline).first.map(String.init) ?? first.displaySummary
        return cards.count > 1 ? "\(agent(cards.first)): \(line)" : line
    }

    /// "codex asked", the question's first line and Answer: one button that
    /// opens the sheet. Side by side, or stacked at the accessibility sizes.
    private var bar: some View {
        Button { showSheet = true } label: {
            let layout = dynamicTypeSize.isAccessibilitySize
                ? AnyLayout(VStackLayout(alignment: .leading, spacing: 10))
                : AnyLayout(HStackLayout(alignment: .center, spacing: 12))
            layout {
                VStack(alignment: .leading, spacing: 2) {
                    HStack(spacing: 7) {
                        Circle().fill(Theme.blocked).frame(width: 7, height: 7)
                        Text(barTitle)
                            .font(.body.weight(.semibold))
                            .foregroundStyle(Theme.ink)
                            .fixedSize(horizontal: false, vertical: true)
                    }
                    Text(barLine)
                        .font(.subheadline)
                        .foregroundStyle(Theme.muted)
                        .lineLimit(dynamicTypeSize.isAccessibilitySize ? 2 : 1)
                }
                .frame(maxWidth: .infinity, alignment: .leading)
                Text("Answer")
                    .font(.body.weight(.semibold))
                    .foregroundStyle(Theme.onAccent)
                    .padding(.horizontal, 18)
                    .frame(maxWidth: dynamicTypeSize.isAccessibilitySize ? .infinity : nil, minHeight: 44)
                    .background(Capsule().fill(Color.accentColor))
                    .fixedSize(horizontal: !dynamicTypeSize.isAccessibilitySize, vertical: false)
            }
            .padding(.horizontal, 14)
            .padding(.vertical, 10)
            .background(RoundedRectangle(cornerRadius: Theme.Radius.large, style: .continuous).fill(Theme.card))
            .overlay(RoundedRectangle(cornerRadius: Theme.Radius.large, style: .continuous).strokeBorder(Theme.blocked, lineWidth: 1.5))
            .contentShape(RoundedRectangle(cornerRadius: Theme.Radius.large, style: .continuous))
        }
        .buttonStyle(.plain)
        .accessibilityElement(children: .ignore)
        .accessibilityLabel("\(barTitle): \(barLine). Answer.")
        .accessibilityHint("Opens the full question")
        .accessibilityAddTraits(.isButton)
        .accessibilityIdentifier("questions.answer")
        .onAppear { announce(cards) }
        .onChange(of: cards.map(\.pending.id)) { _, _ in announce(cards) }
    }

    /// Every card in full, scrolling, under a system navigation bar with
    /// Done (the card sheet's pattern, `CardSheet`).
    private var sheet: some View {
        NavigationStack {
            ScrollView {
                cardList(questionLines: nil, inSheet: true)
                    .padding(.horizontal, 16)
                    .padding(.top, 8)
                    .padding(.bottom, 24)
            }
            .scrollBounceBehavior(.basedOnSize)
            .background(Theme.surface)
            .navigationBarTitleDisplayMode(.inline)
            .navigationTitle(cards.count > 1 ? "\(cards.count) questions" : "Question")
            .toolbar {
                ToolbarItem(placement: .confirmationAction) {
                    Button("Done") { showSheet = false }
                        .fontWeight(.semibold)
                        .accessibilityIdentifier("questions.sheet.close")
                }
            }
        }
        .presentationDetents([.large])
        .presentationDragIndicator(.visible)
        .presentationContentInteraction(.resizes)
        .presentationBackground(Theme.surface)
        .accessibilityIdentifier("questions.sheet")
        // The cards in the sheet do not fly to their conversation rows.
        .environment(\.motionNamespace, nil)
    }

    // MARK: - Inline

    private var stack: some View {
        ScrollView {
            cardList(questionLines: QuestionPresentation.questionLines, inSheet: false)
                .padding(.vertical, 4)
                .onGeometryChange(for: CGFloat.self) { $0.size.height } action: { height in
                    // Whole points only: a sub-point wobble must not relayout.
                    let rounded = height.rounded(.up)
                    if rounded != contentHeight { contentHeight = rounded }
                }
        }
        .scrollBounceBehavior(.basedOnSize)
        .scrollIndicators(contentHeight > cap ? .visible : .hidden)
        .frame(height: min(max(contentHeight, 1), cap))
        .accessibilityIdentifier("questions.floating")
    }

    private func cardList(questionLines: Int?, inSheet: Bool) -> some View {
        VStack(spacing: Self.spacing) {
            ForEach(Array(cards.enumerated()), id: \.element.pending.id) { index, card in
                cardView(card, index: index, questionLines: questionLines)
                    // Rises from the capsule; collapses into its settled
                    // row in the conversation when answered.
                    .matchedInMotion(id: card.pending.id)
                    .transition(reduceMotion ? .opacity : .move(edge: .bottom).combined(with: .opacity))
                    .onAppear { if !inSheet { announce([card]) } }
            }
        }
    }

    private func cardView(_ card: ApprovalFlow.Card, index: Int, questionLines: Int?) -> ApprovalCardView {
        ApprovalCardView(
            card: card,
            primary: index == 0,
            idPrefix: idPrefix(card),
            meta: meta(card, index: index),
            remote: remote(card.pending.id),
            talkTo: remote(card.pending.id).map { r in { talkTo(r.computerID) } },
            questionLines: questionLines,
            answer: { answer(card.pending.id, $0) }
        )
    }

    /// The inline cards, laid out off screen at this width and text size
    /// whatever is showing, so the decision has their natural height in
    /// both presentations. Hidden from VoiceOver, taps and UI tests.
    private var measuring: some View {
        VStack(spacing: Self.spacing) {
            ForEach(Array(cards.enumerated()), id: \.element.pending.id) { index, card in
                cardView(card, index: index, questionLines: QuestionPresentation.questionLines)
                    .onGeometryChange(for: CGFloat.self) { $0.size.height.rounded(.up) } action: { height in
                        if heights[card.pending.id] != height { heights[card.pending.id] = height }
                    }
            }
        }
        .fixedSize(horizontal: false, vertical: true)
        .hidden()
        .allowsHitTesting(false)
        .accessibilityHidden(true)
    }

    // MARK: - Helpers

    private func agent(_ card: ApprovalFlow.Card?) -> String {
        guard let name = card?.pending.agent, !name.isEmpty else { return "An agent" }
        return name
    }

    /// "codex needs your answer. <question>", once per question.
    private func announce(_ cards: [ApprovalFlow.Card]) {
        for card in cards where !announced.contains(card.pending.id) {
            announced.insert(card.pending.id)
            AccessibilityNotification.Announcement("\(agent(card)) needs your answer. \(card.pending.displaySummary)").post()
        }
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
