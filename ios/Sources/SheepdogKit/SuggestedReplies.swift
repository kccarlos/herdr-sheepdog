import Foundation
import Observation

/// The next steps an orchestrator reply suggested ("Yes, start the
/// logging", "Not yet"), and whether they can still be tapped
/// (docs/dev/design/conversation-timeline.md, section 14). A tap is the
/// user typing that line: it goes through the typed-line path, so every
/// rail on the daemon applies unchanged.
///
/// Only the options of the latest orchestrator reply the user has not
/// answered are live; `ConversationTimeline.suggestedReplies` decides,
/// from the whole timeline. Pure.
public struct SuggestedReplies: Equatable, Sendable {
    public enum Status: Equatable, Sendable {
        /// The latest reply, unanswered: its options are buttons.
        case live
        /// Answered, or the conversation moved on: plain text. `chosen` is
        /// the option the user's next line repeated, if it did.
        case stale(chosen: String?)
    }

    /// The reply's event id (`BrainActivity.Reply.id`).
    public var replyID: Int64
    public var options: [String]
    public var status: Status

    public init(replyID: Int64, options: [String], status: Status) {
        self.replyID = replyID
        self.options = options
        self.status = status
    }

    public var isLive: Bool { status == .live }

    /// How one option is drawn.
    public enum OptionState: Equatable, Sendable {
        /// A button that sends it.
        case available
        /// A button, greyed: another option (or a typed line) is on its way.
        case disabled
        /// This one is on its way.
        case sending
        /// This one was sent, or the user's reply repeated it.
        case sent
        /// Plain text: no longer an answer to anything.
        case stale
    }

    /// The state of `option`, given the tap in flight (`SuggestedReplySender`)
    /// and whether the typed line is busy otherwise (`busy`).
    public func state(of option: String, tap: SuggestedReplyTap?, busy: Bool = false) -> OptionState {
        switch status {
        case let .stale(chosen):
            return chosen.map { Self.same($0, option) } == true ? .sent : .stale
        case .live:
            if let tap, tap.replyID == replyID {
                switch tap.phase {
                case .sending: return tap.option == option ? .sending : .disabled
                case .sent: return tap.option == option ? .sent : .disabled
                case .failed: break
                }
            }
            return busy ? .disabled : .available
        }
    }

    /// The message a failed tap on this reply left, if any.
    public func failure(_ tap: SuggestedReplyTap?) -> String? {
        guard isLive, let tap, tap.replyID == replyID, case let .failed(message) = tap.phase else { return nil }
        return message
    }

    /// An option and a line the user said are the same answer: trimmed,
    /// without a final full stop, case and width ignored.
    static func same(_ a: String, _ b: String) -> Bool {
        func norm(_ s: String) -> String {
            var t = s.trimmed
            while let last = t.last, ".!".contains(last) { t.removeLast() }
            return t.trimmed
        }
        return norm(a).compare(norm(b), options: [.caseInsensitive, .widthInsensitive, .diacriticInsensitive]) == .orderedSame
    }
}

/// One tap on a suggested reply, while it matters: on its way, sent, or
/// failed (the options are live again, with the reason).
public struct SuggestedReplyTap: Equatable, Sendable {
    public enum Phase: Equatable, Sendable {
        case sending
        case sent
        /// `message` is nil when the typed path refused without a reason
        /// (the AI consent sheet came up instead, say).
        case failed(message: String?)
    }

    public var replyID: Int64
    public var option: String
    /// The computer it was sent to: a tap never shows on another one's
    /// conversation.
    public var computer: String
    public var phase: Phase

    public init(replyID: Int64, option: String, computer: String, phase: Phase) {
        self.replyID = replyID
        self.option = option
        self.computer = computer
        self.phase = phase
    }
}

/// The one action behind every suggested-reply button: sends the option as
/// a typed line through `sendTyped` (the composer's path, with its
/// idempotency key and its error) and keeps the tap's state for the row.
/// One tap at a time; a tap on a reply that is not live sends nothing.
@MainActor
@Observable
public final class SuggestedReplySender {
    public private(set) var tap: SuggestedReplyTap?

    @ObservationIgnored private let sendTyped: (String) async -> Bool
    /// The typed path's error after a failed send (it is the composer's).
    @ObservationIgnored private let lastError: () -> String?

    public init(sendTyped: @escaping (String) async -> Bool, lastError: @escaping () -> String? = { nil }) {
        self.sendTyped = sendTyped
        self.lastError = lastError
    }

    /// The tap to draw on `computer`'s conversation.
    public func tap(on computer: String) -> SuggestedReplyTap? {
        tap?.computer == computer ? tap : nil
    }

    /// Sends `option` of `replies` to `computer`. Returns true when the
    /// daemon took it.
    @discardableResult
    public func send(_ option: String, of replies: SuggestedReplies, to computer: String) async -> Bool {
        guard replies.isLive, replies.options.contains(option) else { return false }
        if let tap, tap.phase == .sending { return false }
        tap = SuggestedReplyTap(replyID: replies.replyID, option: option, computer: computer, phase: .sending)
        let sent = await sendTyped(option)
        tap?.phase = sent ? .sent : .failed(message: lastError())
        return sent
    }
}

extension ConversationTimeline {
    /// The suggested replies of every orchestrator reply that has options,
    /// by reply id. At most one is live: the newest reply, when after it
    /// there is none of
    ///
    /// - the user's words (said, typed or a tapped option): an answer, and
    ///   the option it repeats is `chosen`;
    /// - a newer orchestrator reply, pending or not;
    /// - a new conversation, a daemon restart or the voice losing its
    ///   context (the voice no longer knows the question it asked);
    /// - a switch to another computer ("Talking to …").
    ///
    /// The voice saying the reply, agent updates and questions do not end
    /// it. A pending reply has no options yet.
    public static func suggestedReplies(_ rows: [TimelineRow]) -> [Int64: SuggestedReplies] {
        var out: [Int64: SuggestedReplies] = [:]
        var open: [Int64] = []
        func settle(chosenFrom said: String? = nil) {
            for id in open {
                guard let replies = out[id] else { continue }
                let chosen = said.flatMap { line in replies.options.first { SuggestedReplies.same($0, line) } }
                out[id]?.status = .stale(chosen: chosen)
            }
            open.removeAll()
        }
        for row in rows {
            switch row {
            case let .brain(reply, _):
                settle()
                guard !reply.pending, !reply.options.isEmpty else { continue }
                out[reply.id] = SuggestedReplies(replyID: reply.id, options: reply.options, status: .live)
                open.append(reply.id)
            case let .item(item) where item.kind == .shepherd:
                settle(chosenFrom: item.text)
            case let .live(bubble) where bubble.speaker == .shepherd:
                settle(chosenFrom: bubble.isFinal ? bubble.text : nil)
            case let .live(bubble) where bubble.kind == .notice:
                if case .talkingTo = LocalNotice(text: bubble.text) { settle() }
            case let .divider(item, _), let .item(item):
                if endsSuggestedReplies(DaemonNotice(item)) { settle() }
            default:
                continue
            }
        }
        return out
    }

    private static func endsSuggestedReplies(_ notice: DaemonNotice) -> Bool {
        switch notice {
        case .reset, .restarted, .contextLost, .freshReconnect, .voiceRestarted: return true
        case .resumedReconnect, .closed, .other: return false
        }
    }
}
