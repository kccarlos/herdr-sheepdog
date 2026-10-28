import SheepdogKit
import SwiftUI

// The conversation's row views: dialogue, what needs you, agent outcomes
// (the ledger) and context. What decides a row's kind is
// `ConversationTimeline.classify`; how it looks is in
// ConversationRowStyle.swift.

private func clock(_ date: Date) -> String { ConversationTimeline.defaultTimeFormatter.string(from: date) }

// MARK: - Dialogue

/// The user on the right in a warm neutral, the voice on the left on a
/// card: the accent stays free for the question. Only dialogue is a bubble.
struct SpeechBubble: View {
    let text: String
    let isShepherd: Bool
    let isFinal: Bool
    let voiceName: String
    /// The user typed it rather than said it (a "Typed" caption).
    var typed = false
    /// The text router answered: the voice wrote it, did not say it.
    var written = false
    /// When it was said, for the long press.
    var at: Date?

    @Environment(\.dynamicTypeSize) private var dynamicTypeSize
    @Environment(\.horizontalSizeClass) private var sizeClass

    /// Most of the row a bubble may take: 80 % yours, 86 % the voice's, 70 %
    /// on the iPad, all of it at the accessibility sizes.
    private var fraction: CGFloat {
        if dynamicTypeSize.isAccessibilitySize { return 1 }
        if sizeClass == .regular { return 0.7 }
        return isShepherd ? 0.8 : 0.86
    }

    private var label: String {
        if isShepherd { return typed ? "You typed, \(text)" : (isFinal ? "You said, \(text)" : "You are saying, \(text)") }
        if written { return "\(voiceName) wrote, \(text)" }
        return isFinal ? "\(voiceName) said, \(text)" : "\(voiceName) is saying, \(text)"
    }

    var body: some View {
        let ax = dynamicTypeSize.isAccessibilitySize
        let shape = bubbleShape(you: isShepherd)
        BubbleWidth(fraction: fraction, trailing: isShepherd) {
            VStack(alignment: isShepherd ? .trailing : .leading, spacing: 3) {
                (Text(text) + (isFinal ? Text("") : Text(" •••").foregroundStyle(Theme.muted)))
                    .font(.body)
                    .foregroundStyle(Theme.ink)
                    .multilineTextAlignment(.leading)
                    .fixedSize(horizontal: false, vertical: true)
                    .padding(.horizontal, ax ? 16 : 14)
                    .padding(.vertical, ax ? 12 : 9)
                    .background(shape.fill(isShepherd ? Theme.bubble : Theme.card))
                    .overlay { if !isShepherd { shape.strokeBorder(Theme.line, lineWidth: 1) } }
                    .opacity(isFinal ? 1 : 0.72)
                if typed {
                    Label("Typed", systemImage: "keyboard")
                        .font(.caption2)
                        .foregroundStyle(Theme.muted)
                        .padding(.horizontal, 6)
                }
            }
        }
        .contextMenu {
            Button("Copy", systemImage: "doc.on.doc") { UIPasteboard.general.string = text }
            if let at {
                Button(clock(at), systemImage: "clock") {}.disabled(true)
            }
        }
        // One element per bubble, read as a sentence: "You said, what's
        // going on." Keeping the words in the label (not the value) is what
        // VoiceOver announces when it moves through the conversation.
        .accessibilityElement(children: .combine)
        .accessibilityLabel(label)
        .accessibilityIdentifier(isShepherd ? "bubble.shepherd" : "bubble.sheepdog")
    }
}

/// A live bubble of this session.
struct BubbleView: View {
    let bubble: ConversationReducer.Bubble
    let voiceName: String
    /// The orchestrator's replies folded under the voice's bubble.
    var attached: [TimelineRow] = []
    var technical = false

