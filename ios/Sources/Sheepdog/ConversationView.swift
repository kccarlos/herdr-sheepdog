import SheepdogKit
import SwiftUI

/// The conversation: the user on the right, the voice on the left, the
/// daemon's pushes and approvals as full-width cards, and quiet dividers
/// for days and restarts. The history loaded from the daemon (or the
/// cache) sits above this session's live bubbles; pulling down at the top
/// loads the page before it.
struct ConversationView: View {
    let rows: [TimelineRow]
    let voiceName: String
    var canLoadOlder = false
    var loadingOlder = false
    /// The questions floating above the capsule, still open.
    var openPendingIDs: Set<String> = []
    /// Rows that fold to a one-line pointer while their question is
    /// pinned on the approval card (`QuestionFocus`).
    var pointerIDs: Set<String> = []
    var loadOlder: () async -> Void = {}
    /// Changes when the user typed something: follow the conversation
    /// to its end even if it was scrolled up (the keyboard shrinks it).
    var followToken = 0
    /// The empty conversation's example questions.
    var ask: ((String) -> Void)?
    /// Settings ▸ Health ▸ Show technical events: every row, ungrouped,
    /// with its exact time.
    var showTechnical = false

    @State private var atBottom = true
    /// The list's own height on screen.
    @State private var visibleHeight: CGFloat = 1000
    @State private var lastSeenID: String?
    /// The "N agent updates" rows the user opened, by their id.
    @State private var openGroups: Set<String> = []

    /// What the list shows: without the plumbing unless asked for.
    private var shown: [TimelineRow] {
        ConversationTimeline.visibleRows(rows, showTechnical: showTechnical)
    }

    /// The list as drawn. The technical view is the raw rows, ungrouped.
    private func display(_ shown: [TimelineRow]) -> [ConversationDisplayRow] {
        showTechnical
            ? shown.map(ConversationDisplayRow.row)
            : ConversationTimeline.grouped(shown, keepVisible: pointerIDs)
    }

    private func hasContent(_ shown: [TimelineRow]) -> Bool {
        shown.contains { if case .day = $0 { return false } else { return true } }
    }

