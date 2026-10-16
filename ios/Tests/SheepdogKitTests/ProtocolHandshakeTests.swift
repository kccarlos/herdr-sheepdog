import XCTest
@testable import SheepdogKit

/// The `/ws` protocol handshake's state machine (docs/ws-contract.md
/// "Protocol version and handshake" on the daemon side): every transition,
/// against a new daemon, an older one, and a mismatch either way.
final class ProtocolHandshakeTests: XCTestCase {
    private func waiting() -> ProtocolHandshake {
        var h = ProtocolHandshake()
        h.start()
        return h
    }

    func testTheConstantsMatchTheContract() {
        XCTAssertEqual(ProtocolHandshake.protocolVersion, 1)
        XCTAssertEqual(ProtocolHandshake.minDaemonProtocol, 1)
        XCTAssertEqual(ProtocolHandshake.timeout, 2)
        XCTAssertEqual(ProtocolHandshake().state, .idle)
    }

    func testANewDaemonStatusFirstThenBotReady() {
        var h = waiting()
        XCTAssertEqual(h.state, .waiting)
        // The daemon's status still comes first, before bot-ready.
        XCTAssertEqual(h.receive(.status(.connected)), .forward)
        XCTAssertEqual(h.state, .waiting)
        XCTAssertEqual(h.receive(.botReady(protocol: 1, daemonVersion: "0.4.2", features: ["playback", "push"])), .swallow)
        XCTAssertEqual(h.state, .ready(protocol: 1, daemonVersion: "0.4.2", features: ["playback", "push"]))
        XCTAssertEqual(h.state.negotiatedProtocol, 1)
        XCTAssertEqual(h.state.features, ["playback", "push"])
        XCTAssertEqual(h.state.summary, "1 (daemon 0.4.2)")
        // A late timer changes nothing.
        h.timedOut()
        XCTAssertEqual(h.state.negotiatedProtocol, 1)
        XCTAssertFalse(h.isMismatch)
    }

    func testANewerDaemonIsSpokenToAtOurProtocol() {
        var h = waiting()
        _ = h.receive(.botReady(protocol: ProtocolHandshake.protocolVersion + 2, daemonVersion: "9"))
        XCTAssertEqual(h.state.negotiatedProtocol, ProtocolHandshake.protocolVersion)
    }

    func testAnOlderDaemonsUnknownTypeErrorIsSwallowedAndMeansProtocolOne() {
        var h = waiting()
        XCTAssertEqual(h.receive(.error(#"wsproto: unknown type "client-ready""#)), .swallow, "not a problem to show")
        XCTAssertEqual(h.state, .legacy)
        XCTAssertEqual(h.state.negotiatedProtocol, 1)
        XCTAssertEqual(h.state.features, [])
        // Later errors are the daemon's to show.
        XCTAssertEqual(h.receive(.error("rate limited")), .forward)
    }

    func testAnOlderDaemonThatSaysNothingIsProtocolOneAfterTheTimeout() {
        var h = waiting()
        XCTAssertEqual(h.receive(.status(.connected)), .forward)
        XCTAssertEqual(h.receive(.transcript(.outgoing, "hi", final: false)), .forward)
        h.timedOut()
        XCTAssertEqual(h.state, .legacy)
        XCTAssertEqual(h.state.summary, "1 (older daemon, no handshake)")
        // A bot-ready after all still wins.
        _ = h.receive(.botReady(protocol: 1, daemonVersion: "late"))
        XCTAssertEqual(h.state, .ready(protocol: 1, daemonVersion: "late", features: []))
    }

    func testTheDaemonRefusingTheAppClosesWithItsOwnWords() {
        var h = waiting()
        let words = "This app speaks /ws protocol 1; the daemon (version 2.0) needs protocol 2 or newer. Update the app."
        XCTAssertEqual(h.receive(.error(words)), .forward)
        XCTAssertEqual(h.receive(.status(.closed, reason: "protocol_too_old")), .close)
        XCTAssertEqual(h.state, .appTooOld(words))
        XCTAssertTrue(h.isMismatch)
        XCTAssertEqual(h.state.mismatchMessage, words)
        XCTAssertEqual(h.state.summary, "Update the app")
        // The mismatch outlives the connection.
        h.reset()
        XCTAssertEqual(h.state, .appTooOld(words))
    }

    func testARefusalWithoutAnErrorStillSaysUpdateTheApp() {
        var h = waiting()
        XCTAssertEqual(h.receive(.status(.closed, reason: "protocol_too_old")), .close)
        XCTAssertTrue(h.state.mismatchMessage?.contains("Update the app") ?? false)
    }

    func testADaemonBelowOurMinimumCloses() {
        var h = waiting()
        XCTAssertEqual(h.receive(.botReady(protocol: 0, daemonVersion: "0.0.1")), .close)
        XCTAssertEqual(h.state, .daemonTooOld(protocol: 0))
        XCTAssertTrue(h.state.mismatchMessage?.contains("Update the daemon") ?? false)
    }

    func testOtherClosedStatusesAreNotTheHandshakes() {
        var h = waiting()
        XCTAssertEqual(h.receive(.status(.closed, reason: "replaced")), .forward)
        XCTAssertEqual(h.receive(.status(.closed, reason: "voice_off")), .forward)
        XCTAssertEqual(h.state, .waiting)
    }

    func testResetForgetsAnythingButAMismatch() {
        var h = waiting()
        _ = h.receive(.botReady(protocol: 1, daemonVersion: "x"))
        h.reset()
        XCTAssertEqual(h.state, .idle)
        h.start()
        XCTAssertEqual(h.state, .waiting)
    }
}
