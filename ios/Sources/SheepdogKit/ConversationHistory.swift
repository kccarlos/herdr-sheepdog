import Foundation

/// One entry of `GET /api/conversation` (docs/ws-contract.md "History and
/// resume"): the conversation rebuilt by the daemon from its event log.
///
/// `id` is the event id of the item's first event — the same number the
/// `/events` stream carries on its `id:` lines — so an item built live from
/// that stream and the one the server rebuilds later share an id, and the
/// two merge instead of showing twice.
public struct ConversationItem: Codable, Equatable, Identifiable, Sendable {
    public enum Kind: String, Codable, Sendable {
        /// What the shepherd said (right-hand bubble).
        case shepherd
        /// What the dog said (left-hand bubble).
        case dog
        /// Something the dog decided to say unprompted.
        case push
        /// A worker's approval prompt, with how it was resolved.
        case approval
        /// A divider: the daemon started, the voice reconnected or closed.
        case notice
        /// A kind this build does not know; shown as a quiet notice.
        case unknown

        public init(from decoder: Decoder) throws {
            let raw = try decoder.singleValueContainer().decode(String.self)
            self = Kind(rawValue: raw) ?? .unknown
        }
    }

    /// An approval's fate.
    public enum Resolution: String, Codable, Sendable {
        case open, confirmed, expired, cancelled, replaced, unknown

        public init(from decoder: Decoder) throws {
            let raw = try decoder.singleValueContainer().decode(String.self)
            self = Resolution(rawValue: raw) ?? .unknown
        }
    }

    public var id: Int64
    public var ts: Date
    public var kind: Kind
    public var text: String
    /// How the shepherd spoke (`voice` | `typed`), or how an approval was
    /// answered (`tap` | `fast_path` | `brain` | `confirm`).
    public var via: String?
    public var agent: String?
    public var urgency: String?
    public var pendingID: String?
    public var options: [PendingOption]?
    public var answered: Bool?
    public var answer: String?
    public var resolution: Resolution?

    enum CodingKeys: String, CodingKey {
        case id, ts, kind, text, via, agent, urgency, options, answered, answer, resolution
        case pendingID = "pending_id"
    }

    public init(
        id: Int64,
        ts: Date,
        kind: Kind,
        text: String,
        via: String? = nil,
        agent: String? = nil,
        urgency: String? = nil,
        pendingID: String? = nil,
        options: [PendingOption]? = nil,
        answered: Bool? = nil,
        answer: String? = nil,
        resolution: Resolution? = nil
    ) {
        self.id = id
        self.ts = ts
        self.kind = kind
        self.text = text
        self.via = via
        self.agent = agent
        self.urgency = urgency
        self.pendingID = pendingID
        self.options = options
        self.answered = answered
        self.answer = answer
        self.resolution = resolution
    }

    public init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        id = try c.decode(Int64.self, forKey: .id)
        ts = try c.decodeIfPresent(Date.self, forKey: .ts) ?? Date(timeIntervalSince1970: TimeInterval(id) / 1_000_000)
        kind = try c.decodeIfPresent(Kind.self, forKey: .kind) ?? .unknown
        text = try c.decodeIfPresent(String.self, forKey: .text) ?? ""
        via = try c.decodeIfPresent(String.self, forKey: .via)
        agent = try c.decodeIfPresent(String.self, forKey: .agent)
        urgency = try c.decodeIfPresent(String.self, forKey: .urgency)
        pendingID = try c.decodeIfPresent(String.self, forKey: .pendingID)
        options = PendingOption.normalised(try? c.decodeIfPresent([PendingOption].self, forKey: .options))
        answered = try c.decodeIfPresent(Bool.self, forKey: .answered)
        answer = try c.decodeIfPresent(String.self, forKey: .answer)
        resolution = try c.decodeIfPresent(Resolution.self, forKey: .resolution)
    }

    /// A push that interrupts is the one that must stand out.
    public var needsUser: Bool { kind == .push && urgency == "interrupt" }

    /// An approval's answered state in plain words: "You approved — Yes,
    /// proceed · by tap". Nil while it is open.
    public var approvalOutcome: String? {
        guard kind == .approval else { return nil }
        switch resolution ?? .open {
        case .open:
            return nil
        case .confirmed:
            let label = answer?.trimmed ?? ""
            let negative = PendingOption(index: 0, label: label).isNegative
            let verb = negative ? "You declined" : "You approved"
            let how = ConversationItem.describe(via: via)
            var out = label.isEmpty ? "Answered" : "\(verb) — \(label)"
            if let how { out += " · \(how)" }
            return out
        case .expired:
            return "Expired without an answer"
        case .cancelled:
            return "No longer needed"
        case .replaced:
            return "Replaced by a newer question"
        case .unknown:
            return "Closed"
        }
    }

    /// `via` of an answered approval, for people: a tap, or the voice.
    public static func describe(via: String?) -> String? {
        switch via {
        case "tap": return "by tap"
        case "fast_path", "brain", "confirm", "voice": return "by voice"
        case let other?: return other.isEmpty ? nil : "via \(other)"
        case nil: return nil
        }
    }
}

