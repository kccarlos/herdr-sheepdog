import Foundation

/// Where the daemon lives and whether it is the paired computer.
///
/// A paired computer is reached over TLS (`https`, `wss`), its key checked
/// against the pairing code's pin, and every request carries a session
/// ticket (`DeviceCredentials`). An unpaired address is plain HTTP, which
/// the daemon serves to loopback only: the fixture daemons in the tests.
///
/// The app enforces that rule itself, here, because App Transport Security
/// is off (it can express neither a pin nor an IP exception): an endpoint
/// that is not `secure` and not loopback builds no URL at all, so no request
/// or web socket can go out in plain text (`allowsConnection`).
public struct DaemonEndpoint: Equatable, Sendable {
    /// Host or IP, without a scheme.
    public var host: String
    public var port: Int
    /// The paired computer behind the address: its pins, its ticket and its
    /// id (`ComputerStore`). Nil for a bare address.
    public var credentials: DeviceCredentials?

    public init(host: String, port: Int = 7777, secure: Bool = false, credentials: DeviceCredentials? = nil) {
        self.host = host
        self.port = port
        self.secure = secure
        self.credentials = credentials
    }

    /// The computer's daemon id, nil for a bare address.
    public var computerID: String? { credentials?.daemonID }

    public static func == (lhs: DaemonEndpoint, rhs: DaemonEndpoint) -> Bool {
        lhs.host == rhs.host && lhs.port == rhs.port && lhs.secure == rhs.secure && lhs.credentials === rhs.credentials
    }

    /// Parses an address: `192.168.1.20:7777`, `http://mac.local:7777`,
    /// `[fd00::1]:7777`. `secure` is whether it is a paired computer.
    public init?(text: String, defaultPort: Int = 7777, secure: Bool = false, credentials: DeviceCredentials? = nil) {
        var s = text.trimmed
        guard !s.isEmpty else { return nil }
        if s.lowercased().hasPrefix("https://") {
            s = String(s.dropFirst(8))
        } else if s.lowercased().hasPrefix("http://") {
            s = String(s.dropFirst(7))
        }
        if let slash = s.firstIndex(of: "/") { s = String(s[s.startIndex ..< slash]) }
        guard !s.isEmpty else { return nil }
        var port = defaultPort
        // An IPv6 literal in brackets keeps its colons.
        if s.hasPrefix("["), let close = s.firstIndex(of: "]") {
            let after = s[s.index(after: close)...]
            if after.hasPrefix(":"), let n = Int(after.dropFirst()) { port = n }
            s = String(s[s.index(after: s.startIndex) ..< close])
        } else if let colon = s.lastIndex(of: ":") {
            if let n = Int(s[s.index(after: colon)...]) {
                port = n
                s = String(s[s.startIndex ..< colon])
            }
        }
        guard !s.isEmpty, port > 0, port < 65_536 else { return nil }
        self.host = s
        self.port = port
        self.secure = secure
        self.credentials = credentials
    }

    /// TLS, pinned, with a ticket: a paired computer.
    public var secure = false

    public var scheme: String { secure ? "https" : "http" }
    public var webSocketScheme: String { secure ? "wss" : "ws" }

    /// False for plain HTTP to anything but this device: the URL builders
    /// below then return nil and `DaemonClient` refuses with
    /// `PairingProblem.notPaired`. The offline demo's made-up `.invalid`
    /// host is allowed too: it never resolves and never leaves the app
    /// (`DemoURLProtocol`).
    public var allowsConnection: Bool { secure || Self.isLoopback(host) || host == DemoDaemon.host }

    /// `localhost`, `127.0.0.0/8` or `::1`, written as literals. A name that
    /// only resolves to loopback (`127.0.0.1.nip.io`) is not loopback.
    public static func isLoopback(_ host: String) -> Bool {
        var h = host.lowercased()
        if h.hasPrefix("["), h.hasSuffix("]") { h = String(h.dropFirst().dropLast()) }
        if h.hasSuffix(".") { h = String(h.dropLast()) }
        if h == "localhost" { return true }
        var v4 = in_addr()
        if h.withCString({ inet_pton(AF_INET, $0, &v4) }) == 1 {
            return UInt32(bigEndian: v4.s_addr) >> 24 == 127
        }
        var v6 = in6_addr()
        if h.withCString({ inet_pton(AF_INET6, $0, &v6) }) == 1 {
            return withUnsafeBytes(of: &v6) { Array($0) } == [UInt8](repeating: 0, count: 15) + [1]
        }
        return false
    }