    var body: some View {
        switch bubble.speaker {
        case .shepherd:
            SpeechBubble(text: bubble.text, isShepherd: true, isFinal: bubble.isFinal, voiceName: voiceName, at: bubble.at)
        case .sheepdog:
            SpeechBubble(text: bubble.text, isShepherd: false, isFinal: bubble.isFinal, voiceName: voiceName, at: bubble.at)
                .detailsUnder(attached, voiceName: voiceName)
        case .system:
            let notice = LocalNotice(text: bubble.text)
            if case let .talkingTo(name) = notice, bubble.kind == .notice {
                // Where the conversation is now: a divider, with the time.
                NoticeDivider(title: "Talking to \(name) · \(clock(bubble.at))", symbol: "desktopcomputer")
            } else if technical, bubble.kind == .notice, !isTimelineNotice(notice) {
                PlumbingLine(text: bubble.text, symbol: notice.symbol)
            } else {
                SystemCard(text: bubble.text, kind: bubble.kind, voiceName: voiceName, at: bubble.at)
            }
        }
    }

    /// The notices the default view lists: where you are, and any it does
    /// not recognise.
    private func isTimelineNotice(_ notice: LocalNotice) -> Bool {
        switch notice {
        case .talkingTo, .unrecognised: return true
        default: return false
        }
    }
}

extension LocalNotice {
    /// The technical view's small glyph for this plumbing line.
    var symbol: String {
        switch self {
        case .talkingTo: return "desktopcomputer"
        case .routeChanged: return "speaker.wave.2"
        case .pausedForInterruption: return "pause"
        case .listeningAgain: return "waveform"
        case .audioNotGivenBack, .audioFailedToResume, .mediaServicesReset, .audioStopped, .voiceOff: return "mic.slash"
        case .voiceClosed: return "waveform"
        case .unrecognised: return "gearshape"
        }
    }
}

extension View {
    /// The orchestrator's replies the voice's bubble just said, as one
    /// "Details" line under it.
    @ViewBuilder
    func detailsUnder(_ attached: [TimelineRow], voiceName: String) -> some View {
        let replies: [BrainActivity.Reply] = attached.compactMap { if case let .brain(reply, _) = $0 { return reply } else { return nil } }
        if replies.isEmpty {
            self
        } else {
            VStack(alignment: .leading, spacing: 0) {
                self
                ReplyDetails(replies: replies, voiceName: voiceName)
            }
        }
    }
}

/// The orchestrator's replies the voice's bubble just said: one "Details"
/// line under the bubble. Open, a 2 pt purple rule, what the orchestrator
/// told the voice, the choices (display only) and, behind one more tap, the
/// raw payload. Purple is the orchestrator's colour; never a card here.
struct ReplyDetails: View {
    let replies: [BrainActivity.Reply]
    let voiceName: String

    @State private var expanded = false
    @State private var raw = false
    @Environment(\.accessibilityReduceMotion) private var reduceMotion