    var body: some View {
        let visible = shown
        ScrollViewReader { proxy in
            ScrollView {
                LazyVStack(spacing: 10) {
                    if canLoadOlder {
                        OlderButton(loading: loadingOlder) {
                            Task { await loadKeepingPosition(proxy) }
                        }
                    }
                    if !hasContent(visible) {
                        EmptyConversation(voiceName: voiceName, ask: ask)
                            .padding(.top, 60)
                    }
                    // Runs of the agents' updates fold into one "N agent
                    // updates" row; what was said stays in the open.
                    ForEach(display(visible)) { display in
                        Group {
                            switch display {
                            case let .row(row):
                                if showTechnical {
                                    TechnicalRow(row: row, voiceName: voiceName, openPendingIDs: openPendingIDs)
                                } else if pointerIDs.contains(row.id) {
                                    QuestionPointer(row: row)
                                } else {
                                    RowView(row: row, voiceName: voiceName, openPendingIDs: openPendingIDs)
                                }
                            case let .updates(id, members):
                                UpdatesGroup(
                                    rows: members,
                                    isOpen: openGroups.contains(id),
                                    voiceName: voiceName,
                                    openPendingIDs: openPendingIDs
                                ) {
                                    withAnimation(Theme.ease) {
                                        if openGroups.contains(id) { openGroups.remove(id) } else { openGroups.insert(id) }
                                    }
                                }
                            }
                        }
                            .id(display.id)
                    }
                    Color.clear
                        .frame(height: 1)
                        .id(Self.bottomAnchor)
                        // Folded into the sentinel rather than a separate
                        // `.onChange(of: atBottom)`: a relayout burst (a
                        // connect, or a sheet dismissing) can make this view
                        // appear and disappear within one frame, and a Bool
                        // `onChange` firing twice in that frame is a
                        // reproducible SwiftUI "action tried to update
                        // multiple times per frame" fault that hangs the
                        // main thread on this iOS build. The write is
                        // pushed to the next run-loop turn, not made
                        // in-line, because `onAppear`/`onDisappear` firing
                        // *during* the same relayout that triggered them
                        // (a taller, more-often-full-height conversation
                        // makes that common) re-enters SwiftUI's update
                        // while it is still settling and reproduced as an
                        // indefinite main-thread hang with a longer history.
                        .onAppear {
                            DispatchQueue.main.async {
                                atBottom = true
                                lastSeenID = visible.last?.id
                            }
                        }
                        .onDisappear {
                            DispatchQueue.main.async { atBottom = false }
                        }
                }
                .padding(.horizontal, 16)
                .padding(.vertical, 12)
            }
            .defaultScrollAnchor(.bottom)
            .onGeometryChange(for: CGFloat.self) { $0.size.height } action: { h in
                let rounded = h.rounded()
                if rounded != visibleHeight { visibleHeight = rounded }
            }
            .refreshable {
                // Pulling down at the top is "show me what came before".
                guard canLoadOlder else { return }
                await loadKeepingPosition(proxy)
            }
            .overlay(alignment: .bottom) {
                // Not when the list is a sliver (keyboard and question up):
                // the pill would sit on the agent strip.
                if !atBottom, visibleHeight > 160, let newest = visible.last?.id, newest != lastSeenID {
                    Button {
                        withAnimation { proxy.scrollTo(Self.bottomAnchor, anchor: .bottom) }
                    } label: {
                        Label("Jump to latest", systemImage: "arrow.down")
                            .font(.footnote.weight(.semibold))
                            .padding(.horizontal, 14)
                            .padding(.vertical, 8)
                            .background(.regularMaterial, in: Capsule())
                    }
                    .padding(.bottom, 10)
                    .transition(.move(edge: .bottom).combined(with: .opacity))
                }
            }
            .overlay(alignment: .topLeading) {
                if showTechnical {
                    TechnicalEventsTag()
                        .padding(.top, 8)
                        .padding(.leading, 10)
                }
            }
            // One handler, not two:`visible.last?.id` and `visible.last` change
            // together on every new message, and two separate `onChange`s
            // each calling `proxy.scrollTo` inside the same SwiftUI
            // transaction — cheap on a short list — became a reliable
            // main-thread hang once the conversation was long enough (seen
            // while testing this bar with a pre-populated history).
            .onChange(of: visible.last) { _, newest in
                guard atBottom else { return }
                if let id = newest?.id, id != lastSeenID { lastSeenID = id }
                withAnimation(Theme.ease) {
                    proxy.scrollTo(Self.bottomAnchor, anchor: .bottom)
                }
            }
            .onChange(of: followToken) { _, _ in
                atBottom = true
                withAnimation(Theme.ease) {
                    proxy.scrollTo(Self.bottomAnchor, anchor: .bottom)
                }
            }
            .onChange(of: openPendingIDs) { _, _ in
                // The pinned card appearing or leaving resizes the list:
                // keep the newest message, the one it is about, in view.
                Task { @MainActor in
                    try? await Task.sleep(nanoseconds: 350_000_000)
                    withAnimation(Theme.ease) {
                        proxy.scrollTo(Self.bottomAnchor, anchor: .bottom)
                    }
                }
            }
        }
    }

    /// Loads the older page and puts the row that was at the top back at
    /// the top, so the conversation does not jump under the thumb.
    private func loadKeepingPosition(_ proxy: ScrollViewProxy) async {
        // A display row's id: the first update of a folded run is not on
        // screen under its own id.
        let anchor = display(shown)
            .first { if case .row(.day) = $0 { return false } else { return true } }?.id
        await loadOlder()
        if let anchor {
            proxy.scrollTo(anchor, anchor: .top)
        }
    }

    private static let bottomAnchor = "conversation.bottom"
}