    /// What the error banners say: `192.168.1.20:7777`.
    public var display: String { "\(host):\(port)" }

    /// Sets the current ticket on a request built synchronously (the `/ws`
    /// and `/term` upgrades); `DaemonClient` signs in when there is none.
    public func authorize(_ request: inout URLRequest) {
        guard secure, let header = credentials?.header() else { return }
        request.setValue(header, forHTTPHeaderField: "Authorization")
    }

    public var webSocketURL: URL? {
        guard allowsConnection else { return nil }
        return URL(string: "\(webSocketScheme)://\(hostForURL):\(port)/ws")
    }

    private var hostForURL: String {
        host.contains(":") && !host.hasPrefix("[") ? "[\(host)]" : host
    }

    public func url(path: String, query: [URLQueryItem] = []) -> URL? {
        guard allowsConnection, var components = URLComponents(string: "\(scheme)://\(hostForURL):\(port)") else { return nil }
        components.path = path
        components.queryItems = query.isEmpty ? nil : query
        return components.url
    }

    /// `url(path:query:)` for a path whose segments are already
    /// percent-encoded (a pane id with `%` or `/` in it).
    public func url(percentEncodedPath: String, query: [URLQueryItem] = []) -> URL? {
        guard allowsConnection, var components = URLComponents(string: "\(scheme)://\(hostForURL):\(port)") else { return nil }
        components.percentEncodedPath = percentEncodedPath
        components.queryItems = query.isEmpty ? nil : query
        return components.url
    }
}

/// Every failure the app has to explain in plain words.
public enum DaemonError: Error, Equatable, Sendable {
    /// Nothing answered: wrong address, daemon not running, phone on another network.
    case unreachable(endpoint: String, detail: String)
    /// The request went out but no answer came in time. Not the same as
    /// unreachable: with the computer otherwise online, it is one slow
    /// answer (macOS asking for folder access, a busy daemon), worth a
    /// "try again" where the user tapped, not "can't reach".
    case timedOut(endpoint: String)
    /// Something answered over TLS with a key that matches none of the
    /// computer's pins: nothing was sent. On its own this is not "scan
    /// again" (decision 1 of docs/dev/design/ios-state-architecture.md):
    /// the connection's `ComputerLink` decides, across the computer's
    /// addresses, between the untrusted-answer warning and a rescan.
    case untrusted(endpoint: String)
    /// 401 that signing in again did not fix.
    case unauthorized(endpoint: String)
    /// The app must scan the pairing code again (revoked, a changed key…).
    case pairing(PairingProblem)
    /// Something answered, but it was not a sheepdog daemon.
    case notSheepdog(endpoint: String)
    case http(status: Int, endpoint: String)
    case malformed(String)
    /// 409 from `POST /api/pending/answer`: the question is no longer open.
    case pendingGone
    /// Another refusal with the daemon's own words (`{"error": "…"}`).
    case rejected(status: Int, message: String)

    /// What a request that ran out of time says, where the user tapped.
    public static let slowAnswer = "Your computer is taking longer than usual to answer."

    /// The banner headline.
    public var message: String {
        switch self {
        case let .unreachable(endpoint, _):
            return "Can't reach the daemon at \(endpoint) — is it running?"
        case .timedOut:
            return Self.slowAnswer
        case let .untrusted(endpoint):
            return "Something answered at \(endpoint), but not with this computer's key, so the app did not use it."
        case let .unauthorized(endpoint):
            return "\(endpoint) turned this phone down. Scan the pairing code again."
        case let .pairing(problem):
            return problem.title
        case let .notSheepdog(endpoint):
            return "Something answered at \(endpoint), but it isn't a sheepdog daemon."
        case let .http(status, endpoint):
            return "\(endpoint) answered \(status). Check the daemon's log."
        case let .malformed(what):
            return "The daemon sent something the app could not read (\(what))."
        case .pendingGone:
            return ApprovalFlow.goneMessage
        case let .rejected(status, message):
            return message.isEmpty ? "The daemon refused (\(status))." : "The daemon refused: \(message)"
        }
    }

