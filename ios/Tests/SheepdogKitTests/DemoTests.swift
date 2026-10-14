import XCTest
@testable import SheepdogKit

/// The offline demo (App Store review): the scripted world, and the app's
/// own client talking to it with no network.
final class DemoWorldTests: XCTestCase {
    private let t0 = Date(timeIntervalSince1970: 1_800_000_000)

    func testScriptFinishesAsksAndFails() {
        var world = DemoWorld(now: t0)
        XCTAssertEqual(world.takeLines().map(\.text), [DemoWorld.greeting])
        XCTAssertTrue(world.agents.allSatisfy { $0.state == .working })
        XCTAssertNil(world.pending)

        world.advance(to: t0.addingTimeInterval(40))
        let byAlias = Dictionary(uniqueKeysWithValues: world.agents.map { ($0.alias, $0) })
        XCTAssertEqual(byAlias["auth"]?.state, .done)
        XCTAssertEqual(byAlias["docs"]?.state, .done)
        XCTAssertEqual(byAlias["billing"]?.state, .blocked)
        XCTAssertEqual(byAlias["migrate"]?.state, .idle)
        XCTAssertTrue(byAlias["migrate"]?.lastResult?.spoken?.contains("failed") == true)
        XCTAssertEqual(world.pending?.isAnswerPrompt, true)
        XCTAssertEqual(world.takeLines().count, 4, "auth, billing's question, migrate, docs")
        XCTAssertNil(world.nextStepIn, "the script waits for the user")

        // The conversation is folded from the same events the stream carries.
        let kinds = world.history.items.map(\.kind)
        XCTAssertTrue(kinds.contains(.approval))
        XCTAssertEqual(kinds.filter { $0 == .push }.count, 5)
    }

    func testApproveThenBillingFinishes() throws {
        var world = DemoWorld(now: t0)
        world.advance(to: t0.addingTimeInterval(20))
        let id = try XCTUnwrap(world.pending?.id)
        let said = try world.answer(pendingID: id, option: 1)
        XCTAssertTrue(said.hasPrefix("Approved"))
        XCTAssertNil(world.pending)
        XCTAssertThrowsError(try world.answer(pendingID: id, option: 1), "answered twice")
        XCTAssertEqual(world.history.items.first { $0.pendingID == id }?.resolution, .confirmed)

        world.advance(to: t0.addingTimeInterval(29))
        XCTAssertEqual(world.agents.first { $0.alias == "billing" }?.state, .done)
    }

    func testDeclineLeavesBillingIdle() throws {
        var world = DemoWorld(now: t0)
        world.advance(to: t0.addingTimeInterval(20))
        let said = try world.answer(pendingID: XCTUnwrap(world.pending?.id), option: 2)
        XCTAssertTrue(said.hasPrefix("Declined"))
        world.advance(to: t0.addingTimeInterval(120))
        XCTAssertEqual(world.agents.first { $0.alias == "billing" }?.state, .idle)
    }

    func testQuestions() {
        var world = DemoWorld(now: t0)
        XCTAssertEqual(world.ask("What failed?"), "Nothing has failed so far.")
        world.advance(to: t0.addingTimeInterval(30))
        XCTAssertTrue(world.ask("What failed?").contains("users.plan"))
        XCTAssertTrue(world.ask("What's going on?").contains("billing is waiting for your approval"))
        XCTAssertTrue(world.ask("What is billing asking?").contains("npm install p-retry"))
        XCTAssertTrue(world.ask("What did auth do?").contains("42"))
        XCTAssertTrue(world.ask("sing me a song").contains("demo"))
        // Said, not tapped: approval by words.
        XCTAssertTrue(world.ask("yes, approve it").hasPrefix("Approved"))
        XCTAssertNil(world.pending)
        let typed = world.history.items.filter { $0.kind == .shepherd }.count
        XCTAssertEqual(typed, 7)
        XCTAssertEqual(world.history.items.filter { $0.kind == .dog }.count, 7)
    }

    /// The demo asks for speech recognition; iOS kills an app that does so
    /// without a usage string.
    func testSpeechRecognitionUsageDescription() throws {
        let text = try XCTUnwrap(Bundle.main.infoDictionary?["NSSpeechRecognitionUsageDescription"] as? String)
        XCTAssertTrue(text.contains("on-device"))
    }

    func testAgentWindow() throws {
        var world = DemoWorld(now: t0)
        world.advance(to: t0.addingTimeInterval(30))
        let screen = try XCTUnwrap(world.screen(paneID: DemoWorld.billingPane))
        XCTAssertTrue(screen.text?.contains("npm install p-retry") == true)
        XCTAssertNil(world.screen(paneID: "nope"))
    }
}

/// `DaemonClient` against `DemoDaemon` through `DemoURLProtocol`: the
/// JSON the demo writes is what the app reads.
final class DemoDaemonTests: XCTestCase {
    private var now = Date(timeIntervalSince1970: 1_800_000_000)
    private var daemon: DemoDaemon!
    private var client: DaemonClient!

    override func setUp() {
        super.setUp()
        daemon = DemoDaemon(clock: { [unowned self] in self.now })
        DemoDaemon.current = daemon
        client = DaemonClient(endpoint: DemoDaemon.endpoint, session: DemoDaemon.session())
    }

    override func tearDown() {
        daemon.stop()
        super.tearDown()
    }

    func testRoundTrip() async throws {
        let hello = try await client.hello()
        XCTAssertEqual(hello.version, "demo")
        XCTAssertFalse(hello.needsToken)

        now = now.addingTimeInterval(20)
        let state = try await client.state()
        XCTAssertEqual(state.agents.count, 4)
        let pending = try XCTUnwrap(state.pendingAction)
        XCTAssertEqual(pending.options?.count, 2)
        XCTAssertEqual(state.agents.first { $0.alias == "auth" }?.state, .done)
        XCTAssertNotNil(state.agents.first { $0.alias == "auth" }?.lastResult?.at)

        let page = try await client.conversation()
        XCTAssertTrue(page.items.contains { $0.kind == .approval && $0.pendingID == pending.id })

        let spoken = try await client.answerPending(id: pending.id, option: 1)
        XCTAssertTrue(spoken.hasPrefix("Approved"))
        do {
            _ = try await client.answerPending(id: pending.id, option: 1)
            XCTFail("a second answer is refused")
        } catch DaemonError.pendingGone {}

        let reply = try await client.sendUtterance("What's going on?")
        XCTAssertTrue(reply.spoken?.contains("auth is done") == true)

        let screen = try await client.agentScreen(paneID: "demo-auth", hash: nil)
        XCTAssertEqual(screen?.alias, "auth")

        let events = try await client.recentEvents()
        XCTAssertTrue(events.contains { $0.type == "pending.confirmed" })
        XCTAssertNotNil(PendingAction(event: try XCTUnwrap(events.first { $0.type == "pending.created" })))
    }

    func testEventStreamReplaysThenStaysOpen() async throws {
        now = now.addingTimeInterval(20)
        var seen: [String] = []
        for try await output in client.events(since: 0) {
            if case let .event(event) = output { seen.append(event.type) }
            if seen.contains("pending.created") { break }
        }
        XCTAssertEqual(seen.first, "daemon.start")
        XCTAssertTrue(seen.contains("voice.push"))
    }
}
