import Foundation

/// One read of an agent's terminal window, from
/// `GET /api/agents/{pane_id}/screen` (docs/ws-contract.md in the daemon,
/// "Agent window: screen and input"). When the client passed the last
/// `hash` back and nothing changed, `unchanged` is true and `text` is nil.
public struct AgentScreen: Codable, Equatable, Sendable {
    public var paneID: String
    public var alias: String
    public var spoken: String?
    public var kind: String?
    public var state: Agent.State
    public var stateSince: Date?
    public var text: String?
    public var lines: Int?
    public var truncated: Bool?
    public var hash: String
    public var unchanged: Bool?

    enum CodingKeys: String, CodingKey {
        case alias, spoken, kind, state, text, lines, truncated, hash, unchanged
        case paneID = "pane_id"
        case stateSince = "state_since"
    }

    public init(paneID: String, alias: String, spoken: String? = nil, kind: String? = nil, state: Agent.State, stateSince: Date? = nil, text: String? = nil, lines: Int? = nil, truncated: Bool? = nil, hash: String, unchanged: Bool? = nil) {
        self.paneID = paneID
        self.alias = alias
        self.spoken = spoken
        self.kind = kind
        self.state = state
        self.stateSince = stateSince
        self.text = text
        self.lines = lines
        self.truncated = truncated
        self.hash = hash
        self.unchanged = unchanged
    }

    /// The name the user hears, falling back to the alias.
    public var displayName: String {
        if let s = spoken?.trimmed, !s.isEmpty { return s }
        return alias
    }
}

/// The reply to `POST /api/agents/{pane_id}/input`.
public struct AgentInputReply: Codable, Equatable, Sendable {
    /// `prompt`, `keys`, or `answer` (the keys answered the open question
    /// through the tap path).
    public var kind: String?
    public var spoken: String?

    public init(kind: String? = nil, spoken: String? = nil) {
        self.kind = kind
        self.spoken = spoken
    }
}

/// The keys the window's key bar sends — the daemon's allow-list, in the
/// spelling herdr's `pane.send_keys` takes.
public enum AgentKey: String, CaseIterable, Sendable, Identifiable {
    case enter, esc
    case ctrlC = "ctrl+c"
    case one = "1", two = "2", three = "3"
    case up, down, tab

    public var id: String { rawValue }

    /// The key bar's label.
    public var label: String {
        switch self {
        case .enter: return "Enter"
        case .esc: return "Esc"
        case .ctrlC: return "Ctrl-C"
        case .one, .two, .three: return rawValue
        case .up: return "↑"
        case .down: return "↓"
        case .tab: return "Tab"
        }
    }

    /// What VoiceOver says.
    public var accessibilityLabel: String {
        switch self {
        case .up: return "Up arrow"
        case .down: return "Down arrow"
        case .ctrlC: return "Control C"
        case .esc: return "Escape"
        default: return label
        }
    }

    /// Esc and Ctrl-C interrupt or dismiss what the agent is doing, so the
    /// window asks before sending them.
    public var needsConfirmation: Bool { self == .esc || self == .ctrlC }

    /// The confirmation dialog's question.
    public func confirmationTitle(agent: String) -> String {
        switch self {
        case .ctrlC: return "Send Ctrl-C to \(agent)? It interrupts what it is doing."
        case .esc: return "Send Esc to \(agent)? It dismisses the current prompt or stops the turn."
        default: return "Send \(label) to \(agent)?"
        }
    }
}

/// The window's state, without SwiftUI: what to show, whether a fetched
/// frame changes anything, whether the controls are usable, and what the
/// next request should carry. `AgentScreenView` drives it.
public struct AgentScreenModel: Equatable, Sendable {
    /// How often the window polls while it is on screen.
    public static let pollInterval: TimeInterval = 1.5
    /// How many lines it asks for.
    public static let lines = 200

