import XCTest
@testable import SheepdogKit

/// The orchestrator's suggested replies: which are live, which option the
/// user chose, how each option is drawn, and the one action that sends
/// (docs/dev/design/conversation-timeline.md, section 14).
final class SuggestedRepliesTests: XCTestCase {
    private let base = Date(timeIntervalSince1970: 1_790_000_000)
    private let options = ["Yes, start the logging", "Not yet"]

    private func at(_ seconds: TimeInterval) -> Date { base.addingTimeInterval(seconds) }

    private func reply(_ id: Int64, options: [String]? = nil, pending: Bool = false) -> TimelineRow {
        .brain(.init(id: id, ts: at(TimeInterval(id)), spoken: "Shall it start the echo logging?", options: options ?? self.options, pending: pending), compact: false)
    }

    private func item(_ id: Int64, _ kind: ConversationItem.Kind, _ text: String = "t", subkind: String? = nil, via: String? = nil) -> TimelineRow {
        let item = ConversationItem(id: id, ts: at(TimeInterval(id)), kind: kind, text: text, via: via, subkind: subkind)
        return kind == .notice ? .divider(item, title: text) : .item(item)
    }

    private func live(_ id: Int, _ speaker: ConversationReducer.Bubble.Speaker, _ text: String, kind: ConversationReducer.Bubble.Kind = .speech, final: Bool = true) -> TimelineRow {
        .live(.init(id: id, speaker: speaker, kind: kind, text: text, isFinal: final, at: at(TimeInterval(id))))
    }

    private func status(_ rows: [TimelineRow], _ id: Int64) -> SuggestedReplies.Status? {
        ConversationTimeline.suggestedReplies(rows)[id]?.status
    }

    // MARK: - Which reply is live

    func testTheStaleRule() {
        let cases: [(name: String, rows: [TimelineRow], live: Bool)] = [
            ("alone", [item(1, .shepherd), reply(2)], true),
            ("the voice says it", [reply(2), item(3, .dog, "Shall it start the echo logging?"), live(4, .sheepdog, "Shall it?")], true),
            ("agent updates and questions", [reply(2), item(3, .push), item(4, .approval), item(5, .card)], true),
            ("a resumed reconnect", [reply(2), item(3, .notice, "Voice reconnected; the conversation continues.", subkind: "resumed")], true),
            ("a new day", [reply(2), .day(at(3))], true),
            ("the user speaks", [reply(2), item(3, .shepherd, "something else")], false),
            ("the user types", [reply(2), item(3, .shepherd, "status", via: "typed")], false),
            ("the user starts speaking", [reply(2), live(3, .shepherd, "wai", final: false)], false),
            ("a newer reply", [reply(2), reply(3, options: [])], false),
            ("a newer pending reply", [reply(2), reply(3, options: [], pending: true)], false),
            ("a new conversation", [reply(2), item(3, .notice, "New conversation", subkind: "reset")], false),
            ("a daemon restart", [reply(2), item(3, .notice, "Sheepdog started.", subkind: "start")], false),
            ("the voice lost its context", [reply(2), item(3, .notice, "Voice context lost.", subkind: "context_lost")], false),
            ("a fresh voice", [reply(2), item(3, .notice, "Voice reconnected with a fresh session; earlier context was lost.")], false),
            ("a computer switch", [reply(2), live(3, .system, "Talking to Work Mac", kind: .notice)], false),
        ]
        for c in cases {
            XCTAssertEqual(status(c.rows, 2), c.live ? .live : .stale(chosen: nil), c.name)
        }
    }

    func testOnlyTheNewestOfTwoRepliesIsLive() {
        let all = ConversationTimeline.suggestedReplies([reply(2), item(3, .dog), reply(4, options: ["Ship it", "Wait"])])
        XCTAssertEqual(all[2]?.status, .stale(chosen: nil))
        XCTAssertEqual(all[4]?.status, .live)
        XCTAssertEqual(all.values.filter(\.isLive).count, 1)
    }

    func testANewestReplyWithoutOptionsLeavesNothingLive() {
        let all = ConversationTimeline.suggestedReplies([reply(2), reply(3, options: [])])
        XCTAssertNil(all[3], "no options, nothing to tap")
        XCTAssertFalse(all.values.contains(where: \.isLive))
    }

    func testAPendingReplyOffersNothing() {
        XCTAssertNil(ConversationTimeline.suggestedReplies([reply(2, pending: true)])[2])
    }

    func testTheUsersNextLineNamesTheChosenOption() {
        XCTAssertEqual(status([reply(2), item(3, .shepherd, "not yet.", via: "typed")], 2), .stale(chosen: "Not yet"))
        XCTAssertEqual(status([reply(2), live(3, .shepherd, "Yes, start the logging")], 2), .stale(chosen: "Yes, start the logging"))
        // Only the first line after it answers it.
        XCTAssertEqual(status([reply(2), item(3, .shepherd, "hm"), item(4, .shepherd, "Not yet")], 2), .stale(chosen: nil))
        // A switch first: nothing was chosen.
        XCTAssertEqual(status([reply(2), live(3, .system, "Talking to Home", kind: .notice), item(4, .shepherd, "Not yet")], 2), .stale(chosen: nil))
    }

