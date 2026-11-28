import CryptoKit
import Foundation
import Network

/// A tiny HTTP server inside the UI test runner, for the error states a
/// stranger meets that a closed port cannot produce: a daemon that turns the
/// token down, or something on the port that is not sheepdog. The simulator
/// shares the Mac's loopback, so the app reaches it at 127.0.0.1:<port>.
/// Needs no daemon checkout, so these tests also run on Xcode Cloud.
final class StubDaemon {
    enum Mode {
        /// `/api/hello` answers as a sheepdog daemon; everything else is 401.
        case wrongToken
        /// Every path answers 200 with an HTML page (a router, a web server).
        case notSheepdog
        /// A daemon that accepts the token: `/api/hello` and `/api/state`
        /// (no agents) answer, with `update` as `/api/state`'s update
        /// object when given (nil: a daemon too old to report one);
        /// everything else is 404.
        case daemon(update: String?)
        /// A computer of several: `/api/hello` names it, `/api/state`
        /// sends these agents and questions (JSON arrays), and an answer
        /// is accepted.
        ///
        /// `history` is how many conversation lines it has; with more than
        /// one, the last is "Latest from <name>."
        ///
        /// `reply`, when given, is an orchestrator reply with these options
        /// (`voice.tool_result` from `to_supervisor` in `/api/events`), half
        /// a minute after the first line; every line typed
        /// (`POST /api/turn`) is then added to the conversation.
        case computer(name: String, agents: String, questions: String, history: Int = 1, idBase: Int = 0, reply: (spoken: String, options: [String])? = nil, factory: String? = nil, pendingAction: String? = nil)
    }

    private let listener: NWListener
    private let mode: Mode
    private let queue = DispatchQueue(label: "stub-daemon")
    private(set) var port: UInt16 = 0
    /// How long a POST waits before its answer (a slow computer), so a
    /// second tap lands while the first is in flight.
    var postDelay: TimeInterval = 0
    /// Optional response delay for `POST /api/factory/projects/check` alone.
    var checkDelay: TimeInterval {
        get { lock.withLock { _checkDelay } }
        set { lock.withLock { _checkDelay = newValue } }
    }
    private var _checkDelay: TimeInterval = 0
    /// Optional response delay for `GET /api/factory/.../settings` alone.
    var settingsGetDelay: TimeInterval {
        get { lock.withLock { _settingsGetDelay } }
        set { lock.withLock { _settingsGetDelay = newValue } }
    }
    private var _settingsGetDelay: TimeInterval = 0
    private let lock = NSLock()
    private var seen: [String] = []
    /// The lines typed so far (`POST /api/turn`'s `text`).
    private var typed: [String] = []
    /// Raw JSON bodies sent to `POST /api/pending/answer`.
    private var answers: [String] = []
    /// Terminal screen snapshots by pane id (`GET /api/agents/<pane>/screen`).
    var screens: [String: String] = [:]
    /// Dynamic overrides for `/api/state` fields (when set).
    var factoryState: String? {
        get { lock.withLock { _factoryState } }
        set { lock.withLock { _factoryState = newValue } }
    }
    private var _factoryState: String?
    var questionsState: String? {
        get { lock.withLock { _questionsState } }
        set { lock.withLock { _questionsState = newValue } }
    }
    private var _questionsState: String?
    var pendingActionState: String? {
        get { lock.withLock { _pendingActionState } }
        set { lock.withLock { _pendingActionState = newValue } }
    }
    private var _pendingActionState: String?
    /// Graph JSON by project id (`GET /api/factory/projects/<id>/graph`).
    var factoryGraphs: [String: String] = [:]
    /// Optional HTTP error `(status, json)` for graph requests by project id.
    var factoryGraphErrors: [String: (Int, String)] = [:]
    /// Optional features array advertised by `GET /api/hello` in `.computer` mode.
    var helloFeatures: [String]?
    /// `GET /api/spaces` JSON or error `(status, json)` in `.computer` mode.
    var spacesJSON: String? {
        get { lock.withLock { _spacesJSON } }
        set { lock.withLock { _spacesJSON = newValue } }
    }
    private var _spacesJSON: String?
    var spacesError: (Int, String)? {
        get { lock.withLock { _spacesError } }
        set { lock.withLock { _spacesError = newValue } }
    }
    private var _spacesError: (Int, String)?
    /// Simulates a computer whose daemon has no factory (`503 factory is not available`).
    var factoryUnavailable: Bool {
        get { lock.withLock { _factoryUnavailable } }
        set { lock.withLock { _factoryUnavailable = newValue } }
    }
    private var _factoryUnavailable = false
    /// Settings JSON by project id (`""` for `GET /api/factory/settings`).
    var factorySettings: [String: String] = [:]
    var factorySettingsErrors: [String: (Int, String)] = [:]
    var factoryAsks: String = #"{"protocol":1,"asks":[]}"#
    var factoryAsksError: (Int, String)?
    /// Optional custom handlers for factory and answer POST routes.
    var sayHandler: ((String, String) -> (Int, String))?
    var stopHandler: ((String) -> (Int, String))?
    var startHandler: ((String) -> (Int, String))?
    var checkHandler: ((String) -> (Int, String))?
    var proposeSettingHandler: ((String, String) -> (Int, String))?
    var answerHandler: ((String) -> (Int, String))?
    var askAnswerHandler: ((String, String) -> (Int, String))?
    /// Optional custom handler for `POST /api/agents/<pane>/input`.
    var agentInputHandler: ((String, String) -> (Int, String))?
    /// Optional custom handler for `GET /api/conversation` (receives typed lines so far).
    var conversationHandler: (([String]) -> String)?
    /// Enable SSE `/events` streaming so tests can push `factory.changed`.
    var enableEvents = false
    /// Enable live `/term` WebSocket streaming from `screens` so agent/coordinator windows go live instead of falling back to snapshots.
    var enableTerm = false
    private var eventClients: [NWConnection] = []
    private var termClients: [NWConnection] = []
    private var nextEventID: UInt64 = 1_795_550_000_000_001
    private var says: [(project: String, body: String)] = []
    private var stops: [String] = []
    private var starts: [String] = []
    private var checks: [String] = []
    private var proposals: [(project: String, body: String)] = []
    private var agentInputs: [(pane: String, body: String)] = []

