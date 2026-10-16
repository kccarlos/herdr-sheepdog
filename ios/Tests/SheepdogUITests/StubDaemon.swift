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
    }

    private let listener: NWListener
    private let mode: Mode
    private let queue = DispatchQueue(label: "stub-daemon")
    private(set) var port: UInt16 = 0

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
            let path = request.split(separator: " ").dropFirst().first.map(String.init) ?? "/"
            let response = self.response(for: path)
            connection.send(content: response, completion: .contentProcessed { _ in connection.cancel() })
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
        }
    }

    private func http(_ status: Int, _ type: String, _ body: String) -> Data {
        let reason = status == 200 ? "OK" : "Unauthorized"
        let bytes = Data(body.utf8)
        let head = "HTTP/1.1 \(status) \(reason)\r\nContent-Type: \(type)\r\nContent-Length: \(bytes.count)\r\nConnection: close\r\n\r\n"
        return Data(head.utf8) + bytes
    }
}
