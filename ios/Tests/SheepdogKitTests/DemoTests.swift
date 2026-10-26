import XCTest
@testable import SheepdogKit

/// The offline demo (App Store review): the scripted world, and the app's
/// own client talking to it with no network.
final class DemoWorldTests: XCTestCase {
    private let t0 = Date(timeIntervalSince1970: 1_800_000_000)

    /// The demo's "Yes, go ahead." chip is what the voice would hand `ask`:
    /// the history says the approval was answered by voice, not by tap.
    func testSayingYesApprovesByVoice() throws {
        var world = DemoWorld(now: t0)
        world.advance(to: t0.addingTimeInterval(40))
        let id = try XCTUnwrap(world.pending?.id)
        XCTAssertTrue(world.ask(DemoWorld.sayYes).hasPrefix("Approved"))
        XCTAssertNil(world.pending)
        let item = try XCTUnwrap(world.history.items.first { $0.pendingID == id })
        XCTAssertEqual(item.resolution, .confirmed)
        XCTAssertEqual(item.approvalOutcome, "You approved — Yes, proceed · by voice")
    }

    func testScriptFinishesAsksAndFails() {
        var world = DemoWorld(now: t0)
        XCTAssertEqual(world.takeLines().map(\.text), [DemoWorld.greeting])
        XCTAssertTrue(world.agents.allSatisfy { $0.state == .working })
        XCTAssertNil(world.pending)

        world.advance(to: t0.addingTimeInterval(40))
        let byAlias = Dictionary(uniqueKeysWithValues: world.agents.map { ($0.alias, $0) })
        XCTAssertEqual(byAlias["claude-auth"]?.state, .done)
        XCTAssertEqual(byAlias["codex-docs"]?.state, .done)
        XCTAssertEqual(byAlias["codex-payments"]?.state, .blocked)
        XCTAssertEqual(byAlias["claude-migration"]?.state, .idle)
        XCTAssertTrue(byAlias["claude-migration"]?.lastResult?.spoken?.contains("failed") == true)
        XCTAssertEqual(world.pending?.isAnswerPrompt, true)
        XCTAssertEqual(world.takeLines().count, 4, "claude-auth, claude-migration, codex-payments' question, codex-docs")
        XCTAssertEqual(Dictionary(uniqueKeysWithValues: world.agents.map { ($0.alias, $0.kind) }),
                       ["claude-auth": "claude", "codex-payments": "codex", "claude-migration": "claude", "codex-docs": "codex"])
        XCTAssertNil(world.nextStepIn, "the script waits for the user")

        // The conversation is folded from the same events the stream carries.
        let kinds = world.history.items.map(\.kind)
        XCTAssertTrue(kinds.contains(.approval))
        XCTAssertEqual(kinds.filter { $0 == .push }.count, 5)
    }

    func testApproveThenPaymentsFinishes() throws {
        var world = DemoWorld(now: t0)
        world.advance(to: t0.addingTimeInterval(20))
        let id = try XCTUnwrap(world.pending?.id)
        let said = try world.answer(pendingID: id, option: 1)
        XCTAssertTrue(said.hasPrefix("Approved"))
        XCTAssertNil(world.pending)
        XCTAssertThrowsError(try world.answer(pendingID: id, option: 1), "answered twice")
        XCTAssertEqual(world.history.items.first { $0.pendingID == id }?.resolution, .confirmed)

        world.advance(to: t0.addingTimeInterval(29))
        XCTAssertEqual(world.agents.first { $0.alias == "codex-payments" }?.state, .done)
    }

    /// The README hero's lines: after the approval, the status leaves out
    /// the agent just approved; once it finishes, it is back in.
    func testStatusAfterApproval() throws {
        var world = DemoWorld(now: t0)
        world.advance(to: t0.addingTimeInterval(17))
        XCTAssertEqual(world.takeLines().last?.text, "codex-payments wants to run npm install p-retry. Approve?")
        try world.answer(pendingID: XCTUnwrap(world.pending?.id), option: 1)
        world.advance(to: t0.addingTimeInterval(20))
        XCTAssertEqual(world.ask("What's going on?"), "claude-auth is done, claude-migration failed, codex-docs is still writing.")
        XCTAssertEqual(world.ask("Why did the migration fail?"), "The column already existed. It rolled back, nothing changed.")
        world.advance(to: t0.addingTimeInterval(26))
        XCTAssertEqual(world.ask("What's going on?"), "claude-auth is done, codex-payments is done, claude-migration failed, codex-docs is still writing.")
    }

    func testDeclineLeavesPaymentsIdle() throws {
        var world = DemoWorld(now: t0)
        world.advance(to: t0.addingTimeInterval(20))
        let said = try world.answer(pendingID: XCTUnwrap(world.pending?.id), option: 2)
        XCTAssertTrue(said.hasPrefix("Declined"))
        world.advance(to: t0.addingTimeInterval(120))
        XCTAssertEqual(world.agents.first { $0.alias == "codex-payments" }?.state, .idle)
    }

