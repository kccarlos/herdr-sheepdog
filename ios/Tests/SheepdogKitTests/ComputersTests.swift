import Foundation
import XCTest
@testable import SheepdogKit

/// What the several-computers UI says about a computer, and how another
/// computer's questions are labelled, routed and ordered.
final class ComputersTests: XCTestCase {
    private var utc: Calendar {
        var c = Calendar(identifier: .gregorian)
        c.timeZone = TimeZone(identifier: "UTC")!
        return c
    }

    private func agent(_ alias: String, _ state: Agent.State) -> Agent {
        Agent(paneID: "p-\(alias)", alias: alias, state: state)
    }

    func testTheSummaryLine() {
        let now = ISO8601DateFormatter().date(from: "2026-10-23T12:00:00Z")!
        var c = ComputerSnapshot(id: "h", name: "Home Mac", isCurrent: false, reachability: .online,
                                 agents: [agent("a", .working), agent("b", .blocked), agent("c", .working), agent("d", .idle), agent("e", .exited)])
        XCTAssertEqual(c.summary(now: now, calendar: utc), "4 agents · 1 waiting")
        c.agents = [agent("a", .working)]
        XCTAssertEqual(c.summary(now: now, calendar: utc), "1 agent")
        c.agents = []
        XCTAssertEqual(c.summary(now: now, calendar: utc), "No agents")
        c.reachability = .connecting
        XCTAssertEqual(c.summary(now: now, calendar: utc), "Connecting…")
        c.reachability = .offline(since: ISO8601DateFormatter().date(from: "2026-10-23T09:12:00Z"))
        XCTAssertEqual(c.summary(now: now, calendar: utc), "Offline since 9:12")
        c.reachability = .offline(since: nil)
        XCTAssertEqual(c.summary(now: now, calendar: utc), "Offline")
    }

    func testARemoteQuestionIsLabelledAndRoutedToItsComputer() {
        let q = PendingAction(id: "pa-3", paneID: "h1:p1", kind: "answer_prompt", options: [PendingOption(index: 1, label: "Yes")], agent: "writer")
        let remote = RemoteQuestion(computerID: "home", computerName: "Home Mac", place: "blog", pending: q)
        XCTAssertEqual(remote.label, "Home Mac · blog")
        XCTAssertNotEqual(remote.namespaced.id, "pa-3", "never the same id as the current computer's pa-3")
        let parsed = RemoteQuestion.parse(remote.namespaced.id)
        XCTAssertEqual(parsed?.computer, "home")
        XCTAssertEqual(parsed?.question, "pa-3")
        XCTAssertNil(RemoteQuestion.parse("pa-3"), "the current computer's")
        XCTAssertEqual(RemoteQuestion(computerID: "h", computerName: "Home Mac", place: "", pending: q).label, "Home Mac")
    }

    func testOtherComputersQuestionsGoByAgeAcrossComputers() {
        func r(_ id: String, _ computer: String, _ time: String?) -> RemoteQuestion {
            RemoteQuestion(computerID: computer, computerName: computer, place: "",
                           pending: PendingAction(id: id, paneID: id, kind: "answer_prompt", createdAt: time.flatMap { ISO8601DateFormatter().date(from: $0) }))
        }
        let list = [r("h2", "home", "2026-10-23T09:10:00Z"), r("h1", "home", "2026-10-23T09:00:00Z"),
                    r("s1", "studio", "2026-10-23T09:05:00Z"), r("x", "studio", nil)]
        XCTAssertEqual(QuestionOrder.others(list).map(\.pending.id), ["h1", "s1", "h2", "x"])
    }

    func testTheQuestionsTimeIsDecoded() throws {
        let json = #"{"id":"q","pane_id":"p","kind":"answer_prompt","created_at":"2026-10-23T09:00:00.123456789Z"}"#
        let q = try DaemonClient.decoder.decode(PendingAction.self, from: Data(json.utf8))
        XCTAssertNotNil(q.createdAt)
    }
}