/// "Earlier conversation" at the top of the list: the tappable twin of
/// pull-to-load, for VoiceOver and for anyone who does not pull.
private struct OlderButton: View {
    let loading: Bool
    let action: () -> Void

    var body: some View {
        Button(action: action) {
            HStack(spacing: 6) {
                if loading {
                    ProgressView().controlSize(.small)
                } else {
                    Image(systemName: "arrow.up.circle")
                }
                Text(loading ? "Loading…" : "Earlier conversation")
            }
            .font(.footnote.weight(.medium))
            .foregroundStyle(.secondary)
            .padding(.vertical, 6)
            .frame(maxWidth: .infinity)
        }
        .buttonStyle(.plain)
        .disabled(loading)
        .accessibilityIdentifier("conversation.older")
    }
}

/// One row of the timeline.
struct RowView: View {
    let row: TimelineRow
    let voiceName: String
    let openPendingIDs: Set<String>

    var body: some View {
        switch row {
        case let .day(date):
            DayDivider(date: date)
        case let .divider(_, title):
            NoticeDivider(title: title)
        case let .item(item):
            switch item.kind {
            case .shepherd, .dog:
                SpeechBubble(text: item.text, isShepherd: item.kind == .shepherd, isFinal: true, voiceName: voiceName)
            case .push:
                SystemCard(text: item.text, kind: .push(needsUser: item.needsUser), category: row.category, voiceName: voiceName)
            case .approval:
                ApprovalItemCard(item: item, isPinned: item.pendingID.map { openPendingIDs.contains($0) } ?? false)
            case .card:
                CardRow(card: item.card, text: item.text, at: item.card?.at ?? item.ts)
            case .notice, .unknown:
                NoticeDivider(title: item.text)
            }
        case let .live(bubble):
            BubbleView(bubble: bubble, voiceName: voiceName)
        case let .brain(reply, compact):
            BrainReplyCard(reply: reply, compact: compact, voiceName: voiceName)
        case let .held(push):
            HeldPushCard(push: push)
        case let .card(card):
            CardRow(card: card, text: card.displayTitle, at: card.at ?? Date())
        }
    }
}

/// The per-phone switch in Settings ▸ Health (`@AppStorage`).
enum TechnicalEvents {
    static let key = "showTechnicalEvents"
}

/// "Technical events shown": on top of the conversation while the switch
/// is on, so a screenshot explains itself.
private struct TechnicalEventsTag: View {
    var body: some View {
        Label("Technical events shown", systemImage: TimelineCategory.system.symbol)
            .font(.caption.weight(.semibold))
            .foregroundStyle(Theme.muted)
            .padding(.horizontal, 10)
            .padding(.vertical, 5)
            .background(.regularMaterial, in: Capsule())
            .accessibilityElement(children: .combine)
            .accessibilityIdentifier("conversation.technicalTag")
    }
}

/// A row in the technical view: its original text, muted when it is
/// plumbing, under the exact time and category. A long press copies the
/// row's raw JSON.
struct TechnicalRow: View {
    let row: TimelineRow
    let voiceName: String
    let openPendingIDs: Set<String>

    /// "12:46:03 · System". A card this phone has no time for says only its
    /// category.
    private var caption: String {
        row.date == .distantFuture
            ? row.category.title
            : "\(row.date.formatted(date: .omitted, time: .standard)) · \(row.category.title)"
    }

    private var content: some View {
        VStack(alignment: .leading, spacing: 2) {
            if case .day = row {
                RowView(row: row, voiceName: voiceName, openPendingIDs: openPendingIDs)
            } else {
                Text(caption)
                    .font(.caption2.monospacedDigit())
                    .foregroundStyle(Theme.muted)
                    .padding(.horizontal, 6)
                if case let .divider(item, _) = row {
                    // The daemon's own words, not the divider's tidied title.
                    NoticeDivider(title: item.text)
                } else {
                    RowView(row: row, voiceName: voiceName, openPendingIDs: openPendingIDs)
                }
            }
        }
        .opacity(row.category == .system ? 0.7 : 1)
    }