    public let paneID: String
    /// The last full frame (text included).
    public private(set) var screen: AgentScreen?
    /// The text the view renders.
    public private(set) var text: String = ""
    /// Bumped whenever `text` changes, so the view can follow the bottom.
    public private(set) var revision = 0
    /// The last read failed; the view shows `error` inline.
    public private(set) var error: String?
    /// The daemon predates the route (a plain 404).
    public private(set) var unsupported = false
    /// An input is in flight.
    public private(set) var sending = false
    /// The last input's failure, shown under the input field.
    public private(set) var inputError: String?
    /// The last input's reply (`Told auth Yes.` for an answer).
    public private(set) var lastReply: String?

    public init(paneID: String) {
        self.paneID = paneID
    }

    /// The hash to send with the next read: unchanged frames come back
    /// without text.
    public var hashForNextRead: String? { screen?.hash }

    /// The controls work only while the window is live: a frame arrived and
    /// the last read did not fail.
    public var canOperate: Bool { screen != nil && error == nil && !unsupported && !sending }

    /// Applies a read. Returns true when what the view shows changed.
    @discardableResult
    public mutating func apply(_ frame: AgentScreen) -> Bool {
        let hadError = error != nil
        error = nil
        if frame.unchanged == true, var current = screen, current.hash == frame.hash {
            // Same text; the metadata may still move (a rename).
            let before = current
            current.spoken = frame.spoken ?? current.spoken
            current.state = frame.state
            current.stateSince = frame.stateSince ?? current.stateSince
            screen = current
            return hadError || current != before
        }
        guard let newText = frame.text else {
            // "Unchanged" for a hash we no longer hold: keep what we have
            // and drop the hash so the next read carries the text.
            if var current = screen { current.hash = ""; screen = current }
            return hadError
        }
        screen = frame
        if newText != text {
            text = newText
            revision += 1
            return true
        }
        return true
    }

    /// Records a failed read.
    public mutating func fail(_ error: Error) {
        if let daemon = error as? DaemonError {
            self.error = daemon.message
        } else {
            self.error = error.localizedDescription
        }
    }

    /// Records a failure the daemon pushed (`screen-error`).
    public mutating func fail(message: String) {
        error = message
    }

    /// Records that the daemon has no agent windows (a plain 404).
    public mutating func markUnsupported() {
        unsupported = true
        error = AgentScreenModel.unsupportedMessage
    }

    public static let unsupportedMessage = "This daemon doesn't have agent windows yet. Update sheepdog on your computer."

    public mutating func beginSend() {
        sending = true
        inputError = nil
    }

    public mutating func finishSend(reply: AgentInputReply) {
        sending = false
        lastReply = reply.spoken?.trimmed.isEmpty == false ? reply.spoken : nil
        // The next read must show what the input did.
        if var current = screen { current.hash = ""; screen = current }
    }

    public mutating func finishSend(error: Error) {
        sending = false
        if let daemon = error as? DaemonError {
            inputError = daemon.message
        } else {
            inputError = error.localizedDescription
        }
    }

    /// The prompt field's text is worth sending.
    public static func canSend(_ text: String) -> Bool {
        let t = text.trimmed
        return !t.isEmpty && t.utf8.count <= 4000
    }

    /// Whether a scroll view whose bottom anchor sits at `anchorMaxY`, in a
    /// viewport `viewportHeight` tall, counts as "at the bottom" (a few
    /// points of slack): only then does a new frame scroll it down.
    public static func isAtBottom(anchorMaxY: CGFloat, viewportHeight: CGFloat, slack: CGFloat = 24) -> Bool {
        anchorMaxY <= viewportHeight + slack
    }
}

/// What the daemon pushed about the open window.
public enum AgentScreenEvent: Equatable, Sendable {
    case frame(AgentScreen)
    /// `ended`: the subscription is over (the agent is gone).
    case error(String, ended: Bool)
}

/// One push for the open window, numbered so two equal pushes in a row
/// are still a change.
public struct AgentScreenPush: Equatable, Sendable {
    public let seq: Int
    /// The window it is for: a view applies only its own pane's pushes.
    public let paneID: String
    public let event: AgentScreenEvent