    /// How many requests started with `prefix` ("POST /api/turn").
    func requests(_ prefix: String) -> Int {
        lock.withLock { seen.filter { $0.hasPrefix(prefix) }.count }
    }

    func answerBodies() -> [String] { lock.withLock { answers } }
    func sayBodies() -> [(project: String, body: String)] { lock.withLock { says } }
    func stopBodies() -> [String] { lock.withLock { stops } }
    func startBodies() -> [String] { lock.withLock { starts } }
    func checkBodies() -> [String] { lock.withLock { checks } }
    func proposeBodies() -> [(project: String, body: String)] { lock.withLock { proposals } }
    func agentInputBodies() -> [(pane: String, body: String)] { lock.withLock { agentInputs } }

    /// Pushes a `factory.changed` SSE event to connected `/events` clients.
    func emitFactoryChanged(project: String) {
        let (id, clients) = lock.withLock { () -> (UInt64, [NWConnection]) in
            let id = nextEventID; nextEventID += 1
            return (id, eventClients)
        }
        let payload = "id: \(id)\nevent: log\ndata: {\"id\":\(id),\"ts\":\"2026-11-26T00:00:00Z\",\"source\":\"factory\",\"type\":\"factory.changed\",\"payload\":{\"project\":\(Self.json(project))}}\n\n"
        let data = Data(payload.utf8)
        queue.async {
            for client in clients { client.send(content: data, completion: .idempotent) }
        }
    }

    init(_ mode: Mode) throws {
        self.mode = mode
        let parameters = NWParameters.tcp
        parameters.requiredLocalEndpoint = NWEndpoint.hostPort(host: "127.0.0.1", port: .any)
        listener = try NWListener(using: parameters)
    }

