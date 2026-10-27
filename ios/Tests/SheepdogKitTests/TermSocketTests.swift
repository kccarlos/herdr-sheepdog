import XCTest
@testable import SheepdogKit

/// The `/term` client (docs/dev/design/phone-terminal.md section 4) against
/// a fake transport: no daemon, no network.
@MainActor
final class TermSocketTests: XCTestCase {
    // MARK: - Messages

    func testClientMessagesEncodeAsTheContractSays() throws {
        let client = WSMessage.ClientInfo(name: "sheepdog-ios", version: "test", platform: "ios")
        XCTAssertEqual(try json(.hello(client: client)),
                       #"{"client":{"name":"sheepdog-ios","platform":"ios","version":"test"},"protocol":1,"type":"term-hello"}"#)
        XCTAssertEqual(try json(.open(ch: 3, target: .pane("w1:p2"), mode: .control, cols: 58, rows: 30)),
                       #"{"ch":3,"cols":58,"mode":"control","rows":30,"target":{"pane_id":"w1:p2"},"type":"term-open"}"#)
        XCTAssertEqual(try json(.open(ch: 1, target: .pane("w1:p2"), mode: .observe, cols: 40, rows: 20, takeover: true)),
                       #"{"ch":1,"cols":40,"mode":"observe","rows":20,"takeover":true,"target":{"pane_id":"w1:p2"},"type":"term-open"}"#)
        XCTAssertEqual(try json(.open(ch: 2, target: .newShell(cwd: "/src/app"), mode: .control, cols: 58, rows: 30)),
                       #"{"ch":2,"cols":58,"mode":"control","rows":30,"target":{"shell":{"cwd":"/src/app"}},"type":"term-open"}"#)
        XCTAssertEqual(try json(.resize(ch: 3, cols: 60, rows: 25)), #"{"ch":3,"cols":60,"rows":25,"type":"term-resize"}"#)
        XCTAssertEqual(try json(.scroll(ch: 3, .up, lines: 5)), #"{"ch":3,"dir":"up","lines":5,"type":"term-scroll"}"#)
        XCTAssertEqual(try json(.scroll(ch: 3, .down, lines: 3, col: 10, row: 4)),
                       #"{"ch":3,"col":10,"dir":"down","lines":3,"row":4,"type":"term-scroll"}"#)
        XCTAssertEqual(try json(.mouse(ch: 3, .down, button: "left", col: 2, row: 7)),
                       #"{"action":"down","button":"left","ch":3,"col":2,"row":7,"type":"term-mouse"}"#)
        XCTAssertEqual(try json(.close(ch: 3)), #"{"ch":3,"type":"term-close"}"#)
    }

    func testDaemonMessagesDecode() throws {
        let ready = try TermMessage.decode(Data(#"{"type":"term-ready","protocol":1,"limits":{"channels":4},"shell":true}"#.utf8))
        XCTAssertEqual(ready.type, .ready)
        XCTAssertEqual(ready.limits?.channels, 4)
        XCTAssertEqual(ready.shell, true)
        let opened = try TermMessage.decode(Data(#"{"type":"term-opened","ch":3,"pane_id":"w1:p2","mode":"observe","cols":58,"rows":30,"kind":"agent"}"#.utf8))
        XCTAssertEqual(opened.paneID, "w1:p2")
        XCTAssertEqual(opened.mode, .observe)
        XCTAssertEqual(opened.kind, "agent")
        let busy = try TermMessage.decode(Data(#"{"type":"term-closed","ch":3,"reason":"refused:busy","can_take_over":true}"#.utf8))
        XCTAssertEqual(TermCloseReason(busy.reason!), .refused("busy"))
        XCTAssertEqual(busy.canTakeOver, true)
        let error = try TermMessage.decode(Data(#"{"type":"term-error","ch":3,"message":"retrying"}"#.utf8))
        XCTAssertEqual(error.message, "retrying")
    }

    func testCloseReasons() {
        XCTAssertEqual(TermCloseReason("released"), .released)
        XCTAssertEqual(TermCloseReason("taken_over"), .takenOver)
        XCTAssertEqual(TermCloseReason("gone"), .gone)
        XCTAssertEqual(TermCloseReason("herdr"), .herdr)
        XCTAssertEqual(TermCloseReason("refused:too_many"), .refused("too_many"))
        XCTAssertEqual(TermCloseReason("refused:shell_off"), .refused("shell_off"))
        XCTAssertEqual(TermCloseReason("refused:not_tracked"), .refused("not_tracked"))
        XCTAssertEqual(TermCloseReason("something_new"), .other("something_new"))
    }

    func testOutputFramesAreBigEndian() {
        let wire = Data([0x01, 0x01, 0x02, 0x00, 0x00, 0x01, 0x05, 0x00, 0x3a, 0x00, 0x1e, 0x01, 0x41, 0x42])
        let frame = TermFrame.decode(wire)
        XCTAssertEqual(frame, TermFrame(ch: 258, seq: 261, cols: 58, rows: 30, full: true, bytes: [0x41, 0x42]))
        XCTAssertEqual(frame?.encode(), wire)
        XCTAssertNil(TermFrame.decode(Data([0x01, 0x00, 0x01])), "too short")
        XCTAssertNil(TermFrame.decode(Data([0x02] + [UInt8](repeating: 0, count: 12))), "an input frame is not output")
        XCTAssertEqual(TermFrame.decode(Data([0x01, 0, 1, 0xff, 0xff, 0xff, 0xff, 0, 1, 0, 1, 0]))?.seq, UInt32.max)
    }

    func testInputFramesAreTaggedAndSplitAt64KB() {
        XCTAssertEqual(TermInput.frames(ch: 3, bytes: Array("ls\r".utf8)), [Data([0x02, 0x00, 0x03, 0x6c, 0x73, 0x0d])])
        XCTAssertEqual(TermInput.frames(ch: 1, bytes: []), [])
        let paste = [UInt8](repeating: 0x61, count: 150_000)
        let frames = TermInput.frames(ch: 0x0102, bytes: paste)
        XCTAssertEqual(frames.count, 3)
        XCTAssertTrue(frames.allSatisfy { $0.count <= TermInput.maxFrameSize })
        XCTAssertEqual(frames.compactMap(TermInput.decode).flatMap(\.bytes), paste, "in order, nothing lost")
        XCTAssertTrue(frames.compactMap(TermInput.decode).allSatisfy { $0.ch == 0x0102 })
    }

    func testTheURLIsTermOnTheSameAddress() {
        XCTAssertEqual(TermSocket.url(for: DaemonEndpoint(host: "127.0.0.1", port: 7777))?.absoluteString, "ws://127.0.0.1:7777/term")
        XCTAssertEqual(TermSocket.url(for: DaemonEndpoint(host: "fd7a:115c:a1e0::1", port: 7777, secure: true))?.absoluteString, "wss://[fd7a:115c:a1e0::1]:7777/term")
        XCTAssertNil(TermSocket.url(for: DaemonEndpoint(host: "192.168.1.20", port: 7777)), "no plain ws off this device")
        var secure = DaemonEndpoint(host: "mac.example.ts.net", port: 443)
        secure.secure = true
        XCTAssertEqual(TermSocket.url(for: secure)?.absoluteString, "wss://mac.example.ts.net:443/term")
    }

    // MARK: - Connection

    func testHelloFirstWithTheTicketOnTheUpgrade() throws {
        let rig = Rig(token: "s3cret")
        rig.socket.connect()
        let t = try XCTUnwrap(rig.last)
        XCTAssertEqual(t.request.url?.path, "/term")
        XCTAssertEqual(t.request.value(forHTTPHeaderField: "Authorization"), "Bearer sds_s3cret")
        XCTAssertNil(t.request.url?.query, "the token is never in the URL")
        XCTAssertEqual(try t.messages().first?.type, .hello)
        XCTAssertEqual(rig.socket.state, .connecting)
    }

    func testOpenWaitsForReadyThenGoesOut() throws {
        let rig = Rig()
        rig.socket.connect()
        let ch = try XCTUnwrap(rig.socket.open(target: .pane("w1:p2"), mode: .control, cols: 58, rows: 30))
        XCTAssertEqual(try rig.last!.messages().map(\.type), [.hello], "nothing before term-ready")
        rig.ready(channels: 4, shell: false)
        XCTAssertEqual(rig.socket.state, .ready(.init(protocolVersion: 1, channels: 4, shell: false)))
        XCTAssertEqual(try rig.last!.messages().last, .open(ch: ch, target: .pane("w1:p2"), mode: .control, cols: 58, rows: 30))
        rig.last!.text(#"{"type":"term-opened","ch":\#(ch),"pane_id":"w1:p2","mode":"control","cols":58,"rows":30,"kind":"agent"}"#)
        XCTAssertEqual(rig.opened.first?.1, TermSocket.Opened(paneID: "w1:p2", mode: .control, cols: 58, rows: 30, kind: "agent"))
    }

    func testFramesInOrderAreDelivered() throws {
        let (rig, ch) = try openRig()
        rig.frame(ch, seq: 1, full: true)
        rig.frame(ch, seq: 2, full: false)
        rig.frame(ch, seq: 3, full: false)
        XCTAssertEqual(rig.frames.map(\.seq), [1, 2, 3])
        XCTAssertEqual(rig.resyncs, [])
    }

    func testAGapAsksForARepaintAndDropsDiffsUntilAFullFrame() throws {
        let (rig, ch) = try openRig()
        rig.frame(ch, seq: 1, full: true)
        rig.frame(ch, seq: 3, full: false)  // 2 went missing
        rig.frame(ch, seq: 4, full: false)
        XCTAssertEqual(rig.frames.map(\.seq), [1], "diffs on a stale screen are dropped")
        XCTAssertEqual(rig.resyncs, [ch], "one repaint request, not one per diff")
        XCTAssertEqual(try rig.last!.messages().last, .resize(ch: ch, cols: 58, rows: 30), "a resize to the current size")
        rig.frame(ch, seq: 5, full: true)
        rig.frame(ch, seq: 6, full: false)
        XCTAssertEqual(rig.frames.map(\.seq), [1, 5, 6])
    }

    func testADiffBeforeAnyFullFrameAsksForARepaint() throws {
        let (rig, ch) = try openRig()
        rig.frame(ch, seq: 7, full: false)
        XCTAssertEqual(rig.frames, [])
        XCTAssertEqual(rig.resyncs, [ch])
    }

    func testAnUnansweredRepaintIsAskedAgainAfterASecond() throws {
        var clock = Date(timeIntervalSinceReferenceDate: 0)
        let (rig, ch) = try openRig(now: { clock })
        rig.frame(ch, seq: 1, full: true)
        rig.frame(ch, seq: 3, full: false)
        clock += 0.5
        rig.frame(ch, seq: 4, full: false)
        XCTAssertEqual(rig.resyncs.count, 1)
        clock += 0.6
        rig.frame(ch, seq: 5, full: false)
        XCTAssertEqual(rig.resyncs.count, 2)
    }

    func testResizeIsDebouncedAndTheSameSizeIsSentToo() async throws {
        let (rig, ch) = try openRig()
        let before = try rig.last!.messages().count
        rig.socket.resize(ch: ch, cols: 50, rows: 30)
        rig.socket.resize(ch: ch, cols: 52, rows: 28)
        try await Task.sleep(nanoseconds: 80_000_000)
        XCTAssertEqual(try rig.last!.messages().count, before, "nothing inside the 150 ms window")
        try await waitUntil { (try? rig.last!.messages().count) == before + 1 }
        XCTAssertEqual(try rig.last!.messages().last, .resize(ch: ch, cols: 52, rows: 28), "only the last size")
        rig.socket.resize(ch: ch, cols: 52, rows: 28)
        try await waitUntil { (try? rig.last!.messages().count) == before + 2 }
        XCTAssertEqual(try rig.last!.messages().last, .resize(ch: ch, cols: 52, rows: 28), "the same size asks for a repaint")
    }

    func testInputGoesAsBinaryFramesOnlyOnceOpen() throws {
        let rig = Rig()
        rig.socket.connect()
        let ch = try XCTUnwrap(rig.socket.open(target: .pane("w1:p2"), mode: .control, cols: 58, rows: 30))
        XCTAssertFalse(rig.socket.send(input: [0x03], ch: ch), "not before term-ready")
        rig.ready()
        XCTAssertTrue(rig.socket.send(input: Array("y\r".utf8), ch: ch))
        XCTAssertEqual(rig.last!.binary, [Data([0x02, 0x00, UInt8(ch), 0x79, 0x0d])])
        XCTAssertFalse(rig.socket.send(input: [0x03], ch: 99), "unknown channel")
    }

    func testScrollMouseAndClose() throws {
        let (rig, ch) = try openRig()
        rig.socket.scroll(ch: ch, .up, lines: 5)
        rig.socket.scroll(ch: ch, .down, lines: 2, col: 29, row: 20)
        rig.socket.mouse(ch: ch, .down, button: "left", col: 3, row: 4)
        rig.socket.close(ch: ch)
        XCTAssertEqual(Array(try rig.last!.messages().suffix(4)),
                       [.scroll(ch: ch, .up, lines: 5), .scroll(ch: ch, .down, lines: 2, col: 29, row: 20),
                        .mouse(ch: ch, .down, button: "left", col: 3, row: 4), .close(ch: ch)])
        XCTAssertEqual(rig.socket.openChannels, [])
        rig.frame(ch, seq: 9, full: true)
        XCTAssertEqual(rig.frames, [], "frames for a closed channel are ignored")
    }

    func testChannelLimitFromTermReady() throws {
        let rig = Rig()
        rig.socket.connect()
        rig.ready(channels: 2)
        XCTAssertNotNil(rig.socket.open(target: .pane("a"), mode: .control, cols: 58, rows: 30))
        XCTAssertNotNil(rig.socket.open(target: .pane("b"), mode: .observe, cols: 58, rows: 30))
        XCTAssertNil(rig.socket.open(target: .pane("c"), mode: .observe, cols: 58, rows: 30))
        XCTAssertEqual(Set(rig.socket.openChannels).count, 2, "distinct channel ids")
    }

    func testTermClosedEndsTheChannelWithItsReason() throws {
        let (rig, ch) = try openRig()
        rig.last!.text(#"{"type":"term-closed","ch":\#(ch),"reason":"refused:busy","can_take_over":true}"#)
        XCTAssertEqual(rig.closed.count, 1)
        XCTAssertEqual(rig.closed.first?.reason, .refused("busy"))
        XCTAssertEqual(rig.closed.first?.canTakeOver, true)
        XCTAssertEqual(rig.socket.openChannels, [])
    }

    func testTermFreeReachesItsChannel() throws {
        let (rig, ch) = try openRig()
        rig.last!.text(#"{"type":"term-free","ch":\#(ch),"pane_id":"w1:p2"}"#)
        rig.last!.text(#"{"type":"term-free","ch":999,"pane_id":"w1:p2"}"#)
        XCTAssertEqual(rig.freed, [ch], "an unknown channel is ignored")
        XCTAssertEqual(rig.socket.openChannels, [ch], "the channel stays open")
    }

    func testALostConnectionWaitsForTheLinkThenReopensChannelsWithTheirCurrentSize() async throws {
        let (rig, ch) = try openRig(takeover: true)
        let gone = try XCTUnwrap(rig.socket.open(target: .pane("w1:p9"), mode: .observe, cols: 40, rows: 20))
        rig.last!.text(#"{"type":"term-closed","ch":\#(gone),"reason":"gone"}"#)
        rig.frame(ch, seq: 1, full: true)
        rig.socket.resize(ch: ch, cols: 44, rows: 30)
        rig.last!.drop()
        XCTAssertEqual(rig.socket.state, .reconnecting)
        // It used to retry after 1 s by itself; only the link reopens it.
        try await Task.sleep(nanoseconds: 1_300_000_000)
        XCTAssertEqual(rig.transports.count, 1, "no attempt of its own")
        rig.socket.reopen(reporter: nil)
        XCTAssertEqual(rig.transports.count, 2)
        let second = rig.last!
        XCTAssertEqual(try second.messages().map(\.type), [.hello], "hello first again; opens wait for term-ready")
        rig.ready()
        XCTAssertEqual(try second.messages().dropFirst().map { $0 },
                       [.open(ch: ch, target: .pane("w1:p2"), mode: .control, cols: 44, rows: 30)],
                       "same channel id, latest size, no takeover; the closed channel stays closed")
        rig.frame(ch, seq: 1, full: false)
        XCTAssertEqual(rig.resyncs, [ch], "a new bridge starts its seq again; a diff first still needs a full frame")
        // A reopen with the connection up (the interface changed) replaces it.
        rig.socket.reopen(reporter: nil)
        XCTAssertEqual(rig.transports.count, 3)
        XCTAssertTrue(rig.transports[1].isClosed, "the old one is closed")
        rig.ready()
        XCTAssertEqual(try rig.last!.messages().dropFirst().map(\.type), [.open], "its channels reopen there")
        rig.socket.disconnect()
        rig.socket.reopen(reporter: nil)
        XCTAssertEqual(rig.transports.count, 3, "nothing reopens what is no longer wanted")
    }

    func testAMissedPongDropsTheConnectionForTheLink() async throws {
        let rig = Rig()
        rig.socket.connect()
        rig.ready()
        rig.socket.pingTick()
        rig.last!.pong()
        rig.socket.pingTick()
        XCTAssertEqual(rig.socket.state, .ready(.init(protocolVersion: 1, channels: 4, shell: false)), "answered pings keep it up")
        rig.socket.pingTick()
        XCTAssertEqual(rig.socket.state, .reconnecting, "a ping unanswered at the next tick drops the connection")
        XCTAssertEqual(rig.transports.count, 1, "and waits for the link")
    }

    func testDisconnectStopsEverything() throws {
        let (rig, _) = try openRig()
        rig.socket.disconnect()
        XCTAssertEqual(rig.socket.state, .idle)
        XCTAssertTrue(rig.last!.isClosed)
        XCTAssertEqual(rig.socket.openChannels, [])
        rig.last!.drop()
        XCTAssertEqual(rig.socket.state, .idle, "no reconnect after the user closed it")
    }

    /// Recorded herdr frames, wrapped as the daemon would send them, come out
    /// of the socket into a grid that matches herdr's own text.
    func testRecordedFramesThroughTheSocketDrawHerdrsScreen() throws {
        let (rig, ch) = try openRig()
        let recorded = try TerminalFixture.frames("width-narrow")
        let grid = TerminalRenderer.makeGrid(cols: recorded[0].cols, rows: recorded[0].rows)
        rig.socket.onEvent = { event in
            if case let .frame(frame) = event { grid.feed(frame.bytes) }
        }
        for f in recorded {
            rig.last!.binary(TermFrame(ch: ch, seq: f.seq, cols: f.cols, rows: f.rows, full: f.full, bytes: f.bytes).encode())
        }
        XCTAssertEqual(grid.snapshotText(), try TerminalFixture.visible("width-narrow").joined(separator: "\n").trimmingCharacters(in: .newlines))
    }

    /// The real URLSession transport against a local WebSocket server: the
    /// hello goes out before the server says anything, and term-ready makes
    /// the connection ready (the server's greeting, a `/ws` status, is not a
    /// `/term` message and is ignored).
    func testTheURLSessionTransportSaysHelloAndBecomesReady() async throws {
        let server = try LocalWebSocketServer()
        defer { server.stop() }
        let port = try await server.start()
        let endpoint = DaemonEndpoint(host: "127.0.0.1", port: Int(port))
        let socket = TermSocket(endpoint: endpoint)
        socket.client = WSMessage.ClientInfo(name: "sheepdog-ios", version: "test", platform: "ios")
        socket.connect()
        defer { socket.disconnect() }
        let texts = try await server.texts(count: 1, timeout: 10)
        XCTAssertEqual(try TermMessage.decode(Data(texts[0].utf8)), .hello(client: socket.client))
        server.send(#"{"type":"term-ready","protocol":1,"limits":{"channels":4},"shell":false}"#)
        try await waitUntil(timeout: 5) { if case .ready = socket.state { return true } else { return false } }
        let ch = try XCTUnwrap(socket.open(target: .pane("w1:p1"), mode: .observe, cols: 40, rows: 14))
        let all = try await server.texts(count: 2, timeout: 5)
        XCTAssertEqual(try TermMessage.decode(Data(all[1].utf8)), .open(ch: ch, target: .pane("w1:p1"), mode: .observe, cols: 40, rows: 14))
    }

    // MARK: - Helpers

    private func json(_ message: TermMessage) throws -> String {
        String(decoding: try message.encode(), as: UTF8.self)
    }

    private func openRig(takeover: Bool = false, now: @escaping () -> Date = Date.init) throws -> (Rig, Int) {
        let rig = Rig(now: now)
        rig.socket.connect()
        rig.ready()
        let ch = try XCTUnwrap(rig.socket.open(target: .pane("w1:p2"), mode: .control, cols: 58, rows: 30, takeover: takeover))
        return (rig, ch)
    }

    private func waitUntil(timeout: TimeInterval = 2, _ condition: () -> Bool) async throws {
        let deadline = Date().addingTimeInterval(timeout)
        while !condition() {
            if Date() > deadline { throw URLError(.timedOut) }
            try await Task.sleep(nanoseconds: 10_000_000)
        }
    }
}

/// A `TermSocket` on fake transports, with everything it reported.
@MainActor
private final class Rig {
    let socket: TermSocket
    var transports: [FakeTermTransport] = []
    var frames: [TermFrame] = []
    var resyncs: [Int] = []
    var opened: [(Int, TermSocket.Opened)] = []
    var closed: [(ch: Int, reason: TermCloseReason, canTakeOver: Bool)] = []
    var freed: [Int] = []

    var last: FakeTermTransport? { transports.last }

    init(token: String = "", now: @escaping () -> Date = Date.init) {
        weak var weakSelf: Rig?
        let credentials = DeviceCredentials(daemonID: "d", secrets: InMemorySecretStore())
        if !token.isEmpty { credentials.adopt(SessionTicket(ticket: token, expires: Date().addingTimeInterval(3600))) }
        socket = TermSocket(endpoint: DaemonEndpoint(host: "127.0.0.1", port: 7777, secure: !token.isEmpty, credentials: credentials),
                            makeTransport: { request in
                                let t = FakeTermTransport(request: request)
                                weakSelf?.transports.append(t)
                                return t
                            },
                            now: now)
        weakSelf = self
        socket.client = WSMessage.ClientInfo(name: "sheepdog-ios", version: "test", platform: "ios")
        socket.onEvent = { [weak self] event in
            guard let self else { return }
            switch event {
            case let .frame(frame): self.frames.append(frame)
            case let .resync(ch): self.resyncs.append(ch)
            case let .opened(ch, opened): self.opened.append((ch, opened))
            case let .closed(ch, reason, canTakeOver): self.closed.append((ch, reason, canTakeOver))
            case let .free(ch): self.freed.append(ch)
            default: break
            }
        }
    }

    func ready(channels: Int = 4, shell: Bool = false) {
        last!.text(#"{"type":"term-ready","protocol":1,"limits":{"channels":\#(channels)},"shell":\#(shell)}"#)
    }

    func frame(_ ch: Int, seq: UInt32, full: Bool) {
        last!.binary(TermFrame(ch: ch, seq: seq, cols: 58, rows: 30, full: full, bytes: Array("x".utf8)).encode())
    }
}

@MainActor
private final class FakeTermTransport: TermTransport {
    var onEvent: ((TermTransportEvent) -> Void)?
    let request: URLRequest
    var texts: [String] = []
    var binary: [Data] = []
    var isClosed = false
    private var pendingPing: ((Bool) -> Void)?

    init(request: URLRequest) {
        self.request = request
    }

    func messages() throws -> [TermMessage] {
        try texts.map { try TermMessage.decode(Data($0.utf8)) }
    }

    func start() {}
    func send(text: String) { texts.append(text) }
    func send(data: Data) { binary.append(data) }
    func ping(_ completion: @escaping (Bool) -> Void) { pendingPing = completion }
    func close() { isClosed = true }

    // The daemon's side.
    func text(_ s: String) { onEvent?(.text(s)) }
    func binary(_ d: Data) { onEvent?(.data(d)) }
    func pong() { pendingPing?(true); pendingPing = nil }
    /// The connection drops.
    func drop(_ reason: String? = "gone") { onEvent?(.closed(reason)) }
}
