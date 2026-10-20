import XCTest
@testable import SheepdogKit

/// The agent window over `/ws` (the daemon's docs/ws-contract.md "Agent
/// window over /ws"): the messages as the Go side sends them, and when the
/// window streams or falls back to polling.
final class AgentScreenStreamTests: XCTestCase {
    private let ready = ProtocolHandshake.State.ready(protocol: 1, daemonVersion: "0.5.0", features: ["playback", "history", "screen"])
    private let oldDaemon = ProtocolHandshake.State.ready(protocol: 1, daemonVersion: "0.4.2", features: ["playback", "history"])

    private func frame(_ pane: String = "w1:p2", text: String = "$ make test\nok", hash: String = "9f2c") -> WSMessage {
        .screen(AgentScreen(paneID: pane, alias: "auth", state: .working, text: text, lines: 200, truncated: false, hash: hash))
    }

    // MARK: - Wire

    func testSubscribeMessagesEncodeAsTheContractSays() throws {
        XCTAssertEqual(
            try WSMessage.screenSubscribe(paneID: "w1:p2", lines: 200).encodedString(),
            #"{"lines":200,"pane_id":"w1:p2","type":"screen-subscribe"}"#
        )
        XCTAssertEqual(try WSMessage.screenSubscribe(paneID: "w1:p2").encodedString(), #"{"pane_id":"w1:p2","type":"screen-subscribe"}"#)
        XCTAssertEqual(try WSMessage.screenUnsubscribe.encodedString(), #"{"type":"screen-unsubscribe"}"#)
        XCTAssertThrowsError(try WSMessage.screenSubscribe(paneID: "").encode(), "pane_id is required")
        XCTAssertThrowsError(try WSMessage.screenSubscribe(paneID: "w1:p2", lines: 0).encode(), "lines must be positive")
    }

    func testScreenFrameDecodesAsTheDaemonSendsIt() throws {
        let json = #"{"type":"screen","screen":{"pane_id":"w1:p2","alias":"auth","spoken":"Auth fix","kind":"codex","state":"working","state_since":"2026-10-05T09:12:03Z","text":"› run the tests\n• Running go test ./...","lines":200,"truncated":true,"hash":"9f2c0e1a44b7d310"}}"#
        let m = try WSMessage.decode(Data(json.utf8))
        XCTAssertEqual(m.type, .screen)
        let screen = try XCTUnwrap(m.screen)
        XCTAssertEqual(screen.paneID, "w1:p2")
        XCTAssertEqual(screen.displayName, "Auth fix")
        XCTAssertEqual(screen.state, .working)
        XCTAssertEqual(screen.stateSince, ISO8601DateFormatter().date(from: "2026-10-05T09:12:03Z"))
        XCTAssertEqual(screen.text, "› run the tests\n• Running go test ./...")
        XCTAssertEqual(screen.truncated, true)
        XCTAssertEqual(screen.hash, "9f2c0e1a44b7d310")
        XCTAssertNil(screen.unchanged)
        XCTAssertThrowsError(try WSMessage.decode(Data(#"{"type":"screen"}"#.utf8)), "screen is required")
    }

    func testScreenErrorDecodes() throws {
        let gone = try WSMessage.decode(Data(#"{"type":"screen-error","pane_id":"w1:p9","message":"no such agent","ended":true}"#.utf8))
        XCTAssertEqual(gone.type, .screenError)
        XCTAssertEqual(gone.paneID, "w1:p9")
        XCTAssertEqual(gone.message, "no such agent")
        XCTAssertEqual(gone.ended, true)
        let failing = try WSMessage.decode(Data(#"{"type":"screen-error","pane_id":"w1:p2","message":"herdr: pane.read: read: EOF"}"#.utf8))
        XCTAssertNil(failing.ended)
    }

    func testBotReadyWithTheScreenFeature() throws {
        let m = try WSMessage.decode(Data(#"{"type":"bot-ready","protocol":1,"min_protocol":1,"daemon":{"version":"0.4.2"},"features":["playback","speaking","silence","provider","history","push","models","screen"]}"#.utf8))
        var handshake = ProtocolHandshake()
        handshake.start()
        _ = handshake.receive(m)
        XCTAssertTrue(handshake.state.features.contains(AgentScreenStream.feature))
    }

    // MARK: - Stream or poll

    func testStreamsOnlyAfterTheFirstFrame() {
        var stream = AgentScreenStream()
        XCTAssertNil(stream.open(paneID: "w1:p2"), "no connection yet: poll")
        XCTAssertFalse(stream.isStreaming("w1:p2"))
        XCTAssertEqual(stream.handshake(ready), .screenSubscribe(paneID: "w1:p2", lines: AgentScreenModel.lines))
        XCTAssertFalse(stream.isStreaming("w1:p2"), "subscribed, nothing back yet: keep polling")
        XCTAssertEqual(stream.receive(frame()), .frame(frame().screen!))
        XCTAssertTrue(stream.isStreaming("w1:p2"))
        XCTAssertFalse(stream.isStreaming("w1:p3"))
    }

    func testOpeningOnAReadyConnectionSubscribesAtOnce() {
        var stream = AgentScreenStream()
        XCTAssertNil(stream.handshake(ready))
        XCTAssertEqual(stream.open(paneID: "w1:p2", lines: 50), .screenSubscribe(paneID: "w1:p2", lines: 50))
        XCTAssertNil(stream.open(paneID: "w1:p2", lines: 50), "already subscribed")
    }

    func testAnOlderDaemonOrLegacyConnectionPolls() {
        var stream = AgentScreenStream()
        _ = stream.open(paneID: "w1:p2")
        XCTAssertNil(stream.handshake(.waiting))
        XCTAssertNil(stream.handshake(oldDaemon), "no screen feature: never subscribe")
        XCTAssertNil(stream.handshake(.legacy))
        XCTAssertFalse(stream.isStreaming("w1:p2"))
        XCTAssertNil(stream.receive(frame()), "a frame nobody asked for is ignored")
        XCTAssertFalse(stream.isStreaming("w1:p2"))
    }

    func testADroppedConnectionPollsThenResubscribes() {
        var stream = AgentScreenStream()
        _ = stream.handshake(ready)
        _ = stream.open(paneID: "w1:p2")
        _ = stream.receive(frame())
        XCTAssertTrue(stream.isStreaming("w1:p2"))
        stream.socketClosed()
        XCTAssertFalse(stream.isStreaming("w1:p2"), "no connection: poll")
        XCTAssertNil(stream.close(paneID: "w1:p3"), "another window's close changes nothing")
        XCTAssertNil(stream.handshake(.waiting))
        XCTAssertEqual(stream.handshake(ready), .screenSubscribe(paneID: "w1:p2", lines: AgentScreenModel.lines), "the new connection subscribes again")
        XCTAssertFalse(stream.isStreaming("w1:p2"))
        _ = stream.receive(frame())
        XCTAssertTrue(stream.isStreaming("w1:p2"))
    }

    func testClosingTheWindowUnsubscribes() {
        var stream = AgentScreenStream()
        _ = stream.handshake(ready)
        _ = stream.open(paneID: "w1:p2")
        XCTAssertNil(stream.close(paneID: "w1:p3"))
        XCTAssertEqual(stream.close(paneID: "w1:p2"), .screenUnsubscribe)
        XCTAssertNil(stream.close(paneID: "w1:p2"), "once")
        XCTAssertNil(stream.receive(frame()), "a late frame after closing is dropped")
        stream.socketClosed()
        _ = stream.open(paneID: "w1:p2")
        XCTAssertNil(stream.close(paneID: "w1:p2"), "nothing subscribed without a connection")
    }

    func testOneSubscriptionAtATime() {
        var stream = AgentScreenStream()
        _ = stream.handshake(ready)
        _ = stream.open(paneID: "w1:p2")
        _ = stream.receive(frame())
        // A second window replaces the first (the daemon allows one).
        XCTAssertEqual(stream.open(paneID: "w1:p3"), .screenSubscribe(paneID: "w1:p3", lines: AgentScreenModel.lines))
        XCTAssertNil(stream.receive(frame("w1:p2")), "the old window's frames are dropped")
        XCTAssertFalse(stream.isStreaming("w1:p2"))
        // The first window's disappearance does not end the second's stream.
        XCTAssertNil(stream.close(paneID: "w1:p2"))
        XCTAssertNotNil(stream.receive(frame("w1:p3")))
        XCTAssertTrue(stream.isStreaming("w1:p3"))
    }

    func testAnEndedSubscriptionFallsBackToPolling() {
        var stream = AgentScreenStream()
        _ = stream.handshake(ready)
        _ = stream.open(paneID: "w1:p2")
        _ = stream.receive(frame())
        XCTAssertEqual(stream.receive(.screenError(paneID: "w1:p2", "herdr: pane.read: read: EOF")), .error("herdr: pane.read: read: EOF", ended: false))
        XCTAssertTrue(stream.isStreaming("w1:p2"), "a failed read keeps the subscription")
        XCTAssertNil(stream.receive(.screenError(paneID: "w1:p9", "no such agent", ended: true)), "another pane's error")
        XCTAssertEqual(stream.receive(.screenError(paneID: "w1:p2", "no such agent", ended: true)), .error("no such agent", ended: true))
        XCTAssertFalse(stream.isStreaming("w1:p2"))
        XCTAssertNil(stream.close(paneID: "w1:p2"), "the daemon already ended it")
    }

    // MARK: - The window model with pushed frames

    func testPushedFramesDriveTheWindow() {
        var model = AgentScreenModel(paneID: "w1:p2")
        XCTAssertTrue(model.apply(frame().screen!))
        XCTAssertEqual(model.text, "$ make test\nok")
        XCTAssertEqual(model.revision, 1)
        XCTAssertTrue(model.canOperate)
        model.fail(message: "herdr: pane.read: read: EOF")
        XCTAssertEqual(model.error, "herdr: pane.read: read: EOF")
        XCTAssertFalse(model.canOperate)
        // The daemon re-sends the window after a failure even if unchanged.
        XCTAssertTrue(model.apply(frame().screen!))
        XCTAssertNil(model.error)
        XCTAssertEqual(model.revision, 1, "same text: no scroll")
        XCTAssertTrue(model.apply(frame(text: "$ make test\nok\nPASS", hash: "a1b2").screen!))
        XCTAssertEqual(model.revision, 2)
    }

    // MARK: - Colour (screen-ansi)

    func testSubscribesInColourWhenTheDaemonCan() throws {
        let colour = ProtocolHandshake.State.ready(protocol: 1, daemonVersion: "0.9.0", features: ["screen", "screen-ansi"])
        var stream = AgentScreenStream()
        _ = stream.open(paneID: "w1:p2")
        let sub = stream.handshake(colour)
        XCTAssertEqual(sub, .screenSubscribe(paneID: "w1:p2", ansi: true))
        XCTAssertEqual(try sub?.encodedString(), #"{"ansi":true,"pane_id":"w1:p2","type":"screen-subscribe"}"#)
    }

    func testANSISnapshotKeepsColours() {
        let bytes = SnapshotScreen.bytes(text: "\u{1b}[1;32m•\u{1b}[0m ok\nplain", rows: 10, ansi: true)
        let text = String(decoding: bytes, as: UTF8.self)
        XCTAssertTrue(text.contains("\u{1b}[1;32m•\u{1b}[0m ok\u{1b}[0m\r\nplain\u{1b}[0m"), text)
        let plain = String(decoding: SnapshotScreen.bytes(text: "\u{1b}[1;32m•", rows: 10), as: UTF8.self)
        XCTAssertTrue(plain.hasSuffix("[1;32m•"), "plain text drops ESC as before")
    }

    func testANSIScreenDecodes() throws {
        let screen = try DaemonClient.decoder.decode(AgentScreen.self, from: Data(#"{"pane_id":"w1:p2","alias":"auth","state":"working","text":"x","hash":"7","ansi":true}"#.utf8))
        XCTAssertEqual(screen.ansi, true)
        var model = AgentScreenModel(paneID: "w1:p2")
        model.apply(screen)
        XCTAssertTrue(model.isANSI)
    }
}
