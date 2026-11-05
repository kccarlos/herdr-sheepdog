import XCTest
@testable import SheepdogKit

/// Unread, pinned and muted: the daemon's attention fields, the list order
/// (against the fixture the daemon's Go tests and the panel share), the
/// requests, the mute times and the optimistic overlay.
final class AttentionFixtureTests: XCTestCase {
    private final class BundleToken {}

    struct Marks: Decodable {
        var turn: Int?
        var seen: Int?
        var word: String
        var pinned: Bool?
        var error: Bool?
    }

    struct ClassifyCase: Decodable {
        var name: String
        var state: String
        var marks: Marks
        var group: String
        var unread: Bool
        var pinned: Bool
    }

    struct OrderAgent: Decodable {
        var name: String
        var state: String
        var marks: Marks
    }

    struct OrderCase: Decodable {
        var name: String
        var agents: [OrderAgent]
        var expected: [String]
    }

    struct Fixture: Decodable {
        var classify: [ClassifyCase]
        var orders: [OrderCase]
    }

    private func load() throws -> Fixture {
        let url = try XCTUnwrap(
            Bundle(for: BundleToken.self).url(forResource: "agent-attention", withExtension: "json"),
            "missing fixture agent-attention.json in the test bundle"
        )
        return try JSONDecoder().decode(Fixture.self, from: Data(contentsOf: url))
    }

    static func classify(state: String, marks m: Marks) -> AgentAttention {
        AgentAttention.classify(state: Agent.State(rawValue: state) ?? .unknown, turn: m.turn ?? 0, seen: m.seen, word: m.word,
                                error: m.error ?? false, pinned: m.pinned ?? false)
    }

    private func agent(_ name: String, state: String, marks: Marks) -> Agent {
        Agent(paneID: "p-\(name)", alias: name, state: Agent.State(rawValue: state) ?? .unknown,
              attention: AttentionFixtureTests.classify(state: state, marks: marks))
    }

    func testTheClassifierMatchesTheFixture() throws {
        for c in try load().classify {
            let a = Self.classify(state: c.state, marks: c.marks)
            XCTAssertEqual(a.group.rawValue, c.group, c.name)
            XCTAssertEqual(a.unread, c.unread, c.name)
            XCTAssertEqual(a.pinned, c.pinned, c.name)
        }
    }

    func testEveryOrderCaseSortsAsTheFixtureSays() throws {
        let cases = try load().orders
        XCTAssertGreaterThanOrEqual(cases.count, 5)
        for c in cases {
            let agents = c.agents.map { agent($0.name, state: $0.state, marks: $0.marks) }
            XCTAssertEqual(AgentOrder.sorted(agents).map(\.alias), c.expected, c.name)
            XCTAssertEqual(AgentOrder.sorted(agents.reversed()).map(\.alias), c.expected, "\(c.name), reversed input")
            XCTAssertEqual(Flock(agents).all.map(\.alias), c.expected, "\(c.name), through Flock")
        }
    }

    func testTheSectionsHoldTheRunningAgentsUnderTheirHeaders() throws {
        let first = try XCTUnwrap(load().orders.first)
        let flock = Flock(first.agents.map { agent($0.name, state: $0.state, marks: $0.marks) })
        XCTAssertEqual(flock.sections.map(\.group), [.needsYou, .working, .done])
        XCTAssertEqual(flock.sections.map(\.title), ["Needs you", "Working", "Done"])
        XCTAssertEqual(flock.sections.map { $0.agents.map(\.alias) }, [["charlie"], ["bravo"], ["alpha", "delta", "foxtrot"]])
        XCTAssertEqual(flock.notRunning.map(\.alias), ["echo"])
    }

    func testAnEmptySectionIsLeftOut() {
        let flock = Flock([Agent(paneID: "p1", alias: "a", state: .working, attention: AgentAttention(group: .working))])
        XCTAssertEqual(flock.sections.map(\.group), [.working])
        XCTAssertTrue(Flock([]).sections.isEmpty)
    }
}

