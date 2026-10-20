import XCTest
@testable import SheepdogKit

final class AgentScreenClientTests: XCTestCase {
    override func tearDown() {
        StubProtocol.handler = nil
        StubProtocol.seen = []
    }

    private let frame = #"{"pane_id":"w1:p2","alias":"auth","spoken":"Auth fix","kind":"codex","state":"working","state_since":"2026-10-03T01:00:00Z","text":"$ make test\nok","lines":200,"truncated":false,"hash":"9f2c0e1a44b7d310","unchanged":false}"#

    func testScreenDecodesTheContractsFrame() async throws {
        StubProtocol.handler = { _ in (200, Data(self.frame.utf8)) }
        let screen = try await StubProtocol.client(token: "tok").agentScreen(paneID: "w1:p2")
        XCTAssertEqual(screen?.displayName, "Auth fix")
        XCTAssertEqual(screen?.state, .working)
        XCTAssertEqual(screen?.text, "$ make test\nok")
        XCTAssertEqual(screen?.hash, "9f2c0e1a44b7d310")
        let request = try XCTUnwrap(StubProtocol.seen.first)
        XCTAssertEqual(request.url?.absoluteString, "http://127.0.0.1:7799/api/agents/w1:p2/screen?lines=200&ansi=1")
        XCTAssertEqual(request.value(forHTTPHeaderField: "Authorization"), "Bearer tok")
    }

