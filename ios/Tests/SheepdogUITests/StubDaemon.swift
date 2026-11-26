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
    /// Settings JSON by project id (`""` for `GET /api/factory/settings`).
    var factorySettings: [String: String] = [:]
    var factorySettingsErrors: [String: (Int, String)] = [:]
    var factoryAsks: String = #"{"protocol":1,"asks":[]}"#
    var factoryAsksError: (Int, String)?
    /// Optional custom handlers for factory and answer POST routes.
    var sayHandler: ((String, String) -> (Int, String))?
    var stopHandler: ((String) -> (Int, String))?
    var startHandler: ((String) -> (Int, String))?
    var proposeSettingHandler: ((String, String) -> (Int, String))?
    var answerHandler: ((String) -> (Int, String))?
    var askAnswerHandler: ((String, String) -> (Int, String))?
    /// Optional custom handler for `POST /api/agents/<pane>/input`.
    var agentInputHandler: ((String, String) -> (Int, String))?
    /// Enable SSE `/events` streaming so tests can push `factory.changed`.
    var enableEvents = false
    private var eventClients: [NWConnection] = []
    private var nextEventID: UInt64 = 1_795_550_000_000_001
    private var says: [(project: String, body: String)] = []
    private var stops: [String] = []
    private var starts: [String] = []
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
            let c = eventClients; eventClients = []; return c
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
            self.answer(connection, String(decoding: received, as: UTF8.self))
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

    private func answer(_ connection: NWConnection, _ request: String) {
        let words = request.split(separator: " ")
        let method = words.first.map(String.init) ?? "GET"
        let path = words.dropFirst().first.map(String.init) ?? "/"
        let body = request.range(of: "\r\n\r\n").map { String(request[$0.upperBound...]) } ?? ""
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
        let reply = response(for: path, method: method, body: body)
        let delay = method == "POST" ? postDelay : 0
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
                return http(200, "application/json", #"{"name":"sheepdog","version":"v0.1.4","auth":"token","ws":"/ws","audio":{"in_hz":16000,"out_hz":24000,"format":"pcm16le mono"},"computer":{"id":"stub","name":""# + name + #""}}"#)
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
                return http(200, "application/json", #"{"reply":"# + Self.json(text) + #","turn_id":"stub-turn-"# + String(turn) + #","acted":false}"#)
            }
            if path.hasPrefix("/api/conversation") {
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
        let clean = String(path.prefix { $0 != "?" })
        if method == "GET", clean == "/api/factory/projects" {
            return http(200, "application/json", #"{"protocol":1,"projects":"# + (factoryState ?? fallback ?? "[]") + "}")
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
        let reason = [200: "OK", 400: "Bad Request", 401: "Unauthorized", 403: "Forbidden", 404: "Not Found", 409: "Conflict", 503: "Service Unavailable"][status] ?? "Error"
        let bytes = Data(body.utf8)
        let head = "HTTP/1.1 \(status) \(reason)\r\nContent-Type: \(type)\r\nContent-Length: \(bytes.count)\r\nConnection: close\r\n\r\n"
        return Data(head.utf8) + bytes
    }
}