    /// The smaller line under the headline.
    public var hint: String? {
        switch self {
        case let .unreachable(_, detail):
            return detail.isEmpty ? SetupHelp.unreachableFix : "\(detail) \(SetupHelp.unreachableFix)"
        case .unauthorized:
            return SetupHelp.pairFix
        case .timedOut:
            return "Try again."
        case .untrusted:
            return "If this is a hotel or office network, try another one."
        case let .pairing(problem):
            return problem.message
        case .notSheepdog:
            return SetupHelp.notSheepdogFix
        case .http, .malformed, .pendingGone, .rejected:
            return nil
        }
    }
}

/// The daemon's HTTP surface: `/api/hello`, `/api/state`, and the `/events`
/// SSE stream.
///
/// Auth is the session ticket (`Authorization: Bearer sds_…`) on everything
/// sent to the paired computer, including the WebSocket upgrades
/// (docs/dev/ws-contract.md "Auth"); `perform` signs in when there is no
/// ticket and once more on a `401 session_expired`.
public final class DaemonClient: @unchecked Sendable {
    public private(set) var endpoint: DaemonEndpoint
    private let session: URLSession
    private let streamSession: URLSession

    /// The daemon's SSE keepalive period. `timeoutIntervalForRequest` is an
    /// inactivity timeout, so the stream's session has to tolerate a gap
    /// longer than this or `/events` dies every few seconds and the app
    /// silently stops seeing pushes.
    public static let eventKeepalive: TimeInterval = 15

    /// The pins of the endpoint's computer, followed across `update`.
    private let trust: EndpointTrust

    /// Every finished request's outcome, for the connection's
    /// `ComputerLink` (`requestSucceeded` / `requestFailed`). Called off the
    /// main thread; set it before the first request.
    public var onOutcome: (@Sendable (RequestOutcome) -> Void)?

    /// How one request ended, classified for that request alone.
    public enum RequestOutcome: Equatable, Sendable {
        case succeeded(rtt: TimeInterval)
        case failed(LinkError)
    }

    public init(endpoint: DaemonEndpoint, session: URLSession? = nil) {
        self.endpoint = endpoint
        trust = EndpointTrust(endpoint.credentials)
        ownsSessions = session == nil
        if let session {
            self.session = session
            self.streamSession = session
        } else {
            self.session = trust.session(configuration: DeviceCredentials.ephemeral(timeout: 8))

            let stream = URLSessionConfiguration.ephemeral
            stream.timeoutIntervalForRequest = DaemonClient.eventKeepalive * 4
            stream.timeoutIntervalForResource = 24 * 60 * 60
            stream.waitsForConnectivity = false
            stream.shouldUseExtendedBackgroundIdleMode = true
            self.streamSession = trust.session(configuration: stream)
        }
    }

    /// The sessions were made here (not handed in, like the demo's).
    private let ownsSessions: Bool

    /// Cancels what is in flight and releases the sessions (and their
    /// delegates). The client is unusable afterwards.
    public func invalidate() {
        guard ownsSessions else { return }
        session.invalidateAndCancel()
        streamSession.invalidateAndCancel()
    }

    public func update(endpoint: DaemonEndpoint) {
        self.endpoint = endpoint
        trust.follow(endpoint.credentials)
    }

    public func request(path: String, query: [URLQueryItem] = [], method: String = "GET", body: Data? = nil) throws -> URLRequest {
        try request(url: endpoint.url(path: path, query: query), method: method, body: body)
    }

    /// `request(path:…)` for an already percent-encoded path.
    public func request(encodedPath: String, query: [URLQueryItem] = [], method: String = "GET", body: Data? = nil) throws -> URLRequest {
        try request(url: endpoint.url(percentEncodedPath: encodedPath, query: query), method: method, body: body)
    }

