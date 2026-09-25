import XCTest
@testable import SheepdogKit

/// The flock as the daemon really sends it (`state.Agent`), the brain's
/// replies and held pushes (`BrainActivity`), where they land in the
/// timeline, and the two new client calls.
final class FlockAndBrainTests: XCTestCase {
    private func event(_ id: UInt64, _ type: String, _ payload: [String: JSONValue], ts: Date? = nil) -> DaemonEvent {
        DaemonEvent(seq: id, ts: ts ?? Date(timeIntervalSince1970: TimeInterval(id)), source: "voice", type: type, payload: payload)
    }

    // MARK: - Agents, as /api/agents and /api/state send them

    /// Captured from a `--herdr=mock` daemon at herdr-sheepdog `0be70e6`
    /// (`/api/inject/blocked`, then `/api/inject/done`), plus a result and a
    /// journal entry in `state.Result` / `state.JournalEntry`'s shape.
    private let capturedAgents = """
    [{"pane_id":"p-cap","alias":"capture","kind":"codex","workspace":"synthetic","state":"done",
      "state_since":"2026-09-25T02:36:31.971905-07:00",
      "last_block":{"question":"allow command","seen_at":"2026-09-25T02:36:31.96228-07:00","answered":true},
      "last_result":{"spoken":"capture finished the build fix.","at":"2026-09-25T02:36:32.1-07:00","detail":"It removed the stale build folder and reran the tests."},
      "journal":[
        {"at":"2026-09-25T02:30:00Z","event":"blocked","instruction":"fix the build"},
        {"at":"2026-09-25T02:36:32Z","event":"done","last_assistant":"Removed build/ and reran the suite: 42 passed.","instruction":"fix the build","files_touched":["Makefile"],"tools":{"Bash":3}}
      ],
      "last_interaction":"2026-09-25T02:36:31.962502-07:00","synthetic":true},
     {"pane_id":"w1:p1","alias":"auth","kind":"claude","state":"blocked","state_since":"2026-09-25T02:33:06.328452-07:00",
      "last_block":{"question":"would you like to run the following command","seen_at":"2026-09-25T02:33:06.328452-07:00","answered":false}}]
    """

    func testDecodesTheDaemonsRealAgentShape() throws {
        let agents = try DaemonClient.decoder.decode([Agent].self, from: Data(capturedAgents.utf8))
        XCTAssertEqual(agents.count, 2)
        let capture = agents[0]
        XCTAssertEqual(capture.lastBlock?.question, "allow command")
        XCTAssertEqual(capture.lastBlock?.answered, true)
        XCTAssertNotNil(capture.lastBlock?.seenAt)
        XCTAssertEqual(capture.lastResult?.spoken, "capture finished the build fix.", "last_result.spoken, not the summary the app used to read")
        XCTAssertEqual(capture.lastResult?.detail, "It removed the stale build folder and reran the tests.")
        XCTAssertEqual(capture.journal?.count, 2)
        XCTAssertEqual(capture.lastWorkerMessage?.lastAssistant, "Removed build/ and reran the suite: 42 passed.")
        XCTAssertEqual(capture.lastWorkerMessage?.filesTouched, ["Makefile"])
        XCTAssertEqual(capture.lastWorkerMessage?.tools?["Bash"], 3)
        XCTAssertEqual(capture.synthetic, true)
        XCTAssertNil(agents[1].lastWorkerMessage, "no journal: no message, and nothing made up")
    }

    func testTheSubtitleSkipsAnAnsweredBlockAndReadsTheSpokenResult() throws {
        let agents = try DaemonClient.decoder.decode([Agent].self, from: Data(capturedAgents.utf8))
        XCTAssertEqual(agents[0].subtitle(pendingQuestion: nil), "capture finished the build fix.")
        XCTAssertEqual(agents[1].subtitle(pendingQuestion: nil), "would you like to run the following command")
        var answered = agents[1]
        answered.lastBlock?.answered = true
        answered.currentTask = "run the migration"
        XCTAssertEqual(answered.subtitle(pendingQuestion: nil), "run the migration", "an answered block is not what it waits on")
    }

