import XCTest
@testable import SheepdogKit

/// The protocol handshake against a real daemon, new or old. Skipped unless
/// `SHEEPDOG_LIVE_WS` names one (`127.0.0.1:7791`; pass it to xcodebuild as
/// `TEST_RUNNER_SHEEPDOG_LIVE_WS=…`; ios.yml points it at its fixture daemon). A
/// daemon whose `/api/hello` has `protocol` must answer `bot-ready`; one
/// without it (from before the handshake) must leave the app at protocol 1
/// with no error shown and the socket open.
@MainActor
final class LiveHandshakeTests: XCTestCase {
    func testHandshakeAgainstALiveDaemon() async throws {
        guard let address = ProcessInfo.processInfo.environment["SHEEPDOG_LIVE_WS"],
              let endpoint = DaemonEndpoint(text: address) else {
            throw XCTSkip("SHEEPDOG_LIVE_WS not set")
        }
        let hello = try await DaemonClient(endpoint: endpoint).hello()
        let socket = VoiceSocket(endpoint: endpoint)
        var errors: [String] = []
        socket.onEvent = { event in
            if case let .control(message) = event, message.type == .error { errors.append(message.message ?? "") }
        }
        socket.connect()
        defer { socket.disconnect() }
        let deadline = Date().addingTimeInterval(ProtocolHandshake.timeout + 3)
        while socket.handshake.state == .idle || socket.handshake.state == .waiting {
            if Date() > deadline { break }
            try await Task.sleep(nanoseconds: 50_000_000)
        }
        if let p = hello.protocol {
            guard case let .ready(negotiated, version, features) = socket.handshake.state else {
                return XCTFail("a protocol-\(p) daemon must answer bot-ready; state \(socket.handshake.state)")
            }
            XCTAssertEqual(negotiated, min(p, ProtocolHandshake.protocolVersion))
            XCTAssertEqual(version, hello.version)
            XCTAssertTrue(features.contains("playback"), "\(features)")
        } else {
            XCTAssertEqual(socket.handshake.state, .legacy, "a daemon from before the handshake is protocol 1")
        }
        XCTAssertEqual(errors, [], "nothing to show the user")
        XCTAssertEqual(socket.life, .open)
    }
}
