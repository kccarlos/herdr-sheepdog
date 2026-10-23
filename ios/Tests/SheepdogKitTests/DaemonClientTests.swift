import XCTest
@testable import SheepdogKit

final class DaemonEndpointTests: XCTestCase {
    func testParsesWhatSomeoneActuallyTypes() {
        XCTAssertEqual(DaemonEndpoint(text: "192.168.1.20:7777")?.display, "192.168.1.20:7777")
        XCTAssertEqual(DaemonEndpoint(text: "http://mac.local:7799")?.display, "mac.local:7799")
        XCTAssertEqual(DaemonEndpoint(text: "  mac.local  ")?.display, "mac.local:7777")
        XCTAssertEqual(DaemonEndpoint(text: "http://100.101.102.103:7777/voice")?.display, "100.101.102.103:7777")
        XCTAssertEqual(DaemonEndpoint(text: "[fd7a::1]:7777")?.display, "fd7a::1:7777")
    }

    func testRejectsNonsense() {
        XCTAssertNil(DaemonEndpoint(text: ""))
        XCTAssertNil(DaemonEndpoint(text: "   "))
        XCTAssertNil(DaemonEndpoint(text: "http://"))
        XCTAssertNil(DaemonEndpoint(text: "host:99999"))
    }

    func testBuildsTheURLsTheContractNames() {
        let endpoint = DaemonEndpoint(text: "127.0.0.1:7799")!
        XCTAssertEqual(endpoint.url(path: "/api/hello")?.absoluteString, "http://127.0.0.1:7799/api/hello")
        XCTAssertEqual(endpoint.webSocketURL?.absoluteString, "ws://127.0.0.1:7799/ws")
        XCTAssertEqual(
            endpoint.url(path: "/events", query: [URLQueryItem(name: "since", value: "12")])?.absoluteString,
            "http://127.0.0.1:7799/events?since=12"
        )
    }

    func testAnIPv6LiteralIsBracketedInTheURL() {
        let endpoint = DaemonEndpoint(text: "[fd7a::1]:7777")!
        XCTAssertEqual(endpoint.webSocketURL?.absoluteString, "ws://[fd7a::1]:7777/ws")
    }

    func testThePairedComputerIsTLS() {
        let endpoint = DaemonEndpoint(text: "sheepdog.example:443", secure: true)!
        XCTAssertTrue(endpoint.secure)
        XCTAssertEqual(endpoint.webSocketURL?.absoluteString, "wss://sheepdog.example:443/ws")
    }

    func testTheTicketGoesOnEveryRequestToThePairedComputer() throws {
        DeviceCredentials.shared.adopt(SessionTicket(ticket: "abc", expires: Date().addingTimeInterval(3600)))
        defer { DeviceCredentials.shared.dropTicket() }
        let endpoint = DaemonEndpoint(text: "192.168.1.20:7777", secure: true)!
        let client = DaemonClient(endpoint: endpoint)
        for request in [try client.request(path: "/api/state"), try client.eventsRequest(since: 0)] {
            XCTAssertEqual(request.value(forHTTPHeaderField: "Authorization"), "Bearer sds_abc")
            XCTAssertEqual(request.url?.scheme, "https")
            // Never in the query string: a header is readable and a URL is logged.
            XCTAssertFalse(request.url!.absoluteString.contains("abc"))
        }
        var upgrade = URLRequest(url: endpoint.webSocketURL!)
        endpoint.authorize(&upgrade)
        XCTAssertEqual(upgrade.value(forHTTPHeaderField: "Authorization"), "Bearer sds_abc")
        XCTAssertEqual(upgrade.url?.scheme, "wss")
        // An expired ticket is not sent.
        DeviceCredentials.shared.adopt(SessionTicket(ticket: "old", expires: Date().addingTimeInterval(-1)))
        XCTAssertNil(try client.request(path: "/api/state").value(forHTTPHeaderField: "Authorization"))
    }

    func testNoTokenMeansNoHeaderAtAll() throws {
        let client = DaemonClient(endpoint: DaemonEndpoint(text: "127.0.0.1:7799")!)
        XCTAssertNil(try client.request(path: "/api/hello").value(forHTTPHeaderField: "Authorization"))
    }
}