    /// Starts listening and returns `127.0.0.1:<port>`.
    func start() throws -> String {
        let ready = DispatchSemaphore(value: 0)
        listener.stateUpdateHandler = { state in
            if case .ready = state { ready.signal() }
        }
        listener.newConnectionHandler = { [weak self] connection in self?.serve(connection) }
        listener.start(queue: queue)
        guard ready.wait(timeout: .now() + 5) == .success, let port = listener.port?.rawValue else {
            throw NSError(domain: "StubDaemon", code: 1, userInfo: [NSLocalizedDescriptionKey: "the stub did not start"])
        }
        self.port = port
        return "127.0.0.1:\(port)"
    }

    func stop() {
        let clients = lock.withLock { () -> [NWConnection] in
            let c = eventClients + termClients
            eventClients = []
            termClients = []
            return c
        }
        for client in clients { client.cancel() }
        listener.cancel()
    }

    private func serve(_ connection: NWConnection) {
        connection.start(queue: queue)
        receiveRequest(connection, so: Data())
    }

    /// Reads until the head and the body its Content-Length announces are
    /// in (a body can come in a packet of its own), then answers.
    private func receiveRequest(_ connection: NWConnection, so far: Data) {
        connection.receive(minimumIncompleteLength: 1, maximumLength: 64 * 1024) { [weak self] data, _, done, error in
            guard let self else { return }
            let received = far + (data ?? Data())
            if !done, error == nil, !Self.isComplete(received) {
                self.receiveRequest(connection, so: received)
                return
            }
            self.answer(connection, received)
        }
    }

    private static func isComplete(_ request: Data) -> Bool {
        guard let end = request.range(of: Data("\r\n\r\n".utf8)) else { return false }
        let head = String(decoding: request[..<end.lowerBound], as: UTF8.self).lowercased()
        let length = head.components(separatedBy: "\r\n")
            .first { $0.hasPrefix("content-length:") }
            .flatMap { Int($0.dropFirst("content-length:".count).trimmingCharacters(in: .whitespaces)) } ?? 0
        return request.count - end.upperBound >= length
    }

    private func answer(_ connection: NWConnection, _ received: Data) {
        let headEnd = received.range(of: Data("\r\n\r\n".utf8))
        let headText = headEnd.map { String(decoding: received[..<$0.lowerBound], as: UTF8.self) }
            ?? String(decoding: received, as: UTF8.self)
        let trailing = headEnd.map { Data(received[$0.upperBound...]) } ?? Data()
        let request = String(decoding: received, as: UTF8.self)
        let words = headText.split(separator: " ")
        let method = words.first.map(String.init) ?? "GET"
        let path = words.dropFirst().first.map(String.init) ?? "/"
        let body = String(decoding: trailing, as: UTF8.self)
        lock.withLock {
            seen.append("\(method) \(path)")
            if method == "POST", path.hasPrefix("/api/turn") { typed.append(Self.typedText(request)) }
            if method == "POST", path.hasPrefix("/api/pending/answer") { answers.append(body) }
        }
        if method == "GET", path.hasPrefix("/events"), enableEvents {
            let head = Data("HTTP/1.1 200 OK\r\nContent-Type: text/event-stream\r\nCache-Control: no-cache\r\n\r\n: ok\n\n".utf8)
            lock.withLock { eventClients.append(connection) }
            connection.send(content: head, completion: .idempotent)
            return
        }
        if method == "GET", path.hasPrefix("/term"), enableTerm, let wsKey = Self.headerValue("sec-websocket-key", in: headText) {
            upgradeTermWebSocket(connection, key: wsKey, initial: trailing)
            return
        }
        let reply = response(for: path, method: method, body: body)
        let clean = path.prefix { $0 != "?" }
        let isSettingsGet = method == "GET" && (clean == "/api/factory/settings" || (clean.hasPrefix("/api/factory/projects/") && clean.hasSuffix("/settings")))
        let delay: TimeInterval = if isSettingsGet, settingsGetDelay > 0 {
            settingsGetDelay
        } else if method == "POST", path.hasPrefix("/api/factory/projects/check"), checkDelay > 0 {
            checkDelay
        } else {
            method == "POST" ? postDelay : 0
        }
        queue.asyncAfter(deadline: .now() + delay) {
            connection.send(content: reply, completion: .contentProcessed { _ in connection.cancel() })
        }
    }