    var body: some View {
        VStack(alignment: .leading, spacing: 0) {
            Button {
                withAnimation(Theme.ease(reduceMotion: reduceMotion)) { expanded.toggle() }
            } label: {
                HStack(spacing: 5) {
                    Image(systemName: "info.circle")
                    Text("Details")
                    Image(systemName: expanded ? "chevron.up" : "chevron.down")
                        .font(.caption2.weight(.bold))
                }
                .font(.footnote.weight(.semibold))
                .foregroundStyle(Theme.thinking)
                .padding(.horizontal, 6)
                .frame(minHeight: 44, alignment: .leading)
                .contentShape(Rectangle())
            }
            .buttonStyle(.plain)
            .accessibilityHint("Shows what the orchestrator told \(voiceName)")
            .accessibilityValue(expanded ? "Expanded" : "Collapsed")

            if expanded {
                HStack(alignment: .top, spacing: 0) {
                    Rectangle().fill(Theme.thinkingRule).frame(width: 2)
                    VStack(alignment: .leading, spacing: 8) {
                        ForEach(replies) { reply in
                            VStack(alignment: .leading, spacing: 8) {
                                if !reply.spoken.isEmpty {
                                    MarkdownText(reply.spoken)
                                        .font(.footnote.weight(.semibold))
                                        .foregroundStyle(Theme.ink)
                                        .fixedSize(horizontal: false, vertical: true)
                                        .textSelection(.enabled)
                                }
                                if let detail = reply.detail {
                                    MarkdownText(detail)
                                        .font(.footnote)
                                        .foregroundStyle(Theme.ink)
                                        .fixedSize(horizontal: false, vertical: true)
                                        .textSelection(.enabled)
                                        .accessibilityIdentifier("brain.detail")
                                }
                                if !reply.options.isEmpty {
                                    FlowOptions(options: reply.options, fill: Theme.thinkingSoft)
                                }
                            }
                        }
                        RawToggle(json: replies.map(\.raw).joined(separator: "\n"), isOpen: $raw)
                    }
                    .padding(.leading, 12)
                    .padding(.bottom, 4)
                }
                .padding(.leading, 6)
                .padding(.trailing, 44)
                .transition(.opacity)
            }
        }
        .accessibilityElement(children: .contain)
        .accessibilityIdentifier("bubble.brain")
    }
}

/// "Raw ⌄": the payload, one tap further.
private struct RawToggle: View {
    let json: String
    @Binding var isOpen: Bool

    var body: some View {
        VStack(alignment: .leading, spacing: 6) {
            Button { isOpen.toggle() } label: {
                HStack(spacing: 4) {
                    Text("Raw")
                    Image(systemName: isOpen ? "chevron.up" : "chevron.down").font(.caption2.weight(.bold))
                }
                .font(.footnote.weight(.semibold))
                .foregroundStyle(Theme.thinking)
                .frame(minHeight: 44, alignment: .leading)
                .contentShape(Rectangle())
            }
            .buttonStyle(.plain)
            if isOpen { RawJSONView(json: json) }
        }
    }
}

/// What the orchestrator told the voice to say (`to_supervisor`'s result)
/// when the voice did not say it: a soft purple panel, left, like the
/// voice. Behind "Detail" the plain explanation and the raw payload.
struct BrainReplyCard: View {
    let reply: BrainActivity.Reply
    let compact: Bool
    let voiceName: String

    @State private var expanded = false
    @Environment(\.dynamicTypeSize) private var dynamicTypeSize

    var body: some View {
        if compact {
            // The voice's bubble above says the words: only the rest.
            ReplyDetails(replies: [reply], voiceName: voiceName)
        } else {
            BubbleWidth(fraction: dynamicTypeSize.isAccessibilitySize ? 1 : 0.86, trailing: false) { panel }
        }
    }