/// The body of `GET /api/conversation`.
public struct ConversationPage: Codable, Equatable, Sendable {
    public var items: [ConversationItem]
    /// The `before` of the next older page; nil when there is none.
    public var nextBefore: Int64?

    enum CodingKeys: String, CodingKey {
        case items
        case nextBefore = "next_before"
    }

    public init(items: [ConversationItem], nextBefore: Int64? = nil) {
        self.items = items
        self.nextBefore = nextBefore
    }
}

/// The conversation the app knows about: items with server ids, from the
/// cache, from `/api/conversation` pages and from the live `/events`
/// stream, kept sorted by id with each id once.
///
/// Pure, so paging, the merge and the live folding are unit-tested. The
/// id-less transcript fragments of the `/ws` socket stay in
/// `ConversationReducer`; `ConversationTimeline` puts the two together.
public struct ConversationHistory: Equatable, Sendable {
    /// How many items the phone keeps on disk per daemon.
    public static let cacheCapacity = 300
    /// How many items are kept in memory; older pages past this are
    /// dropped from the top again.
    public static let memoryCapacity = 2_000

    public private(set) var items: [ConversationItem] = []
    /// Where the next "load older" starts; nil when the server said there
    /// is nothing older (or no page has been loaded yet and the cache did
    /// not know).
    public private(set) var nextBefore: Int64?
    /// True once a server page has been applied this run.
    public private(set) var synced = false

    public init(items: [ConversationItem] = [], nextBefore: Int64? = nil) {
        self.items = ConversationHistory.normalise(items)
        self.nextBefore = nextBefore
    }

    public var isEmpty: Bool { items.isEmpty }
    public var newestID: Int64? { items.last?.id }
    public var oldestID: Int64? { items.first?.id }
    public var canLoadOlder: Bool { nextBefore != nil }

    /// The newest page (no `before`). The server is the record: every item
    /// in the range the page covers is replaced by the server's, and a
    /// local item in that range the server does not have is dropped. Items
    /// newer than the page (live ones that arrived while it was in flight)
    /// are kept, and so are older ones (from the cache or earlier pages)
    /// unless the server said it has nothing older.
    ///
    /// `requestedAt` bounds an empty page: with no items to say where the
    /// server's record ends, it ends when the request was sent.
    public mutating func applyNewest(_ page: ConversationPage, requestedAt: Date = .distantPast) {
        synced = true
        let serverIDs = Set(page.items.map(\.id))
        let lower: Int64 = page.nextBefore == nil ? Int64.min : (page.items.first?.id ?? Int64.min)
        let upper: Int64 = page.items.last?.id ?? Int64(requestedAt.timeIntervalSince1970 * 1_000_000)
        let kept = items.filter { item in
            !serverIDs.contains(item.id) && !(item.id >= lower && item.id <= upper)
        }
        items = ConversationHistory.normalise(kept + page.items)
        nextBefore = page.nextBefore
        trimFront()
    }

    /// An older page (`before` set): merged by id, the server's copy wins.
    public mutating func applyOlder(_ page: ConversationPage) {
        synced = true
        var byID = Dictionary(items.map { ($0.id, $0) }, uniquingKeysWith: { _, new in new })
        for item in page.items { byID[item.id] = item }
        items = ConversationHistory.normalise(Array(byID.values))
        nextBefore = page.nextBefore
        if items.count > ConversationHistory.memoryCapacity {
            // Paging back far enough is a deliberate act; keep the old
            // end and let the newest page bring the rest back.
            items.removeLast(items.count - ConversationHistory.memoryCapacity)
        }
    }

    /// Inserts or replaces one item by id (a live item, or a server copy).
    public mutating func upsert(_ item: ConversationItem) {
        if let i = items.firstIndex(where: { $0.id == item.id }) {
            items[i] = item
            return
        }
        if let last = items.last, last.id > item.id {
            let at = items.firstIndex { $0.id > item.id } ?? items.endIndex
            items.insert(item, at: at)
        } else {
            items.append(item)
        }
        trimFront()
    }

    public mutating func clear() {
        items.removeAll()
        nextBefore = nil
        synced = false
    }

    /// The approval item for a pending id, if the conversation has it.
    public func approval(pendingID: String) -> ConversationItem? {
        items.last { $0.kind == .approval && $0.pendingID == pendingID }
    }

    // MARK: - Live folding