    private func request(url: URL?, method: String, body: Data?) throws -> URLRequest {
        guard endpoint.allowsConnection else { throw DaemonError.pairing(.notPaired) }
        guard let url else {
            throw DaemonError.malformed("the address \(endpoint.display) is not a URL")
        }
        var request = URLRequest(url: url)
        request.httpMethod = method
        request.httpBody = body
        if body != nil { request.setValue("application/json", forHTTPHeaderField: "Content-Type") }
        endpoint.authorize(&request)
        return request
    }

    /// `GET /api/hello` — public even when a token is set, so this is what
    /// "Test connection" calls first.
    public func hello() async throws -> Hello {
        try await get(Hello.self, path: "/api/hello")
    }

    /// `GET /api/state` — needs the token. Also the auth probe: a 401 here
    /// is how the app learns the token is wrong before it opens `/ws`,
    /// where a 401 is much harder to read back.
    public func state() async throws -> DaemonState {
        try await get(DaemonState.self, path: "/api/state")
    }

    /// `GET /api/conversation?limit=&before=` — the conversation the daemon
    /// rebuilt from its log, newest last. Omit `before` for the newest page;
    /// pass the page's `nextBefore` to page back.
    public func conversation(limit: Int = 200, before: Int64? = nil) async throws -> ConversationPage {
        var query = [URLQueryItem(name: "limit", value: String(limit))]
        if let before { query.append(URLQueryItem(name: "before", value: String(before))) }
        return try await get(ConversationPage.self, path: "/api/conversation", query: query)
    }

    /// `POST /api/pending/answer {id, option}` — answers the open question
    /// with the option the user tapped (its 1-based `index`). Returns
    /// the daemon's `spoken` line ("Told auth Yes, proceed."). A question
    /// that is no longer open throws `.pendingGone`.
    public func answerPending(id: String, option: Int) async throws -> String {
        let body = try JSONSerialization.data(withJSONObject: ["id": id, "option": option] as [String: Any])
        let request = try request(path: "/api/pending/answer", method: "POST", body: body)
        let (data, response) = try await perform(request)
        guard let http = response as? HTTPURLResponse else { throw DaemonError.malformed("no HTTP response") }
        let object = (try? JSONSerialization.jsonObject(with: data)) as? [String: Any]
        switch http.statusCode {
        case 200 ..< 300:
            return (object?["spoken"] as? String) ?? ""
        case 401:
            throw DaemonError.unauthorized(endpoint: endpoint.display)
        case 409:
            throw DaemonError.pendingGone
        default:
            throw DaemonError.rejected(status: http.statusCode, message: (object?["error"] as? String) ?? "")
        }
    }

    /// `GET /api/voice/providers` — the live session's provider and the
    /// choices. `nil` when the daemon predates provider switching (404), so
    /// the app hides the picker instead of showing an error.
    public func voiceProviders() async throws -> VoiceProviders? {
        let request = try request(path: "/api/voice/providers")
        let (data, response) = try await perform(request)
        guard let http = response as? HTTPURLResponse else { throw DaemonError.malformed("no HTTP response") }
        switch http.statusCode {
        case 200 ..< 300: break
        case 404: return nil
        case 401: throw DaemonError.unauthorized(endpoint: endpoint.display)
        default: throw DaemonError.http(status: http.statusCode, endpoint: endpoint.display)
        }
        do {
            return try DaemonClient.decoder.decode(VoiceProviders.self, from: data)
        } catch {
            throw DaemonError.malformed("the voice provider list did not decode")
        }
    }

    /// `PUT /api/voice/provider {"provider": id}` — switches the live
    /// session. The daemon answers with the same shape as the list; an
    /// unknown or unavailable provider is a 400 with its reason.
    public func setVoiceProvider(_ id: String) async throws -> VoiceProviders {
        let body = try JSONSerialization.data(withJSONObject: ["provider": id])
        let request = try request(path: "/api/voice/provider", method: "PUT", body: body)
        let (data, response) = try await perform(request)
        guard let http = response as? HTTPURLResponse else { throw DaemonError.malformed("no HTTP response") }
        switch http.statusCode {
        case 200 ..< 300:
            do {
                return try DaemonClient.decoder.decode(VoiceProviders.self, from: data)
            } catch {
                throw DaemonError.malformed("the voice provider reply did not decode")
            }
        case 401:
            throw DaemonError.unauthorized(endpoint: endpoint.display)
        default:
            let object = (try? JSONSerialization.jsonObject(with: data)) as? [String: Any]
            throw DaemonError.rejected(status: http.statusCode, message: (object?["error"] as? String) ?? "")
        }
    }