    private var panel: some View {
        VStack(alignment: .leading, spacing: 5) {
            HStack(spacing: 6) {
                Image(systemName: "brain")
                    .font(.caption)
                    .accessibilityHidden(true)
                Text("Orchestrator → \(voiceName)")
                if reply.clarify {
                    Text("asks you to say more")
                        .font(.caption2.weight(.medium))
                        .padding(.horizontal, 7)
                        .padding(.vertical, 1)
                        .overlay(Capsule().strokeBorder(Theme.thinkingRule, lineWidth: 1))
                }
            }
            .font(.caption.weight(.semibold))
            .foregroundStyle(Theme.thinkingText)
            if reply.pending {
                Text(reply.spoken.isEmpty ? "Still working on it — the answer follows as a push." : reply.spoken)
                    .font(.callout)
                    .foregroundStyle(Theme.muted)
                    .fixedSize(horizontal: false, vertical: true)
            } else {
                MarkdownText(reply.spoken)
                    .font(.callout)
                    .foregroundStyle(Theme.ink)
                    .fixedSize(horizontal: false, vertical: true)
                    .textSelection(.enabled)
            }
            if !reply.options.isEmpty {
                FlowOptions(options: reply.options, fill: Theme.card)
            }
            if reply.detail != nil || !reply.raw.isEmpty {
                Button { withAnimation(Theme.ease) { expanded.toggle() } } label: {
                    Label(reply.detail != nil && !reply.pending ? "Detail" : "Raw", systemImage: "info.circle")
                        .font(.footnote.weight(.semibold))
                        .foregroundStyle(Theme.thinking)
                        .frame(minHeight: 44, alignment: .leading)
                        .contentShape(Rectangle())
                }
                .buttonStyle(.plain)
                if expanded {
                    VStack(alignment: .leading, spacing: 8) {
                        if let detail = reply.detail, !reply.pending {
                            MarkdownText(detail)
                                .font(.footnote)
                                .foregroundStyle(Theme.ink)
                                .fixedSize(horizontal: false, vertical: true)
                                .textSelection(.enabled)
                                .accessibilityIdentifier("brain.detail")
                        }
                        RawJSONView(json: reply.raw)
                    }
                }
            }
        }
        .padding(.horizontal, 12)
        .padding(.vertical, 10)
        .frame(maxWidth: .infinity, alignment: .leading)
        .background(Theme.thinkingSoft, in: RoundedRectangle(cornerRadius: Theme.Radius.card, style: .continuous))
        .accessibilityElement(children: .contain)
        .accessibilityLabel("Orchestrator to \(voiceName)")
        .accessibilityIdentifier("bubble.brain")
    }
}

/// The choices an orchestrator reply offered, as chips (display only).
struct FlowOptions: View {
    let options: [String]
    var fill = Theme.thinkingSoft

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
                .foregroundStyle(Theme.thinkingText)
                .padding(.horizontal, 10)
                .padding(.vertical, 4)
                .background(fill, in: RoundedRectangle(cornerRadius: 12, style: .continuous))
        }
    }
}

/// Raw JSON, monospaced and selectable, in a Fill block: reachable, never
/// in the way.
struct RawJSONView: View {
    let json: String

    var body: some View {
        ScrollView(.horizontal, showsIndicators: false) {
            Text(json)
                .font(.caption2.monospaced())
                .foregroundStyle(Theme.mutedOnFill)
                .textSelection(.enabled)
                .padding(8)
        }
        .background(Theme.fill, in: RoundedRectangle(cornerRadius: 8, style: .continuous))
        .accessibilityIdentifier("raw.json")
    }
}

// MARK: - Needs you

/// A row about the question floating above the capsule, folded to one line
/// that points down at it: the only amber on the page. The card does the
/// answering; a tap makes it pulse once.
struct QuestionPointer: View {
    /// Who asked.
    let agent: String

    var body: some View {
        Button {
            NotificationCenter.default.post(name: .pulseQuestion, object: nil)
        } label: {
            HStack(spacing: 7) {
                Image(systemName: "arrow.down.circle.fill")
                    .symbolRenderingMode(.palette)
                    .foregroundStyle(Theme.onAccent, Color.accentColor)
                    .accessibilityHidden(true)
                (Text("\(agent) asked").foregroundStyle(Theme.ink) + Text(" — answer below").foregroundStyle(Theme.blockedText))
                    .fontWeight(.semibold)
                    .fixedSize(horizontal: false, vertical: true)
                Spacer(minLength: 0)
            }
            .font(.footnote)
            .padding(.horizontal, 6)
            .frame(maxWidth: .infinity, minHeight: 44, alignment: .leading)
            .contentShape(Rectangle())
        }
        .buttonStyle(.plain)
        .accessibilityElement(children: .ignore)
        .accessibilityLabel("\(agent) asked. The question is on the card below.")
        .accessibilityHint("Highlights the question")
        .accessibilityAddTraits(.isButton)
        .accessibilityIdentifier("bubble.pointer")
    }
}