    func testHowLongInAState() {
        let since = Date(timeIntervalSince1970: 1_000)
        let agent = Agent(paneID: "p", alias: "a", state: .working, stateSince: since)
        XCTAssertEqual(agent.timeInState(at: since.addingTimeInterval(20)), "just now")
        XCTAssertEqual(agent.timeInState(at: since.addingTimeInterval(4 * 60 + 59)), "4 min")
        XCTAssertEqual(agent.timeInState(at: since.addingTimeInterval(2 * 3600 + 5 * 60)), "2 h 5 min")
        XCTAssertEqual(agent.timeInState(at: since.addingTimeInterval(3 * 3600)), "3 h")
        XCTAssertEqual(agent.timeInState(at: since.addingTimeInterval(3 * 86_400)), "3 days")
        XCTAssertEqual(agent.timeInState(at: since.addingTimeInterval(-5)), "just now", "a clock a little ahead is not negative")
        XCTAssertNil(Agent(paneID: "p", alias: "a", state: .idle).timeInState(at: since))
    }

    // MARK: - Brain replies

    func testAVoiceReplyKeepsItsDetailAndOptions() {
        var activity = BrainActivity()
        XCTAssertTrue(activity.fold(event(10, "voice.tool_result", [
            "id": .string("fc-1"), "name": .string("to_supervisor"), "spoken": .string("auth wants to run the migration. Should it?"),
            "detail": .string("The migration adds the users table."), "options": .array([.string("Yes"), .string("No")]),
            "clarify": .bool(false), "scheduling": .string("interrupt"),
        ])))
        let reply = activity.replies[0]
        XCTAssertEqual(reply.spoken, "auth wants to run the migration. Should it?", "the ask is the last sentence of spoken")
        XCTAssertEqual(reply.detail, "The migration adds the users table.")
        XCTAssertEqual(reply.options, ["Yes", "No"])
        XCTAssertFalse(reply.isAlsoADogItem)
        XCTAssertTrue(reply.raw.contains(#""name" : "to_supervisor""#))
        XCTAssertFalse(activity.fold(event(10, "voice.tool_result", [
            "name": .string("to_supervisor"), "spoken": .string("auth wants to run the migration. Should it?"),
            "detail": .string("The migration adds the users table."), "options": .array([.string("Yes"), .string("No")]),
            "clarify": .bool(false), "scheduling": .string("interrupt"), "id": .string("fc-1"),
        ])), "a replay changes nothing")
    }

    func testOnlyToSupervisorResultsThatSaySomethingOrArePending() {
        var activity = BrainActivity()
        XCTAssertFalse(activity.fold(event(1, "voice.tool_result", ["name": .string("stop"), "spoken": .string("Stopped.")])))
        XCTAssertFalse(activity.fold(event(2, "voice.tool_result", ["name": .string("to_supervisor"), "spoken": .string("x"), "cancelled": .bool(true)])))
        XCTAssertFalse(activity.fold(event(3, "voice.tool_result", ["name": .string("to_supervisor"), "spoken": .string("")])))
        XCTAssertTrue(activity.fold(event(4, "voice.tool_result", ["name": .string("to_supervisor"), "spoken": .string(""), "pending": .bool(true), "detail": .string("The answer follows.")])))
        XCTAssertEqual(activity.replies.map(\.id), [4])
        XCTAssertTrue(activity.replies[0].pending)
    }

    func testATextRouterReplyIsAlsoTheDogsBubble() {
        var activity = BrainActivity()
        activity.fold(event(5, "voice.tool_result", ["name": .string("to_supervisor"), "spoken": .string("Two agents."), "via": .string("text_router")]))
        XCTAssertTrue(activity.replies[0].isAlsoADogItem)
        XCTAssertFalse(activity.replies[0].hasMore)
    }

    // MARK: - Held pushes

    func testAHeldPushIsReleasedOrDropped() {
        var activity = BrainActivity()
        let held: [String: JSONValue] = ["text": .string("billing finished."), "urgency": .string("idle"), "push_id": .string("push-7"), "reason": .string("no_listener")]
        XCTAssertTrue(activity.fold(event(20, "voice.push_held", held)))
        XCTAssertFalse(activity.fold(event(20, "voice.push_held", held)), "held once")
        XCTAssertEqual(activity.held.map(\.fate), [.held])
        XCTAssertTrue(activity.held[0].isShown)

        XCTAssertTrue(activity.fold(event(21, "voice.push_released", ["text": .string("billing finished."), "push_id": .string("push-7"), "held_ms": .number(42_000)])))
        XCTAssertEqual(activity.held[0].fate, .released(heldMilliseconds: 42_000))
        XCTAssertFalse(activity.held[0].isShown, "the voice.push that follows is the normal card")

        activity.fold(event(30, "voice.push_held", ["text": .string("docs is blocked."), "urgency": .string("interrupt")]))
        XCTAssertEqual(activity.held[1].key, "text:docs is blocked.", "an older daemon without push_id is keyed by its words")
        activity.fold(event(31, "voice.push_dropped", ["text": .string("docs is blocked."), "age_ms": .number(300_000), "reason": .string("no_listener")]))
        XCTAssertEqual(activity.held[1].fate, .dropped(ageMilliseconds: 300_000))
        XCTAssertTrue(activity.held[1].isShown)
    }

    func testSilentPushesAreNeverHeldAndUnknownReleasesAreIgnored() {
        var activity = BrainActivity()
        XCTAssertFalse(activity.fold(event(1, "voice.push_held", ["text": .string("ctx"), "urgency": .string("silent")])))
        XCTAssertFalse(activity.fold(event(2, "voice.push_released", ["text": .string("never held"), "push_id": .string("p-9")])))
        XCTAssertTrue(activity.isEmpty)
    }

    // MARK: - The timeline

    private func ids(_ rows: [TimelineRow]) -> [String] {
        rows.filter { if case .day = $0 { return false } else { return true } }.map(\.id)
    }

    private func item(_ id: Int64, _ kind: ConversationItem.Kind, _ text: String) -> ConversationItem {
        ConversationItem(id: id, ts: Date(timeIntervalSince1970: TimeInterval(id)), kind: kind, text: text)
    }

    func testAVoiceReplyIsACardInTimeOrder() {
        var activity = BrainActivity()
        activity.fold(event(15, "voice.tool_result", ["name": .string("to_supervisor"), "spoken": .string("All quiet."), "detail": .string("Nothing moved.")]))
        let rows = ConversationTimeline.rows(history: [item(10, .shepherd, "status?"), item(20, .dog, "All quiet.")], live: [], activity: activity)
        XCTAssertEqual(ids(rows), ["item.10", "brain.15", "item.20"])
        if case let .brain(reply, compact)? = rows.first(where: { $0.id == "brain.15" }) {
            XCTAssertFalse(compact)
            XCTAssertEqual(reply.detail, "Nothing moved.")
        } else {
            XCTFail("expected the brain card")
        }
    }

    func testATextRouterReplyWithDetailSitsUnderItsDogBubble() {
        var activity = BrainActivity()
        activity.fold(event(20, "voice.tool_result", ["name": .string("to_supervisor"), "spoken": .string("Two agents."), "via": .string("text_router"), "options": .array([.string("auth"), .string("docs")])]))
        activity.fold(event(40, "voice.tool_result", ["name": .string("to_supervisor"), "spoken": .string("Done."), "via": .string("text_router")]))
        let rows = ConversationTimeline.rows(history: [item(10, .shepherd, "who?"), item(20, .dog, "Two agents."), item(30, .shepherd, "ok"), item(40, .dog, "Done.")], live: [], activity: activity)
        XCTAssertEqual(ids(rows), ["item.10", "item.20", "brain.20", "item.30", "item.40"], "no footer for a reply with nothing more to say")
        if case let .brain(_, compact)? = rows.first(where: { $0.id == "brain.20" }) { XCTAssertTrue(compact) } else { XCTFail("expected the footer") }
    }

    func testHeldPushesShowUntilReleased() {
        var activity = BrainActivity()
        activity.fold(event(15, "voice.push_held", ["text": .string("billing finished."), "urgency": .string("idle"), "push_id": .string("push-1")]))
        var rows = ConversationTimeline.rows(history: [item(10, .dog, "hi")], live: [], activity: activity)
        XCTAssertEqual(ids(rows), ["item.10", "held.15"])
        activity.fold(event(25, "voice.push_released", ["text": .string("billing finished."), "push_id": .string("push-1"), "held_ms": .number(9_000)]))
        rows = ConversationTimeline.rows(history: [item(10, .dog, "hi"), item(26, .push, "billing finished.")], live: [], activity: activity)
        XCTAssertEqual(ids(rows), ["item.10", "item.26"], "the pending card turned into the ordinary push card")
    }

    // MARK: - The client

    func testRecentEventsReadsTheTailWithTheirIDs() async throws {
        StubProtocol.handler = { request in
            XCTAssertEqual(request.url?.path, "/api/events")
            XCTAssertTrue(request.url?.query?.contains("n=50") ?? false)
            XCTAssertTrue(request.url?.query?.contains("type=voice.push") ?? false)
            let body = #"[{"id":1790328992048351,"ts":"2026-09-25T09:36:32.048351Z","source":"web","type":"voice.push_held","payload":{"text":"hi","urgency":"idle","push_id":"push-1"}}]"#
            return (200, Data(body.utf8))
        }
        let events = try await StubProtocol.client().recentEvents(type: "voice.push", limit: 50)
        XCTAssertEqual(events.count, 1)
        XCTAssertEqual(events[0].seq, 1_790_328_992_048_351)
        XCTAssertEqual(events[0].type, "voice.push_held")
        XCTAssertEqual(events[0].string("push_id"), "push-1")
        XCTAssertNotNil(events[0].ts)
    }

    func testSendUtterancePostsTheTextAndReadsEitherReply() async throws {
        StubProtocol.handler = { request in
            XCTAssertEqual(request.httpMethod, "POST")
            XCTAssertEqual(request.url?.path, "/api/inject/utterance")
            let body = try? JSONSerialization.jsonObject(with: request.httpBody ?? Data()) as? [String: String]
            XCTAssertEqual(body?["text"], "what is auth doing")
            return (200, Data(#"{"ok":true,"via":"voice","text":"what is auth doing"}"#.utf8))
        }
        let voice = try await StubProtocol.client().sendUtterance("what is auth doing")
        XCTAssertEqual(voice.via, "voice")
        XCTAssertNil(voice.spoken)

        StubProtocol.handler = { _ in
            (200, Data(#"{"tool":"to_supervisor","args":{"text":"x"},"spoken":"The screen ends with: done.","clarify":false,"scheduling":"idle","via":"text_router"}"#.utf8))
        }
        let routed = try await StubProtocol.client().sendUtterance("x")
        XCTAssertEqual(routed.via, "text_router")
        XCTAssertEqual(routed.spoken, "The screen ends with: done.")
    }

    func testSendUtteranceCarriesTheDaemonsRefusal() async {
        StubProtocol.handler = { _ in (422, Data(#"{"error":"text router (debug path, --voice=off): nothing to relay"}"#.utf8)) }
        do {
            _ = try await StubProtocol.client().sendUtterance("   ")
            XCTFail("expected a refusal")
        } catch let error as DaemonError {
            XCTAssertEqual(error, .rejected(status: 422, message: "text router (debug path, --voice=off): nothing to relay"))
        } catch {
            XCTFail("\(error)")
        }
    }
}

/// Settings ▸ About's echo-cancellation line.
final class VoiceProcessingStatusTests: XCTestCase {
    func testActiveOnlyWithEverythingTheCancellerNeeds() {
        let on = VoiceProcessingStatus(inputEnabled: true, outputEnabled: true, bypassed: false, voiceChatMode: true, outputRoute: "Speaker", inputRoute: "iPhone Microphone")
        XCTAssertTrue(on.isActive)
        XCTAssertEqual(on.summary, "On · Speaker ← iPhone Microphone")

        var off = on
        off.inputEnabled = false
        off.outputEnabled = false
        XCTAssertFalse(off.isActive)
        XCTAssertEqual(off.summary, "Off — voice processing is off · Speaker ← iPhone Microphone")

        var bypassed = on
        bypassed.bypassed = true
        XCTAssertEqual(bypassed.summary, "Off — bypassed · Speaker ← iPhone Microphone")

        var wrongMode = on
        wrongMode.voiceChatMode = false
        wrongMode.outputRoute = ""
        wrongMode.inputRoute = ""
        XCTAssertEqual(wrongMode.summary, "Off — session not in voice-chat mode")

        var halfway = on
        halfway.outputEnabled = false
        XCTAssertEqual(halfway.summary, "Off — the output is not voice-processed · Speaker ← iPhone Microphone")
    }
}