    var body: some View {
        content
            .contentShape(Rectangle())
            .contextMenu {
                Button("Copy raw JSON", systemImage: "doc.on.doc") { copy() }
            }
            .accessibilityAction(named: "Copy raw JSON") { copy() }
    }

    private func copy() {
        UIPasteboard.general.string = row.rawJSON
    }
}

/// Three or more consecutive agent updates folded into one quiet row:
/// "3 agent updates" and the newest two; a tap lists them all.
struct UpdatesGroup: View {
    let rows: [TimelineRow]
    let isOpen: Bool
    let voiceName: String
    let openPendingIDs: Set<String>
    let toggle: () -> Void

    @Environment(\.dynamicTypeSize) private var dynamicTypeSize

    private var count: Int { ConversationTimeline.updateCount(rows) }
    private var title: String { ConversationTimeline.updatesTitle(count) }
    private var latest: [String] { ConversationTimeline.previewLines(rows) }

    var body: some View {
        VStack(spacing: 8) {
            Button(action: toggle) {
                HStack(alignment: .top, spacing: 10) {
                    Image(systemName: TimelineCategory.outcome.symbol)
                        .font(.subheadline)
                        .foregroundStyle(Theme.muted)
                        .padding(.top, 1)
                        .accessibilityHidden(true)
                    VStack(alignment: .leading, spacing: 1) {
                        Text(title)
                            .font(.footnote.weight(.semibold))
                            .foregroundStyle(Theme.muted)
                            .contentTransition(.numericText())
                            .animation(Theme.ease, value: count)
                        if !isOpen {
                            ForEach(Array(latest.enumerated()), id: \.offset) { _, line in
                                Text(line)
                                    .font(.subheadline)
                                    .foregroundStyle(Theme.ink)
                                    .lineLimit(dynamicTypeSize.isAccessibilitySize ? 2 : 1)
                                    .truncationMode(.tail)
                            }
                        }
                    }
                    Spacer(minLength: 0)
                    Image(systemName: "chevron.down")
                        .font(.caption.weight(.semibold))
                        .foregroundStyle(Theme.muted)
                        .rotationEffect(.degrees(isOpen ? 180 : 0))
                        .padding(.top, 3)
                        .accessibilityHidden(true)
                }
                .padding(.horizontal, 12)
                .padding(.vertical, 10)
                .frame(maxWidth: .infinity, minHeight: 44, alignment: .leading)
                .rowCard()
                .contentShape(Rectangle())
            }
            .buttonStyle(.plain)
            .accessibilityElement(children: .ignore)
            .accessibilityLabel(isOpen || latest.isEmpty ? title : "\(title). Latest: \(latest.joined(separator: ". "))")
            .accessibilityHint(isOpen ? "Hides them" : "Shows them all")
            .accessibilityAddTraits(.isButton)
            .accessibilityIdentifier("conversation.updates")

            if isOpen {
                ForEach(rows) { row in
                    RowView(row: row, voiceName: voiceName, openPendingIDs: openPendingIDs)
                }
                .transition(.opacity)
            }
        }
    }
}

/// What the orchestrator told the voice to say (`to_supervisor`'s result): the
/// headline, and behind a tap the plain explanation the voice reads only
/// when asked, the choices, and the raw payload. Compact under the voice's
/// own bubble when that bubble already says the words.
struct BrainReplyCard: View {
    let reply: BrainActivity.Reply
    let compact: Bool
    let voiceName: String

    @State private var expanded = false

