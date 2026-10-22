import Foundation

/// The offline demo's daemon: `DemoWorld` behind the same HTTP routes and
/// shapes the real daemon serves, inside the app. `DaemonClient` reaches it
/// through `DemoURLProtocol` on a `URLSession` that never opens a socket,
/// so the app's own code paths (state, conversation, `/events`, approvals,
/// the agent window, typed questions) run unchanged and nothing goes on
/// the network.
///
/// There is no `/ws` here: in the demo the app speaks the voice's lines
/// itself, on the device (`onLines`).
public final class DemoDaemon: @unchecked Sendable {
    /// The made-up address the demo's client points at. `.invalid` can
    /// never resolve, so a request that escaped the protocol would fail
    /// instead of reaching anything.
    public static let host = "demo.sheepdog.invalid"
    public static let endpoint = DaemonEndpoint(host: host, port: 7777)

    /// The daemon `DemoURLProtocol` serves. Set by `start()`.
    public internal(set) static var current: DemoDaemon?

    /// New lines for the voice, on the main queue.
    public var onLines: (([DemoWorld.Line]) -> Void)?
    /// New cards for the screen, on the main queue (the demo's `/ws`
    /// `card` messages).
    public var onCards: (([ScreenCard]) -> Void)?

    private let lock = NSLock()
    private var world: DemoWorld
    private var spaces = DemoSpaces()
    private let clock: () -> Date
    private var timer: DispatchSourceTimer?
    private let queue = DispatchQueue(label: "sheepdog.demo")
    private var streams: [ObjectIdentifier: (since: UInt64, send: (Data) -> Void)] = [:]

    public init(clock: @escaping () -> Date = Date.init) {
        self.clock = clock
        world = DemoWorld(now: clock())
    }

    /// A `URLSession` whose every request is answered by this daemon.
    public static func session() -> URLSession {
        let config = URLSessionConfiguration.ephemeral
        config.protocolClasses = [DemoURLProtocol.self]
        config.timeoutIntervalForRequest = 3600
        return URLSession(configuration: config)
    }

    /// Makes this the served daemon and starts the script's clock.
    public func start() {
        Self.current = self
        let timer = DispatchSource.makeTimerSource(queue: queue)
        timer.schedule(deadline: .now() + 0.5, repeating: 0.5)
        timer.setEventHandler { [weak self] in self?.tick() }
        timer.resume()
        self.timer = timer
        // The greeting.
        queue.async { [weak self] in self?.flushLines() }
    }

    public func stop() {
        timer?.cancel()
        timer = nil
        if Self.current === self { Self.current = nil }
        lock.lock()
        streams.removeAll()
        lock.unlock()
    }

    /// Runs whatever the script has due and tells the streams and the voice.
    func tick() {
        lock.lock()
        world.advance(to: clock())
        lock.unlock()
        publish()
    }

    private func publish() {
        lock.lock()
        let events = world.events
        var sends: [(Data) -> Void] = []
        var payloads: [Data] = []
        for (key, stream) in streams {
            let fresh = events.filter { $0.seq > stream.since }
            guard let last = fresh.last else { continue }
            streams[key] = (last.seq, stream.send)
            sends.append(stream.send)
            payloads.append(Data(fresh.map(Self.sseFrame).joined().utf8))
        }
        lock.unlock()
        for (send, data) in zip(sends, payloads) { send(data) }
        flushLines()
    }

    private func flushLines() {
        lock.lock()
        let lines = world.takeLines()
        lock.unlock()
        guard !lines.isEmpty else { return }
        DispatchQueue.main.async { [weak self] in self?.onLines?(lines) }
    }

    /// An agent's scripted screen, for the demo's live window
    /// (`DemoTermTransport`).
    public func agentScreen(paneID: String) -> AgentScreen? {
        lock.lock()
        defer { lock.unlock() }
        world.advance(to: clock())
        return world.screen(paneID: paneID)
    }

    // MARK: - Routes

    struct Response {
        var status: Int
        var body: Data
        var contentType = "application/json"
    }

