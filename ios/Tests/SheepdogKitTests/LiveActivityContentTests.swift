import XCTest
@testable import SheepdogKit

/// The Live Activity's content (`LiveActivityContent`): what the lock
/// screen shows, built from the session and the agents.
final class LiveActivityContentTests: XCTestCase {
    private let t0 = Date(timeIntervalSince1970: 1_800_000_000)

    private func agent(_ alias: String, _ state: Agent.State, pane: String? = nil, since: TimeInterval = 0, block: String? = nil, answered: Bool? = nil) -> Agent {
        Agent(paneID: pane ?? "p-\(alias)", alias: alias, state: state, stateSince: t0.addingTimeInterval(since),
              lastBlock: block.map { Agent.Block(question: $0, answered: answered) })
    }

    private func question(_ pane: String, _ text: String?, id: String? = nil, summary: String? = nil) -> PendingAction {
        PendingAction(id: id ?? "q-\(pane)", paneID: pane, kind: "answer_prompt", spokenSummary: summary, question: text)
    }

    private func make(_ state: SessionState = .listening, muted: Bool = false, agents: [Agent] = [], questions: [PendingAction] = [], computer: String? = nil, suspended: Bool = false) -> LiveActivityContent? {
        LiveActivityContent.make(state: state, muted: muted, agents: agents, questions: questions, computer: computer, suspendedInBackground: suspended)
    }

    // MARK: - Phase and words

    func testNoSessionMeansNoActivity() {
        XCTAssertNil(make(.disconnected))
        XCTAssertNil(make(.disconnected, suspended: true))
    }

    func testEverySessionStateMapsToAPhase() {
        let expected: [SessionState: LiveActivityContent.Phase] = [
            .connecting: .connecting, .reconnecting: .reconnecting,
            .voiceOff: .voiceOff, .audioPaused: .paused, .noMicrophone: .noMicrophone,
            .stalled: .thinking, .thinking: .thinking, .speaking: .speaking,
            .muted: .muted, .silent: .listening, .listening: .listening, .idle: .listening,
        ]
        for state in SessionState.allCases where state != .disconnected {
            XCTAssertEqual(make(state)?.phase, expected[state], "\(state)")
        }
    }

    func testPausedInTheBackgroundSaysSoButReconnectingStays() {
        XCTAssertEqual(make(.listening, suspended: true)?.phase, .locked)
        XCTAssertEqual(make(.listening, suspended: true)?.title, "Paused while locked")
        XCTAssertEqual(make(.reconnecting, suspended: true)?.phase, .reconnecting)
        XCTAssertEqual(make(.connecting, suspended: true)?.phase, .connecting)
    }

    func testTitles() {
        XCTAssertEqual(make(.listening)?.title, "Listening")
        XCTAssertEqual(make(.speaking)?.title, "Speaking")
        XCTAssertEqual(make(.muted, muted: true)?.title, "Muted")
        XCTAssertEqual(make(.reconnecting)?.title, "Reconnecting…")
        XCTAssertEqual(make(.voiceOff)?.title, "Voice is off")
    }

    func testPhaseRawValuesAreTheWireStrings() {
        XCTAssertEqual(LiveActivityContent.Phase.allCases.map(\.rawValue),
                       ["connecting", "reconnecting", "listening", "speaking", "thinking", "muted", "locked", "paused", "no_microphone", "voice_off"])
    }

    // MARK: - Counts and rows

    func testCountsOnlyRunningAgents() {
        let c = make(agents: [
            agent("a", .working), agent("b", .working), agent("c", .done), agent("d", .idle),
            agent("e", .blocked), agent("f", .exited),
        ])
        XCTAssertEqual(c?.working, 2)
        XCTAssertEqual(c?.done, 1)
        XCTAssertEqual(c?.waiting, 1)
        XCTAssertEqual(c?.countsText, "2 working · 1 done")
    }