/// An approval as the conversation keeps it when it is not a line: what was
/// asked, and where it stands (pinned on the card above the controls, open
/// elsewhere, or how it ended). The buttons live on the pinned card.
struct ApprovalItemCard: View {
    let item: ConversationItem
    /// The pinned card above the controls is showing this question.
    let isPinned: Bool
    var identifier = "bubble.approval"
    /// Collapses the floating question into this card once settled.
    var matchesFloating = true

    private var outcome: String? { item.approvalOutcome }
    private var confirmed: Bool { item.resolution == .confirmed }
    private var declined: Bool { confirmed && PendingOption(index: 0, label: item.answer ?? "").isNegative }

    /// "search asked · 9:52".
    private var asked: String {
        let who = item.agent.map { "\($0) asked" } ?? "An agent asked"
        return "\(who) · \(clock(item.ts))"
    }

    var body: some View {
        let open = outcome == nil && isPinned
        let shape = RoundedRectangle(cornerRadius: Theme.Radius.card, style: .continuous)
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
        .background(shape.fill(Theme.card))
        .overlay(shape.strokeBorder(open ? Theme.blocked : Theme.line, lineWidth: open ? 1.5 : 1))
        // The floating question collapses into this row once settled.
        .modifier(SettledMatch(id: outcome == nil || !matchesFloating ? nil : item.pendingID))
        .accessibilityElement(children: .combine)
        .accessibilityIdentifier(identifier)
    }

    @ViewBuilder
    private var status: some View {
        if let outcome {
            Label(outcome, systemImage: confirmed ? (declined ? "xmark" : "checkmark") : "clock")
                .font(.footnote.weight(confirmed ? .semibold : .regular))
                .foregroundStyle(confirmed ? (declined ? Theme.ink : Theme.done) : Theme.muted)
                .fixedSize(horizontal: false, vertical: true)
                .accessibilityIdentifier("approval.outcome")
        } else if isPinned {
            Label("Waiting for your answer — below", systemImage: "arrow.down")
                .font(.footnote.weight(.semibold))
                .foregroundStyle(Theme.blockedText)
        } else {
            Label("Not answered here", systemImage: "circle.dashed")
                .font(.footnote)
                .foregroundStyle(Theme.muted)
        }
    }
}

// MARK: - The ledger

/// A settled question as one ledger line, "Codex's question (Finder
/// access) — closed" or the answer you gave. The whole line is the tap
/// target; open, the question, the push that announced it and the voice's
/// restatement sit under it in Fill blocks, and for a closed question why.
struct SettledQuestionLine: View {
    let item: ConversationItem
    /// The push that announced it and the voice's unanswered restatement.
    let said: [TimelineRow]
    let voiceName: String
    let openPendingIDs: Set<String>

    @State private var isOpen = false
    @Environment(\.accessibilityReduceMotion) private var reduceMotion

    private var confirmed: Bool { item.resolution == .confirmed }
    private var declined: Bool { confirmed && PendingOption(index: 0, label: item.answer ?? "").isNegative }
    private var line: String { item.settledLine ?? item.text }
    private var time: String { clock(item.ts) }

    private var look: LedgerLook { confirmed ? (declined ? .declined : .approved) : .closed }

    /// Why it closed, from the daemon's reason; the plain fallback for an
    /// older daemon (design doc, phase 3).
    private var reason: String { item.closedReasonText }

    private var blocks: [(label: String, text: String)] {
        var question = item.text
        if let options = item.options?.map(\.label), !options.isEmpty {
            question += "\nOptions: " + options.joined(separator: " · ")
        }
        var out = [(label: "\(item.agent ?? "An agent") asked · \(time)", text: question)]
        for row in said {
            let pushed: Bool = {
                switch row {
                case let .item(i): return i.kind == .push
                case let .live(b): if case .push = b.kind { return true } else { return false }
                default: return false
                }
            }()
            out.append((label: "\(pushed ? "Pushed" : "\(voiceName) said") · \(clock(row.date))", text: ConversationTimeline.updateText(row)))
        }
        if !confirmed { out.append((label: "Why it closed", text: reason)) }
        return out
    }