    var body: some View {
        VStack(alignment: .leading, spacing: 6) {
            if !compact {
                HStack(spacing: 6) {
                    Image(systemName: "brain")
                        .accessibilityHidden(true)
                    Text("Orchestrator → \(voiceName)")
                    if reply.clarify { tag("asks you to say more") }
                }
                .font(.caption.weight(.semibold))
                .foregroundStyle(Theme.thinking)
                if reply.pending {
                    Text(reply.spoken.isEmpty ? "Still working on it — the answer follows as a push." : reply.spoken)
                        .font(.footnote)
                        .foregroundStyle(.secondary)
                        .fixedSize(horizontal: false, vertical: true)
                } else {
                    MarkdownText(reply.spoken)
                        .font(.callout)
                        .foregroundStyle(Theme.ink)
                        .fixedSize(horizontal: false, vertical: true)
                        .textSelection(.enabled)
                }
            }
            if !reply.options.isEmpty {
                FlowOptions(options: reply.options)
            }
            if reply.detail != nil || !reply.raw.isEmpty {
                DisclosureGroup(isExpanded: $expanded) {
                    VStack(alignment: .leading, spacing: 8) {
                        if let detail = reply.detail, !reply.pending {
                            MarkdownText(detail)
                                .font(.footnote)
                                .fixedSize(horizontal: false, vertical: true)
                                .textSelection(.enabled)
                                .accessibilityIdentifier("brain.detail")
                        }
                        RawJSONView(json: reply.raw)
                    }
                    .frame(maxWidth: .infinity, alignment: .leading)
                } label: {
                    Label(reply.detail != nil && !reply.pending ? (compact ? "Orchestrator detail" : "Detail") : "Raw", systemImage: "info.circle")
                        .font(.footnote.weight(.semibold))
                }
                .tint(Theme.thinking)
            }
        }
        .padding(.horizontal, compact ? 6 : 12)
        .padding(.vertical, compact ? 0 : 10)
        .frame(maxWidth: .infinity, alignment: .leading)
        // Compact: a quiet line under the voice's own bubble, no frame.
        .modifier(RowCard(enabled: !compact))
        .padding(.trailing, compact ? 44 : 0)
        .accessibilityElement(children: .contain)
        .accessibilityIdentifier("bubble.brain")
    }

    private func tag(_ text: String) -> some View {
        Text(text)
            .font(.caption2.weight(.medium))
            .padding(.horizontal, 6)
            .padding(.vertical, 1)
            .background(Theme.thinkingSoft, in: Capsule())
    }
}

/// The choices an orchestrator reply offered, as quiet chips.
struct FlowOptions: View {
    let options: [String]

    var body: some View {
        ViewThatFits(in: .horizontal) {
            HStack(spacing: 6) { chips }
            VStack(alignment: .leading, spacing: 4) { chips }
        }
        .accessibilityElement(children: .combine)
        .accessibilityLabel("Options: \(options.joined(separator: ", "))")
    }

    private var chips: some View {
        ForEach(Array(options.enumerated()), id: \.offset) { _, option in
            Text(option)
                .font(.footnote)
                .foregroundStyle(Theme.thinking)
                .padding(.horizontal, 10)
                .padding(.vertical, 4)
                .background(Theme.thinkingSoft, in: Capsule())
        }
    }
}

/// A push the voice is holding because nobody is listening (it is said
/// once a client attaches), or one it gave up on after five minutes.
struct HeldPushCard: View {
    let push: BrainActivity.HeldPush

    private var dropped: Bool {
        if case .dropped = push.fate { return true }
        return false
    }

    var body: some View {
        VStack(alignment: .leading, spacing: 4) {
            Label(dropped ? "Dropped — nobody was listening for 5 minutes" : "Waiting for a listener — said when you connect",
                  systemImage: dropped ? "bell.slash" : "hourglass")
                .font(.caption.weight(.semibold))
                .foregroundStyle(dropped ? Color.secondary : Theme.blockedText)
            Text(push.text)
                .font(.footnote)
                .foregroundStyle(dropped ? .secondary : .primary)
                .strikethrough(dropped, color: .secondary)
                .fixedSize(horizontal: false, vertical: true)
        }
        .padding(.horizontal, 14)
        .padding(.vertical, 10)
        .frame(maxWidth: .infinity, alignment: .leading)
        .overlay(
            RoundedRectangle(cornerRadius: 14, style: .continuous)
                .strokeBorder(style: StrokeStyle(lineWidth: 1.25, dash: [5, 4]))
                .foregroundStyle(dropped ? Color(.separator) : Theme.blocked.opacity(0.6))
        )
        .accessibilityElement(children: .combine)
        .accessibilityIdentifier(dropped ? "bubble.pushDropped" : "bubble.pushHeld")
    }
}