    /// `POST /api/conversation/reset {"via": <via>}` — the gear/⋯ menu's
    /// "New conversation". `nil` when the daemon predates the feature
    /// (404): the caller hides the action instead of showing an error, the
    /// same convention as `voiceProviders()`.
    public func resetConversation(via: String) async throws -> ConversationReset? {
        let body = try JSONSerialization.data(withJSONObject: ["via": via])
        let request = try request(path: "/api/conversation/reset", method: "POST", body: body)
        let (data, response) = try await perform(request)
        guard let http = response as? HTTPURLResponse else { throw DaemonError.malformed("no HTTP response") }
        switch http.statusCode {
        case 200 ..< 300: break
        case 404: return nil
        case 401: throw DaemonError.unauthorized(endpoint: endpoint.display)
        default:
            let object = (try? JSONSerialization.jsonObject(with: data)) as? [String: Any]
            throw DaemonError.rejected(status: http.statusCode, message: (object?["error"] as? String) ?? "")
        }
        do {
            return try DaemonClient.decoder.decode(ConversationReset.self, from: data)
        } catch {
            throw DaemonError.malformed("the reset reply did not decode")
        }
    }

    /// `GET /api/events?n=&type=` — the newest `limit` events of the
    /// daemon's current run (its in-memory ring, 2 000 events), oldest
    /// first; `type` is a prefix filter (`voice.push` matches
    /// `voice.push_held` too).
    public func recentEvents(type: String? = nil, limit: Int = 100) async throws -> [DaemonEvent] {
        var query = [URLQueryItem(name: "n", value: String(limit))]
        if let type, !type.isEmpty { query.append(URLQueryItem(name: "type", value: type)) }
        let request = try request(path: "/api/events", query: query)
        let (data, response) = try await perform(request)
        guard let http = response as? HTTPURLResponse else { throw DaemonError.malformed("no HTTP response") }
        switch http.statusCode {
        case 200 ..< 300: break
        case 401: throw DaemonError.unauthorized(endpoint: endpoint.display)
        default: throw DaemonError.http(status: http.statusCode, endpoint: endpoint.display)
        }
        guard let events = SSEParser.decodeArray(data) else { throw DaemonError.malformed("the event list did not decode") }
        return events
    }

    /// `POST /api/utterance {"text": …}` — what the user typed,
    /// as if spoken. The daemon records it as typed user input first
    /// (so the one rail accepts it like a transcript), then hands it to the
    /// voice session, or with `--voice=off` to its text router, which
    /// answers in the reply. A text nothing can route is a 422 with the
    /// daemon's words.
    public func sendUtterance(_ text: String) async throws -> UtteranceReply {
        let body = try JSONSerialization.data(withJSONObject: ["text": text])
        let request = try request(path: "/api/utterance", method: "POST", body: body)
        let (data, response) = try await perform(request)
        guard let http = response as? HTTPURLResponse else { throw DaemonError.malformed("no HTTP response") }
        switch http.statusCode {
        case 200 ..< 300:
            return (try? DaemonClient.decoder.decode(UtteranceReply.self, from: data)) ?? UtteranceReply()
        case 401:
            throw DaemonError.unauthorized(endpoint: endpoint.display)
        default:
            let object = (try? JSONSerialization.jsonObject(with: data)) as? [String: Any]
            throw DaemonError.rejected(status: http.statusCode, message: (object?["error"] as? String) ?? "")
        }
    }

    /// `GET /api/brain/models[?provider=]` — the orchestrator's model catalog and
    /// current choice. `nil` on a daemon without the route (404); a daemon
    /// whose orchestrator is off answers 503 with the reason, thrown as
    /// `.rejected`.
    public func brainModels(provider: String? = nil) async throws -> BrainModels? {
        var query: [URLQueryItem] = []
        if let provider, !provider.isEmpty { query.append(URLQueryItem(name: "provider", value: provider)) }
        return try await sendJSON(BrainModels.self, path: "/api/brain/models", query: query, what: "the orchestrator's model list")
    }

