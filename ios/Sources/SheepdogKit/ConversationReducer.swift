import Foundation

/// Turns the daemon's transcript fragments, pushes and mirrored events into
/// the chat bubbles the main screen shows.
///
/// The merging rules come from the browser page (`web/voice/app.js`,
/// `addTranscript`) and from docs/ws-contract.md: fragments of one utterance
/// arrive in order and `final:true` closes it; output fragments may never
/// carry `final`, so the next `in` transcript — or a `flush` — is the
/// boundary.
public struct ConversationReducer: Sendable {
    public struct Bubble: Identifiable, Equatable, Sendable {
        public enum Speaker: Equatable, Sendable {
            /// The user — right-hand side.
            case shepherd
            /// The dog — left-hand side.
            case sheepdog
            /// A push or a notice — a full-width card.
            case system
        }

        public enum Kind: Equatable, Sendable {
            case speech
            /// A push the daemon decided to say. `needsUser` marks a block's
            /// question: the one that must stand out.
            case push(needsUser: Bool)
            case notice
        }

        public var id: Int
        public var speaker: Speaker
        public var kind: Kind
        public var text: String
        public var isFinal: Bool
        public var at: Date
        /// When the last fragment arrived; what `settle` measures idleness by.
        public var updatedAt: Date

        public init(id: Int, speaker: Speaker, kind: Kind = .speech, text: String, isFinal: Bool, at: Date = Date()) {
            self.id = id
            self.speaker = speaker
            self.kind = kind
            self.text = text
            self.isFinal = isFinal
            self.at = at
            self.updatedAt = at
        }
    }

    public enum Input: Equatable, Sendable {
        /// A `transcript` frame from `/ws`.
        case transcript(WSMessage.Direction, text: String, final: Bool)
        /// A `flush` frame, or the local Stop button: the open `out`
        /// utterance is over.
        case flush
        /// A local notice ("voice is off on the daemon", "reconnected").
        case notice(String)
        /// "New conversation": the daemon forgot the short-term context.
        /// Closes whatever bubble is still open — the scrollback stays,
        /// only the in-flight turn is not worth continuing — and adds
        /// nothing itself; the divider comes from history (`kind: .notice,
        /// subkind: "reset"`).
        case reset
        /// Nothing guarantees a closing `final` for the voice's reply (and a
        /// phone that was away misses the next `in` that would close it).
        /// Closes every speech bubble that got no fragment for `idle`
        /// seconds; `0` closes them all (the socket closed or reconnected).
        case settle(idle: TimeInterval)
    }

    /// How many bubbles are kept. Far more than a phone screen shows; the
    /// conversation is real time, not an archive.
    public static let capacity = 400

    public private(set) var bubbles: [Bubble] = []
    private var nextID = 1
    private var now: @Sendable () -> Date

    public init(now: @escaping @Sendable () -> Date = { Date() }) {
        self.now = now
    }

    public mutating func apply(_ input: Input) {
        switch input {
        case let .transcript(dir, text, final):
            append(speaker: dir == .incoming ? .shepherd : .sheepdog, text: text, final: final)
        case .flush:
            closeOpen(.sheepdog)
        case let .notice(text):
            push(Bubble(id: takeID(), speaker: .system, kind: .notice, text: text, isFinal: true, at: now()))
        case .reset:
            closeOpen(.shepherd)
            closeOpen(.sheepdog)
        case let .settle(idle):
            let at = now()
            for index in bubbles.indices where bubbles[index].kind == .speech && !bubbles[index].isFinal {
                if at.timeIntervalSince(bubbles[index].updatedAt) >= idle { bubbles[index].isFinal = true }
            }
        }
    }

    public mutating func clear() {
        bubbles.removeAll()
    }

    /// Drops the finished speech bubbles from before `date`: a newest page
    /// of `/api/conversation` fetched after them has them as server items
    /// (the daemon logs every transcript fragment), so showing both would
    /// say everything twice. The open bubble and the notices stay.
    public mutating func dropSettledSpeech(before date: Date) {
        bubbles.removeAll { $0.kind == .speech && $0.isFinal && $0.at < date }
    }

    /// The last bubble that is still growing, if any — what the view
    /// animates with a caret.
    public var openBubble: Bubble? {
        guard let last = bubbles.last, !last.isFinal else { return nil }
        return last
    }

    // MARK: - Internals

    private mutating func append(speaker: Bubble.Speaker, text: String, final: Bool, merge: Bool = true) {
        // The other direction starting closes whatever was open, which is how
        // an `out` utterance that never carries `final` ends.
        closeOpen(speaker == .shepherd ? .sheepdog : .shepherd)
        if merge, var last = bubbles.last, last.speaker == speaker, !last.isFinal, last.kind == .speech {
            last.text += text
            last.isFinal = final
            last.updatedAt = now()
            bubbles[bubbles.count - 1] = last
            return
        }
        guard !(text.isEmpty && final) else { return }
        push(Bubble(id: takeID(), speaker: speaker, text: text, isFinal: final, at: now()))
    }

    private mutating func closeOpen(_ speaker: Bubble.Speaker) {
        guard var last = bubbles.last, last.speaker == speaker, !last.isFinal else { return }
        last.isFinal = true
        bubbles[bubbles.count - 1] = last
    }

    private mutating func push(_ bubble: Bubble) {
        bubbles.append(bubble)
        if bubbles.count > Self.capacity {
            bubbles.removeFirst(bubbles.count - Self.capacity)
        }
    }

    private mutating func takeID() -> Int {
        defer { nextID += 1 }
        return nextID
    }
}
