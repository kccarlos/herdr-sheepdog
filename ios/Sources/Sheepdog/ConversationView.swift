import SheepdogKit
import SwiftUI

/// The conversation: the shepherd on the right, the dog on the left, the
/// daemon's pushes and approvals as full-width cards, and quiet dividers
/// for days and restarts. The history loaded from the daemon (or the
/// cache) sits above this session's live bubbles; pulling down at the top
/// loads the page before it.
struct ConversationView: View {
    let rows: [TimelineRow]
    let voiceName: String
    var canLoadOlder = false
    var loadingOlder = false
    /// The pending id the pinned approval card is showing, if any.
    var activePendingID: String?
    /// Rows that fold to a one-line pointer while their question is
    /// pinned on the approval card (`QuestionFocus`).
    var pointerIDs: Set<String> = []
    var loadOlder: () async -> Void = {}
    /// Changes when the shepherd typed something: follow the conversation
    /// to its end even if it was scrolled up (the keyboard shrinks it).
    var followToken = 0

    @State private var atBottom = true
    @State private var lastSeenID: String?

    private var hasContent: Bool {
        rows.contains { if case .day = $0 { return false } else { return true } }
    }

    var body: some View {
        ScrollViewReader { proxy in
            ScrollView {
                LazyVStack(spacing: 10) {
                    if canLoadOlder {
                        OlderButton(loading: loadingOlder) {
                            Task { await loadKeepingPosition(proxy) }
                        }
                    }
                    if !hasContent {
                        EmptyConversation(voiceName: voiceName)
                            .padding(.top, 28)
                    }
                    ForEach(rows) { row in
                        Group {
                            if pointerIDs.contains(row.id) {
                                QuestionPointer(row: row)
                            } else {
                                RowView(row: row, voiceName: voiceName, activePendingID: activePendingID)
                            }
                        }
                            .id(row.id)
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
                                lastSeenID = rows.last?.id
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
            .refreshable {
                // Pulling down at the top is "show me what came before".
                guard canLoadOlder else { return }
                await loadKeepingPosition(proxy)
            }
            .overlay(alignment: .bottom) {
                if !atBottom, let newest = rows.last?.id, newest != lastSeenID {
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
            // One handler, not two: `rows.last?.id` and `rows.last` change
            // together on every new message, and two separate `onChange`s
            // each calling `proxy.scrollTo` inside the same SwiftUI
            // transaction — cheap on a short list — became a reliable
            // main-thread hang once the conversation was long enough (seen
            // while testing this bar with a pre-populated history).
            .onChange(of: rows.last) { _, newest in
                guard atBottom else { return }
                if let id = newest?.id, id != lastSeenID { lastSeenID = id }
                withAnimation(.easeOut(duration: 0.18)) {
                    proxy.scrollTo(Self.bottomAnchor, anchor: .bottom)
                }
            }
            .onChange(of: followToken) { _, _ in
                atBottom = true
                withAnimation(.easeOut(duration: 0.18)) {
                    proxy.scrollTo(Self.bottomAnchor, anchor: .bottom)
                }
            }
            .onChange(of: activePendingID) { _, _ in
                // The pinned card appearing or leaving resizes the list:
                // keep the newest message, the one it is about, in view.
                Task { @MainActor in
                    try? await Task.sleep(nanoseconds: 350_000_000)
                    withAnimation(.easeOut(duration: 0.2)) {
                        proxy.scrollTo(Self.bottomAnchor, anchor: .bottom)
                    }
                }
            }
        }
    }

    /// Loads the older page and puts the row that was at the top back at
    /// the top, so the conversation does not jump under the thumb.
    private func loadKeepingPosition(_ proxy: ScrollViewProxy) async {
        let anchor = rows.first { if case .day = $0 { return false } else { return true } }?.id
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
    let activePendingID: String?

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
                SystemCard(text: item.text, kind: .push(needsUser: item.needsUser), voiceName: voiceName)
            case .approval:
                ApprovalItemCard(item: item, isPinned: item.pendingID != nil && item.pendingID == activePendingID)
            case .notice, .unknown:
                NoticeDivider(title: item.text)
            }
        case let .live(bubble):
            BubbleView(bubble: bubble, voiceName: voiceName)
        case let .brain(reply, compact):
            BrainReplyCard(reply: reply, compact: compact, voiceName: voiceName)
        case let .held(push):
            HeldPushCard(push: push)
        }
    }
}

/// What the brain told the voice to say (`to_supervisor`'s result): the
/// headline, and behind a tap the plain explanation the voice reads only
/// when asked, the choices, and the raw payload. Compact under the dog's
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
                    Text("Brain → \(voiceName)")
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
                    Text(reply.detail != nil && !reply.pending ? "Detail" : "Raw")
                        .font(.footnote.weight(.medium))
                }
                .tint(Theme.thinking)
            }
        }
        .padding(.horizontal, 12)
        .padding(.vertical, compact ? 6 : 10)
        .frame(maxWidth: .infinity, alignment: .leading)
        .background(
            RoundedRectangle(cornerRadius: 14, style: .continuous)
                .fill(Theme.thinking.opacity(compact ? 0.05 : 0.08))
        )
        .overlay(alignment: .leading) {
            Rectangle().fill(Theme.thinking.opacity(0.6)).frame(width: 3).padding(.vertical, 8)
        }
        .padding(.trailing, compact ? 44 : 0)
        .accessibilityElement(children: .contain)
        .accessibilityIdentifier("bubble.brain")
    }

    private func tag(_ text: String) -> some View {
        Text(text)
            .font(.caption2.weight(.medium))
            .padding(.horizontal, 6)
            .padding(.vertical, 1)
            .background(Theme.thinking.opacity(0.15), in: Capsule())
    }
}