    /// `PUT /api/brain/model {"provider", "model"}` — switches the orchestrator
    /// for its next turn; the daemon keeps the choice across restarts.
    public func setBrainModel(provider: String?, model: String) async throws -> BrainModels {
        var body: [String: String] = ["model": model]
        if let provider, !provider.isEmpty { body["provider"] = provider }
        let data = try JSONSerialization.data(withJSONObject: body)
        guard let models = try await sendJSON(BrainModels.self, path: "/api/brain/model", method: "PUT", body: data, what: "the orchestrator model reply") else {
            throw DaemonError.rejected(status: 404, message: "this daemon cannot switch the orchestrator's model")
        }
        return models
    }

    /// `GET /api/settings` — the chattiness and its levels. `nil` on a
    /// daemon without the route.
    public func settings() async throws -> DaemonSettings? {
        try await sendJSON(DaemonSettings.self, path: "/api/settings", what: "the settings")
    }

    /// `PUT /api/settings {"chattiness": level}`.
    public func setChattiness(_ level: String) async throws -> DaemonSettings {
        let data = try JSONSerialization.data(withJSONObject: ["chattiness": level])
        guard let settings = try await sendJSON(DaemonSettings.self, path: "/api/settings", method: "PUT", body: data, what: "the settings reply") else {
            throw DaemonError.rejected(status: 404, message: "this daemon has no settings to change")
        }
        return settings
    }

    /// `PUT /api/settings {"languages": codes}` — the language preference,
    /// most preferred first (an empty list clears it). The chattiness is
    /// left as it is.
    public func setLanguages(_ codes: [String]) async throws -> DaemonSettings {
        let data = try JSONSerialization.data(withJSONObject: ["languages": codes])
        guard let settings = try await sendJSON(DaemonSettings.self, path: "/api/settings", method: "PUT", body: data, what: "the settings reply") else {
            throw DaemonError.rejected(status: 404, message: "this daemon has no settings to change")
        }
        return settings
    }

    /// One JSON request: 2xx decodes, 404 is `nil` (an older daemon), 401
    /// is the token, anything else carries the daemon's own words.
    private func sendJSON<T: Decodable>(_ type: T.Type, path: String, query: [URLQueryItem] = [], method: String = "GET", body: Data? = nil, what: String) async throws -> T? {
        let request = try request(path: path, query: query, method: method, body: body)
        let (data, response) = try await perform(request)
        guard let http = response as? HTTPURLResponse else { throw DaemonError.malformed("no HTTP response") }
        switch http.statusCode {
        case 200 ..< 300:
            do {
                return try DaemonClient.decoder.decode(T.self, from: data)
            } catch {
                throw DaemonError.malformed("\(what) did not decode")
            }
        case 404:
            return nil
        case 401:
            throw DaemonError.unauthorized(endpoint: endpoint.display)
        default:
            let object = (try? JSONSerialization.jsonObject(with: data)) as? [String: Any]
            throw DaemonError.rejected(status: http.statusCode, message: (object?["error"] as? String) ?? "")
        }
    }

    private func get<T: Decodable>(_ type: T.Type, path: String, query: [URLQueryItem] = []) async throws -> T {
        let request = try request(path: path, query: query)
        let (data, response) = try await perform(request)
        guard let http = response as? HTTPURLResponse else { throw DaemonError.malformed("no HTTP response") }
        switch http.statusCode {
        case 200 ..< 300: break
        case 401: throw DaemonError.unauthorized(endpoint: endpoint.display)
        case 404: throw DaemonError.notSheepdog(endpoint: endpoint.display)
        default: throw DaemonError.http(status: http.statusCode, endpoint: endpoint.display)
        }
        do {
            return try DaemonClient.decoder.decode(T.self, from: data)
        } catch {
            throw DaemonError.notSheepdog(endpoint: endpoint.display)
        }
    }