final class DaemonModelTests: XCTestCase {
    func testDecodesTheHelloPayloadFromTheContract() throws {
        let json = #"{"name":"sheepdog","version":"0.9.1","voice_name":"Rex","auth":"token","ws":"/ws","audio":{"in_hz":16000,"out_hz":24000,"format":"pcm16le mono"}}"#
        let hello = try DaemonClient.decoder.decode(Hello.self, from: Data(json.utf8))
        XCTAssertEqual(hello.name, "sheepdog")
        XCTAssertEqual(hello.voiceName, "Rex")
        XCTAssertTrue(hello.needsToken)
        XCTAssertEqual(hello.audio.inHz, 16_000)
        XCTAssertEqual(hello.audio.outHz, 24_000)
        XCTAssertEqual(hello.displayName, "Rex")
    }

    func testAHelloWithoutAVoiceNameFallsBackToSheepdog() throws {
        let json = #"{"name":"sheepdog","version":"0.9.1","auth":"none","ws":"/ws","audio":{"in_hz":16000,"out_hz":24000,"format":"pcm16le mono"}}"#
        let hello = try DaemonClient.decoder.decode(Hello.self, from: Data(json.utf8))
        XCTAssertFalse(hello.needsToken)
        XCTAssertEqual(hello.displayName, "Sheepdog")
    }

    func testDecodesStateWithAgentsAndAPendingAction() throws {
        let json = """
        {"agents":[{"pane_id":"w1:p1","alias":"auth","kind":"codex","state":"blocked","state_since":"2026-09-22T00:10:00.5Z",
        "current_task":"add the migration","last_block":{"question":"run the migration?"},"last_result":{"spoken":"wrote hello.txt"}}],
        "pending_action":{"id":"p1","pane_id":"w1:p1","kind":"answer_prompt","spoken_summary":"auth is asking",
        "question":"whether to run the migration","options":[{"label":"Yes, proceed"}]},
        "session":{},"voice":{"mode":"off","name":"Rex","status":"closed","reason":"voice_off","muted":false,"stalled":false,"client_connected":true},
        "herdr":"mock","version":"0.9.1","now":"2026-09-22T00:10:01Z"}
        """
        let state = try DaemonClient.decoder.decode(DaemonState.self, from: Data(json.utf8))
        XCTAssertEqual(state.agents.count, 1)
        XCTAssertEqual(state.agents[0].alias, "auth")
        XCTAssertEqual(state.agents[0].state, .blocked)
        XCTAssertNotNil(state.agents[0].stateSince)
        XCTAssertTrue(state.voice.isOff)
        XCTAssertEqual(state.pendingQuestion(for: "w1:p1"), "whether to run the migration")
        XCTAssertNil(state.pendingQuestion(for: "w9:p9"))
        XCTAssertEqual(state.herdr, "mock")
    }

    func testDecodesEveryOpenQuestionTheChosenAnswerTitleAndComputer() throws {
        let json = """
        {"agents":[{"pane_id":"w1:p1","alias":"auth","kind":"codex","state":"blocked","title":"Fix the login test",
        "last_block":{"question":"run the migration?","answered":false}},
        {"pane_id":"w2:p1","alias":"docs","kind":"claude","state":"blocked"},
        {"pane_id":"w3:p1","alias":"web","kind":"claude","state":"idle","last_block":{"question":"push?","answered":true,"answer":"Yes","via":"tap"}}],
        "pending_action":{"id":"p2","pane_id":"w2:p1","kind":"answer_prompt","spoken_summary":"docs is asking","question":"whether to edit README"},
        "questions":[{"id":"p1","pane_id":"w1:p1","kind":"answer_prompt","spoken_summary":"auth is asking","question":"whether to run the migration","options":[{"index":1,"label":"Yes","keys":["y"],"default":true}]},
        {"id":"p2","pane_id":"w2:p1","kind":"answer_prompt","spoken_summary":"docs is asking","question":"whether to edit README"}],
        "voice":{"mode":"off","status":"closed","muted":false,"stalled":false,"client_connected":false}}
        """
        let state = try DaemonClient.decoder.decode(DaemonState.self, from: Data(json.utf8))
        XCTAssertNil(state.pendingQuestion(for: "w1:p1"))
        XCTAssertEqual(state.pendingQuestion(for: "w2:p1"), "whether to edit README")
        XCTAssertEqual(state.question(for: "w2:p1")?.id, "p2")
        XCTAssertEqual(state.question(for: "w1:p1")?.id, "p1")
        XCTAssertNil(state.question(for: "w3:p1"))
        XCTAssertEqual(state.agents[0].title, "Fix the login test")
        XCTAssertEqual(state.agents[2].lastBlock?.answer, "Yes")
        XCTAssertEqual(state.agents[2].lastBlock?.via, "tap")

        let hello = #"{"name":"sheepdog","version":"1.0.0","auth":"none","ws":"/ws","audio":{"in_hz":16000,"out_hz":24000,"format":"pcm16le mono"},"computer":{"id":"AAAAAAAAAAAAAAAAAAAAAA","name":"Studio Mac"}}"#
        let h = try DaemonClient.decoder.decode(Hello.self, from: Data(hello.utf8))
        XCTAssertEqual(h.computer, Hello.Computer(id: "AAAAAAAAAAAAAAAAAAAAAA", name: "Studio Mac"))
    }