    func testScreenSendsTheLastHashBack() async throws {
        StubProtocol.handler = { _ in (200, Data(#"{"pane_id":"w1:p2","alias":"auth","state":"idle","hash":"abc","unchanged":true}"#.utf8)) }
        let screen = try await StubProtocol.client().agentScreen(paneID: "w1:p2", lines: 50, hash: "abc")
        XCTAssertEqual(screen?.unchanged, true)
        XCTAssertNil(screen?.text)
        XCTAssertEqual(StubProtocol.seen.first?.url?.query, "lines=50&ansi=1&hash=abc")
    }

    func testPaneIDsArePathEscaped() throws {
        let client = StubProtocol.client()
        let colon = try client.request(encodedPath: DaemonClient.agentPath(paneID: "fake:1", suffix: "screen"))
        XCTAssertEqual(colon.url?.absoluteString, "http://127.0.0.1:7799/api/agents/fake:1/screen")
        let tmux = try client.request(encodedPath: DaemonClient.agentPath(paneID: "%12", suffix: "input"))
        XCTAssertEqual(tmux.url?.absoluteString, "http://127.0.0.1:7799/api/agents/%2512/input")
        let slash = try client.request(encodedPath: DaemonClient.agentPath(paneID: "a/b", suffix: "screen"))
        XCTAssertEqual(slash.url?.absoluteString, "http://127.0.0.1:7799/api/agents/a%2Fb/screen")
    }

    func testA404WithTheDaemonsErrorIsNoSuchAgent() async {
        StubProtocol.handler = { _ in (404, Data(#"{"error":"no such agent"}"#.utf8)) }
        do {
            _ = try await StubProtocol.client().agentScreen(paneID: "w9:p9")
            XCTFail("expected rejected")
        } catch {
            XCTAssertEqual(error as? DaemonError, .rejected(status: 404, message: "no such agent"))
        }
    }

    func testAPlain404IsAnOlderDaemon() async throws {
        StubProtocol.handler = { _ in (404, Data("404 page not found\n".utf8)) }
        let screen = try await StubProtocol.client().agentScreen(paneID: "w1:p2")
        XCTAssertNil(screen)
        do {
            _ = try await StubProtocol.client().agentInput(paneID: "w1:p2", keys: [.enter])
            XCTFail("expected rejected")
        } catch {
            XCTAssertEqual(error as? DaemonError, .rejected(status: 404, message: AgentScreenModel.unsupportedMessage))
        }
    }

    func testAPromptPostsTextWithSubmit() async throws {
        StubProtocol.handler = { _ in (200, Data(#"{"ok":true,"kind":"prompt","spoken":"Sent."}"#.utf8)) }
        let reply = try await StubProtocol.client().agentInput(paneID: "w1:p2", text: "run the tests")
        XCTAssertEqual(reply.kind, "prompt")
        let request = try XCTUnwrap(StubProtocol.seen.first)
        XCTAssertEqual(request.httpMethod, "POST")
        XCTAssertEqual(request.url?.path, "/api/agents/w1:p2/input")
        let body = try XCTUnwrap(JSONSerialization.jsonObject(with: request.httpBody ?? Data()) as? [String: Any])
        XCTAssertEqual(body["text"] as? String, "run the tests")
        XCTAssertEqual(body["submit"] as? Bool, true)
        XCTAssertNil(body["keys"])
    }

    func testKeysPostTheHerdrSpelling() async throws {
        StubProtocol.handler = { _ in (200, Data(#"{"ok":true,"kind":"answer","spoken":"Told auth Yes."}"#.utf8)) }
        let reply = try await StubProtocol.client().agentInput(paneID: "w1:p2", keys: [.ctrlC, .one, .up, .tab])
        XCTAssertEqual(reply.kind, "answer")
        XCTAssertEqual(reply.spoken, "Told auth Yes.")
        let body = try XCTUnwrap(JSONSerialization.jsonObject(with: StubProtocol.seen.first?.httpBody ?? Data()) as? [String: Any])
        XCTAssertEqual(body["keys"] as? [String], ["ctrl+c", "1", "up", "tab"])
    }

    func testABlockedAgentsRefusalCarriesItsWords() async {
        StubProtocol.handler = { _ in (409, Data(#"{"error":"auth is waiting at a prompt; answer it first"}"#.utf8)) }
        do {
            _ = try await StubProtocol.client().agentInput(paneID: "w1:p2", text: "hi")
            XCTFail("expected rejected")
        } catch {
            XCTAssertEqual(error as? DaemonError, .rejected(status: 409, message: "auth is waiting at a prompt; answer it first"))
        }
    }
}

final class AgentScreenModelTests: XCTestCase {
    private func frame(_ text: String?, hash: String, state: Agent.State = .working, spoken: String? = "Auth fix", unchanged: Bool = false) -> AgentScreen {
        AgentScreen(paneID: "w1:p2", alias: "auth", spoken: spoken, state: state, text: text, hash: hash, unchanged: unchanged)
    }

    func testAFirstFrameShowsAndEnablesTheControls() {
        var model = AgentScreenModel(paneID: "w1:p2")
        XCTAssertFalse(model.canOperate, "nothing read yet")
        XCTAssertNil(model.hashForNextRead)
        XCTAssertTrue(model.apply(frame("hello", hash: "h1")))
        XCTAssertEqual(model.text, "hello")
        XCTAssertEqual(model.revision, 1)
        XCTAssertEqual(model.hashForNextRead, "h1")
        XCTAssertTrue(model.canOperate)
    }

    func testAnUnchangedFrameKeepsTheTextAndRevision() {
        var model = AgentScreenModel(paneID: "w1:p2")
        model.apply(frame("hello", hash: "h1"))
        XCTAssertFalse(model.apply(frame(nil, hash: "h1", unchanged: true)))
        XCTAssertEqual(model.text, "hello")
        XCTAssertEqual(model.revision, 1)
    }

    func testAnUnchangedFrameStillPicksUpARename() {
        var model = AgentScreenModel(paneID: "w1:p2")
        model.apply(frame("hello", hash: "h1"))
        XCTAssertTrue(model.apply(frame(nil, hash: "h1", spoken: "Login bug", unchanged: true)))
        XCTAssertEqual(model.screen?.displayName, "Login bug")
        XCTAssertEqual(model.revision, 1)
    }

    func testANewFrameBumpsTheRevision() {
        var model = AgentScreenModel(paneID: "w1:p2")
        model.apply(frame("hello", hash: "h1"))
        XCTAssertTrue(model.apply(frame("hello\nworld", hash: "h2")))
        XCTAssertEqual(model.revision, 2)
        // Same text, new state: the header changes, the text does not.
        XCTAssertTrue(model.apply(frame("hello\nworld", hash: "h3", state: .blocked)))
        XCTAssertEqual(model.revision, 2)
        XCTAssertEqual(model.screen?.state, .blocked)
    }

    func testUnchangedForAHashWeDoNotHoldAsksForTheTextAgain() {
        var model = AgentScreenModel(paneID: "w1:p2")
        model.apply(frame("hello", hash: "h1"))
        model.apply(frame(nil, hash: "zz", unchanged: true))
        XCTAssertEqual(model.hashForNextRead, "")
        XCTAssertEqual(model.text, "hello")
    }

    func testAFailedReadDisablesTheControlsUntilTheNextFrame() {
        var model = AgentScreenModel(paneID: "w1:p2")
        model.apply(frame("hello", hash: "h1"))
        model.fail(DaemonError.unreachable(endpoint: "127.0.0.1:7799", detail: ""))
        XCTAssertFalse(model.canOperate)
        XCTAssertEqual(model.error, "Can't reach the daemon at 127.0.0.1:7799 — is it running?")
        XCTAssertEqual(model.text, "hello", "the last frame stays on screen")
        XCTAssertTrue(model.apply(frame(nil, hash: "h1", unchanged: true)), "recovering changes what is shown")
        XCTAssertNil(model.error)
        XCTAssertTrue(model.canOperate)
    }

    func testAnOlderDaemonIsUnsupported() {
        var model = AgentScreenModel(paneID: "w1:p2")
        model.markUnsupported()
        XCTAssertFalse(model.canOperate)
        XCTAssertEqual(model.error, AgentScreenModel.unsupportedMessage)
    }

    func testSendingDisablesControlsAndForcesAFullRead() {
        var model = AgentScreenModel(paneID: "w1:p2")
        model.apply(frame("hello", hash: "h1"))
        model.beginSend()
        XCTAssertFalse(model.canOperate)
        model.finishSend(reply: AgentInputReply(kind: "answer", spoken: "Told auth Yes."))
        XCTAssertTrue(model.canOperate)
        XCTAssertEqual(model.lastReply, "Told auth Yes.")
        XCTAssertEqual(model.hashForNextRead, "")
        model.beginSend()
        model.finishSend(error: DaemonError.rejected(status: 400, message: "key \"f1\" is not allowed"))
        XCTAssertEqual(model.inputError, "The daemon refused: key \"f1\" is not allowed")
    }

    func testOnlyEscAndCtrlCAskFirst() {
        XCTAssertEqual(AgentKey.allCases.filter(\.needsConfirmation), [.esc, .ctrlC])
        XCTAssertEqual(AgentKey.allCases.map(\.rawValue), ["enter", "esc", "ctrl+c", "1", "2", "3", "up", "down", "tab"])
        XCTAssertTrue(AgentKey.ctrlC.confirmationTitle(agent: "Auth fix").contains("Auth fix"))
    }

    func testCanSendWantsSomeTextWithinTheLimit() {
        XCTAssertFalse(AgentScreenModel.canSend("   "))
        XCTAssertTrue(AgentScreenModel.canSend("run the tests"))
        XCTAssertTrue(AgentScreenModel.canSend(String(repeating: "x", count: 4001)), "the daemon takes 64 KB now")
        XCTAssertTrue(AgentScreenModel.canSend(String(repeating: "x", count: 64 << 10)))
        XCTAssertFalse(AgentScreenModel.canSend(String(repeating: "x", count: (64 << 10) + 1)))
        XCTAssertFalse(AgentScreenModel.canSend(String(repeating: "界", count: 22_000)), "bytes, not characters")
    }

    func testAtBottomHasSlack() {
        XCTAssertTrue(AgentScreenModel.isAtBottom(anchorMaxY: 610, viewportHeight: 600))
        XCTAssertFalse(AgentScreenModel.isAtBottom(anchorMaxY: 900, viewportHeight: 600))
    }

    func testAgentDecodesTheSpokenName() throws {
        let json = #"{"pane_id":"w1:p1","alias":"auth","spoken":"Auth fix","state":"idle"}"#
        let agent = try DaemonClient.decoder.decode(Agent.self, from: Data(json.utf8))
        XCTAssertEqual(agent.displayName, "Auth fix")
        let old = try DaemonClient.decoder.decode(Agent.self, from: Data(#"{"pane_id":"w1:p1","alias":"auth","state":"idle"}"#.utf8))
        XCTAssertEqual(old.displayName, "auth")
    }
}
