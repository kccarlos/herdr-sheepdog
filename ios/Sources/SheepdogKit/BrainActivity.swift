import Foundation

/// What the brain told the voice, and the pushes waiting for a listener —
/// the two things `GET /api/conversation` does not carry and the `/ws`
/// transcript cannot show, folded from `/events` (and from
/// `GET /api/events` after a relaunch).
///
/// * **Brain replies.** An utterance the voice relays with
///   `to_supervisor` comes back as `voice.tool_result {spoken, detail?,
///   options?, clarify, pending?}` — the headline the voice reads, the plain
///   explanation it reads only when asked, and the choices. The brain's
///   `ask` is not separate on the wire: the daemon folds it into `spoken`
///   as the last sentence (`supervisor.toolResultFrom`). A reply that did
///   not land in time is `pending` (the answer follows as a push).
/// * **Held pushes.** With no client attached, the voice holds every spoken
///   push (`voice.push_held {text, urgency, push_id, reason}`); on attach it
///   is `voice.push_released {…, held_ms}` followed by the usual
///   `voice.push`, and after 5 minutes `voice.push_dropped {…, age_ms}`.
///
/// Kept apart from `ConversationHistory` because the server's newest page
/// replaces every local item in its range: these would be wiped on each
/// sync. Pure, so folding is unit-tested.
public struct BrainActivity: Equatable, Sendable {
    /// One `to_supervisor` tool result.
    public struct Reply: Equatable, Identifiable, Sendable {
        /// The event id.
        public var id: Int64
        public var ts: Date
        /// What the voice was told to say (the brain's `ask` included).
        public var spoken: String
        /// The plain explanation behind the headline, when there is one.
        public var detail: String?
        /// The choices the brain offered.
        public var options: [String]
        /// The brain asks the shepherd to say more.
        public var clarify: Bool
        /// Not the answer yet: it follows as a push.
        public var pending: Bool
        /// `text_router` for a daemon without a voice (its reply is also a
        /// `dog` item with the same id); nil with a live voice.
        public var via: String?
        /// The payload as the daemon logged it, for the raw view.
        public var raw: String

        public init(id: Int64, ts: Date, spoken: String, detail: String? = nil, options: [String] = [], clarify: Bool = false, pending: Bool = false, via: String? = nil, raw: String = "") {
            self.id = id
            self.ts = ts
            self.spoken = spoken
            self.detail = detail
            self.options = options
            self.clarify = clarify
            self.pending = pending
            self.via = via
            self.raw = raw
        }

        /// The reply already shows as the dog's bubble with the same id
        /// (the text router's answer): only its detail and options add
        /// anything.
        public var isAlsoADogItem: Bool {
            guard let via else { return false }
            return !via.isEmpty && via != "voice"
        }

        /// Worth a card of its own, or an expandable footer.
        public var hasMore: Bool { detail != nil || !options.isEmpty }
    }

    /// A spoken push the voice is holding until someone listens.
    public struct HeldPush: Equatable, Identifiable, Sendable {
        public enum Fate: Equatable, Sendable {
            /// Waiting for a client to attach.
            case held
            /// Said once a client attached, after `heldMilliseconds`.
            case released(heldMilliseconds: Int)
            /// Given up on after 5 minutes with nobody listening.
            case dropped(ageMilliseconds: Int)
        }

        /// `push_id`, or the text when an older daemon sent none.
        public var key: String
        /// The event id of `voice.push_held`: where it sits in the timeline.
        public var id: Int64
        public var ts: Date
        public var text: String
        public var urgency: String?
        public var fate: Fate

        public init(key: String, id: Int64, ts: Date, text: String, urgency: String? = nil, fate: Fate = .held) {
            self.key = key
            self.id = id
            self.ts = ts
            self.text = text
            self.urgency = urgency
            self.fate = fate
        }

        /// A released push is an ordinary push card now (its `voice.push`
        /// follows); only a held or dropped one needs its own card.
        public var isShown: Bool {
            if case .released = fate { return false }
            return true
        }
    }

    /// How many replies and held pushes are kept.
    public static let capacity = 200