/// Raw JSON, monospaced and selectable: reachable, never in the way.
struct RawJSONView: View {
    let json: String

    var body: some View {
        ScrollView(.horizontal, showsIndicators: false) {
            Text(json)
                .font(.caption2.monospaced())
                .foregroundStyle(.secondary)
                .textSelection(.enabled)
                .padding(8)
        }
        .background(Color(.tertiarySystemFill), in: RoundedRectangle(cornerRadius: 8, style: .continuous))
        .accessibilityIdentifier("raw.json")
    }
}

/// "Today", "Yesterday", "Monday 21 September".
struct DayDivider: View {
    let date: Date

    var body: some View {
        Text(ConversationTimeline.dayTitle(date))
            .font(.caption.weight(.semibold))
            .foregroundStyle(Theme.muted)
            .padding(.vertical, 2)
            .frame(maxWidth: .infinity)
            .padding(.top, 6)
            .accessibilityAddTraits(.isHeader)
            .accessibilityIdentifier("divider.day")
    }
}

/// A quiet line across: "Sheepdog restarted · 5:42 PM".
struct NoticeDivider: View {
    let title: String

    var body: some View {
        HStack(spacing: 10) {
            line
            Text(title)
                .font(.caption.weight(.medium))
                .foregroundStyle(Theme.muted)
                .lineLimit(2)
                .multilineTextAlignment(.center)
                .layoutPriority(1)
            line
        }
        .padding(.vertical, 4)
        .accessibilityElement(children: .combine)
        .accessibilityIdentifier("divider.notice")
    }

    private var line: some View {
        Rectangle()
            .fill(Theme.line)
            .frame(height: 1)
            .frame(maxWidth: .infinity)
    }
}

/// An approval as the conversation keeps it: what was asked, and how it
/// ended. The buttons live on the pinned card, in thumb reach.
struct ApprovalItemCard: View {
    let item: ConversationItem
    /// The pinned card above the controls is showing this question.
    let isPinned: Bool

    private var outcome: String? { item.approvalOutcome }
    private var confirmed: Bool { item.resolution == .confirmed }
    private var declined: Bool { confirmed && PendingOption(index: 0, label: item.answer ?? "").isNegative }

    /// "search asked · 9:52".
    private var asked: String {
        let who = item.agent.map { "\($0) asked" } ?? "An agent asked"
        return "\(who) · \(ConversationTimeline.defaultTimeFormatter.string(from: item.ts))"
    }

    var body: some View {
        VStack(alignment: .leading, spacing: 4) {
            Text(asked)
                .font(.caption.weight(.semibold))
                .foregroundStyle(Theme.muted)
            Text(item.text)
                .font(.callout)
                .foregroundStyle(Theme.ink)
                .fixedSize(horizontal: false, vertical: true)
            status
        }
        .padding(.horizontal, 12)
        .padding(.vertical, 10)
        .frame(maxWidth: .infinity, alignment: .leading)
        .rowCard(border: outcome == nil && isPinned ? Theme.blocked : Theme.line)
        // The floating question collapses into this row once settled.
        .modifier(SettledMatch(id: outcome == nil ? nil : item.pendingID))
        .accessibilityElement(children: .combine)
        .accessibilityIdentifier("bubble.approval")
    }