    func respond(to request: URLRequest) -> Response {
        let url = request.url
        let path = url.map { URLComponents(url: $0, resolvingAgainstBaseURL: false)?.percentEncodedPath ?? $0.path } ?? ""
        let method = request.httpMethod ?? "GET"
        let body = request.httpBody ?? request.httpBodyStream.map(Self.read) ?? Data()
        let object = (try? JSONSerialization.jsonObject(with: body)) as? [String: Any] ?? [:]

        lock.lock()
        defer { lock.unlock() }
        world.advance(to: clock())

        switch (method, path) {
        case ("GET", "/api/hello"):
            return json([
                "name": "sheepdog", "version": "demo", "voice_name": "Sheepdog", "auth": "none", "ws": "/ws",
                "audio": ["in_hz": 16000, "out_hz": 24000, "format": "pcm16le mono"],
            ])
        case ("GET", "/api/state"):
            return encoded(world.state)
        case ("GET", "/api/conversation"):
            return encoded(ConversationPage(items: world.history.items, nextBefore: nil))
        case ("GET", "/api/events"):
            let list = "[" + world.events.map(Self.eventJSON).joined(separator: ",") + "]"
            return Response(status: 200, body: Data(list.utf8))
        case ("GET", "/api/settings"), ("PUT", "/api/settings"):
            return json(["chattiness": "chatty", "levels": ["quiet", "normal", "chatty"]])
        case ("POST", "/api/pending/answer"):
            let id = object["id"] as? String ?? ""
            let option = object["option"] as? Int ?? 0
            do {
                let spoken = try world.answer(pendingID: id, option: option)
                DispatchQueue.global().async { [weak self] in self?.publish() }
                DispatchQueue.main.async { [weak self] in self?.onLines?([DemoWorld.Line(text: spoken, urgency: "answer")]) }
                return json(["spoken": spoken])
            } catch {
                return json(["error": "that question is no longer open"], status: 409)
            }
        case ("POST", "/api/utterance"):
            let text = (object["text"] as? String ?? "").trimmingCharacters(in: .whitespacesAndNewlines)
            guard !text.isEmpty else { return json(["error": "empty text"], status: 400) }
            let reply = world.ask(text)
            let cards = world.takeCards()
            if !cards.isEmpty {
                // Before the reply reaches the app, as the daemon's `/ws`
                // card comes before its history item.
                DispatchQueue.main.async { [weak self] in self?.onCards?(cards) }
            }
            DispatchQueue.global().async { [weak self] in self?.publish() }
            DispatchQueue.main.async { [weak self] in self?.onLines?([DemoWorld.Line(text: reply, urgency: "answer")]) }
            return json(["via": "typed", "tool": "demo", "spoken": reply])
        case ("GET", "/api/spaces"):
            return json(spaces.view(agents: world.agents))
        case ("POST", "/api/agents"):
            // The failure states, on demand: a worktree in the folder that
            // is not a repository, the branch "main", or a first prompt
            // with "fail" in it (a start that timed out, its pane open).
            let worktree = (object["worktree"] as? [String: Any])?["branch"] as? String
            if worktree != nil, (object["dir"] as? String)?.hasSuffix("/notes") == true {
                return json(["error": "notes is not in a git repository", "reason": "not_git"], status: 400)
            }
            if worktree == "main" {
                return json(["error": "the branch main is already checked out", "reason": "branch_exists"], status: 400)
            }
            if (object["text"] as? String)?.lowercased().contains("fail") == true {
                return json(["error": "herdr never saw the agent running", "reason": "timeout", "pane_id": DemoWorld.authPane], status: 502)
            }
            return json(["ok": true, "pane_id": "", "spoken": "This is the demo: its agents are scripted, so no agent was started."])
        default:
            let busy = world.agents.contains { $0.state == .working || $0.state == .blocked }
            if let reply = spaces.handle(method: method, path: path, body: object, busy: busy) {
                return json(reply.body, status: reply.status)
            }
            if path.hasPrefix("/api/shells/"), path.hasSuffix("/close"), method == "POST" {
                return json(["ok": true])
            }
            if path.hasPrefix("/api/agents/"), method == "POST",
               let last = path.split(separator: "/").last, ["rename", "interrupt", "close"].contains(String(last)) {
                return json(["ok": true, "spoken": "This is the demo: its agents are scripted, so nothing changed."])
            }
            if path.hasPrefix("/api/agents/") {
                let parts = path.dropFirst("/api/agents/".count).split(separator: "/")
                let pane = parts.first.flatMap { String($0).removingPercentEncoding } ?? ""
                if parts.last == "screen", let screen = world.screen(paneID: pane) {
                    return encoded(screen)
                }
                if parts.last == "input", method == "POST" {
                    return json(["ok": true, "kind": "prompt", "spoken": "This is the demo: its agents are scripted, so nothing was sent."])
                }
                if world.screen(paneID: pane) != nil, parts.count >= 2, parts[1] == "changes" || parts[1] == "attachments" {
                    return demoPhase4(parts: parts.map(String.init), method: method, body: body)
                }
                return json(["error": "no such agent"], status: 404)
            }
            return json(["error": "not in the demo"], status: 404)
        }
    }