    public init(seq: Int, paneID: String, event: AgentScreenEvent) {
        self.seq = seq
        self.paneID = paneID
        self.event = event
    }
}

/// The agent window over `/ws` (the daemon's docs/ws-contract.md "Agent
/// window over /ws"), without the socket: which window is open, whether
/// the daemon streams it, and what to send when the window or the
/// connection changes. The daemon allows one subscription per client, so
/// the app holds one of these; `SessionViewModel` feeds it the handshake
/// and socket events and sends what it returns.
///
/// The window streams only once the first `screen` frame for it arrived
/// after a subscribe on the current connection; until then — an older
/// daemon without the `screen` feature, a legacy or missing connection, a
/// subscribe that never got out — `AgentScreenView` polls
/// `GET /api/agents/{pane}/screen` as before.
public struct AgentScreenStream: Equatable, Sendable {
    /// The `bot-ready` feature that says the daemon streams windows.
    public static let feature = "screen"

    /// The window on screen, if any.
    public private(set) var paneID: String?
    /// The lines it asks for.
    public private(set) var lines = AgentScreenModel.lines
    /// The connection is up and its daemon listed `screen`.
    public private(set) var available = false
    /// The pane subscribed on this connection, and whether a frame for it
    /// arrived since.
    public private(set) var subscribed: String?
    public private(set) var confirmed = false

    public init() {}

    /// Whether `paneID`'s window is being pushed (no polling needed).
    public func isStreaming(_ paneID: String) -> Bool {
        available && subscribed == paneID && self.paneID == paneID && confirmed
    }

    /// A window opened (or is still open): the subscribe to send, if any.
    public mutating func open(paneID: String, lines: Int = AgentScreenModel.lines) -> WSMessage? {
        self.paneID = paneID
        self.lines = lines
        return subscribeIfNeeded()
    }

    /// A window closed: the unsubscribe to send, if it was the one
    /// subscribed. Closing a window that another one has replaced does
    /// nothing.
    public mutating func close(paneID: String) -> WSMessage? {
        guard self.paneID == paneID else { return nil }
        self.paneID = nil
        guard subscribed != nil else { return nil }
        subscribed = nil
        confirmed = false
        return available ? .screenUnsubscribe : nil
    }

    /// The handshake moved: a `bot-ready` listing `screen` makes the
    /// stream available (and subscribes the open window); anything else
    /// means poll.
    public mutating func handshake(_ state: ProtocolHandshake.State) -> WSMessage? {
        let nowAvailable: Bool
        if case let .ready(_, _, features) = state {
            nowAvailable = features.contains(AgentScreenStream.feature)
        } else {
            nowAvailable = false
        }
        if !nowAvailable || !available {
            // A new connection (or none): nothing is subscribed on it yet.
            subscribed = nil
            confirmed = false
        }
        available = nowAvailable
        return subscribeIfNeeded()
    }

    /// The socket went away: poll until a new connection is ready.
    public mutating func socketClosed() {
        available = false
        subscribed = nil
        confirmed = false
    }

    /// A control message from the socket: the event for the open window,
    /// or nil (not a window message, or another pane's).
    public mutating func receive(_ message: WSMessage) -> AgentScreenEvent? {
        switch message.type {
        case .screen:
            guard let screen = message.screen, screen.paneID == paneID, subscribed == paneID else { return nil }
            confirmed = true
            return .frame(screen)
        case .screenError:
            guard let pane = message.paneID, pane == paneID, subscribed == paneID else { return nil }
            let ended = message.ended == true
            if ended {
                // Over: the window falls back to polling, which shows the
                // route's answer.
                subscribed = nil
                confirmed = false
            }
            return .error(message.message ?? "", ended: ended)
        default:
            return nil
        }
    }