    /// Sends a request with a fresh ticket (the paired computer), signs in
    /// again once on `401 no_session` / `session_expired`, and turns a
    /// refusal only a new scan fixes into `.pairing`.
    func perform(_ request: URLRequest) async throws -> (Data, URLResponse) {
        let started = Date()
        do {
            let result = try await performOnce(request)
            if let status = (result.1 as? HTTPURLResponse)?.statusCode, status >= 500 {
                onOutcome?(.failed(.httpOther(status)))
            } else {
                onOutcome?(.succeeded(rtt: Date().timeIntervalSince(started)))
            }
            return result
        } catch {
            onOutcome?(.failed(LinkError(error)))
            throw error
        }
    }

    private func performOnce(_ request: URLRequest) async throws -> (Data, URLResponse) {
        guard endpoint.secure else { return try await send(request) }
        var request = request
        try await authorize(&request)
        let first = try await send(request)
        guard let reason = Self.refusal(first) else { return first }
        if let problem = PairingProblem(reason: reason) { throw DaemonError.pairing(problem) }
        endpoint.credentials?.dropTicket()
        try await authorize(&request)
        let second = try await send(request)
        if let reason = Self.refusal(second), let problem = PairingProblem(reason: reason) { throw DaemonError.pairing(problem) }
        return second
    }

    /// Sets a ticket on a request to the paired computer, signing in first
    /// when there is none.
    func authorize(_ request: inout URLRequest) async throws {
        guard endpoint.secure, let base = endpoint.url(path: "/") else { return }
        guard let credentials = endpoint.credentials else { throw DaemonError.pairing(.noKey) }
        do {
            let ticket = try await credentials.ticket(base: base)
            request.setValue(ticket.header, forHTTPHeaderField: "Authorization")
        } catch let problem as PairingProblem {
            throw DaemonError.pairing(problem)
        } catch let error as URLError {
            // The sign-in judged its own handshakes: a mismatch there
            // arrives as `DaemonError.untrusted`, not as a URLError.
            throw Self.translate(error, endpoint: endpoint, sawMismatch: false)
        }
    }

    private func send(_ request: URLRequest) async throws -> (Data, URLResponse) {
        // A per-request delegate answers the challenge instead of the
        // session's: this request's verdict is its own.
        let attempt = trust.delegate.attempt()
        do {
            return try await session.data(for: request, delegate: attempt)
        } catch let error as URLError {
            // The app's own cancellation stays a cancellation: it is never
            // "unreachable" (`LinkError.cancelled`).
            if error.code == .cancelled, !attempt.sawMismatch { throw error }
            throw Self.translate(error, endpoint: endpoint, sawMismatch: attempt.sawMismatch)
        }
    }

    /// A transport error in the app's words. A key that matches no pin on
    /// *this* request is `untrusted`, never "scan again" by itself
    /// (`ComputerLink` decides that across the addresses); a timeout is
    /// `timedOut`; the rest is unreachable.
    public static func translate(_ error: URLError, endpoint: DaemonEndpoint, sawMismatch: Bool) -> DaemonError {
        if endpoint.secure, sawMismatch,
           [.cancelled, .serverCertificateUntrusted, .secureConnectionFailed, .serverCertificateHasUnknownRoot].contains(error.code) {
            return .untrusted(endpoint: endpoint.display)
        }
        if error.code == .timedOut { return .timedOut(endpoint: endpoint.display) }
        return .unreachable(endpoint: endpoint.display, detail: DaemonClient.describe(error))
    }

    /// The `reason` of a 401, nil for any other answer.
    static func refusal(_ result: (Data, URLResponse)) -> String? {
        guard (result.1 as? HTTPURLResponse)?.statusCode == 401 else { return nil }
        let object = (try? JSONSerialization.jsonObject(with: result.0)) as? [String: Any]
        return (object?["reason"] as? String) ?? ""
    }

    public static func describe(_ error: URLError) -> String {
        switch error.code {
        case .cannotConnectToHost: return "Nothing is listening on that port."
        case .cannotFindHost: return "That host name did not resolve."
        case .timedOut: return "It did not answer in time."
        case .notConnectedToInternet: return "This phone is not on a network."
        case .networkConnectionLost: return "The connection dropped."
        case .appTransportSecurityRequiresSecureConnection: return "iOS blocked the plain-HTTP request."
        default: return ""
        }
    }