    func testDecodesSpacesIntoTheNewAgentChoices() throws {
        let json = """
        {"spaces":[{"id":"w3","name":"bench","path":"/code/bench","git":true,"repo":"bench","branch":"main","worktree":false,
        "agents":[{"pane_id":"w3:p1","alias":"bench-claude","name":"Claude","kind":"claude","state":"idle"}],
        "shells":["w3:p2"],"commands":[{"id":"a1b2c3d4","name":"Run tests","command":"go test ./..."}]}],
        "folders":[{"name":"app","path":"/code/app","git":false}],"kinds":[{"kind":"claude","name":"Claude"},{"kind":"codex","name":"Codex"},{"kind":"agy","name":"Antigravity"}],"worktrees":true}
        """
        let view = try DaemonClient.decoder.decode(SpacesView.self, from: Data(json.utf8))
        XCTAssertEqual(view.spaces[0].agents[0].paneID, "w3:p1")
        XCTAssertEqual(view.spaces[0].commands[0].command, "go test ./...")
        let options = NewAgentOptions(spaces: view)
        XCTAssertEqual(options.kinds, ["claude", "codex", "agy"])
        XCTAssertEqual(options.projects.map(\.name), ["bench", "app"])
        XCTAssertEqual(options.projects[0].space, "w3")
        XCTAssertNil(options.projects[1].space)
        XCTAssertTrue(options.worktree)
        XCTAssertEqual(options.kindName("agy"), "Antigravity")
    }

    func testDecodesConversationStartedAtWhenTheDaemonReportsIt() throws {
        let json = #"{"agents":[],"voice":{"mode":"off","status":"closed","muted":false,"stalled":false,"client_connected":false},"conversation_started_at":"2026-09-23T01:40:29.425157Z"}"#
        let state = try DaemonClient.decoder.decode(DaemonState.self, from: Data(json.utf8))
        XCTAssertNotNil(state.conversationStartedAt)
    }

    func testAnOlderStateWithoutConversationStartedAtDecodesAsNil() throws {
        let json = #"{"agents":[],"voice":{"mode":"off","status":"closed","muted":false,"stalled":false,"client_connected":false}}"#
        let state = try DaemonClient.decoder.decode(DaemonState.self, from: Data(json.utf8))
        XCTAssertNil(state.conversationStartedAt)
    }

    func testDecodesTheConversationResetReply() throws {
        let json = #"{"ok":true,"reset_id":"rs-abc123","at":"2026-09-23T01:40:29.425157Z"}"#
        let reset = try DaemonClient.decoder.decode(ConversationReset.self, from: Data(json.utf8))
        XCTAssertTrue(reset.ok)
        XCTAssertEqual(reset.resetID, "rs-abc123")
    }

    func testAnUnknownAgentStateDoesNotFailTheWholeResponse() throws {
        let json = #"{"agents":[{"pane_id":"a","alias":"a","state":"dreaming"}],"voice":{"mode":"gemini","status":"connected","muted":false,"stalled":false,"client_connected":false}}"#
        let state = try DaemonClient.decoder.decode(DaemonState.self, from: Data(json.utf8))
        XCTAssertEqual(state.agents[0].state, .unknown)
    }