    var body: some View {
        LedgerCard {
            VStack(alignment: .leading, spacing: 0) {
                LedgerLine(
                    look: look, text: line, weight: confirmed ? .semibold : .regular,
                    sub: confirmed ? item.questionTitle : nil, time: time,
                    disclosure: .always, isOpen: isOpen, collapsedLines: confirmed ? 1 : 2
                )
                .contentShape(Rectangle())
                .onTapGesture { toggle() }
                // One element, read as a sentence; the default action opens it.
                .accessibilityElement(children: .combine)
                .accessibilityHint(isOpen ? "Hides the question" : "Shows the question")
                .accessibilityAction { toggle() }
                .accessibilityIdentifier("bubble.approval")
                if isOpen { LedgerBlocks(blocks: blocks) }
            }
        }
        // The floating question collapses into this row once settled.
        .modifier(SettledMatch(id: item.pendingID))
    }

    private func toggle() {
        withAnimation(Theme.ease(reduceMotion: reduceMotion)) { isOpen.toggle() }
    }
}

/// A push nobody heard, held or dropped, as an update like any other:
/// "While you were away · forumind finished the design doc…". Not struck
/// through: it was true. A tap shows all of it.
struct MissedPushRow: View {
    let push: BrainActivity.HeldPush

    @State private var isOpen = false
    @Environment(\.accessibilityReduceMotion) private var reduceMotion

    private var time: String { clock(push.ts) }

    var body: some View {
        LedgerCard {
            LedgerLine(look: .missed, prefix: "While you were away · ", text: push.text, time: time, disclosure: .always, isOpen: isOpen, collapsedLines: 1)
                .contentShape(Rectangle())
                .onTapGesture { toggle() }
                .accessibilityElement(children: .combine)
                .accessibilityLabel("While you were away, \(time). \(push.text)")
                .accessibilityHint(isOpen ? "Shows less" : "Shows all of it")
                .accessibilityAction { toggle() }
                .accessibilityIdentifier("bubble.pushMissed")
        }
    }

    private func toggle() {
        withAnimation(Theme.ease(reduceMotion: reduceMotion)) { isOpen.toggle() }
    }
}

/// A push or a local notice. A push is an agent's news, a ledger line; a
/// notice this build does not recognise is a dashed outline (it may be
/// asking for a tap, and any action it names is in its words).
struct SystemCard: View {
    let text: String
    let kind: ConversationReducer.Bubble.Kind
    let voiceName: String
    let at: Date

    @State private var isOpen = false
    @Environment(\.accessibilityReduceMotion) private var reduceMotion

    var body: some View {
        if case .push = kind {
            LedgerCard {
                LedgerLine(look: .news, text: text, time: clock(at), disclosure: .ifTruncated, isOpen: isOpen, collapsedLines: 1)
                    .contentShape(Rectangle())
                    .onTapGesture { withAnimation(Theme.ease(reduceMotion: reduceMotion)) { isOpen.toggle() } }
                    .accessibilityElement(children: .combine)
                    .accessibilityLabel("Update, \(clock(at)). \(text)")
                    .accessibilityIdentifier("bubble.push")
            }
        } else {
            HStack(alignment: .top, spacing: 10) {
                Image(systemName: "gearshape")
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
            .padding(.vertical, 9)
            .frame(maxWidth: .infinity, alignment: .leading)
            .overlay(
                RoundedRectangle(cornerRadius: Theme.Radius.card, style: .continuous)
                    .strokeBorder(Theme.dash, style: StrokeStyle(lineWidth: 1, dash: [4, 3]))
            )
            .accessibilityElement(children: .combine)
            .accessibilityLabel("Notice, \(text)")
            .accessibilityIdentifier("bubble.notice")
        }
    }
}

/// A push the voice is holding because nobody is listening (it is said once
/// a client attaches), or one it gave up on after five minutes: the
/// technical view's ledger shape in a dashed outline. Not amber, not struck.
struct HeldPushCard: View {
    let push: BrainActivity.HeldPush

