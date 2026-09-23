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
    var loadOlder: () async -> Void = {}

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
                        RowView(row: row, voiceName: voiceName, activePendingID: activePendingID)
                            .id(row.id)
                    }
                    Color.clear
                        .frame(height: 1)
                        .id(Self.bottomAnchor)
                        .onAppear { atBottom = true }
                        .onDisappear { atBottom = false }
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
            .onChange(of: rows.last?.id) { _, newest in
                guard let newest else { return }
                if atBottom {
                    lastSeenID = newest
                    withAnimation(.easeOut(duration: 0.18)) {
                        proxy.scrollTo(Self.bottomAnchor, anchor: .bottom)
                    }
                }
            }
            .onChange(of: rows.last) { _, _ in
                // An item updated in place (an approval answered) keeps
                // the same id; follow it too while at the bottom.
                if atBottom { proxy.scrollTo(Self.bottomAnchor, anchor: .bottom) }
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
            .onChange(of: atBottom) { _, isBottom in
                if isBottom { lastSeenID = rows.last?.id }
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
        }
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
                    .foregroundStyle(outcome == nil ? Theme.blocked : .secondary)
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
                .fill(outcome == nil ? Theme.blocked.opacity(0.10) : Color(.tertiarySystemBackground))
        )
        .overlay(
            RoundedRectangle(cornerRadius: 14, style: .continuous)
                .strokeBorder(outcome == nil ? Theme.blocked.opacity(0.45) : .clear, lineWidth: 1)
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
                .foregroundStyle(Theme.blocked)
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

/// A push or a local notice: a full-width card.
struct SystemCard: View {
    let text: String
    let kind: ConversationReducer.Bubble.Kind
    let voiceName: String

    var body: some View {
        let needsUser: Bool = {
            if case let .push(needs) = kind { return needs }
            return false
        }()
        let isPush: Bool = {
            if case .push = kind { return true }
            return false
        }()
        let tint: Color = needsUser ? Theme.blocked : Theme.idle

        HStack(alignment: .top, spacing: 10) {
            Image(systemName: needsUser ? "hand.raised.fill" : (isPush ? "bell.fill" : "info.circle"))
                .font(.footnote)
                .foregroundStyle(tint)
                .padding(.top, 2)
            Text(text)
                .font(needsUser ? .callout.weight(.medium) : .footnote)
                .foregroundStyle(needsUser ? .primary : .secondary)
                .fixedSize(horizontal: false, vertical: true)
            Spacer(minLength: 0)
        }
        .padding(.horizontal, 14)
        .padding(.vertical, 11)
        .frame(maxWidth: .infinity, alignment: .leading)
        .background(
            RoundedRectangle(cornerRadius: 14, style: .continuous)
                .fill(needsUser ? tint.opacity(0.12) : Color(.tertiarySystemBackground))
        )
        .overlay(
            RoundedRectangle(cornerRadius: 14, style: .continuous)
                .strokeBorder(needsUser ? tint.opacity(0.5) : .clear, lineWidth: 1.5)
        )
        .accessibilityElement(children: .combine)
        .accessibilityLabel("\(needsUser ? "Needs your decision" : (isPush ? "\(voiceName) says" : "Notice")), \(text)")
        .accessibilityIdentifier(isPush ? "bubble.push" : "bubble.notice")
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