    func testTheChipSubtitlePrefersThePendingQuestion() {
        let agent = Agent(
            paneID: "w1:p1", alias: "auth", state: .blocked,
            currentTask: "add the migration",
            lastBlock: .init(question: "run the migration?"),
            lastResult: .init(spoken: "wrote hello.txt")
        )
        XCTAssertEqual(agent.subtitle(pendingQuestion: "whether to run it"), "whether to run it")
        XCTAssertEqual(agent.subtitle(pendingQuestion: nil), "run the migration?")

        let working = Agent(paneID: "w1:p2", alias: "docs", state: .working, currentTask: "write the README", lastResult: .init(spoken: "done earlier"))
        XCTAssertEqual(working.subtitle(pendingQuestion: nil), "done earlier")

        let fresh = Agent(paneID: "w1:p3", alias: "new", state: .working, currentTask: "write the README")
        XCTAssertEqual(fresh.subtitle(pendingQuestion: nil), "write the README")
        XCTAssertNil(Agent(paneID: "w1:p4", alias: "quiet", state: .idle).subtitle(pendingQuestion: nil))
    }

    func testEveryErrorReadsLikeASentenceAndNamesTheDaemon() {
        XCTAssertEqual(
            DaemonError.unreachable(endpoint: "192.168.1.20:7777", detail: "").message,
            "Can't reach the daemon at 192.168.1.20:7777 — is it running?"
        )
        XCTAssertTrue(DaemonError.unauthorized(endpoint: "mac:7777").message.contains("Scan the pairing code"))
        XCTAssertEqual(DaemonError.pairing(.revoked).message, "Please scan again")
        XCTAssertEqual(DaemonError.pairing(.codeUsed).message, "This code has expired or was used")
        XCTAssertTrue(DaemonError.notSheepdog(endpoint: "mac:80").message.contains("isn't a sheepdog daemon"))
        for error: DaemonError in [
            .unreachable(endpoint: "a", detail: ""), .unauthorized(endpoint: "a"),
            .notSheepdog(endpoint: "a"), .http(status: 500, endpoint: "a"), .malformed("x"),
        ] {
            XCTAssertFalse(error.message.isEmpty)
            XCTAssertFalse(error.message.contains("Error Domain"), "no raw errors in front of the user")
        }
    }
}

final class SettingsStoreTests: XCTestCase {
    private func store() -> (SettingsStore, InMemorySecretStore) {
        let defaults = UserDefaults(suiteName: "sheepdog.tests.\(UUID().uuidString)")!
        let secrets = InMemorySecretStore()
        return (SettingsStore(defaults: defaults, credentials: DeviceCredentials(secrets: secrets)), secrets)
    }

    func testThePairedComputerLivesInTheSecretStoreAndNotInDefaults() {
        let (settings, secrets) = store()
        let computer = PairedComputer(daemonID: "d1", name: "Studio", addresses: ["192.168.1.20:7777"], pin: "pin123456", deviceID: "dev1", pairedAt: Date(timeIntervalSince1970: 1_800_000_000))
        settings.computer = computer
        XCTAssertEqual(secrets.values.count, 1)
        XCTAssertEqual(DeviceCredentials(secrets: secrets).computer, computer)
        XCTAssertEqual(computer.shortPin, "pin12345")
        settings.computer = nil
        XCTAssertTrue(secrets.values.isEmpty)
    }

    func testOnlyThePairedComputerIsTLS() {
        let (settings, _) = store()
        XCTAssertNil(settings.endpoint)
        settings.address = "127.0.0.1:7799"
        XCTAssertEqual(settings.endpoint?.display, "127.0.0.1:7799")
        XCTAssertEqual(settings.endpoint?.secure, false)
        settings.computer = PairedComputer(daemonID: "d1", name: "", addresses: ["127.0.0.1:7799"], pin: "p", deviceID: "x", pairedAt: Date())
        XCTAssertEqual(settings.endpoint?.secure, true)
        XCTAssertEqual(settings.endpoint?.url(path: "/api/hello")?.scheme, "https")
    }

    func testFirstLaunchIsUnconfiguredAndBackgroundListeningIsOnByDefault() {
        let (settings, _) = store()
        XCTAssertFalse(settings.hasBeenConfigured)
        XCTAssertTrue(settings.keepListeningWhenLocked)
        settings.hasBeenConfigured = true
        settings.keepListeningWhenLocked = false
        XCTAssertTrue(settings.hasBeenConfigured)
        XCTAssertFalse(settings.keepListeningWhenLocked)
    }
}
