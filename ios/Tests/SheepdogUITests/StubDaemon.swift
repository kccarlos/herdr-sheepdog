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
        case computer(name: String, agents: String, questions: String)
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

    /// How many requests started with `prefix` ("POST /api/utterance").
    func requests(_ prefix: String) -> Int {
        lock.withLock { seen.filter { $0.hasPrefix(prefix) }.count }
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

    func stop() { listener.cancel() }

    private func serve(_ connection: NWConnection) {
        connection.start(queue: queue)
        connection.receive(minimumIncompleteLength: 1, maximumLength: 64 * 1024) { [weak self] data, _, _, _ in
            guard let self else { return }
            let request = data.flatMap { String(data: $0, encoding: .utf8) } ?? ""
            let words = request.split(separator: " ")
            let method = words.first.map(String.init) ?? "GET"
            let path = words.dropFirst().first.map(String.init) ?? "/"
            self.lock.withLock { self.seen.append("\(method) \(path)") }
            let response = self.response(for: path)
            let delay = method == "POST" ? self.postDelay : 0
            self.queue.asyncAfter(deadline: .now() + delay) {
                connection.send(content: response, completion: .contentProcessed { _ in connection.cancel() })
            }
        }
    }

    private func response(for path: String) -> Data {
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
        case let .computer(name, agents, questions):
            if path.hasPrefix("/api/hello") {
                return http(200, "application/json", #"{"name":"sheepdog","version":"v0.1.4","auth":"token","ws":"/ws","audio":{"in_hz":16000,"out_hz":24000,"format":"pcm16le mono"},"computer":{"id":"stub","name":""# + name + #""}}"#)
            }
            if path.hasPrefix("/api/state") {
                let voice = #""voice":{"mode":"off","status":"off","muted":false,"stalled":false,"client_connected":false}"#
                return http(200, "application/json", #"{"agents":"# + agents + #","questions":"# + questions + "," + voice + #","version":"v0.1.4"}"#)
            }
            if path.hasPrefix("/api/pending/answer") {
                return http(200, "application/json", #"{"ok":true,"spoken":"Answered."}"#)
            }
            if path.hasPrefix("/api/utterance") {
                return http(200, "application/json", #"{"ok":true,"via":"voice"}"#)
            }
            if path.hasPrefix("/api/conversation") {
                // One line that says whose conversation this is.
                return http(200, "application/json", #"{"items":[{"id":1790127618456893,"ts":"2026-10-23T09:00:00Z","kind":"dog","text":"Hi from "# + name + #"."}]}"#)
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

    private func http(_ status: Int, _ type: String, _ body: String) -> Data {
        let reason = [200: "OK", 401: "Unauthorized", 404: "Not Found"][status] ?? "Error"
        let bytes = Data(body.utf8)
        let head = "HTTP/1.1 \(status) \(reason)\r\nContent-Type: \(type)\r\nContent-Length: \(bytes.count)\r\nConnection: close\r\n\r\n"
        return Data(head.utf8) + bytes
    }
}