    private mutating func subscribeIfNeeded() -> WSMessage? {
        guard available, let paneID, subscribed != paneID else { return nil }
        subscribed = paneID
        confirmed = false
        return .screenSubscribe(paneID: paneID, lines: lines)
    }
}

extension DaemonClient {
    /// `/api/agents/<pane id>/<suffix>`, percent-encoded. A pane id carries
    /// `:` (`w1:p2`) and with tmux `%` (`%12`); `/` would split the path, so
    /// it is escaped too.
    public static func agentPath(paneID: String, suffix: String) -> String {
        var allowed = CharacterSet.urlPathAllowed
        allowed.remove(charactersIn: "/%?#")
        let escaped = paneID.addingPercentEncoding(withAllowedCharacters: allowed) ?? paneID
        return "/api/agents/\(escaped)/\(suffix)"
    }

    /// `GET /api/agents/{pane_id}/screen?lines=&hash=` — the agent's current
    /// window. `nil` when the daemon predates the route (a plain 404); a
    /// pane the daemon does not track is `.rejected(404, "no such agent")`.
    public func agentScreen(paneID: String, lines: Int = AgentScreenModel.lines, hash: String? = nil) async throws -> AgentScreen? {
        var query = [URLQueryItem(name: "lines", value: String(lines))]
        if let hash, !hash.isEmpty { query.append(URLQueryItem(name: "hash", value: hash)) }
        let request = try request(encodedPath: DaemonClient.agentPath(paneID: paneID, suffix: "screen"), query: query)
        let (data, response) = try await perform(request)
        return try decodeAgentReply(AgentScreen.self, data: data, response: response, what: "the agent's screen")
    }

    /// `POST /api/agents/{pane_id}/input {"text", "submit": true}` — a prompt.
    public func agentInput(paneID: String, text: String) async throws -> AgentInputReply {
        let body = try JSONSerialization.data(withJSONObject: ["text": text, "submit": true] as [String: Any])
        return try await postAgentInput(paneID: paneID, body: body)
    }

    /// `POST /api/agents/{pane_id}/input {"keys": […]}` — keys from the
    /// allow-list.
    public func agentInput(paneID: String, keys: [AgentKey]) async throws -> AgentInputReply {
        let body = try JSONSerialization.data(withJSONObject: ["keys": keys.map(\.rawValue)])
        return try await postAgentInput(paneID: paneID, body: body)
    }

    private func postAgentInput(paneID: String, body: Data) async throws -> AgentInputReply {
        let request = try request(encodedPath: DaemonClient.agentPath(paneID: paneID, suffix: "input"), method: "POST", body: body)
        let (data, response) = try await perform(request)
        guard let reply = try decodeAgentReply(AgentInputReply.self, data: data, response: response, what: "the input reply") else {
            throw DaemonError.rejected(status: 404, message: AgentScreenModel.unsupportedMessage)
        }
        return reply
    }

    /// 2xx decodes; a 404 with the daemon's `{"error"}` is a refusal (no
    /// such agent), a 404 without one is an older daemon (`nil`); 401 is the
    /// token; anything else carries the daemon's words.
    private func decodeAgentReply<T: Decodable>(_ type: T.Type, data: Data, response: URLResponse, what: String) throws -> T? {
        guard let http = response as? HTTPURLResponse else { throw DaemonError.malformed("no HTTP response") }
        let object = (try? JSONSerialization.jsonObject(with: data)) as? [String: Any]
        switch http.statusCode {
        case 200 ..< 300:
            do {
                return try DaemonClient.decoder.decode(T.self, from: data)
            } catch {
                throw DaemonError.malformed("\(what) did not decode")
            }
        case 401:
            throw DaemonError.unauthorized(endpoint: endpoint.display)
        case 404:
            guard let message = object?["error"] as? String else { return nil }
            throw DaemonError.rejected(status: 404, message: message)
        default:
            throw DaemonError.rejected(status: http.statusCode, message: (object?["error"] as? String) ?? "")
        }
    }
}