    func testTheTextRoutersReplyUnderItsOwnBubble() {
        // A daemon without a voice: the reply is also the dog item, and its
        // options sit right under it.
        let compact = TimelineRow.brain(.init(id: 3, ts: at(3), spoken: "Two agents.", options: options, via: "text_router"), compact: true)
        XCTAssertEqual(status([item(2, .shepherd), item(3, .dog), compact], 3), .live)
        XCTAssertEqual(status([item(3, .dog), compact, item(4, .shepherd, "Not yet")], 3), .stale(chosen: "Not yet"))
    }

    // MARK: - How each option is drawn

    func testOptionStates() {
        let live = SuggestedReplies(replyID: 2, options: options, status: .live)
        let yes = options[0], no = options[1]
        XCTAssertEqual(live.state(of: yes, tap: nil), .available)
        XCTAssertEqual(live.state(of: yes, tap: nil, busy: true), .disabled, "the composer is sending")

        let sending = SuggestedReplyTap(replyID: 2, option: yes, computer: "c", phase: .sending)
        XCTAssertEqual(live.state(of: yes, tap: sending), .sending)
        XCTAssertEqual(live.state(of: no, tap: sending), .disabled)

        var sent = sending
        sent.phase = .sent
        XCTAssertEqual(live.state(of: yes, tap: sent), .sent)
        XCTAssertEqual(live.state(of: no, tap: sent), .disabled)

        var failed = sending
        failed.phase = .failed(message: "The computer did not answer.")
        XCTAssertEqual(live.state(of: yes, tap: failed), .available, "a failure gives the options back")
        XCTAssertEqual(live.failure(failed), "The computer did not answer.")

        let other = SuggestedReplyTap(replyID: 9, option: yes, computer: "c", phase: .sending)
        XCTAssertEqual(live.state(of: yes, tap: other), .available, "a tap on another reply")

        let answered = SuggestedReplies(replyID: 2, options: options, status: .stale(chosen: no))
        XCTAssertEqual(answered.state(of: no, tap: sent), .sent)
        XCTAssertEqual(answered.state(of: yes, tap: sent), .stale)
        XCTAssertNil(answered.failure(failed), "an answered reply shows no old failure")
        XCTAssertEqual(SuggestedReplies(replyID: 2, options: options, status: .stale(chosen: nil)).state(of: yes, tap: nil), .stale)
    }

    // MARK: - The action

    @MainActor
    func testATapSendsTheOptionAsATypedLine() async {
        var said: [String] = []
        let sender = SuggestedReplySender(sendTyped: { said.append($0); return true })
        let replies = SuggestedReplies(replyID: 2, options: options, status: .live)
        let sent = await sender.send("Not yet", of: replies, to: "home")
        XCTAssertTrue(sent)
        XCTAssertEqual(said, ["Not yet"], "exactly the option's words, once")
        XCTAssertEqual(sender.tap, SuggestedReplyTap(replyID: 2, option: "Not yet", computer: "home", phase: .sent))
        XCTAssertNil(sender.tap(on: "work"), "never shown on another computer")
    }

    @MainActor
    func testAFailedSendGivesTheOptionsBackWithTheReason() async {
        let sender = SuggestedReplySender(sendTyped: { _ in false }, lastError: { "The computer did not answer." })
        let replies = SuggestedReplies(replyID: 2, options: options, status: .live)
        let sent = await sender.send("Not yet", of: replies, to: "home")
        XCTAssertFalse(sent)
        XCTAssertEqual(sender.tap?.phase, .failed(message: "The computer did not answer."))
        XCTAssertEqual(replies.state(of: "Not yet", tap: sender.tap), .available)
    }

    @MainActor
    func testAStaleReplyOrAnUnknownOptionSendsNothing() async {
        var said: [String] = []
        let sender = SuggestedReplySender(sendTyped: { said.append($0); return true })
        let stale = SuggestedReplies(replyID: 2, options: options, status: .stale(chosen: nil))
        let a = await sender.send("Not yet", of: stale, to: "home")
        let b = await sender.send("Something else", of: SuggestedReplies(replyID: 3, options: options, status: .live), to: "home")
        XCTAssertFalse(a || b)
        XCTAssertEqual(said, [])
        XCTAssertNil(sender.tap)
    }

    @MainActor
    func testASecondTapWhileTheFirstIsOnItsWayIsIgnored() async {
        var said: [String] = []
        var release: CheckedContinuation<Void, Never>?
        let sender = SuggestedReplySender(sendTyped: { text in
            said.append(text)
            await withCheckedContinuation { release = $0 }
            return true
        })
        let replies = SuggestedReplies(replyID: 2, options: options, status: .live)
        let first = Task { await sender.send("Yes, start the logging", of: replies, to: "home") }
        while release == nil { await Task.yield() }
        XCTAssertEqual(sender.tap?.phase, .sending)
        let second = await sender.send("Not yet", of: replies, to: "home")
        XCTAssertFalse(second)
        release?.resume()
        let firstSent = await first.value
        XCTAssertTrue(firstSent)
        XCTAssertEqual(said, ["Yes, start the logging"])
        XCTAssertEqual(sender.tap?.option, "Yes, start the logging")
    }
}