    /// Folds one `/events` event into the history, by the daemon's own
    /// rules (`internal/web/conversation.go`) for every kind that has an
    /// event id of its own. Transcript fragments are not folded here: the
    /// `/ws` socket delivers them with less delay, and a newest-page sync
    /// turns them into server items afterwards. Idempotent: replaying an
    /// event changes nothing. Returns what changed, or nil.
    @discardableResult
    public mutating func fold(_ event: DaemonEvent) -> ConversationItem? {
        let id = Int64(clamping: event.seq)
        guard id > 0 else { return nil }
        let ts = event.ts ?? Date(timeIntervalSince1970: TimeInterval(id) / 1_000_000)
        func text(_ key: String) -> String { (event.string(key) ?? "").trimmed }

        var made: ConversationItem?
        switch event.type {
        case "daemon.start":
            made = ConversationItem(id: id, ts: ts, kind: .notice, text: "Sheepdog started.")
        case "voice.resume":
            made = ConversationItem(id: id, ts: ts, kind: .notice, text: event.bool("resumed") == true
                ? "Voice reconnected; the conversation continues."
                : "Voice reconnected with a fresh session; earlier context was lost.")
        case "voice.handle_dropped":
            made = ConversationItem(id: id, ts: ts, kind: .notice, text: "Voice context lost.")
        case "voice.closed":
            let reason = text("reason")
            made = ConversationItem(id: id, ts: ts, kind: .notice, text: reason.isEmpty ? "Voice session closed." : "Voice session closed: \(reason).")
        case "voice.text":
            made = ConversationItem(id: id, ts: ts, kind: .shepherd, text: text("text"), via: "typed")
        case "web.utterance":
            guard event.string("via") != "voice" else { return nil }
            made = ConversationItem(id: id, ts: ts, kind: .shepherd, text: text("text"), via: "typed")
        case "voice.tool_result":
            let via = event.string("via") ?? ""
            guard !via.isEmpty, via != "voice", event.bool("cancelled") != true else { return nil }
            made = ConversationItem(id: id, ts: ts, kind: .dog, text: text("spoken"))
        case "voice.push":
            guard event.string("urgency") != "silent" else { return nil }
            made = ConversationItem(id: id, ts: ts, kind: .push, text: text("text"), agent: event.string("agent"), urgency: event.string("urgency"))
        case "pending.created":
            guard event.string("kind") == "answer_prompt", let pending = PendingAction(event: event) else { return nil }
            // A replay must not reopen an approval that was resolved since.
            if items.contains(where: { $0.id == id }) { return nil }
            made = ConversationItem(
                id: id, ts: ts, kind: .approval, text: text("summary").isEmpty ? text("question") : text("summary"),
                agent: pending.agent, pendingID: pending.id, options: pending.options,
                answered: false, resolution: .open
            )
        case "pending.confirmed":
            return resolve(pendingID: event.string("id"), as: .confirmed, answer: event.string("answer"), via: event.string("via"))
        case "pending.expired":
            return resolve(pendingID: event.string("id"), as: .expired)
        case "pending.cancelled":
            return resolve(pendingID: event.string("id"), as: .cancelled)
        case "pending.replaced":
            return resolve(pendingID: event.string("old_id"), as: .replaced)
        default:
            return nil
        }
        guard let item = made, !(item.text.isEmpty && item.kind != .approval) else { return nil }
        if items.first(where: { $0.id == id }) == item { return nil }
        upsert(item)
        return item
    }

    private mutating func resolve(pendingID: String?, as how: ConversationItem.Resolution, answer: String? = nil, via: String? = nil) -> ConversationItem? {
        guard let pendingID, let i = items.lastIndex(where: { $0.kind == .approval && $0.pendingID == pendingID }) else { return nil }
        var item = items[i]
        if how == .confirmed {
            item.answered = true
            item.answer = answer
            item.via = via
            item.resolution = .confirmed
        } else {
            // An answered approval stays answered.
            guard item.resolution == nil || item.resolution == .open else { return nil }
            item.resolution = how
        }
        guard item != items[i] else { return nil }
        items[i] = item
        return item
    }

    // MARK: - Internals

    private mutating func trimFront() {
        if items.count > ConversationHistory.memoryCapacity {
            let drop = items.count - ConversationHistory.memoryCapacity
            items.removeFirst(drop)
            nextBefore = items.first?.id
        }
    }

    private static func normalise(_ items: [ConversationItem]) -> [ConversationItem] {
        var seen = Set<Int64>()
        var out: [ConversationItem] = []
        out.reserveCapacity(items.count)
        // The last copy of an id wins.
        for item in items.reversed() where seen.insert(item.id).inserted {
            out.append(item)
        }
        return out.sorted { $0.id < $1.id }
    }
}