final class AttentionDecodeTests: XCTestCase {
    /// The literal JSON of docs/dev/ws-contract.md "Attention".
    private let newDaemon = #"""
    {"agents":[{"pane_id": "w5:p1", "alias": "auth", "state": "done",
     "attention": {"group": "done", "unread": true, "pinned": false,
                   "turn": 7, "seen": 6, "muted": false, "shared": true}}],
     "voice":{"mode":"off","status":"off","muted":false,"stalled":false,"client_connected":false},
     "attention_counts": {"needs_you": 1, "working": 2, "done_unread": 1},
     "space_mutes": [{"path": "/Users/u/code/auth", "name": "auth", "until": "2026-11-03T11:00:00Z"},
                     {"path": "/Users/u/code/docs", "name": "docs"}]}
    """#

    private let oldDaemon = #"""
    {"agents":[{"pane_id": "w5:p1", "alias": "auth", "state": "done"}],
     "voice":{"mode":"off","status":"off","muted":false,"stalled":false,"client_connected":false}}
    """#

    func testADaemonWithTheFeatureDecodes() throws {
        let state = try DaemonClient.decoder.decode(DaemonState.self, from: Data(newDaemon.utf8))
        let attention = try XCTUnwrap(state.agents.first?.attention)
        XCTAssertEqual(attention, AgentAttention(group: .done, unread: true, pinned: false, turn: 7, seen: 6, muted: false, shared: true))
        XCTAssertEqual(state.attentionCounts, AttentionCounts(needsYou: 1, working: 2, doneUnread: 1))
        XCTAssertEqual(state.spaceMutes?.map(\.path), ["/Users/u/code/auth", "/Users/u/code/docs"])
        XCTAssertEqual(state.spaceMutes?.first?.until, ISO8601DateFormatter().date(from: "2026-11-03T11:00:00Z"))
        XCTAssertNil(state.spaceMutes?.last?.until, "no end time is until the user unmutes")
        XCTAssertTrue(state.agents[0].isUnread)
        XCTAssertTrue(state.agents[0].hasAttention)
    }

    /// Captured from a private daemon on a real herdr-core (scripts/e2e,
    /// scenarios agent-unread and space-mute): `until` is a JSON null.
    func testAPayloadCapturedFromARealDaemonDecodes() throws {
        let state = #"""
        {"agents":[{"pane_id":"e2e:muted-approval","alias":"mutedauth","state":"blocked","attention":{"group":"needs_you","unread":false,"pinned":false,"turn":0,"muted":true,"shared":false}},
                   {"pane_id":"e2e:work","alias":"build","state":"done","attention":{"group":"done","unread":true,"pinned":false,"turn":0,"muted":false,"shared":false}}],
         "voice":{"mode":"off","status":"off","muted":false,"stalled":false,"client_connected":false},
         "attention_counts":{"needs_you":2,"working":0,"done_unread":1},
         "space_mutes":[{"path":"/private/tmp/home/muted","name":"muted"}]}
        """#
        let decoded = try DaemonClient.decoder.decode(DaemonState.self, from: Data(state.utf8))
        XCTAssertTrue(decoded.agents[0].isMuted)
        XCTAssertFalse(decoded.agents[0].attention?.shared ?? true)
        XCTAssertTrue(decoded.agents[1].isUnread)
        XCTAssertEqual(decoded.attentionCounts?.doneUnread, 1)
        let spaces = #"{"spaces":[{"id":"sM","name":"muted","path":"/p/muted","git":false,"worktree":false,"agents":[],"shells":[],"commands":[],"muted":true,"until":null}],"folders":[],"kinds":[],"worktrees":false}"#
        let view = try DaemonClient.decoder.decode(SpacesView.self, from: Data(spaces.utf8))
        XCTAssertTrue(view.spaces[0].isMuted)
        XCTAssertNil(view.spaces[0].until)
    }

    func testAnOldDaemonDecodesAndGroupsByState() throws {
        let state = try DaemonClient.decoder.decode(DaemonState.self, from: Data(oldDaemon.utf8))
        let agent = try XCTUnwrap(state.agents.first)
        XCTAssertNil(agent.attention)
        XCTAssertNil(state.attentionCounts)
        XCTAssertNil(state.spaceMutes)
        XCTAssertFalse(agent.hasAttention, "no pin or unread controls without the feature")
        XCTAssertFalse(agent.isUnread)
        XCTAssertFalse(agent.isPinned)
        XCTAssertEqual(agent.group, .done)
    }

    func testAnUnknownGroupFromANewerDaemonReadsAsNotRunning() throws {
        let json = #"{"group":"snoozed","unread":true}"#
        let a = try JSONDecoder().decode(AgentAttention.self, from: Data(json.utf8))
        XCTAssertEqual(a.group, .notRunning)
        XCTAssertTrue(a.shared, "shared defaults to true")
        XCTAssertFalse(a.muted)
    }

    func testTheOldDaemonFallbackGroupsByStateAndNamesWithoutRegardToCase() {
        func agent(_ name: String, _ state: Agent.State) -> Agent { Agent(paneID: "p-\(name)", alias: name, state: state) }
        let flock = Flock([agent("Zed", .done), agent("beta", .idle), agent("Alpha", .idle), agent("w", .working),
                           agent("b", .blocked), agent("x", .exited), agent("y", .unknown)])
        XCTAssertEqual(flock.running.map(\.alias), ["b", "w", "Alpha", "beta", "Zed"])
        XCTAssertEqual(flock.sections.map(\.group), [.needsYou, .working, .done])
        XCTAssertEqual(flock.notRunning.map(\.alias), ["x", "y"])
        XCTAssertEqual(flock.waitingCount, 1)
    }

    func testTheBadgeCountsBlockedAgentsNotUnreadOnes() {
        let unread = Agent(paneID: "p1", alias: "a", state: .done, attention: AgentAttention(group: .done, unread: true, turn: 2, seen: 1))
        let blocked = Agent(paneID: "p2", alias: "b", state: .blocked, attention: AgentAttention(group: .needsYou))
        XCTAssertEqual(Flock([unread, blocked]).waitingCount, 1)
        XCTAssertEqual(Flock([unread]).waitingCount, 0)
    }

    func testSpacesDecodeWithAndWithoutMute() throws {
        let base = #"{"id":"w1","name":"auth","path":"/code/auth","git":true,"worktree":false,"agents":[],"shells":[],"commands":[]"#
        let old = try JSONDecoder().decode(SpacesView.Space.self, from: Data((base + "}").utf8))
        XCTAssertFalse(old.supportsMute)
        XCTAssertFalse(old.isMuted)
        let muted = try DaemonClient.decoder.decode(SpacesView.Space.self, from: Data((base + #","muted":true,"until":"2026-11-03T11:00:00Z"}"#).utf8))
        XCTAssertTrue(muted.supportsMute)
        XCTAssertTrue(muted.isMuted)
        XCTAssertNotNil(muted.until)
        let off = try DaemonClient.decoder.decode(SpacesView.Space.self, from: Data((base + #","muted":false}"#).utf8))
        XCTAssertTrue(off.supportsMute)
        XCTAssertFalse(off.isMuted)
    }
}

final class MuteTimeTests: XCTestCase {
    private func calendar(_ zone: String) -> Calendar {
        var c = Calendar(identifier: .gregorian)
        c.timeZone = TimeZone(identifier: zone)!
        return c
    }

    private func date(_ text: String) -> Date { ISO8601DateFormatter().date(from: text)! }

    private func morning(_ choice: MuteChoice, now: String, zone: String = "UTC") -> String {
        let until = choice.until(from: date(now), calendar: calendar(zone))
        return until.map(MuteChoice.rfc3339) ?? "nil"
    }

    func testAnHourIsAnHour() {
        XCTAssertEqual(morning(.hour, now: "2026-11-03T10:00:00Z"), "2026-11-03T11:00:00Z")
        XCTAssertEqual(MuteChoice.untilOff.until(from: date("2026-11-03T10:00:00Z")), nil)
    }

    func testTomorrowMorningIsTheNextEightStrictlyAfterNow() {
        // Asked at 07:00 it ends in an hour, at 08:00 sharp the next day, at 09:00 the next day.
        XCTAssertEqual(morning(.tomorrowMorning, now: "2026-11-03T07:00:00Z"), "2026-11-03T08:00:00Z")
        XCTAssertEqual(morning(.tomorrowMorning, now: "2026-11-03T08:00:00Z"), "2026-11-04T08:00:00Z")
        XCTAssertEqual(morning(.tomorrowMorning, now: "2026-11-03T09:00:00Z"), "2026-11-04T08:00:00Z")
        XCTAssertEqual(morning(.tomorrowMorning, now: "2026-11-03T23:59:59Z"), "2026-11-04T08:00:00Z")
        XCTAssertEqual(morning(.tomorrowMorning, now: "2026-11-04T00:00:00Z"), "2026-11-04T08:00:00Z")
        XCTAssertEqual(morning(.tomorrowMorning, now: "2026-12-31T20:00:00Z"), "2027-01-01T08:00:00Z", "across the year")
    }

    func testTheMorningIsInTheUsersZone() {
        // 08:00 in Los Angeles in November is 16:00 UTC.
        XCTAssertEqual(morning(.tomorrowMorning, now: "2026-11-03T10:00:00Z", zone: "America/Los_Angeles"), "2026-11-03T16:00:00Z")
        XCTAssertEqual(morning(.tomorrowMorning, now: "2026-11-03T17:00:00Z", zone: "America/Los_Angeles"), "2026-11-04T16:00:00Z")
        // Kolkata is +05:30: 08:00 local is 02:30 UTC.
        XCTAssertEqual(morning(.tomorrowMorning, now: "2026-11-03T01:00:00Z", zone: "Asia/Kolkata"), "2026-11-03T02:30:00Z")
        XCTAssertEqual(morning(.tomorrowMorning, now: "2026-11-03T03:00:00Z", zone: "Asia/Kolkata"), "2026-11-04T02:30:00Z")
        // Auckland is ahead of the date line: it is already the 4th there.
        XCTAssertEqual(morning(.tomorrowMorning, now: "2026-11-03T12:00:00Z", zone: "Pacific/Auckland"), "2026-11-03T19:00:00Z")
    }

    func testSpringForwardNightIsShorterThan24Hours() {
        // Los Angeles, 2026-03-08: 02:00 PST jumps to 03:00 PDT.
        // 23:00 PST on the 7th (07:00Z on the 8th): 08:00 PDT is 15:00Z, 8 hours later, not 9.
        XCTAssertEqual(morning(.tomorrowMorning, now: "2026-03-08T07:00:00Z", zone: "America/Los_Angeles"), "2026-03-08T15:00:00Z")
        // 09:00 PST on the 7th: the next 08:00 is on the 8th, PDT, 22 hours later.
        XCTAssertEqual(morning(.tomorrowMorning, now: "2026-03-07T17:00:00Z", zone: "America/Los_Angeles"), "2026-03-08T15:00:00Z")
        // 09:00 PDT on the 8th: the 9th at 08:00 PDT.
        XCTAssertEqual(morning(.tomorrowMorning, now: "2026-03-08T16:00:00Z", zone: "America/Los_Angeles"), "2026-03-09T15:00:00Z")
    }

    func testFallBackNightIsLongerThan24Hours() {
        // Los Angeles, 2026-11-01: 02:00 PDT falls back to 01:00 PST.
        // 09:00 PDT on 31 Oct (16:00Z): the next 08:00 is on 1 Nov, PST, 25 hours later.
        XCTAssertEqual(morning(.tomorrowMorning, now: "2026-10-31T16:00:00Z", zone: "America/Los_Angeles"), "2026-11-01T16:00:00Z")
        // 00:30 PDT on 1 Nov (07:30Z): 08:00 PST is 16:00Z.
        XCTAssertEqual(morning(.tomorrowMorning, now: "2026-11-01T07:30:00Z", zone: "America/Los_Angeles"), "2026-11-01T16:00:00Z")
        // 09:00 PST on 1 Nov (17:00Z): the 2nd at 08:00 PST.
        XCTAssertEqual(morning(.tomorrowMorning, now: "2026-11-01T17:00:00Z", zone: "America/Los_Angeles"), "2026-11-02T16:00:00Z")
    }

    func testTheInstantIsRFC3339WithWholeSeconds() {
        XCTAssertEqual(MuteChoice.rfc3339(Date(timeIntervalSince1970: 1_793_000_000.7)), "2026-10-26T07:33:20Z")
    }

    func testTheStatusLine() {
        let cal = calendar("UTC")
        let now = date("2026-11-03T06:00:00Z")
        let locale = Locale(identifier: "en_US")
        XCTAssertEqual(MuteChoice.status(until: nil, now: now, calendar: cal, locale: locale), "Muted until you turn it back on")
        XCTAssertTrue(MuteChoice.status(until: date("2026-11-03T08:00:00Z"), now: now, calendar: cal, locale: locale).hasPrefix("Muted until 8"))
        XCTAssertTrue(MuteChoice.status(until: date("2026-11-04T08:00:00Z"), now: now, calendar: cal, locale: locale).contains("Wed"))
    }
}

final class AttentionRequestTests: XCTestCase {
    override func setUp() {
        StubProtocol.seen = []
        StubProtocol.handler = nil
    }

    private func body(_ request: URLRequest) throws -> [String: Any] {
        try XCTUnwrap(JSONSerialization.jsonObject(with: XCTUnwrap(request.httpBody)) as? [String: Any])
    }

    func testSeenWithATurn() async throws {
        StubProtocol.handler = { _ in (200, Data(#"{"ok":true,"attention":{"group":"done","unread":false,"pinned":false,"turn":7,"seen":7}}"#.utf8)) }
        let attention = try await StubProtocol.client().markSeen(paneID: "w5:p1", turn: 7)
        let request = try XCTUnwrap(StubProtocol.seen.last)
        XCTAssertEqual(request.httpMethod, "POST")
        XCTAssertEqual(request.url?.path, "/api/agents/w5:p1/seen")
        XCTAssertEqual(request.value(forHTTPHeaderField: "Content-Type"), "application/json")
        let sent = try body(request)
        XCTAssertEqual(sent["turn"] as? Int, 7)
        XCTAssertNil(sent["unread"], "exactly one of turn and unread")
        XCTAssertEqual(attention?.seen, 7)
        XCTAssertEqual(attention?.unread, false)
    }

    func testTurnZeroIsSentAsZero() async throws {
        StubProtocol.handler = { _ in (200, Data(#"{"ok":true}"#.utf8)) }
        let attention = try await StubProtocol.client().markSeen(paneID: "p1", turn: 0)
        XCTAssertEqual(try body(XCTUnwrap(StubProtocol.seen.last))["turn"] as? Int, 0)
        XCTAssertNil(attention, "an answer without marks is not an error")
    }

    func testMarkAsUnread() async throws {
        StubProtocol.handler = { _ in (200, Data(#"{"ok":true}"#.utf8)) }
        try await StubProtocol.client().markUnread(paneID: "w5:p1")
        let sent = try body(XCTUnwrap(StubProtocol.seen.last))
        XCTAssertEqual(sent["unread"] as? Bool, true)
        XCTAssertNil(sent["turn"])
    }

    func testPinAndUnpin() async throws {
        StubProtocol.handler = { _ in (200, Data(#"{"ok":true}"#.utf8)) }
        let client = StubProtocol.client()
        try await client.setPinned(paneID: "w5:p1", true)
        try await client.setPinned(paneID: "w5:p1", false)
        XCTAssertEqual(StubProtocol.seen.map(\.url?.path), ["/api/agents/w5:p1/pin", "/api/agents/w5:p1/pin"])
        XCTAssertEqual(try StubProtocol.seen.map { try body($0)["pinned"] as? Bool }, [true, false])
    }

    func testAPaneIdIsEscapedInThePath() async throws {
        StubProtocol.handler = { _ in (200, Data(#"{"ok":true}"#.utf8)) }
        try await StubProtocol.client().setPinned(paneID: "a/b%1", true)
        XCTAssertEqual(StubProtocol.seen.last?.url?.absoluteString.contains("/api/agents/a%2Fb%251/pin"), true)
    }

    func testMuteForAWhileSendsTheInstant() async throws {
        StubProtocol.handler = { _ in (200, Data(#"{"ok":true,"space":{"id":"w1","path":"/code/auth","muted":true}}"#.utf8)) }
        let until = ISO8601DateFormatter().date(from: "2026-11-03T11:00:00Z")!
        try await StubProtocol.client().setSpaceMute(spaceID: "w1", muted: true, until: until)
        let request = try XCTUnwrap(StubProtocol.seen.last)
        XCTAssertEqual(request.url?.path, "/api/spaces/w1/mute")
        let sent = try body(request)
        XCTAssertEqual(sent["muted"] as? Bool, true)
        XCTAssertEqual(sent["until"] as? String, "2026-11-03T11:00:00Z")
    }

    func testMuteUntilTurnedOffSendsNoInstantAndUnmuteSendsNone() async throws {
        StubProtocol.handler = { _ in (200, Data(#"{"ok":true}"#.utf8)) }
        let client = StubProtocol.client()
        try await client.setSpaceMute(spaceID: "w1", muted: true)
        XCTAssertNil(try body(XCTUnwrap(StubProtocol.seen.last))["until"])
        // `until` with `muted: false` is a 400 on the daemon, so it is never sent.
        try await client.setSpaceMute(spaceID: "w1", muted: false, until: Date())
        let sent = try body(XCTUnwrap(StubProtocol.seen.last))
        XCTAssertEqual(sent["muted"] as? Bool, false)
        XCTAssertNil(sent["until"])
    }

    func testAnOldOrFeaturelessDaemonIsNotAFailureToShow() async {
        for status in [404, 503] {
            StubProtocol.handler = { _ in (status, Data(#"{"error":"nope"}"#.utf8)) }
            do {
                try await StubProtocol.client().setPinned(paneID: "p1", true)
                XCTFail("expected a refusal")
            } catch let error as DaemonError {
                XCTAssertTrue(error.meansFeatureAbsent, "\(status)")
            } catch {
                XCTFail("\(error)")
            }
        }
        StubProtocol.handler = { _ in (502, Data(#"{"error":"herdr refused"}"#.utf8)) }
        do {
            try await StubProtocol.client().setPinned(paneID: "p1", true)
            XCTFail("expected a refusal")
        } catch let error as DaemonError {
            XCTAssertFalse(error.meansFeatureAbsent)
        } catch {
            XCTFail("\(error)")
        }
    }
}

final class AttentionOverlayTests: XCTestCase {
    private let t0 = Date(timeIntervalSince1970: 1_800_000_000)

    private func done(_ name: String, turn: Int = 3, seen: Int? = 2, unread: Bool = true, pinned: Bool = false, muted: Bool = false) -> Agent {
        Agent(paneID: "p-\(name)", alias: name, state: .done,
              attention: AgentAttention(group: .done, unread: unread, pinned: pinned, turn: turn, seen: seen, muted: muted))
    }

    func testAPinShowsAtOnceAndSettlesWhenTheStateAgrees() {
        var overlay = AttentionOverlay()
        overlay.setPinned("p-a", true, at: t0)
        let shown = overlay.applying(to: [done("a", unread: false)], now: t0.addingTimeInterval(1))
        XCTAssertTrue(shown[0].isPinned)
        overlay.settle(with: [done("a", pinned: true)], now: t0.addingTimeInterval(1))
        XCTAssertTrue(overlay.isEmpty)
    }

    func testAPinThatTheDaemonNeverShowsGivesUpAfterFiveSeconds() {
        var overlay = AttentionOverlay()
        overlay.setPinned("p-a", true, at: t0)
        XCTAssertTrue(overlay.applying(to: [done("a")], now: t0.addingTimeInterval(4.9))[0].isPinned)
        XCTAssertFalse(overlay.applying(to: [done("a")], now: t0.addingTimeInterval(5))[0].isPinned)
        overlay.settle(with: [done("a")], now: t0.addingTimeInterval(5))
        XCTAssertTrue(overlay.isEmpty)
    }

    func testAFailedWriteRollsTheRowBack() {
        var overlay = AttentionOverlay()
        overlay.setPinned("p-a", true, at: t0)
        overlay.rollback("p-a", .pinned)
        XCTAssertFalse(overlay.applying(to: [done("a")], now: t0)[0].isPinned)
        XCTAssertTrue(overlay.isEmpty)
    }

    func testSeenClearsTheDotUntilTheStateCatchesUp() {
        var overlay = AttentionOverlay()
        overlay.markSeen("p-a", turn: 3, at: t0)
        let shown = overlay.applying(to: [done("a")], now: t0)
        XCTAssertFalse(shown[0].isUnread)
        XCTAssertEqual(shown[0].attention?.seen, 3)
        // The daemon still says seen 2: keep the overlay, it is within 5 s.
        overlay.settle(with: [done("a")], now: t0.addingTimeInterval(2))
        XCTAssertFalse(overlay.isEmpty)
        overlay.settle(with: [done("a", seen: 3, unread: false)], now: t0.addingTimeInterval(2))
        XCTAssertTrue(overlay.isEmpty)
    }

    func testSeenDoesNotClearAnAgentThatFinishedAnotherTurn() {
        var overlay = AttentionOverlay()
        overlay.markSeen("p-a", turn: 3, at: t0)
        let shown = overlay.applying(to: [done("a", turn: 4, seen: 2)], now: t0)
        XCTAssertTrue(shown[0].isUnread, "turn 4 was never shown")
    }

    func testMarkAsUnreadShowsTheDotAndNeverAtTurnZero() {
        var overlay = AttentionOverlay()
        overlay.markUnread("p-a", at: t0)
        overlay.markUnread("p-b", at: t0)
        let shown = overlay.applying(to: [done("a", turn: 3, seen: 3, unread: false), done("b", turn: 0, seen: nil, unread: false)], now: t0)
        XCTAssertTrue(shown[0].isUnread)
        XCTAssertEqual(shown[0].attention?.seen, 2)
        XCTAssertFalse(shown[1].isUnread, "nothing has finished at turn 0")
    }

    func testMuteCoversEveryAgentOfTheSpaceAndSettles() {
        var overlay = AttentionOverlay()
        overlay.setMuted(["p-a", "p-b"], true, at: t0)
        let agents = [done("a"), done("b"), done("c")]
        XCTAssertEqual(overlay.applying(to: agents, now: t0).map(\.isMuted), [true, true, false])
        overlay.settle(with: [done("a", muted: true), done("b", muted: true), done("c")], now: t0)
        XCTAssertTrue(overlay.isEmpty)
    }

    func testAnOldDaemonsAgentsAreLeftAlone() {
        var overlay = AttentionOverlay()
        overlay.setPinned("p-a", true, at: t0)
        let old = Agent(paneID: "p-a", alias: "a", state: .done)
        XCTAssertEqual(overlay.applying(to: [old], now: t0), [old])
    }

    func testAnAgentThatLeftTakesItsEntryWithIt() {
        var overlay = AttentionOverlay()
        overlay.setPinned("p-gone", true, at: t0)
        overlay.settle(with: [done("a")], now: t0)
        XCTAssertTrue(overlay.isEmpty)
    }

    func testPinnedRowsMoveToTheTopOfTheirSectionThroughTheOverlay() {
        var overlay = AttentionOverlay()
        overlay.setPinned("p-z", true, at: t0)
        let agents = [done("a", unread: false), done("z", unread: false)]
        XCTAssertEqual(Flock(overlay.applying(to: agents, now: t0)).all.map(\.alias), ["z", "a"])
    }
}

final class SeenTrackerTests: XCTestCase {
    private func agent(turn: Int, seen: Int?, unread: Bool = true) -> Agent {
        Agent(paneID: "p1", alias: "a", state: .done, attention: AgentAttention(group: .done, unread: unread, turn: turn, seen: seen))
    }

    func testWritesOncePerTurnWhileVisible() {
        var tracker = SeenTracker()
        XCTAssertEqual(tracker.writes(visible: [agent(turn: 3, seen: 2)], appActive: true), [.init(paneID: "p1", turn: 3)])
        XCTAssertEqual(tracker.writes(visible: [agent(turn: 3, seen: 2)], appActive: true), [], "a turn already written")
        // The next turn finishes while the window stays open.
        XCTAssertEqual(tracker.writes(visible: [agent(turn: 4, seen: 3)], appActive: true), [.init(paneID: "p1", turn: 4)])
    }

    func testNothingIsWrittenFromTheBackground() {
        var tracker = SeenTracker()
        XCTAssertEqual(tracker.writes(visible: [agent(turn: 3, seen: 2)], appActive: false), [])
        // Back in front, it writes.
        XCTAssertEqual(tracker.writes(visible: [agent(turn: 3, seen: 2)], appActive: true).count, 1)
    }

    func testAnAgentAlreadySeenIsNotWritten() {
        var tracker = SeenTracker()
        XCTAssertEqual(tracker.writes(visible: [agent(turn: 3, seen: 3, unread: false)], appActive: true), [])
        XCTAssertEqual(tracker.writes(visible: [agent(turn: 3, seen: 5, unread: false)], appActive: true), [])
    }

    func testNoRecordTakesTheFirstLook() {
        var tracker = SeenTracker()
        XCTAssertEqual(tracker.writes(visible: [agent(turn: 0, seen: nil)], appActive: true), [.init(paneID: "p1", turn: 0)])
    }

    func testAFailedWriteIsRetriedOnTheNextLook() {
        var tracker = SeenTracker()
        let write = tracker.writes(visible: [agent(turn: 3, seen: 2)], appActive: true)[0]
        tracker.failed(write)
        XCTAssertEqual(tracker.writes(visible: [agent(turn: 3, seen: 2)], appActive: true), [write])
    }

    func testMarkingUnreadLetsTheNextLookWriteAgain() {
        var tracker = SeenTracker()
        _ = tracker.writes(visible: [agent(turn: 3, seen: 2)], appActive: true)
        tracker.forget("p1")
        XCTAssertEqual(tracker.writes(visible: [agent(turn: 3, seen: 2)], appActive: true).count, 1)
    }

    func testAnOldDaemonsAgentIsNeverWritten() {
        var tracker = SeenTracker()
        XCTAssertEqual(tracker.writes(visible: [Agent(paneID: "p1", alias: "a", state: .done)], appActive: true), [])
    }
}

final class AttentionEventTests: XCTestCase {
    func testAttentionAndMuteEventsRefreshTheState() {
        XCTAssertTrue(AttentionEvents.refreshesState("agent.attention"))
        XCTAssertTrue(AttentionEvents.refreshesState("space.muted"))
        XCTAssertFalse(AttentionEvents.refreshesState("agent.other"))
        XCTAssertFalse(AttentionEvents.refreshesState("herdr.transition"))
    }

    func testAMutedEventDoesNotAlert() {
        let muted = DaemonEvent(seq: 1, source: "supervisor", type: "pending.created", payload: ["muted": .bool(true)])
        let plain = DaemonEvent(seq: 2, source: "supervisor", type: "pending.created", payload: [:])
        let off = DaemonEvent(seq: 3, source: "supervisor", type: "pending.created", payload: ["muted": .bool(false)])
        XCTAssertTrue(AttentionEvents.isMuted(muted))
        XCTAssertFalse(AttentionEvents.isMuted(plain))
        XCTAssertFalse(AttentionEvents.isMuted(off))
    }
}

#if os(iOS)
import SwiftUI
import UIKit
@testable import Sheepdog

@MainActor
final class UnreadNameWeightTests: XCTestCase {
    private func render<V: View>(
        _ view: V,
        style: UIUserInterfaceStyle,
        sizeCategory: UIContentSizeCategory,
        legibility: UILegibilityWeight
    ) -> (size: CGSize, inkMass: Double, orangePixels: Int) {
        let host = UIHostingController(rootView: view.fixedSize())
        host.traitOverrides.userInterfaceStyle = style
        host.traitOverrides.preferredContentSizeCategory = sizeCategory
        host.traitOverrides.legibilityWeight = legibility
        let window = UIWindow(frame: CGRect(x: 0, y: 0, width: 800, height: 400))
        window.rootViewController = host
        window.makeKeyAndVisible()
        host.view.setNeedsLayout()
        host.view.layoutIfNeeded()
        let fit = host.sizeThatFits(in: CGSize(width: 800, height: 400))
        host.view.frame = CGRect(origin: .zero, size: fit)
        host.view.layoutIfNeeded()

        let format = UIGraphicsImageRendererFormat()
        format.scale = 2
        let img = UIGraphicsImageRenderer(size: fit, format: format).image { _ in
            host.view.drawHierarchy(in: CGRect(origin: .zero, size: fit), afterScreenUpdates: true)
        }
        window.isHidden = true
        guard let cg = img.cgImage else { return (fit, 0, 0) }
        let w = cg.width, h = cg.height
        var buf = [UInt8](repeating: 0, count: w * h * 4)
        let ctx = CGContext(data: &buf, width: w, height: h, bitsPerComponent: 8, bytesPerRow: w * 4,
                            space: CGColorSpaceCreateDeviceRGB(),
                            bitmapInfo: CGImageAlphaInfo.premultipliedLast.rawValue)!
        ctx.draw(cg, in: CGRect(x: 0, y: 0, width: w, height: h))
        // Corner pixel approximates background luminance; contrast against it measures foreground ink.
        let bgLuma = (Double(buf[0]) + Double(buf[1]) + Double(buf[2])) / 3.0
        var ink = 0.0
        var orange = 0
        for i in stride(from: 0, to: buf.count, by: 4) {
            let r = Double(buf[i]), g = Double(buf[i + 1]), b = Double(buf[i + 2])
            let luma = (r + g + b) / 3.0
            ink += abs(luma - bgLuma) / 255.0
            if r > 200, g > 80, g < 180, b < 80 { orange += 1 }
        }
        return (fit, ink, orange)
    }

    func testUnreadNameIsBolderThanReadAcrossModesSizesAndBoldTextWithoutADot() throws {
        let suite = "UnreadNameWeightTests.\(UUID().uuidString)"
        let defaults = UserDefaults(suiteName: suite)!
        let cacheDir = FileManager.default.temporaryDirectory.appendingPathComponent(suite)
        defer {
            defaults.removePersistentDomain(forName: suite)
            try? FileManager.default.removeItem(at: cacheDir)
        }
        let store = ComputerStore(defaults: defaults, secrets: InMemorySecretStore())
        let model = SessionViewModel(settings: SettingsStore(defaults: defaults, computers: store), cache: ConversationCache(directory: cacheDir))

        let unread = Agent(paneID: "p1", alias: "claude-auth", kind: "claude", state: .done,
                           attention: AgentAttention(group: .done, unread: true, turn: 2, seen: 1))
        let read = Agent(paneID: "p1", alias: "claude-auth", kind: "claude", state: .done,
                         attention: AgentAttention(group: .done, unread: false, turn: 2, seen: 2))
        XCTAssertEqual(AgentAttentionWords.value(unread), "Unread")
        XCTAssertEqual(AgentAttentionWords.value(read), "")

        for style in [UIUserInterfaceStyle.light, .dark] {
            for cat in [UIContentSizeCategory.large, .accessibilityExtraExtraExtraLarge] {
                for leg in [UILegibilityWeight.regular, .bold] {
                    let uChip = render(AgentChip(agent: unread, subtitle: nil), style: style, sizeCategory: cat, legibility: leg)
                    let rChip = render(AgentChip(agent: read, subtitle: nil), style: style, sizeCategory: cat, legibility: leg)
                    let ctx = "chip style=\(style.rawValue) cat=\(cat.rawValue) leg=\(leg.rawValue) u=\(uChip) r=\(rChip)"
                    XCTAssertEqual(uChip.orangePixels, 0, "no accent unread dot: \(ctx)")
                    XCTAssertGreaterThan(uChip.size.width, rChip.size.width, "bold name is wider: \(ctx)")
                    XCTAssertGreaterThan(uChip.inkMass, rChip.inkMass * 1.05, "unread reads heavier: \(ctx)")

                    let uCard = render(
                        AgentCard(agent: unread, question: nil, expanded: .constant(false), answer: { _, _ in })
                            .frame(width: 360)
                            .environmentObject(model),
                        style: style, sizeCategory: cat, legibility: leg
                    )
                    let rCard = render(
                        AgentCard(agent: read, question: nil, expanded: .constant(false), answer: { _, _ in })
                            .frame(width: 360)
                            .environmentObject(model),
                        style: style, sizeCategory: cat, legibility: leg
                    )
                    let cardCtx = "card style=\(style.rawValue) cat=\(cat.rawValue) leg=\(leg.rawValue) uInk=\(uCard.inkMass) rInk=\(rCard.inkMass)"
                    XCTAssertEqual(uCard.orangePixels, 0, "no accent unread dot on card: \(cardCtx)")
                    XCTAssertGreaterThan(uCard.inkMass, rCard.inkMass * 1.02, "card unread name reads heavier: \(cardCtx)")
                }
            }
        }
    }
}
#endif