    public private(set) var replies: [Reply] = []
    public private(set) var held: [HeldPush] = []

    public init() {}

    public var isEmpty: Bool { replies.isEmpty && held.isEmpty }

    /// The reply with this event id, if any.
    public func reply(id: Int64) -> Reply? {
        replies.first { $0.id == id }
    }

    /// Folds one event. Idempotent; returns true when anything changed.
    @discardableResult
    public mutating func fold(_ event: DaemonEvent) -> Bool {
        let id = Int64(clamping: event.seq)
        guard id > 0 else { return false }
        let ts = event.ts ?? Date(timeIntervalSince1970: TimeInterval(id) / 1_000_000)
        func text(_ key: String) -> String? {
            guard let s = event.string(key)?.trimmingCharacters(in: .whitespacesAndNewlines), !s.isEmpty else { return nil }
            return s
        }

        switch event.type {
        case "voice.tool_result":
            guard event.string("name") == "to_supervisor", event.bool("cancelled") != true else { return false }
            let pending = event.bool("pending") ?? false
            let spoken = text("spoken") ?? ""
            guard !spoken.isEmpty || pending else { return false }
            var options: [String] = []
            if case let .array(values)? = event.payload["options"] {
                options = values.compactMap { if case let .string(s) = $0 { return s } else { return nil } }
            }
            let reply = Reply(
                id: id, ts: ts, spoken: spoken, detail: text("detail"), options: options,
                clarify: event.bool("clarify") ?? false, pending: pending, via: event.string("via"),
                raw: BrainActivity.rawJSON(event.payload)
            )
            if let i = replies.firstIndex(where: { $0.id == id }) {
                guard replies[i] != reply else { return false }
                replies[i] = reply
                return true
            }
            let at = replies.firstIndex { $0.id > id } ?? replies.endIndex
            replies.insert(reply, at: at)
            if replies.count > BrainActivity.capacity { replies.removeFirst(replies.count - BrainActivity.capacity) }
            return true

        case "voice.push_held":
            guard event.string("urgency") != "silent", let body = text("text") else { return false }
            let key = text("push_id") ?? "text:\(body)"
            if held.contains(where: { $0.key == key }) { return false }
            held.append(HeldPush(key: key, id: id, ts: ts, text: body, urgency: event.string("urgency")))
            held.sort { $0.id < $1.id }
            if held.count > BrainActivity.capacity { held.removeFirst(held.count - BrainActivity.capacity) }
            return true

        case "voice.push_released":
            return settle(event, as: .released(heldMilliseconds: event.int("held_ms") ?? 0))

        case "voice.push_dropped":
            return settle(event, as: .dropped(ageMilliseconds: event.int("age_ms") ?? 0))

        default:
            return false
        }
    }

    private mutating func settle(_ event: DaemonEvent, as fate: HeldPush.Fate) -> Bool {
        let body = event.string("text")?.trimmingCharacters(in: .whitespacesAndNewlines) ?? ""
        let key = event.string("push_id").flatMap { $0.isEmpty ? nil : $0 } ?? "text:\(body)"
        guard let i = held.lastIndex(where: { $0.key == key }), held[i].fate != fate else { return false }
        held[i].fate = fate
        return true
    }

    public mutating func clear() {
        replies.removeAll()
        held.removeAll()
    }

    /// A payload as pretty, key-sorted JSON.
    public static func rawJSON(_ payload: [String: JSONValue]) -> String {
        let encoder = JSONEncoder()
        encoder.outputFormatting = [.prettyPrinted, .sortedKeys, .withoutEscapingSlashes]
        guard let data = try? encoder.encode(payload) else { return "" }
        return String(decoding: data, as: UTF8.self)
    }
}

extension DaemonEvent {
    /// A numeric payload field as an integer.
    public func int(_ key: String) -> Int? {
        if case let .number(n)? = payload[key] { return Int(n) }
        return nil
    }

    /// A numeric payload field.
    public func double(_ key: String) -> Double? {
        if case let .number(n)? = payload[key] { return n }
        return nil
    }
}
