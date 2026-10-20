import Foundation

/// Something the orchestrator put on the user's screen with its `show`
/// tool instead of saying it (docs/ws-contract.md "Cards"): a `/ws`
/// `card` message, and the `card` of a `card` item of
/// `GET /api/conversation`. Display only: an approval card carries the
/// question read-only, never a way to answer it.
public struct ScreenCard: Codable, Equatable, Hashable, Identifiable, Sendable {
    public enum Kind: String, Codable, Sendable {
        /// A title and markdown: lists, tables, steps, code.
        case card
        /// A way into an agent's window (`pane_id`).
        case agent
        /// The last lines of an agent's screen, verbatim (`text`).
        case excerpt
        /// An open approval, read-only, pointing at the card that answers it.
        case approval
        /// A kind this build does not know: shown by its title.
        case unknown

        public init(from decoder: Decoder) throws {
            let raw = try decoder.singleValueContainer().decode(String.self)
            self = Kind(rawValue: raw) ?? .unknown
        }
    }

    public var cardID: String
    public var kind: Kind
    public var at: Date?
    public var title: String
    public var markdown: String?
    /// The agent's alias, and its spoken name.
    public var agent: String?
    public var spoken: String?
    public var paneID: String?
    /// An excerpt's screen lines.
    public var text: String?
    /// An approval card's open question.
    public var pendingID: String?
    public var question: String?
    public var command: String?
    public var options: [PendingOption]?

    public var id: String { cardID }

    enum CodingKeys: String, CodingKey {
        case kind, at, title, markdown, agent, spoken, text, question, command, options
        case cardID = "card_id"
        case paneID = "pane_id"
        case pendingID = "pending_id"
    }

    public init(
        cardID: String,
        kind: Kind,
        at: Date? = nil,
        title: String,
        markdown: String? = nil,
        agent: String? = nil,
        spoken: String? = nil,
        paneID: String? = nil,
        text: String? = nil,
        pendingID: String? = nil,
        question: String? = nil,
        command: String? = nil,
        options: [PendingOption]? = nil
    ) {
        self.cardID = cardID
        self.kind = kind
        self.at = at
        self.title = title
        self.markdown = markdown
        self.agent = agent
        self.spoken = spoken
        self.paneID = paneID
        self.text = text
        self.pendingID = pendingID
        self.question = question
        self.command = command
        self.options = PendingOption.normalised(options)
    }

    public init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        cardID = try c.decode(String.self, forKey: .cardID)
        kind = try c.decodeIfPresent(Kind.self, forKey: .kind) ?? .unknown
        at = try? c.decodeIfPresent(Date.self, forKey: .at)
        title = try c.decodeIfPresent(String.self, forKey: .title) ?? ""
        markdown = try c.decodeIfPresent(String.self, forKey: .markdown)
        agent = try c.decodeIfPresent(String.self, forKey: .agent)
        spoken = try c.decodeIfPresent(String.self, forKey: .spoken)
        paneID = try c.decodeIfPresent(String.self, forKey: .paneID)
        text = try c.decodeIfPresent(String.self, forKey: .text)
        pendingID = try c.decodeIfPresent(String.self, forKey: .pendingID)
        question = try c.decodeIfPresent(String.self, forKey: .question)
        command = try c.decodeIfPresent(String.self, forKey: .command)
        options = PendingOption.normalised(try? c.decodeIfPresent([PendingOption].self, forKey: .options))
    }

    /// The heading: the title, else a plain one for the kind.
    public var displayTitle: String {
        let t = title.trimmed
        if !t.isEmpty { return t }
        switch kind {
        case .agent: return agentName.map { "\($0)'s window" } ?? "Agent window"
        case .excerpt: return agentName.map { "\($0)'s screen" } ?? "Agent screen"
        case .approval: return agentName.map { "\($0) is asking" } ?? "An agent is asking"
        case .card, .unknown: return "Card"
        }
    }

    /// The agent's name as the voice says it.
    public var agentName: String? {
        for candidate in [spoken, agent] {
            if let s = candidate?.trimmed, !s.isEmpty { return s }
        }
        return nil
    }

    /// The agent window this card can open, if any.
    public var windowPaneID: String? {
        guard let p = paneID?.trimmed, !p.isEmpty else { return nil }
        return p
    }

    /// The SF Symbol for the kind.
    public var symbol: String {
        switch kind {
        case .card, .unknown: return "rectangle.on.rectangle"
        case .agent: return "macwindow"
        case .excerpt: return "text.alignleft"
        case .approval: return "hand.raised"
        }
    }
}

/// The client's `visibility` reports (feature `cards`): whether the screen
/// can be seen and the voice screen is the one in front. The daemon shows
/// a card only while the last report said both.
///
/// Shaped like `AgentScreenStream`: the view model tells it what changed,
/// it answers with the message to send, if any. A connection starts "not
/// visible" on the daemon, so every new ready handshake re-sends the
/// current state; otherwise only a change is sent.
public struct CardVisibility: Equatable, Sendable {
    /// The `bot-ready` feature that says the daemon takes reports and
    /// sends cards.
    public static let feature = "cards"