    private var dropped: Bool {
        if case .dropped = push.fate { return true }
        return false
    }

    var body: some View {
        LedgerCard(dashed: true) {
            LedgerLine(
                look: dropped ? .dropped : .held,
                kicker: dropped ? "Dropped — nobody was listening for 5 minutes" : "Waiting for a listener — said when you connect",
                kickerColor: Theme.muted,
                text: push.text
            )
        }
        .accessibilityElement(children: .combine)
        .accessibilityIdentifier(dropped ? "bubble.pushDropped" : "bubble.pushHeld")
    }
}

/// Three or more consecutive agent updates folded into one ledger line:
/// stacked wells, "3 agent updates" in ink and the newest two lines; a tap
/// lists them all in the same card. When some were missed, "Catch me up"
/// sits in the card's footer: the voice never said them.
struct UpdatesGroup: View {
    let rows: [TimelineRow]
    let isOpen: Bool
    let voiceName: String
    let openPendingIDs: Set<String>
    /// Sends a line to the orchestrator as if typed (the composer's path);
    /// nil without a daemon, and then there is no chip.
    var catchMeUp: ((String) -> Void)?
    /// "Catch me up" was tapped and the voice has not started answering.
    var asking = false
    let toggle: () -> Void

    @Environment(\.ledgerPosition) private var position

    private var count: Int { ConversationTimeline.updateCount(rows) }
    private var title: String { ConversationTimeline.updatesTitle(rows) }
    private var latest: [String] { ConversationTimeline.previewLines(rows) }
    private var hasMissed: Bool { rows.contains(where: \.isMissed) }
    /// What "Catch me up" asks.
    static let catchMeUpRequest = "Catch me up on what I missed."

    var body: some View {
        // The head, then the members; a restart divider inside the run
        // closes the card and a new one starts after it.
        let isLedger: [Bool] = [true] + (isOpen ? rows.map { $0.classification.ledger } : [])
        VStack(spacing: 0) {
            head
                .environment(\.ledgerPosition, LedgerPosition(joinsAbove: position.joinsAbove, joinsBelow: isLedger.count > 1 ? isLedger[1] : position.joinsBelow))
            if isOpen {
                ForEach(Array(rows.enumerated()), id: \.element.id) { offset, row in
                    let i = offset + 1
                    if isLedger[i] {
                        RowView(row: row, voiceName: voiceName, openPendingIDs: openPendingIDs)
                            .environment(\.ledgerPosition, LedgerPosition(
                                joinsAbove: isLedger[i - 1],
                                joinsBelow: i + 1 < isLedger.count ? isLedger[i + 1] : position.joinsBelow
                            ))
                    } else {
                        RowView(row: row, voiceName: voiceName, openPendingIDs: openPendingIDs)
                            .padding(.vertical, 8)
                    }
                }
                .transition(.opacity)
            }
        }
    }

    private var head: some View {
        LedgerCard {
            VStack(alignment: .leading, spacing: 8) {
                Button(action: toggle) {
                    LedgerLine(
                        look: .group(missed: hasMissed), kicker: title,
                        previews: isOpen ? [] : latest, disclosure: .always, isOpen: isOpen
                    )
                    .animation(Theme.ease, value: count)
                    .frame(maxWidth: .infinity, alignment: .leading)
                    .contentShape(Rectangle())
                }
                .buttonStyle(.plain)
                .accessibilityElement(children: .ignore)
                .accessibilityLabel(isOpen || latest.isEmpty ? title : "\(title). Latest: \(latest.joined(separator: ". "))")
                .accessibilityHint(isOpen ? "Hides them" : "Shows them all")
                .accessibilityAddTraits(.isButton)
                .accessibilityIdentifier("conversation.updates")

                // Its own button, for VoiceOver and touch.
                if hasMissed, let catchMeUp {
                    CatchMeUpChip(asking: asking) { catchMeUp(Self.catchMeUpRequest) }
                        .modifier(TextColumn())
                }
            }
        }
    }
}

/// Insets a view to a ledger line's text column.
private struct TextColumn: ViewModifier {
    @Environment(\.dynamicTypeSize) private var dynamicTypeSize
    @ScaledMetric(relativeTo: .subheadline) private var well: CGFloat = 28