    /// `/events`: what happened after `since`, then held open; later
    /// events are pushed as the script runs.
    func openStream(since: UInt64, id: ObjectIdentifier, send: @escaping (Data) -> Void) -> Data {
        lock.lock()
        world.advance(to: clock())
        let backlog = world.events.filter { $0.seq > since }
        streams[id] = (backlog.last?.seq ?? max(since, world.events.last?.seq ?? 0), send)
        lock.unlock()
        return Data((": demo\n\n" + backlog.map(Self.sseFrame).joined()).utf8)
    }

    func closeStream(id: ObjectIdentifier) {
        lock.lock()
        streams[id] = nil
        lock.unlock()
    }

    // MARK: - Encoding

    func json(_ object: [String: Any], status: Int = 200) -> Response {
        Response(status: status, body: (try? JSONSerialization.data(withJSONObject: object)) ?? Data())
    }

    func encoded<T: Encodable>(_ value: T) -> Response {
        Response(status: 200, body: (try? Self.encoder.encode(value)) ?? Data())
    }

    static let encoder: JSONEncoder = {
        let e = JSONEncoder()
        e.dateEncodingStrategy = .custom { date, encoder in
            var c = encoder.singleValueContainer()
            try c.encode(ISO8601DateFormatter.sheepdog.string(from: date))
        }
        return e
    }()

    static func eventJSON(_ event: DaemonEvent) -> String {
        var object: [String: JSONValue] = [
            "id": .number(Double(event.seq)),
            "source": .string(event.source),
            "type": .string(event.type),
            "payload": .object(event.payload),
        ]
        if let ts = event.ts { object["ts"] = .string(ISO8601DateFormatter.sheepdog.string(from: ts)) }
        if let pane = event.paneID { object["pane_id"] = .string(pane) }
        let data = (try? encoder.encode(object)) ?? Data("{}".utf8)
        return String(decoding: data, as: UTF8.self)
    }

    static func sseFrame(_ event: DaemonEvent) -> String {
        "id: \(event.seq)\nevent: log\ndata: \(eventJSON(event))\n\n"
    }

    private static func read(_ stream: InputStream) -> Data {
        stream.open()
        defer { stream.close() }
        var data = Data()
        var buffer = [UInt8](repeating: 0, count: 4096)
        while stream.hasBytesAvailable {
            let n = stream.read(&buffer, maxLength: buffer.count)
            guard n > 0 else { break }
            data.append(buffer, count: n)
        }
        return data
    }
}

/// Answers every request of `DemoDaemon.session()` from `DemoDaemon.current`.
public final class DemoURLProtocol: URLProtocol {
    override public class func canInit(with request: URLRequest) -> Bool {
        request.url?.host == DemoDaemon.host
    }

    override public class func canonicalRequest(for request: URLRequest) -> URLRequest { request }

    override public func startLoading() {
        guard let url = request.url, let daemon = DemoDaemon.current else {
            client?.urlProtocol(self, didFailWithError: URLError(.cannotConnectToHost))
            return
        }
        if url.path == "/events" {
            let since = request.value(forHTTPHeaderField: "Last-Event-ID").flatMap(UInt64.init)
                ?? URLComponents(url: url, resolvingAgainstBaseURL: false)?.queryItems?.first { $0.name == "since" }?.value.flatMap(UInt64.init)
                ?? 0
            let response = HTTPURLResponse(url: url, statusCode: 200, httpVersion: "HTTP/1.1", headerFields: ["Content-Type": "text/event-stream"])!
            client?.urlProtocol(self, didReceive: response, cacheStoragePolicy: .notAllowed)
            let first = daemon.openStream(since: since, id: ObjectIdentifier(self)) { [weak self] data in
                guard let self else { return }
                self.client?.urlProtocol(self, didLoad: data)
            }
            client?.urlProtocol(self, didLoad: first)
            return
        }
        let answer = daemon.respond(to: request)
        let response = HTTPURLResponse(url: url, statusCode: answer.status, httpVersion: "HTTP/1.1", headerFields: ["Content-Type": answer.contentType])!
        client?.urlProtocol(self, didReceive: response, cacheStoragePolicy: .notAllowed)
        client?.urlProtocol(self, didLoad: answer.body)
        client?.urlProtocolDidFinishLoading(self)
    }

    override public func stopLoading() {
        DemoDaemon.current?.closeStream(id: ObjectIdentifier(self))
    }
}