    func testQuestions() {
        var world = DemoWorld(now: t0)
        XCTAssertEqual(world.ask("What failed?"), "Nothing has failed so far.")
        XCTAssertEqual(world.ask("What's going on?"),
                       "claude-auth is still fixing the login, codex-payments is adding retries, claude-migration is still migrating, codex-docs is still writing.")
        world.advance(to: t0.addingTimeInterval(30))
        XCTAssertEqual(world.ask("What failed?"), "claude-migration failed. The column already existed. It rolled back, nothing changed.")
        XCTAssertEqual(world.ask("Why did the migration fail?"), "The column already existed. It rolled back, nothing changed.")
        XCTAssertTrue(world.ask("What's going on?").contains("codex-payments is waiting for your approval"))
        XCTAssertTrue(world.ask("What is codex-payments asking?").contains("npm install p-retry"))
        XCTAssertTrue(world.ask("What did claude-auth do?").contains("42"))
        XCTAssertEqual(world.ask("How is codex-docs doing?"), "codex-docs is still writing the install section of the README.")
        XCTAssertTrue(world.ask("sing me a song").contains("demo"))
        // Said, not tapped: approval by words.
        XCTAssertTrue(world.ask("yes, approve it").hasPrefix("Approved"))
        XCTAssertNil(world.pending)
        let typed = world.history.items.filter { $0.kind == .shepherd }.count
        XCTAssertEqual(typed, 10)
        XCTAssertEqual(world.history.items.filter { $0.kind == .dog }.count, 10)
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
        let screen = try XCTUnwrap(world.screen(paneID: DemoWorld.paymentsPane))
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

    func testUpdateOnlyWhenARecordingSetsOne() async throws {
        let plain = try await client.state()
        XCTAssertNil(plain.update, "a demo daemon has no release to compare with")
        daemon.update = UpdateInfo(current: "v0.1.16", latest: "v0.1.17", available: true, how: "brew upgrade sheepdog")
        let state = try await client.state()
        XCTAssertEqual(state.update?.latest, "v0.1.17")
        XCTAssertEqual(state.update?.notice?.command, "brew upgrade sheepdog")
    }

    func testNewAgentStartsAScriptedAgent() async throws {
        let options = try await client.newAgentOptions()
        let shop = try XCTUnwrap(options.projects.first { $0.name == "shop" })
        let reply = try await client.newAgent(kind: "claude", project: shop, text: "Fix the migration: users.plan already exists.")
        XCTAssertEqual(reply.spoken, "Claude is ready in shop.")
        let pane = try XCTUnwrap(reply.paneID)
        let state = try await client.state()
        let agent = try XCTUnwrap(state.agents.first { $0.paneID == pane })
        XCTAssertEqual(agent.alias, "claude-3")
        XCTAssertEqual(agent.state, .working)
        XCTAssertEqual(agent.currentTask, "Fix the migration: users.plan already exists.")
        let screen = try await client.agentScreen(paneID: pane)
        XCTAssertNotNil(screen, "its window opens")
    }

    func testAPromptIsSentOnlyInARecording() async throws {
        let plain = try await client.agentInput(paneID: DemoWorld.authPane, text: "Handle the staging error too.")
        XCTAssertTrue(plain.spoken?.hasPrefix("This is the demo") == true)
        daemon.recording = true
        let sent = try await client.agentInput(paneID: DemoWorld.authPane, text: "Handle the staging error too.")
        XCTAssertEqual(sent.spoken, "Sent to claude-auth.")
    }

    @MainActor
    func testTheShellAnswersAFewCommands() {
        XCTAssertEqual(DemoTermTransport.shellAnswer("git  status").first, "On branch main")
        XCTAssertEqual(DemoTermTransport.shellAnswer("git log --oneline -3").count, 3)
        XCTAssertEqual(DemoTermTransport.shellAnswer("pwd"), ["/Users/demo/code/shop"])
        XCTAssertTrue(DemoTermTransport.shellAnswer("rm -rf /").first?.hasPrefix("demo: only") == true, "anything else runs nothing")
        XCTAssertEqual(DemoTermTransport.wrap("abcdefgh", cols: 3), ["abc", "def", "gh"], "a long word is cut at the width")
        XCTAssertEqual(DemoTermTransport.wrap("go.mod  internal  migrations", cols: 18), ["go.mod  internal", "migrations"], "between words where it can")
    }

    func testOneProjectPath() async throws {
        // Demo and real shots cut together: every path is shop under /Users/demo/code.
        let state = try await client.state()
        XCTAssertTrue(state.agents.allSatisfy { $0.cwd == "~/code/shop" })
        let changes = try await client.agentChanges(paneID: DemoWorld.authPane)
        XCTAssertEqual(changes?.root, "/Users/demo/code/shop")
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
        XCTAssertEqual(state.agents.first { $0.alias == "claude-auth" }?.state, .done)
        XCTAssertNotNil(state.agents.first { $0.alias == "claude-auth" }?.lastResult?.at)

        let page = try await client.conversation()
        XCTAssertTrue(page.items.contains { $0.kind == .approval && $0.pendingID == pending.id })

        let spoken = try await client.answerPending(id: pending.id, option: 1)
        XCTAssertTrue(spoken.hasPrefix("Approved"))
        do {
            _ = try await client.answerPending(id: pending.id, option: 1)
            XCTFail("a second answer is refused")
        } catch DaemonError.pendingGone {}

        let reply = try await client.sendUtterance("What's going on?")
        // The storyboard's exact line: the agent just approved is left out.
        XCTAssertEqual(reply.spoken, "claude-auth is done, claude-migration failed, codex-docs is still writing.")

        let screen = try await client.agentScreen(paneID: DemoWorld.authPane, hash: nil)
        XCTAssertEqual(screen?.alias, "claude-auth")

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