    /// What the screen is right now.
    public private(set) var visible = false
    public private(set) var voiceScreen = false
    /// The connection is up and its daemon listed `cards`.
    public private(set) var available = false
    /// What this connection was last told; nil until the first report.
    public private(set) var sent: (visible: Bool, voiceScreen: Bool)?

    public init() {}

    public static func == (a: CardVisibility, b: CardVisibility) -> Bool {
        a.visible == b.visible && a.voiceScreen == b.voiceScreen && a.available == b.available
            && a.sent?.visible == b.sent?.visible && a.sent?.voiceScreen == b.sent?.voiceScreen
    }

    /// Cards can be shown: the daemon supports them and the screen is up.
    public var showing: Bool { available && visible && voiceScreen }

    /// The screen changed: the report to send, if any.
    public mutating func update(visible: Bool, voiceScreen: Bool) -> WSMessage? {
        self.visible = visible
        // The voice screen cannot be in front of a screen nobody sees.
        self.voiceScreen = visible && voiceScreen
        return reportIfNeeded()
    }

    /// The handshake moved: a `bot-ready` listing `cards` (re)sends the
    /// state to this connection.
    public mutating func handshake(_ state: ProtocolHandshake.State) -> WSMessage? {
        if case let .ready(_, _, features) = state, features.contains(CardVisibility.feature) {
            if !available { sent = nil }
            available = true
        } else {
            available = false
            sent = nil
        }
        return reportIfNeeded()
    }

    /// The socket went away: the next connection starts afresh.
    public mutating func socketClosed() {
        available = false
        sent = nil
    }

    private mutating func reportIfNeeded() -> WSMessage? {
        guard available else { return nil }
        if let sent, sent.visible == visible, sent.voiceScreen == voiceScreen { return nil }
        sent = (visible, voiceScreen)
        return .visibility(visible: visible, voiceScreen: voiceScreen)
    }
}

/// The cards this phone knows: the ones the `/ws` socket delivered this
/// session that the conversation history does not have yet (shown as
/// their own rows), the content of every card seen (a history item whose
/// store lost the card still opens what this phone got), and which ids
/// were ever seen, so a card pops up by itself at most once — and never
/// one that came from history, the cache or a replay.
public struct CardInbox: Equatable, Sendable {
    /// What a live card should do.
    public enum Arrival: Equatable, Sendable {
        /// New, and the screen can take it: open it as a sheet.
        case open
        /// New, but something else is in front: only the row.
        case row
        /// Seen before: nothing.
        case duplicate
    }

    /// Live cards not yet in the history, oldest first.
    public private(set) var live: [ScreenCard] = []
    private var seen: Set<String> = []
    private var known: [String: ScreenCard] = [:]

    /// How many cards' content is remembered.
    public static let capacity = 200

    public init() {}

    /// A `card` message from `/ws`.
    public mutating func receive(_ card: ScreenCard, canPresent: Bool, now: Date = Date()) -> Arrival {
        guard !card.cardID.isEmpty, seen.insert(card.cardID).inserted else { return .duplicate }
        var card = card
        if card.at == nil { card.at = now }
        remember(card)
        live.append(card)
        live.sort { ($0.at ?? .distantPast) < ($1.at ?? .distantPast) }
        if live.count > Self.capacity { live.removeFirst(live.count - Self.capacity) }
        return canPresent ? .open : .row
    }

    /// The history changed (a page, the cache, a fold): its card ids are
    /// seen, its cards remembered, and a live card the history now has
    /// stops showing twice.
    public mutating func absorb(_ items: [ConversationItem]) {
        var inHistory = Set<String>()
        for item in items where item.kind == .card {
            guard let id = item.cardID, !id.isEmpty else { continue }
            inHistory.insert(id)
            seen.insert(id)
            if let card = item.card, known[id] == nil { remember(card) }
        }
        if !inHistory.isEmpty { live.removeAll { inHistory.contains($0.cardID) } }
    }

    /// The card for a history item: its own, else what this phone got.
    public func card(for item: ConversationItem) -> ScreenCard? {
        if let card = item.card { return card }
        guard let id = item.cardID else { return nil }
        return known[id]
    }

    public func card(id: String) -> ScreenCard? { known[id] }

    public func hasSeen(_ cardID: String) -> Bool { seen.contains(cardID) }

    public mutating func clear() {
        live.removeAll()
    }

    private mutating func remember(_ card: ScreenCard) {
        known[card.cardID] = card
        if known.count > Self.capacity * 2 {
            // Rare: drop the oldest half by time.
            let keep = known.values.sorted { ($0.at ?? .distantPast) > ($1.at ?? .distantPast) }.prefix(Self.capacity)
            known = Dictionary(uniqueKeysWithValues: keep.map { ($0.cardID, $0) })
        }
    }
}