    public static let decoder: JSONDecoder = {
        let d = JSONDecoder()
        d.dateDecodingStrategy = .custom { decoder in
            let text = try decoder.singleValueContainer().decode(String.self)
            if let date = ISO8601DateFormatter.sheepdog.date(from: text) { return date }
            let plain = ISO8601DateFormatter()
            if let date = plain.date(from: text) { return date }
            throw DecodingError.dataCorruptedError(in: try decoder.singleValueContainer(), debugDescription: "bad date \(text)")
        }
        return d
    }()

    // MARK: - Events

    /// The `/events` request that resumes after event id `since`: the
    /// `Last-Event-ID` header a reconnecting `EventSource` would send, and
    /// `?since=` as the fallback (the header wins on the daemon). With no
    /// id, neither — the daemon reads a small `?since=` as a count.
    public func eventsRequest(since: UInt64) throws -> URLRequest {
        var query: [URLQueryItem] = []
        if since > 0 { query.append(URLQueryItem(name: "since", value: String(since))) }
        var request = try self.request(path: "/events", query: query)
        request.timeoutInterval = DaemonClient.eventKeepalive * 4
        request.setValue("text/event-stream", forHTTPHeaderField: "Accept")
        if since > 0 { request.setValue(String(since), forHTTPHeaderField: "Last-Event-ID") }
        return request
    }

    /// `GET /events` as an async stream. Reconnecting is the caller's job;
    /// pass the last event id back as `since` so nothing is missed — the
    /// daemon replays what came after it, across restarts.
    ///
    /// `onOpen` runs once the daemon has answered 200, before any event: a
    /// quiet stream (only `: keepalive` comments, which the parser drops) is
    /// up, and the connection chip must not call it "Connecting".
    public func events(since: UInt64 = 0, onOpen: (@Sendable () -> Void)? = nil) -> AsyncThrowingStream<SSEParser.Output, Error> {
        AsyncThrowingStream { continuation in
            let task = Task {
                do {
                    var request = try self.eventsRequest(since: since)
                    try await self.authorize(&request)
                    let bytes: URLSession.AsyncBytes
                    let response: URLResponse
                    let attempt = self.trust.delegate.attempt()
                    do {
                        (bytes, response) = try await self.streamSession.bytes(for: request, delegate: attempt)
                    } catch let error as URLError {
                        if error.code == .cancelled, !attempt.sawMismatch { throw error }
                        throw Self.translate(error, endpoint: self.endpoint, sawMismatch: attempt.sawMismatch)
                    }
                    if let http = response as? HTTPURLResponse, http.statusCode == 401 {
                        let body = try? await bytes.reduce(into: Data()) { $0.append($1) }
                        if let reason = Self.refusal((body ?? Data(), response)), let problem = PairingProblem(reason: reason) {
                            throw DaemonError.pairing(problem)
                        }
                        // An expired ticket: the next attempt signs in again.
                        self.endpoint.credentials?.dropTicket()
                        throw DaemonError.unauthorized(endpoint: self.endpoint.display)
                    }
                    onOpen?()
                    var parser = SSEParser(lastSeq: since)
                    // Not `bytes.lines`: Foundation's AsyncLineSequence drops
                    // empty lines, and an empty line is exactly what ends one
                    // SSE record — with it gone, no event is ever complete.
                    var line = [UInt8]()
                    for try await byte in bytes {
                        if byte != 0x0A {
                            line.append(byte)
                            if line.count > 1 << 20 { line.removeAll(keepingCapacity: true) }
                            continue
                        }
                        if line.last == 0x0D { line.removeLast() }
                        let text = String(decoding: line, as: UTF8.self)
                        line.removeAll(keepingCapacity: true)
                        if let out = parser.push(line: text) { continuation.yield(out) }
                        if Task.isCancelled { break }
                    }
                    continuation.finish()
                } catch is CancellationError {
                    continuation.finish()
                } catch let error as URLError where error.code == .cancelled {
                    continuation.finish()
                } catch let error as URLError {
                    continuation.finish(throwing: DaemonError.unreachable(endpoint: self.endpoint.display, detail: DaemonClient.describe(error)))
                } catch {
                    continuation.finish(throwing: error)
                }
            }
            continuation.onTermination = { _ in task.cancel() }
        }
    }
}