    private func response(for path: String, method: String = "GET", body: String = "") -> Data {
        switch mode {
        case .notSheepdog:
            return http(200, "text/html", "<html><body>Router login</body></html>")
        case .wrongToken:
            if path.hasPrefix("/api/hello") {
                return http(200, "application/json", #"{"name":"sheepdog","version":"0.0.0-stub","auth":"token","ws":"/ws","audio":{"in_hz":16000,"out_hz":24000,"format":"pcm16le mono"}}"#)
            }
            return http(401, "application/json", #"{"error":"unauthorized"}"#)
        case .daemon(let update):
            if path.hasPrefix("/api/hello") {
                return http(200, "application/json", #"{"name":"sheepdog","version":"v0.1.4","auth":"token","ws":"/ws","audio":{"in_hz":16000,"out_hz":24000,"format":"pcm16le mono"}}"#)
            }
            if path.hasPrefix("/api/state") {
                let voice = #""voice":{"mode":"gemini","status":"connected","muted":false,"stalled":false,"client_connected":false}"#
                let tail = update.map { #","update":"# + $0 } ?? ""
                return http(200, "application/json", #"{"agents":[],"# + voice + #","version":"v0.1.4""# + tail + "}")
            }
            return http(404, "application/json", #"{"error":"not found"}"#)
        case let .computer(name, agents, questions, history, idBase, reply, factory, pendingAction):
            if path.hasPrefix("/api/hello") {
                let feats = helloFeatures.map { #","features":["# + $0.map(Self.json).joined(separator: ",") + "]" } ?? ""
                return http(200, "application/json", #"{"name":"sheepdog","version":"v0.1.4","auth":"token","ws":"/ws","audio":{"in_hz":16000,"out_hz":24000,"format":"pcm16le mono"},"computer":{"id":"stub","name":""# + name + #""}"# + feats + "}")
            }
            if method == "GET", path.hasPrefix("/api/spaces") {
                if let err = spacesError { return http(err.0, "application/json", err.1) }
                if let spaces = spacesJSON { return http(200, "application/json", spaces) }
            }
            if path.hasPrefix("/api/state") {
                let voice = #""voice":{"mode":"off","status":"off","muted":false,"stalled":false,"client_connected":false}"#
                let currentFactory = factoryState ?? factory
                let currentQuestions = questionsState ?? questions
                let currentPending = pendingActionState ?? pendingAction
                let factoryPart = currentFactory.map { #","factory":"# + $0 } ?? ""
                let pendingPart = currentPending.map { #","pending_action":"# + $0 } ?? ""
                return http(200, "application/json", #"{"agents":"# + agents + #","questions":"# + currentQuestions + factoryPart + pendingPart + "," + voice + #","version":"v0.1.4"}"#)
            }
            if path.hasPrefix("/api/factory/") {
                return factoryResponse(path: path, method: method, body: body, fallback: factory)
            }
            if path.hasPrefix("/api/pending/answer") {
                let (status, json) = answerHandler?(body) ?? (200, #"{"ok":true,"spoken":"Answered."}"#)
                return http(status, "application/json", json)
            }
            if path.hasPrefix("/api/turn") {
                let turn = lock.withLock { typed.count }
                let text = reply?.spoken ?? "Received."
                return http(200, "application/json", #"{"reply":"# + Self.json(text) + #","turn_id":"stub-turn-\#(turn)","acted":false}"#)
            }
            if path.hasPrefix("/api/conversation") {
                if let custom = conversationHandler {
                    let typedLines = lock.withLock { self.typed }
                    return http(200, "application/json", custom(typedLines))
                }
                // The first line says whose conversation this is.
                let lines = (0 ..< history).map { index -> String in
                    let text = history > 1 && index == history - 1 ? "Latest from \(name)."
                        : index == 0 ? "Hi from \(name)." : "Line \(index) from \(name)."
                    return #"{"id":"# + String(1_790_127_618_456_893 + index + idBase) + #","ts":"2026-10-23T09:00:00Z","kind":"dog","text":""# + text + #""}"#
                }
                let typed = reply == nil ? [] : lock.withLock { self.typed }
                let said = typed.enumerated().map { index, text -> String in
                    let ts = "2026-10-23T09:01:" + String(format: "%02d", min(index, 59)) + "Z"
                    return #"{"id":"# + String(1_790_127_718_456_893 + index + idBase) + #","ts":""# + ts + #"","kind":"shepherd","via":"typed","text":"# + Self.json(text) + "}"
                }
                return http(200, "application/json", #"{"items":["# + (lines + said).joined(separator: ",") + "]}")
            }
            if path.hasPrefix("/api/events"), let reply {
                // The orchestrator's reply; no held pushes.
                guard path.contains("voice.tool_result") else { return http(200, "application/json", "[]") }
                let options = reply.options.map(Self.json).joined(separator: ",")
                let payload = #"{"name":"to_supervisor","spoken":"# + Self.json(reply.spoken) + #","options":["# + options + "]}"
                let event = #"{"id":"# + String(1_790_127_648_456_893 + idBase) + #","ts":"2026-10-23T09:00:30.000Z","source":"voice","type":"voice.tool_result","payload":"# + payload + "}"
                return http(200, "application/json", "[" + event + "]")
            }
            if path.hasPrefix("/api/agents/") {
                let raw = path.dropFirst("/api/agents/".count).prefix { $0 != "/" && $0 != "?" }
                let pane = String(raw).removingPercentEncoding ?? String(raw)
                if path.contains("/screen"), let text = lock.withLock({ screens[pane] }) {
                    return http(200, "application/json", #"{"pane_id":"# + Self.json(pane) + #","alias":"agent","state":"working","text":"# + Self.json(text) + #","hash":"h1"}"#)
                }
                if path.contains("/input") {
                    lock.withLock { agentInputs.append((pane, body)) }
                    let (status, json) = agentInputHandler?(pane, body) ?? (200, #"{"kind":"prompt","spoken":"Sent."}"#)
                    return http(status, "application/json", json)
                }
            }
            return http(404, "application/json", #"{"error":"not found"}"#)
        }
    }

    /// One blocked agent with an open question, as `/api/state` sends it.
    static func blocked(alias: String, space: String, pane: String, question: String, created: String = "2026-10-23T09:00:00Z") -> (agent: String, question: String) {
        let agent = #"{"pane_id":""# + pane + #"","alias":""# + alias + #"","kind":"claude","state":"blocked","workspace_label":""# + space + #""}"#
        let q = #"{"id":"q-"# + pane + #"","pane_id":""# + pane + #"","kind":"answer_prompt","agent":""# + alias + #"","summary":""# + question + #"","created_at":""# + created + #"","options":[{"index":1,"label":"Yes"},{"index":2,"label":"No"}]}"#
        return (agent, q)
    }

    /// A string as a JSON literal, quotes included.
    private static func json(_ text: String) -> String {
        let data = (try? JSONSerialization.data(withJSONObject: [text])) ?? Data(#"[""]"#.utf8)
        return String(String(decoding: data, as: UTF8.self).dropFirst().dropLast())
    }

    /// `{"text": …}` from a request's body, when it came with the head.
    private static func typedText(_ request: String) -> String {
        guard let range = request.range(of: "\r\n\r\n"),
              let object = try? JSONSerialization.jsonObject(with: Data(request[range.upperBound...].utf8)) as? [String: Any],
              let text = object["text"] as? String else { return "(typed)" }
        return text
    }

    private func factoryResponse(path: String, method: String, body: String, fallback: String?) -> Data {
        if factoryUnavailable {
            return http(503, "application/json", #"{"error":"factory is not available"}"#)
        }
        let clean = String(path.prefix { $0 != "?" })
        if method == "GET", clean == "/api/factory/projects" {
            return http(200, "application/json", #"{"protocol":1,"projects":"# + (factoryState ?? fallback ?? "[]") + "}")
        }
        if method == "POST", clean == "/api/factory/projects/check" {
            lock.withLock { checks.append(body) }
            let (status, json) = checkHandler?(body) ?? (200, #"{"results":[]}"#)
            return http(status, "application/json", json)
        }
        if method == "GET", clean == "/api/factory/asks" {
            if let err = lock.withLock({ factoryAsksError }) { return http(err.0, "application/json", err.1) }
            return http(200, "application/json", lock.withLock { factoryAsks })
        }
        if method == "POST", clean.hasPrefix("/api/factory/asks/"), clean.hasSuffix("/answer") {
            let rawID = clean.dropFirst("/api/factory/asks/".count).dropLast("/answer".count)
            let id = String(rawID).removingPercentEncoding ?? String(rawID)
            lock.withLock { answers.append(#"{"id":"fask-\#(id)","raw":\#(Self.json(body))}"#) }
            let (status, json) = askAnswerHandler?(id, body) ?? answerHandler?(body) ?? (200, #"{"ok":true,"ask_id":\#(Self.json(id))}"#)
            return http(status, "application/json", json)
        }
        if method == "GET", clean == "/api/factory/settings" {
            if let err = lock.withLock({ factorySettingsErrors[""] }) { return http(err.0, "application/json", err.1) }
            if let rows = lock.withLock({ factorySettings[""] }) { return http(200, "application/json", rows) }
            return http(404, "application/json", #"{"error":"not found"}"#)
        }
        if method == "POST", clean == "/api/factory/projects/stop" {
            lock.withLock { stops.append(body) }
            let (status, json) = stopHandler?(body) ?? (200, #"{"ok":true}"#)
            return http(status, "application/json", json)
        }
        if method == "POST", clean == "/api/factory/projects/start" {
            lock.withLock { starts.append(body) }
            let (status, json) = startHandler?(body) ?? (200, #"{"ok":true}"#)
            return http(status, "application/json", json)
        }
        let prefix = "/api/factory/projects/"
        if clean.hasPrefix(prefix) {
            let rest = clean.dropFirst(prefix.count)
            let rawID = rest.prefix { $0 != "/" }
            let id = String(rawID).removingPercentEncoding ?? String(rawID)
            let suffix = String(rest.dropFirst(rawID.count))
            if method == "GET", suffix == "/graph" {
                if let err = lock.withLock({ factoryGraphErrors[id] }) { return http(err.0, "application/json", err.1) }
                if let graph = lock.withLock({ factoryGraphs[id] }) { return http(200, "application/json", graph) }
                return http(404, "application/json", #"{"error":"factory project not found"}"#)
            }
            if method == "GET", suffix == "/settings" {
                if let err = lock.withLock({ factorySettingsErrors[id] }) { return http(err.0, "application/json", err.1) }
                if let rows = lock.withLock({ factorySettings[id] }) { return http(200, "application/json", rows) }
                return http(404, "application/json", #"{"error":"factory project not found"}"#)
            }
            if method == "POST", suffix == "/settings" {
                lock.withLock { proposals.append((id, body)) }
                let (status, json) = proposeSettingHandler?(id, body) ?? (404, #"{"error":"factory project not found"}"#)
                return http(status, "application/json", json)
            }
            if method == "POST", suffix == "/say" {
                lock.withLock { says.append((id, body)) }
                let (status, json) = sayHandler?(id, body) ?? (200, #"{"ok":true}"#)
                return http(status, "application/json", json)
            }
        }
        return http(404, "application/json", #"{"error":"not found"}"#)
    }

    private func http(_ status: Int, _ type: String, _ body: String) -> Data {
        let reason = [200: "OK", 400: "Bad Request", 401: "Unauthorized", 403: "Forbidden", 404: "Not Found", 409: "Conflict", 502: "Bad Gateway", 503: "Service Unavailable"][status] ?? "Error"
        let bytes = Data(body.utf8)
        let head = "HTTP/1.1 \(status) \(reason)\r\nContent-Type: \(type)\r\nContent-Length: \(bytes.count)\r\nConnection: close\r\n\r\n"
        return Data(head.utf8) + bytes
    }

    // MARK: - Live `/term` WebSocket

    private final class TermConnectionState {
        var buffer = Data()
        var channels: [Int: (pane: String, cols: Int, rows: Int, seq: UInt32)] = [:]
    }

    private static func headerValue(_ name: String, in head: String) -> String? {
        let lower = name.lowercased() + ":"
        for line in head.components(separatedBy: "\r\n") {
            if line.lowercased().hasPrefix(lower) {
                return line.dropFirst(lower.count).trimmingCharacters(in: .whitespaces)
            }
        }
        return nil
    }

    private func upgradeTermWebSocket(_ connection: NWConnection, key: String, initial: Data) {
        let magic = key + "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"
        let accept = Data(Insecure.SHA1.hash(data: Data(magic.utf8))).base64EncodedString()
        let response = "HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: \(accept)\r\n\r\n"
        lock.withLock { termClients.append(connection) }
        let state = TermConnectionState()
        state.buffer = initial
        connection.send(content: Data(response.utf8), completion: .contentProcessed { [weak self] error in
            guard let self, error == nil else { return }
            self.drainTermFrames(connection, state: state)
            self.receiveTermData(connection, state: state)
        })
    }

    private func receiveTermData(_ connection: NWConnection, state: TermConnectionState) {
        connection.receive(minimumIncompleteLength: 1, maximumLength: 64 * 1024) { [weak self] data, _, done, error in
            guard let self else { return }
            if let data, !data.isEmpty {
                state.buffer.append(data)
                self.drainTermFrames(connection, state: state)
            }
            if !done, error == nil {
                self.receiveTermData(connection, state: state)
            }
        }
    }

    private func drainTermFrames(_ connection: NWConnection, state: TermConnectionState) {
        while let (opcode, payload, consumed) = Self.parseWSFrame(state.buffer) {
            state.buffer.removeFirst(consumed)
            switch opcode {
            case 0x1:
                handleTermText(connection, payload: payload, state: state)
            case 0x8:
                connection.send(content: Self.encodeWSFrame(opcode: 0x8, payload: Data()), completion: .contentProcessed { _ in connection.cancel() })
                return
            case 0x9:
                connection.send(content: Self.encodeWSFrame(opcode: 0xA, payload: payload), completion: .idempotent)
            default:
                break
            }
        }
    }

    private func handleTermText(_ connection: NWConnection, payload: Data, state: TermConnectionState) {
        guard let obj = try? JSONSerialization.jsonObject(with: payload) as? [String: Any],
              let type = obj["type"] as? String else { return }
        switch type {
        case "term-hello":
            sendWSText(connection, #"{"type":"term-ready","protocol":1,"limits":{"channels":4},"shell":false}"#)
        case "term-open":
            guard let ch = obj["ch"] as? Int,
                  let target = obj["target"] as? [String: Any],
                  let pane = target["pane_id"] as? String else { return }
            let cols = obj["cols"] as? Int ?? 58
            let rows = obj["rows"] as? Int ?? 30
            guard lock.withLock({ screens[pane] }) != nil else {
                sendWSText(connection, #"{"type":"term-closed","ch":\#(ch),"reason":"refused:not_tracked"}"#)
                return
            }
            state.channels[ch] = (pane: pane, cols: cols, rows: rows, seq: 0)
            sendWSText(connection, #"{"type":"term-opened","ch":\#(ch),"pane_id":\#(Self.json(pane)),"mode":"control","cols":\#(cols),"rows":\#(rows),"kind":"agent"}"#)
            sendTermFullFrame(connection, ch: ch, state: state)
        case "term-resize":
            guard let ch = obj["ch"] as? Int, var channel = state.channels[ch] else { return }
            if let cols = obj["cols"] as? Int { channel.cols = cols }
            if let rows = obj["rows"] as? Int { channel.rows = rows }
            state.channels[ch] = channel
            sendTermFullFrame(connection, ch: ch, state: state)
        case "term-close":
            if let ch = obj["ch"] as? Int { state.channels[ch] = nil }
        default:
            break
        }
    }

    private func sendTermFullFrame(_ connection: NWConnection, ch: Int, state: TermConnectionState) {
        guard var channel = state.channels[ch],
              let text = lock.withLock({ screens[channel.pane] }) else { return }
        channel.seq &+= 1
        state.channels[ch] = channel
        var lines = text.replacingOccurrences(of: "\r\n", with: "\n").components(separatedBy: "\n")
        while lines.last?.isEmpty == true { lines.removeLast() }
        if channel.rows > 0, lines.count > channel.rows { lines = Array(lines.suffix(channel.rows)) }
        var ansi = Data("\u{1b}[?2026h\u{1b}[?25l\u{1b}[0m\u{1b}[2J".utf8)
        for (index, line) in lines.enumerated() {
            ansi.append(contentsOf: "\u{1b}[\(index + 1);1H".utf8)
            ansi.append(contentsOf: line.utf8)
        }
        ansi.append(contentsOf: "\u{1b}[0m\u{1b}[?2026l".utf8)
        var frame = Data([
            0x01,
            UInt8((ch >> 8) & 0xff), UInt8(ch & 0xff),
            UInt8((channel.seq >> 24) & 0xff), UInt8((channel.seq >> 16) & 0xff),
            UInt8((channel.seq >> 8) & 0xff), UInt8(channel.seq & 0xff),
            UInt8((channel.cols >> 8) & 0xff), UInt8(channel.cols & 0xff),
            UInt8((channel.rows >> 8) & 0xff), UInt8(channel.rows & 0xff),
            0x01,
        ])
        frame.append(ansi)
        connection.send(content: Self.encodeWSFrame(opcode: 0x2, payload: frame), completion: .idempotent)
    }

    private func sendWSText(_ connection: NWConnection, _ text: String) {
        connection.send(content: Self.encodeWSFrame(opcode: 0x1, payload: Data(text.utf8)), completion: .idempotent)
    }

    private static func parseWSFrame(_ data: Data) -> (opcode: UInt8, payload: Data, consumed: Int)? {
        let bytes = [UInt8](data)
        guard bytes.count >= 2 else { return nil }
        let opcode = bytes[0] & 0x0f
        let masked = (bytes[1] & 0x80) != 0
        var length = Int(bytes[1] & 0x7f)
        var offset = 2
        if length == 126 {
            guard bytes.count >= offset + 2 else { return nil }
            length = Int(bytes[offset]) << 8 | Int(bytes[offset + 1])
            offset += 2
        } else if length == 127 {
            guard bytes.count >= offset + 8 else { return nil }
            var value: UInt64 = 0
            for i in 0 ..< 8 { value = (value << 8) | UInt64(bytes[offset + i]) }
            length = Int(value)
            offset += 8
        }
        let maskOffset = offset
        if masked {
            guard bytes.count >= offset + 4 else { return nil }
            offset += 4
        }
        guard bytes.count >= offset + length else { return nil }
        var payload = Array(bytes[offset ..< offset + length])
        if masked {
            let mask = Array(bytes[maskOffset ..< maskOffset + 4])
            for i in payload.indices { payload[i] ^= mask[i % 4] }
        }
        return (opcode, Data(payload), offset + length)
    }

    private static func encodeWSFrame(opcode: UInt8, payload: Data) -> Data {
        var out = Data([0x80 | (opcode & 0x0f)])
        if payload.count < 126 {
            out.append(UInt8(payload.count))
        } else if payload.count <= Int(UInt16.max) {
            out.append(126)
            out.append(UInt8((payload.count >> 8) & 0xff))
            out.append(UInt8(payload.count & 0xff))
        } else {
            out.append(127)
            let count = UInt64(payload.count)
            for shift in stride(from: 56, through: 0, by: -8) {
                out.append(UInt8((count >> shift) & 0xff))
            }
        }
        out.append(payload)
        return out
    }
}