    @ViewBuilder
    private var status: some View {
        if let outcome {
            Label(outcome, systemImage: confirmed ? (declined ? "xmark" : "checkmark") : "clock.badge.xmark")
                .font(.footnote.weight(confirmed ? .semibold : .regular))
                .foregroundStyle(confirmed ? (declined ? Theme.danger : Theme.done) : Theme.muted)
                .fixedSize(horizontal: false, vertical: true)
                .accessibilityIdentifier("approval.outcome")
        } else if isPinned {
            Label("Waiting for your answer — below", systemImage: "arrow.down.circle")
                .font(.footnote.weight(.medium))
                .foregroundStyle(Theme.blockedText)
        } else {
            Label("Not answered here", systemImage: "circle.dashed")
                .font(.footnote)
                .foregroundStyle(Theme.muted)
        }
    }
}

/// A live bubble of this session.
struct BubbleView: View {
    let bubble: ConversationReducer.Bubble
    let voiceName: String

    var body: some View {
        switch bubble.speaker {
        case .shepherd, .sheepdog:
            SpeechBubble(text: bubble.text, isShepherd: bubble.speaker == .shepherd, isFinal: bubble.isFinal, voiceName: voiceName)
        case .system:
            SystemCard(text: bubble.text, kind: bubble.kind, category: ConversationTimeline.classify(bubble).category, voiceName: voiceName)
        }
    }
}

/// The user on the right in the accent colour, the voice on the left.
struct SpeechBubble: View {
    let text: String
    let isShepherd: Bool
    let isFinal: Bool
    let voiceName: String

    var body: some View {
        // The user's words on the right in a warm neutral, the voice's on
        // the left on a card: the accent stays free for the question.
        let shape = UnevenRoundedRectangle(
            topLeadingRadius: 20,
            bottomLeadingRadius: isShepherd ? 20 : 6,
            bottomTrailingRadius: isShepherd ? 6 : 20,
            topTrailingRadius: 20,
            style: .continuous
        )
        HStack {
            if isShepherd { Spacer(minLength: 56) }
            Text(text + (isFinal ? "" : " …"))
                .font(.body)
                .foregroundStyle(Theme.ink)
                .multilineTextAlignment(.leading)
                .fixedSize(horizontal: false, vertical: true)
                .padding(.horizontal, 14)
                .padding(.vertical, 9)
                .background(shape.fill(isShepherd ? Theme.bubble : Theme.card))
                .overlay { if !isShepherd { shape.strokeBorder(Theme.line, lineWidth: 1) } }
                .opacity(isFinal ? 1 : 0.75)
            if !isShepherd { Spacer(minLength: 40) }
        }
        // One element per bubble, read as a sentence: "You said, what's
        // going on." Keeping the words in the label (not the value) is what
        // VoiceOver announces when it moves through the conversation.
        .accessibilityElement(children: .combine)
        .accessibilityLabel("\(isShepherd ? "You" : voiceName) said, \(text)")
        .accessibilityIdentifier(isShepherd ? "bubble.shepherd" : "bubble.sheepdog")
    }
}

/// A push or a local notice: a full-width card, quiet whatever the push's
/// urgency. The amber "needs you" look belongs to the approval card alone:
/// a push that has an open question folds to a pointer to that card
/// (`QuestionPointer`), and one that has none is news, not an alarm
/// (review B3).
struct SystemCard: View {
    let text: String
    let kind: ConversationReducer.Bubble.Kind
    /// Picks the icon: one per category.
    let category: TimelineCategory
    let voiceName: String

    var body: some View {
        let isPush: Bool = {
            if case .push = kind { return true }
            return false
        }()

        HStack(alignment: .top, spacing: 10) {
            Image(systemName: category.symbol)
                .font(.subheadline)
                .foregroundStyle(Theme.muted)
                .padding(.top, 1)
                .accessibilityHidden(true)
            Text(text)
                .font(.subheadline)
                .foregroundStyle(Theme.ink)
                .fixedSize(horizontal: false, vertical: true)
            Spacer(minLength: 0)
        }
        .padding(.horizontal, 12)
        .padding(.vertical, 10)
        .frame(maxWidth: .infinity, alignment: .leading)
        .rowCard()
        .accessibilityElement(children: .combine)
        .accessibilityLabel("\(isPush ? "\(voiceName) says" : "Notice"), \(text)")
        .accessibilityIdentifier(isPush ? "bubble.push" : "bubble.notice")
    }
}