    func testRowsFollowTheQuestionsThenHowLongTheyWaited() {
        let agents = [
            agent("late", .blocked, since: 300, block: "Late?"),
            agent("early", .blocked, since: 100, block: "Early?"),
            agent("asked", .blocked, since: 900),
            agent("busy", .working),
        ]
        let c = make(agents: agents, questions: [question("p-asked", "Run npm test in webapp?")])
        XCTAssertEqual(c?.rows.map(\.agent), ["asked", "early", "late"])
        XCTAssertEqual(c?.rows.map(\.question), ["Run npm test in webapp?", "Early?", "Late?"])
        XCTAssertEqual(c?.rows.first?.url.absoluteString, "sheepdog://window/p-asked")
    }

    func testAtMostThreeRowsButEveryWaitingAgentIsCounted() {
        let agents = (1...5).map { agent("a\($0)", .blocked, since: Double($0)) }
        let c = make(agents: agents)
        XCTAssertEqual(c?.rows.count, 3)
        XCTAssertEqual(c?.waiting, 5)
    }

    func testTheQuestionFallsBackThroughWhatIsKnown() {
        let agents = [
            agent("spoken", .blocked, since: 1),
            agent("block", .blocked, since: 2, block: "Which branch?"),
            agent("answered", .blocked, since: 3, block: "Old question", answered: true),
        ]
        let c = make(agents: agents, questions: [question("p-spoken", nil, summary: "Wants to push to main")])
        XCTAssertEqual(c?.rows.map(\.question), ["Wants to push to main", "Which branch?", "Needs you"])
    }

    func testQuestionsAreOneLineAndCut() {
        let long = String(repeating: "word ", count: 60)
        let c = make(agents: [agent("a", .blocked)], questions: [question("p-a", "First line\n\n  second   line")])
        XCTAssertEqual(c?.rows.first?.question, "First line second line")
        let cut = make(agents: [agent("a", .blocked)], questions: [question("p-a", long)])
        XCTAssertEqual(cut?.rows.first?.question.count, LiveActivityContent.questionLimit)
        XCTAssertEqual(cut?.rows.first?.question.last, "…")
    }

    // MARK: - One computer and several

    func testOneComputerHasNoComputerName() {
        let c = make(agents: [agent("a", .blocked), agent("b", .working)])
        XCTAssertNil(c?.computer)
        XCTAssertNil(c?.rows.first?.computer)
        XCTAssertEqual(c?.subtitle(voiceName: "Rex"), "1 working")
        XCTAssertEqual(make()?.subtitle(voiceName: "Rex"), "Rex")
    }

    func testSeveralComputersNameTheCurrentOneAndOnlyOtherRowsGetAChip() {
        let c = make(agents: [agent("a", .blocked), agent("b", .working)], computer: "Work Mac")
        XCTAssertEqual(c?.subtitle(voiceName: "Rex"), "Work Mac · 1 working")
        XCTAssertNil(c?.rows.first?.computer)
        let other = LiveActivityContent.Row(pane: "w2:p1", agent: "blog", question: "Publish?", computer: "Home Mac", computerID: "home")
        XCTAssertEqual(other.url.absoluteString, "sheepdog://window/w2:p1?computer=home")
        XCTAssertEqual(other.accessibilityLabel, "blog asks: Publish?, on Home Mac")
    }

    // MARK: - What fits

    func testVisibleRowsLeaveTheLastSlotForTheRest() {
        let rows = (1...3).map { LiveActivityContent.Row(pane: "p\($0)", agent: "a\($0)", question: "Q\($0)?") }
        let three = LiveActivityContent(phase: .listening, rows: rows)
        XCTAssertEqual(three.visibleRows(slots: 3).rows.count, 3)
        XCTAssertEqual(three.visibleRows(slots: 3).more, 0)
        XCTAssertEqual(three.visibleRows(slots: 2).rows.count, 1)
        XCTAssertEqual(three.visibleRows(slots: 2).more, 2)
        XCTAssertEqual(three.visibleRows(slots: 1).rows.count, 0)
        XCTAssertEqual(three.visibleRows(slots: 1).more, 3)

        let five = LiveActivityContent(phase: .listening, rows: rows, waiting: 5)
        XCTAssertEqual(five.visibleRows(slots: 3).rows.map(\.agent), ["a1", "a2"])
        XCTAssertEqual(five.visibleRows(slots: 3).more, 3)

        let one = LiveActivityContent(phase: .listening, rows: [rows[0]])
        XCTAssertEqual(one.visibleRows(slots: 1).rows.count, 1)
        XCTAssertEqual(one.visibleRows(slots: 1).more, 0)

        XCTAssertEqual(LiveActivityContent.moreText(3, afterRows: true), "3 more waiting")
        XCTAssertEqual(LiveActivityContent.moreText(3, afterRows: false), "3 agents waiting")
        XCTAssertEqual(LiveActivityContent.moreText(1, afterRows: false), "1 agent waiting")
    }