    func body(content: Content) -> some View {
        content.padding(.leading, LedgerInset.width(well: well, accessibility: dynamicTypeSize.isAccessibilitySize))
    }
}

/// "Catch me up": asks the orchestrator what the user missed. After the tap
/// it says "Asking…" until the voice starts answering.
struct CatchMeUpChip: View {
    var asking = false
    let action: () -> Void

    var body: some View {
        Button(action: action) {
            HStack(spacing: 6) {
                if asking {
                    ProgressView().controlSize(.small)
                    Text("Asking…")
                } else {
                    Image(systemName: "sparkles")
                    Text("Catch me up")
                }
            }
            .font(.footnote.weight(.semibold))
            .foregroundStyle(Theme.ink)
            .padding(.horizontal, 14)
            .frame(minHeight: 36)
            .background(Capsule().fill(Theme.surface))
            .overlay(Capsule().strokeBorder(Theme.line, lineWidth: 1))
            .frame(minHeight: 44)
            .contentShape(Rectangle())
        }
        .buttonStyle(.plain)
        .disabled(asking)
        .frame(maxWidth: .infinity, alignment: .leading)
        .accessibilityLabel(asking ? "Asking" : "Catch me up")
        .accessibilityHint("Asks the orchestrator to summarise what you missed")
        .accessibilityIdentifier("conversation.catchMeUp")
    }
}

// MARK: - Context

/// "Today", "Yesterday", "Monday 21 September": the section header, so it
/// sticks to the top while its day scrolls under it.
struct DayDivider: View {
    let date: Date
    /// Space above the heading (between days); inside its background.
    var topInset: CGFloat = 0

    var body: some View {
        Text(ConversationTimeline.dayTitle(date))
            .font(.caption.weight(.semibold))
            .foregroundStyle(Theme.muted)
            .padding(.vertical, 4)
            .padding(.top, topInset)
            .frame(maxWidth: .infinity)
            // Opaque, so rows scrolling under the pinned heading do not
            // show through; it is the page colour, so there is no visible
            // box while it is not pinned. It bleeds through the 16 pt gutter
            // and the 12 pt above (the list's top padding), where the
            // pinned heading would otherwise leave a strip of rows.
            .background(Theme.surface.padding(.horizontal, -16).padding(.top, -12))
            .accessibilityAddTraits(.isHeader)
            .accessibilityIdentifier("divider.day")
    }
}

/// A quiet line across: "Sheepdog restarted · 5:42 PM".
struct NoticeDivider: View {
    let title: String
    var symbol: String?

    var body: some View {
        ContextDivider(title: title, symbol: symbol)
    }
}

extension DaemonNotice {
    /// The glyph for a context divider or a plumbing line.
    var symbol: String? {
        switch self {
        case .reset: return "plus.bubble"
        case .restarted: return "arrow.clockwise"
        case .voiceRestarted, .contextLost, .closed: return "waveform"
        case .freshReconnect, .resumedReconnect: return "link"
        case .other: return nil
        }
    }
}

// MARK: - The empty conversation

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

/// `matchedInMotion` for a settled approval row with a question id.
struct SettledMatch: ViewModifier {
    let id: String?

    func body(content: Content) -> some View {
        if let id {
            content.matchedInMotion(id: id)
        } else {
            content
        }
    }
}