/// A row about the question pinned on the approval card, folded to one
/// line that points down at the card (review B1).
struct QuestionPointer: View {
    let row: TimelineRow

    private var text: String {
        switch row {
        case let .item(item):
            if item.kind == .approval { return "\(item.agent ?? "An agent") asked — answer below" }
            return item.text
        case let .live(bubble): return bubble.text
        default: return ""
        }
    }

    var body: some View {
        HStack(spacing: 6) {
            Image(systemName: TimelineCategory.needsYou.symbol)
                .foregroundStyle(Theme.blockedText)
            Text(text)
                .foregroundStyle(Theme.muted)
                .lineLimit(1)
                .truncationMode(.tail)
        }
        .font(.footnote)
        .frame(maxWidth: .infinity, alignment: .leading)
        .padding(.horizontal, 6)
        .accessibilityElement(children: .combine)
        .accessibilityLabel("\(text). The question is on the card below.")
        .accessibilityIdentifier("bubble.pointer")
    }
}

struct EmptyConversation: View {
    let voiceName: String
    /// Sends an example as if typed; nil when there is no daemon to ask.
    var ask: ((String) -> Void)?

    private static let examples = ["What's going on?", "Is anyone stuck?"]

    var body: some View {
        VStack(spacing: 12) {
            MascotCard(name: "MascotListening", width: 150)
                .frame(height: 167)
            Text("Just talk")
                .font(.title.weight(.bold))
                .fontDesign(.rounded)
                .foregroundStyle(Theme.ink)
                .padding(.top, 6)
            Text("Ask what your agents are doing, or tell one what to do next.")
                .font(.body)
                .foregroundStyle(Theme.muted)
                .multilineTextAlignment(.center)
                .fixedSize(horizontal: false, vertical: true)
            if let ask {
                OptionFlow(spacing: 8) {
                    ForEach(Self.examples, id: \.self) { example in
                        Button { ask(example) } label: {
                            Text(example)
                                .font(.subheadline)
                                .foregroundStyle(Theme.ink)
                                .padding(.horizontal, 14)
                                .frame(minHeight: 44)
                                .background(Capsule().fill(Theme.card))
                                .overlay(Capsule().strokeBorder(Theme.line, lineWidth: 1))
                        }
                        .buttonStyle(.plain)
                        .accessibilityHint("Asks \(voiceName)")
                    }
                }
                .fixedSize()
                .padding(.top, 6)
                .accessibilityIdentifier("conversation.examples")
            }
        }
        .padding(.horizontal, 32)
        .frame(maxWidth: .infinity)
        .accessibilityElement(children: .contain)
        .accessibilityIdentifier("conversation.empty")
    }
}

/// The conversation's card look (canvas): white on the warm surface, a
/// hairline, radius 14. The border can carry a state (an open question's
/// amber).
struct RowCard: ViewModifier {
    var enabled = true
    var border: Color = Theme.line

    func body(content: Content) -> some View {
        if enabled {
            content
                .background(RoundedRectangle(cornerRadius: Theme.Radius.card, style: .continuous).fill(Theme.card))
                .overlay(RoundedRectangle(cornerRadius: Theme.Radius.card, style: .continuous).strokeBorder(border, lineWidth: 1))
        } else {
            content
        }
    }
}

extension View {
    func rowCard(border: Color = Theme.line) -> some View {
        modifier(RowCard(border: border))
    }
}

/// `matchedInMotion` for a settled approval row with a question id.
private struct SettledMatch: ViewModifier {
    let id: String?

    func body(content: Content) -> some View {
        if let id {
            content.matchedInMotion(id: id)
        } else {
            content
        }
    }
}