    func testRowsWithoutTextStillCountAsWaiting() {
        // A pushed update may carry counts only.
        let counts = LiveActivityContent(phase: .locked, waiting: 2, working: 1)
        XCTAssertEqual(counts.visibleRows(slots: 3).rows.count, 0)
        XCTAssertEqual(counts.visibleRows(slots: 3).more, 2)
    }

    // MARK: - VoiceOver

    func testAccessibilitySentences() {
        let c = make(.speaking, muted: true, agents: [agent("a", .blocked), agent("b", .blocked), agent("c", .working)], computer: "Work Mac")
        XCTAssertEqual(c?.accessibilityLabel(voiceName: "Rex"),
                       "Rex is speaking, on Work Mac. Microphone muted. 2 agents waiting for an answer. 1 working, 0 done")
        XCTAssertEqual(c?.compactAccessibilityLabel, "Speaking, muted, 2 waiting")
        XCTAssertEqual(make(.muted, muted: true)?.accessibilityLabel(voiceName: "Rex"), "Microphone muted. Rex can still talk")
        XCTAssertEqual(make(agents: [agent("a", .blocked)], questions: [question("p-a", "Run tests?")])?.rows.first?.accessibilityLabel,
                       "a asks: Run tests?")
    }

    // MARK: - The wire

    func testEncodesWithinActivityKitsLimitAtMostContent() throws {
        let long = String(repeating: "x", count: 500)
        let agents = (1...10).map { agent(long + "\($0)", .blocked, pane: "w\($0):p\($0)", since: Double($0)) }
        let questions = agents.map { question($0.paneID, String(repeating: "é ", count: 400)) }
        var c = try XCTUnwrap(make(agents: agents, questions: questions, computer: long))
        c.rows = c.rows.map { var r = $0; r.computer = String(long.prefix(LiveActivityContent.nameLimit)); r.computerID = String(repeating: "f", count: 64); return r }
        let data = try JSONEncoder().encode(c)
        // The whole update, push envelope included, must stay under 4 KB.
        XCTAssertLessThan(data.count, 3000, "\(data.count) bytes")
    }

    func testRoundTripsAndDecodesLeniently() throws {
        let c = LiveActivityContent(phase: .muted, muted: true, computer: "Work Mac",
                                    rows: [.init(pane: "w1:p1", agent: "a", question: "Q?", computer: "Home Mac", computerID: "home")],
                                    waiting: 2, working: 1, done: 4)
        let data = try JSONEncoder().encode(c)
        XCTAssertEqual(try JSONDecoder().decode(LiveActivityContent.self, from: data), c)
        let json = try XCTUnwrap(String(data: data, encoding: .utf8))
        XCTAssertTrue(json.contains("\"computer_id\":\"home\""), json)

        let odd = #"{"phase":"howling","waiting":1}"#
        let decoded = try JSONDecoder().decode(LiveActivityContent.self, from: Data(odd.utf8))
        XCTAssertEqual(decoded.phase, .listening)
        XCTAssertEqual(decoded.waiting, 1)
        XCTAssertEqual(decoded.rows, [])
    }

    func testRefreshComesWellBeforeStale() {
        XCTAssertLessThan(LiveActivityContent.refreshInterval * 2, LiveActivityContent.staleAfter)
    }
}
