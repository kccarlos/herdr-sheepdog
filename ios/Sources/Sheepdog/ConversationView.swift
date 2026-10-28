import SheepdogKit
import SwiftUI

/// The conversation: the user on the right and the voice on the left in
/// bubbles, what agents did as full-width ledger lines, the open question as
/// a one-line pointer to the card floating above the capsule, and quiet
/// dividers for days and restarts. The history loaded from the daemon (or
/// the cache) sits above this session's live bubbles; pulling down at the
/// top loads the page before it. How each row looks is in
/// ConversationRows.swift and ConversationRowStyle.swift.
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
    /// When "Catch me up" was tapped, until the voice starts answering.
    @State private var catchingUp: Date?
    @Environment(\.horizontalSizeClass) private var sizeClass

    /// How long "Asking…" waits for the voice before it gives up.
    private static let catchUpTimeout: Duration = .seconds(20)

    /// The list as drawn: without the plumbing unless asked for, the
    /// neighbours' decisions applied, runs of updates folded. The technical
    /// view is every row, ungrouped.
    private var layout: ConversationLayout {
        ConversationTimeline.layout(rows, showTechnical: showTechnical, keepVisible: pointerIDs)
    }

    private func hasContent(_ display: [ConversationDisplayRow]) -> Bool {
        display.contains { if case .row(.day) = $0 { return false } else { return true } }
    }

    /// One day's rows under its sticky heading.
    private struct DaySection: Identifiable {
        var id: String
        var day: Date?
        var rows: [ConversationDisplayRow]
    }

    private static func sections(_ rows: [ConversationDisplayRow]) -> [DaySection] {
        var out: [DaySection] = []
        for display in rows {
            if case let .row(.day(date)) = display {
                out.append(DaySection(id: display.id, day: date, rows: []))
            } else if out.isEmpty {
                out.append(DaySection(id: "start", day: nil, rows: [display]))
            } else {
                out[out.count - 1].rows.append(display)
            }
        }
        return out
    }

    var body: some View {
        let layout = layout
        // One pointer per question: the push and the voice's restatement
        // fold into the approval's.
        let pointers = showTechnical ? [:] : QuestionFocus.pointerLines(rows: rows, pointerIDs: pointerIDs)
        let display = layout.rows.filter { row in
            guard !showTechnical, case let .row(r) = row, pointerIDs.contains(r.id) else { return true }
            return pointers[r.id] != nil
        }
        let positions = showTechnical ? [:] : ConversationTimeline.ledgerPositions(display, excluding: pointerIDs)
        let gaps = Self.gaps(display, positions: positions, technical: showTechnical)
        let sections = Self.sections(display)
        ScrollViewReader { proxy in
            ScrollView {
                LazyVStack(spacing: 0, pinnedViews: [.sectionHeaders]) {
                    if canLoadOlder {
                        OlderButton(loading: loadingOlder) {
                            Task { await loadKeepingPosition(proxy) }
                        }
                    }
                    if !hasContent(layout.rows) {
                        EmptyConversation(voiceName: voiceName, ask: ask)
                            .padding(.top, 60)
                    }
                    // Runs of the agents' updates fold into one "N agent
                    // updates" row; what was said stays in the open.
                    ForEach(Array(sections.enumerated()), id: \.element.id) { index, section in
                        SwiftUI.Section {
                            ForEach(section.rows) { row in
                                rowView(row, layout: layout, pointers: pointers)
                                    .environment(\.ledgerPosition, positions[row.id] ?? .single)
                                    .padding(.top, gaps[row.id] ?? 0)
                                    .id(row.id)
                            }
                        } header: {
                            if let day = section.day {
                                DayDivider(date: day)
                                    .id(section.id)
                            }
                        } footer: {
                            // The gap before the next day lives here, not in
                            // the next heading, so a heading sits at the same
                            // depth pinned and at rest.
                            if index < sections.count - 1 { Color.clear.frame(height: 8) }
                        }
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
                                lastSeenID = layout.rows.last?.id
                            }
                        }
                        .onDisappear {
                            DispatchQueue.main.async { atBottom = false }
                        }
                }
                // The iPad reads in a column, not edge to edge.
                .frame(maxWidth: sizeClass == .regular ? 680 : .infinity)
                .frame(maxWidth: .infinity)
                .padding(.horizontal, 16)
                // The top 12 pt live inside the first day heading (its own
                // inset), so a pinned heading covers the strip above it
                // without reaching up over the row above it when it is not.
                .padding(.top, sections.first?.day == nil ? 12 : 0)
                .padding(.bottom, 12)
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
                if !atBottom, visibleHeight > 160, let newest = layout.rows.last?.id, newest != lastSeenID {
                    Button {
                        withAnimation { proxy.scrollTo(Self.bottomAnchor, anchor: .bottom) }
                    } label: {
                        Label("Jump to latest", systemImage: "arrow.down")
                            .font(.footnote.weight(.semibold))
                            .foregroundStyle(Theme.ink)
                            .padding(.horizontal, 14)
                            .frame(minHeight: 36)
                            .floatingPill()
                            .frame(minHeight: 44)
                            .contentShape(Rectangle())
                    }
                    .buttonStyle(.plain)
                    .padding(.bottom, 6)
                    .transition(.move(edge: .bottom).combined(with: .opacity))
                }
            }
            .overlay(alignment: .top) {
                if showTechnical {
                    TechnicalEventsTag()
                        .padding(.top, 8)
                }
            }
            // One handler, not two:`layout.rows.last?.id` and `layout.rows.last` change
            // together on every new message, and two separate `onChange`s
            // each calling `proxy.scrollTo` inside the same SwiftUI
            // transaction — cheap on a short list — became a reliable
            // main-thread hang once the conversation was long enough (seen
            // while testing this bar with a pre-populated history).
            .onChange(of: layout.rows.last) { _, newest in
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
            // "Catch me up" is answered when the voice (or the
            // orchestrator) says something after the tap.
            .onChange(of: rows.last?.id) { _, _ in
                if let since = catchingUp, ConversationTimeline.voiceAnswered(rows, since: since) { catchingUp = nil }
            }
            .task(id: catchingUp) {
                guard let since = catchingUp else { return }
                try? await Task.sleep(for: Self.catchUpTimeout)
                if catchingUp == since { catchingUp = nil }
            }
        }
    }

    /// One row, drawn by its kind.
    @ViewBuilder
    private func rowView(_ display: ConversationDisplayRow, layout: ConversationLayout, pointers: [String: String]) -> some View {
        switch display {
        case let .row(row):
            if showTechnical {
                TechnicalRow(row: row, voiceName: voiceName, openPendingIDs: openPendingIDs)
            } else if let agent = pointers[row.id] {
                QuestionPointer(agent: agent)
            } else {
                RowView(row: row, voiceName: voiceName, openPendingIDs: openPendingIDs, attached: layout.attached[row.id] ?? [])
            }
        case let .updates(id, members):
            UpdatesGroup(
                rows: members,
                isOpen: openGroups.contains(id),
                voiceName: voiceName,
                openPendingIDs: openPendingIDs,
                catchMeUp: ask.map { send in { text in startCatchUp(text, send: send) } },
                asking: catchingUp != nil
            ) {
                withAnimation(Theme.ease) {
                    if openGroups.contains(id) { openGroups.remove(id) } else { openGroups.insert(id) }
                }
            }
        }
    }

    /// The space above each row, by its id.
    private static func gaps(_ rows: [ConversationDisplayRow], positions: [String: LedgerPosition], technical: Bool) -> [String: CGFloat] {
        var out: [String: CGFloat] = [:]
        for (previous, row) in zip(rows, rows.dropFirst()) {
            let joined = positions[previous.id]?.joinsBelow == true && positions[row.id]?.joinsAbove == true
            out[row.id] = ConversationTimeline.gap(from: previous, to: row, joined: joined, technical: technical)
        }
        return out
    }

    private func startCatchUp(_ text: String, send: (String) -> Void) {
        guard catchingUp == nil else { return }
        catchingUp = Date()
        send(text)
    }

    /// Loads the older page and puts the row that was at the top back at
    /// the top, so the conversation does not jump under the thumb.
    private func loadKeepingPosition(_ proxy: ScrollViewProxy) async {
        // A display row's id: the first update of a folded run is not on
        // screen under its own id.
        let anchor = layout.rows
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
            .foregroundStyle(Theme.muted)
            .frame(maxWidth: .infinity, minHeight: 44)
            .contentShape(Rectangle())
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
    /// The rows folded into this one (`ConversationLayout.attached`): the
    /// orchestrator's replies under the voice's bubble, the push and the
    /// voice's restatement under a settled question.
    var attached: [TimelineRow] = []
    /// The technical view: every row as the daemon sent it, nothing
    /// shortened.
    var technical = false

    var body: some View {
        switch row {
        case let .day(date):
            DayDivider(date: date, topInset: 0)
        case let .divider(item, title):
            NoticeDivider(title: title, symbol: DaemonNotice(item).symbol)
        case let .item(item):
            switch item.kind {
            case .shepherd:
                SpeechBubble(text: item.text, isShepherd: true, isFinal: true, voiceName: voiceName, typed: item.via == "typed", at: item.ts)
            case .dog:
                SpeechBubble(text: item.text, isShepherd: false, isFinal: true, voiceName: voiceName, written: item.isWritten, at: item.ts)
                    .detailsUnder(attached, voiceName: voiceName)
            case .push:
                SystemCard(text: item.text, kind: .push(needsUser: item.needsUser), voiceName: voiceName, at: item.ts)
            case .approval:
                if item.approvalOutcome != nil, !technical {
                    SettledQuestionLine(item: item, said: attached, voiceName: voiceName, openPendingIDs: openPendingIDs)
                } else {
                    ApprovalItemCard(item: item, isPinned: item.pendingID.map { openPendingIDs.contains($0) } ?? false)
                }
            case .card:
                CardRow(card: item.card, text: item.text, at: item.card?.at ?? item.ts)
            case .notice, .unknown:
                NoticeDivider(title: item.text, symbol: DaemonNotice(item).symbol)
            }
        case let .live(bubble):
            BubbleView(bubble: bubble, voiceName: voiceName, attached: attached, technical: technical)
        case let .brain(reply, compact):
            // The technical view never folds a reply into its bubble.
            BrainReplyCard(reply: reply, compact: compact && !technical, voiceName: voiceName)
        case let .held(push):
            if technical {
                HeldPushCard(push: push)
            } else {
                MissedPushRow(push: push)
            }
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
        Label("Technical events shown · long press copies JSON", systemImage: TimelineCategory.system.symbol)
            .font(.caption.weight(.semibold))
            .foregroundStyle(Theme.muted)
            .multilineTextAlignment(.leading)
            .padding(.horizontal, 11)
            .padding(.vertical, 5)
            .floatingPill()
            .padding(.horizontal, 16)
            .accessibilityElement(children: .combine)
            .accessibilityIdentifier("conversation.technicalTag")
    }
}

/// A row in the technical view: a 62 pt gutter with the exact time and the
/// category's code, then the row (plumbing monospaced and muted). A long
/// press copies the row's raw JSON.
struct TechnicalRow: View {
    let row: TimelineRow
    let voiceName: String
    let openPendingIDs: Set<String>

    @ScaledMetric(relativeTo: .caption2) private var gutter: CGFloat = 62

    private static let timeFormatter: DateFormatter = {
        let f = DateFormatter()
        f.dateFormat = "HH:mm:ss"
        return f
    }()

    /// A card this phone has no time for has none.
    private var time: String { row.date == .distantFuture ? "" : Self.timeFormatter.string(from: row.date) }

    @ViewBuilder
    private var content: some View {
        switch row {
        case .day:
            RowView(row: row, voiceName: voiceName, openPendingIDs: openPendingIDs, technical: true)
        case let .divider(item, _):
            // The daemon's own words, not the divider's tidied title.
            let notice = DaemonNotice(item)
            if row.category == .system {
                PlumbingLine(text: item.text, symbol: notice.symbol ?? "gearshape")
            } else {
                NoticeDivider(title: item.text, symbol: notice.symbol)
            }
        default:
            RowView(row: row, voiceName: voiceName, openPendingIDs: openPendingIDs, technical: true)
        }
    }

    private var gutterView: some View {
        VStack(alignment: .trailing, spacing: 2) {
            Text(time).font(.caption2.monospacedDigit())
            Text(row.category.code)
                .font(.system(size: 9.5, weight: .semibold, design: .monospaced))
                .padding(.horizontal, 4)
                .overlay(RoundedRectangle(cornerRadius: 4, style: .continuous).strokeBorder(Theme.line, lineWidth: 1))
        }
        .foregroundStyle(Theme.muted)
        .padding(.top, 3)
        .padding(.trailing, 8)
        .frame(width: gutter, alignment: .trailing)
        .overlay(alignment: .trailing) { Rectangle().fill(Theme.line).frame(width: 1) }
        .accessibilityElement(children: .ignore)
        .accessibilityLabel(time.isEmpty ? row.category.title : "\(time), \(row.category.title)")
    }

    var body: some View {
        Group {
            if case .day = row {
                content
            } else {
                HStack(alignment: .top, spacing: 8) {
                    gutterView
                    content.frame(maxWidth: .infinity, alignment: .leading)
                }
            }
        }
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