/// The choices a brain reply offered, as quiet chips.
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
                .font(.caption.weight(.medium))
                .padding(.horizontal, 8)
                .padding(.vertical, 3)
                .background(Color(.tertiarySystemFill), in: Capsule())
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
            .foregroundStyle(.secondary)
            .padding(.horizontal, 10)
            .padding(.vertical, 3)
            .background(Color(.tertiarySystemFill), in: Capsule())
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
                .font(.caption2)
                .foregroundStyle(.secondary)
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
            .fill(Color(.separator))
            .frame(height: 0.5)
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

    var body: some View {
        VStack(alignment: .leading, spacing: 6) {
            HStack(spacing: 6) {
                Image(systemName: "hand.raised.fill")
                    .font(.caption)
                    .foregroundStyle(outcome == nil && isPinned ? Theme.blocked : .secondary)
                Text(item.agent.map { "\($0) asked" } ?? "An agent asked")
                    .font(.caption.weight(.semibold))
                    .foregroundStyle(.secondary)
            }
            Text(item.text)
                .font(.callout)
                .fixedSize(horizontal: false, vertical: true)
            status
        }
        .padding(.horizontal, 14)
        .padding(.vertical, 11)
        .frame(maxWidth: .infinity, alignment: .leading)
        .background(
            RoundedRectangle(cornerRadius: 14, style: .continuous)
                .fill(outcome == nil && isPinned ? Theme.blocked.opacity(0.10) : Color(.tertiarySystemBackground))
        )
        .overlay(
            RoundedRectangle(cornerRadius: 14, style: .continuous)
                .strokeBorder(outcome == nil && isPinned ? Theme.blocked.opacity(0.45) : .clear, lineWidth: 1)
        )
        .accessibilityElement(children: .combine)
        .accessibilityIdentifier("bubble.approval")
    }

    @ViewBuilder
    private var status: some View {
        if let outcome {
            Label(outcome, systemImage: confirmed ? (declined ? "xmark.circle.fill" : "checkmark.circle.fill") : "clock.badge.xmark")
                .font(.footnote.weight(confirmed ? .semibold : .regular))
                .foregroundStyle(confirmed ? (declined ? Color(.systemRed) : Theme.done) : .secondary)
                .accessibilityIdentifier("approval.outcome")
        } else if isPinned {
            Label("Waiting for your answer — below", systemImage: "arrow.down.circle")
                .font(.footnote.weight(.medium))
                .foregroundStyle(Theme.blockedText)
        } else {
            Label("Not answered here", systemImage: "circle.dashed")
                .font(.footnote)
                .foregroundStyle(.secondary)
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
            SystemCard(text: bubble.text, kind: bubble.kind, voiceName: voiceName)
        }
    }
}

/// The shepherd on the right in the accent colour, the dog on the left.
struct SpeechBubble: View {
    let text: String
    let isShepherd: Bool
    let isFinal: Bool
    let voiceName: String

    var body: some View {
        HStack {
            if isShepherd { Spacer(minLength: 44) }
            Text(text + (isFinal ? "" : " …"))
                .font(.body)
                .multilineTextAlignment(.leading)
                .fixedSize(horizontal: false, vertical: true)
                .padding(.horizontal, 14)
                .padding(.vertical, 10)
                .background(
                    isShepherd ? Color.accentColor : Color(.secondarySystemBackground),
                    in: RoundedRectangle(cornerRadius: 18, style: .continuous)
                )
                .foregroundStyle(isShepherd ? Color.white : Color.primary)
                .frame(maxWidth: 300, alignment: isShepherd ? .trailing : .leading)
            if !isShepherd { Spacer(minLength: 44) }
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
    let voiceName: String

    var body: some View {
        let isPush: Bool = {
            if case .push = kind { return true }
            return false
        }()

        HStack(alignment: .top, spacing: 10) {
            Image(systemName: isPush ? "bell.fill" : "info.circle")
                .font(.footnote)
                .foregroundStyle(Theme.idle)
                .padding(.top, 2)
            Text(text)
                .font(.footnote)
                .foregroundStyle(.secondary)
                .fixedSize(horizontal: false, vertical: true)
            Spacer(minLength: 0)
        }
        .padding(.horizontal, 14)
        .padding(.vertical, 11)
        .frame(maxWidth: .infinity, alignment: .leading)
        .background(
            RoundedRectangle(cornerRadius: 14, style: .continuous)
                .fill(Color(.tertiarySystemBackground))
        )
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
            Image(systemName: "arrow.down.circle")
                .foregroundStyle(Theme.blockedText)
            Text(text)
                .foregroundStyle(.secondary)
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

    var body: some View {
        VStack(spacing: 8) {
            Image(systemName: "bubble.left.and.bubble.right")
                .font(.title2)
                .foregroundStyle(.tertiary)
            Text("Nothing said yet")
                .font(.subheadline.weight(.medium))
                .foregroundStyle(.secondary)
            Text("Connect and talk to \(voiceName). What you say and what it answers shows up here.")
                .font(.footnote)
                .foregroundStyle(.tertiary)
                .multilineTextAlignment(.center)
                .padding(.horizontal, 28)
        }
        .frame(maxWidth: .infinity)
    }
}
