import XCTest
@testable import SheepdogKit

final class ConversationItemDecodingTests: XCTestCase {
    func testDecodesEveryKindOfARealPage() throws {
        let page = try DaemonClient.decoder.decode(ConversationPage.self, from: Data(Fixtures.conversationPage.utf8))
        XCTAssertEqual(page.items.count, 10)
        XCTAssertEqual(page.nextBefore, 1_790_065_877_490_932)
        XCTAssertEqual(Set(page.items.map(\.kind)), [.notice, .shepherd, .dog, .approval, .push])

        let shepherd = try XCTUnwrap(page.items.first { $0.kind == .shepherd })
        XCTAssertEqual(shepherd.via, "typed")
        XCTAssertEqual(shepherd.text, "what's going on")

        let approval = try XCTUnwrap(page.items.first { $0.pendingID == "ap-tlsnbh-1" })
        XCTAssertEqual(approval.kind, .approval)
        XCTAssertEqual(approval.agent, "billing")
        XCTAssertEqual(approval.options?.map(\.index), [1, 2, 3])
        XCTAssertEqual(approval.options?.first?.isDefault, true)
        XCTAssertEqual(approval.options?.first?.keys, ["y"])
        XCTAssertEqual(approval.answered, true)
        XCTAssertEqual(approval.resolution, .confirmed)
        XCTAssertEqual(approval.approvalOutcome, "You approved — Yes, proceed · by tap")

        let expired = try XCTUnwrap(page.items.first { $0.pendingID == "ap-tlrbpl-2" })
        XCTAssertNil(expired.options, "older lines carry no options")
        XCTAssertEqual(expired.approvalOutcome, "Expired without an answer")

        let push = try XCTUnwrap(page.items.last)
        XCTAssertTrue(push.needsUser)
        // `.5Z` — one fraction digit — still parses.
        XCTAssertEqual(push.ts.timeIntervalSince1970, 1_790_127_629.5, accuracy: 0.001)
    }

    func testAnUnknownKindAndResolutionDoNotFailThePage() throws {
        let json = #"{"items":[{"id":5,"ts":"2026-09-23T01:40:29.425157Z","kind":"hologram","text":"x","resolution":"vaporised"}],"next_before":null}"#
        let page = try DaemonClient.decoder.decode(ConversationPage.self, from: Data(json.utf8))
        XCTAssertEqual(page.items.first?.kind, .unknown)
        XCTAssertEqual(page.items.first?.resolution, .unknown)
        XCTAssertNil(page.nextBefore)
    }

    func testDecodesThePendingViewOfState() throws {
        let pending = try DaemonClient.decoder.decode(PendingAction.self, from: Data(Fixtures.statePendingAction.utf8))
        XCTAssertEqual(pending.id, "ap-tlsnbh-1")
        XCTAssertEqual(pending.paneID, "p-billing")
        XCTAssertTrue(pending.isAnswerPrompt)
        XCTAssertEqual(pending.agent, "billing")
        XCTAssertEqual(pending.command, "printf '%s\\n' 'hello from codex.' > hello.txt")
        XCTAssertEqual(pending.displaySummary, "billing asks to run printf '%s\\n' 'hello from codex.' > hello.txt.")
        XCTAssertEqual(pending.defaultOption?.label, "Yes, proceed")
        XCTAssertEqual(pending.options?.map(\.isNegative), [false, false, true])
        XCTAssertEqual(pending.options?.first?.accessibilityLabel, "Approve: Yes, proceed")
        XCTAssertEqual(pending.options?.last?.accessibilityLabel, "Decline: No, and tell Codex what to do differently")
    }

    func testAPendingActionWithoutIndexesIsNumberedByPosition() throws {
        let json = #"{"id":"p1","pane_id":"w1:p1","kind":"answer_prompt","spoken_summary":"auth is asking","options":[{"label":"Yes"},{"label":"No"}]}"#
        let pending = try DaemonClient.decoder.decode(PendingAction.self, from: Data(json.utf8))
        XCTAssertEqual(pending.options?.map(\.index), [1, 2])
        XCTAssertEqual(pending.displaySummary, "auth is asking")
    }

    func testAConfirmPendingIsNotAnApprovalCard() {
        let pending = PendingAction(id: "c1", paneID: "w1:p1", kind: "confirm_send", spokenSummary: "Send it?")
        XCTAssertFalse(pending.isAnswerPrompt)
    }

    func testBuildsThePendingActionFromARealPendingCreatedEvent() throws {
        var parser = SSEParser()
        var events: [DaemonEvent] = []
        for line in Fixtures.sseCreatedAndConfirmed {
            if case let .event(e)? = parser.push(line: line) { events.append(e) }
        }
        XCTAssertEqual(events.map(\.type), ["pending.created", "voice.push", "pending.confirmed"])
        let pending = try XCTUnwrap(PendingAction(event: events[0]))
        XCTAssertEqual(pending.id, "ap-tlsnbh-1")
        XCTAssertEqual(pending.paneID, "p-billing")
        XCTAssertEqual(pending.agent, "billing")
        XCTAssertEqual(pending.options?.count, 3)
        XCTAssertEqual(pending.command, "printf '%s\\n' 'hello from codex.' > hello.txt")
        XCTAssertNil(PendingAction(event: events[1]))
    }
}

final class ConversationHistoryTests: XCTestCase {
    private func item(_ id: Int64, _ kind: ConversationItem.Kind = .shepherd, _ text: String = "t") -> ConversationItem {
        ConversationItem(id: id, ts: Date(timeIntervalSince1970: TimeInterval(id)), kind: kind, text: text)
    }

    private func events(_ lines: [String]) -> [DaemonEvent] {
        var parser = SSEParser()
        return lines.compactMap { if case let .event(e)? = parser.push(line: $0) { return e } else { return nil } }
    }

    func testTheNewestPageWinsOverwhatThePhoneHadForItsRange() {
        var history = ConversationHistory(items: [item(10, .shepherd, "cached old"), item(20, .dog, "cached stale"), item(25, .push, "gone on server")])
        history.applyNewest(ConversationPage(items: [item(20, .dog, "server"), item(30)], nextBefore: 20))
        XCTAssertEqual(history.items.map(\.id), [10, 20, 30], "the older cached item stays, the one the server dropped goes")
        XCTAssertEqual(history.items[1].text, "server")
        XCTAssertEqual(history.nextBefore, 20)
        XCTAssertTrue(history.synced)
    }

    func testLiveItemsNewerThanThePageSurviveIt() {
        var history = ConversationHistory()
        history.upsert(item(40, .push, "arrived while the page was in flight"))
        history.applyNewest(ConversationPage(items: [item(10), item(20)], nextBefore: nil))
        XCTAssertEqual(history.items.map(\.id), [10, 20, 40])
    }

    func testAPageThatSaysNothingIsOlderDropsOlderCache() {
        var history = ConversationHistory(items: [item(1), item(2)])
        history.applyNewest(ConversationPage(items: [item(10)], nextBefore: nil))
        XCTAssertEqual(history.items.map(\.id), [10])
        XCTAssertFalse(history.canLoadOlder)
    }

    func testAnEmptyServerDropsWhatCameBeforeTheRequest() {
        var history = ConversationHistory(items: [item(1_000_000), item(2_000_000)])
        history.applyNewest(ConversationPage(items: [], nextBefore: nil), requestedAt: Date(timeIntervalSince1970: 1.5))
        XCTAssertEqual(history.items.map(\.id), [2_000_000], "only what is newer than the request is kept")
    }

    func testPagingOlderMergesByIDAndFollowsNextBefore() {
        var history = ConversationHistory()
        history.applyNewest(ConversationPage(items: [item(30), item(40)], nextBefore: 30))
        XCTAssertTrue(history.canLoadOlder)
        history.applyOlder(ConversationPage(items: [item(10), item(20), item(30, .dog, "dup")], nextBefore: 10))
        XCTAssertEqual(history.items.map(\.id), [10, 20, 30, 40])
        XCTAssertEqual(history.items[2].text, "dup", "the server's copy wins")
        XCTAssertEqual(history.nextBefore, 10)
        history.applyOlder(ConversationPage(items: [item(5)], nextBefore: nil))
        XCTAssertEqual(history.items.first?.id, 5)
        XCTAssertFalse(history.canLoadOlder)
    }

    func testUpsertKeepsIDOrderAndEachIDOnce() {
        var history = ConversationHistory()
        history.upsert(item(30))
        history.upsert(item(10))
        history.upsert(item(20))
        history.upsert(item(20, .dog, "replaced"))
        XCTAssertEqual(history.items.map(\.id), [10, 20, 30])
        XCTAssertEqual(history.items[1].text, "replaced")
    }

    func testFoldsLiveEventsIntoItemsWithTheServersIDs() throws {
        var history = ConversationHistory()
        for e in events(Fixtures.sseCreatedAndConfirmed) { history.fold(e) }
        XCTAssertEqual(history.items.map(\.id), [1_790_127_629_425_157, 1_790_127_629_426_365], "the same ids /api/conversation gives them")
        let approval = try XCTUnwrap(history.approval(pendingID: "ap-tlsnbh-1"))
        XCTAssertEqual(approval.resolution, .confirmed)
        XCTAssertEqual(approval.answer, "Yes, proceed")
        XCTAssertEqual(approval.via, "tap")
        XCTAssertEqual(approval.options?.count, 3)
        XCTAssertEqual(history.items[1].kind, .push)

        // The server's page for the same range is the same conversation.
        let page = try DaemonClient.decoder.decode(ConversationPage.self, from: Data(Fixtures.conversationPage.utf8))
        let serverApproval = try XCTUnwrap(page.items.first { $0.pendingID == "ap-tlsnbh-1" })
        XCTAssertEqual(serverApproval.id, approval.id)
        XCTAssertEqual(serverApproval.resolution, approval.resolution)
    }

    func testFoldingIsIdempotentAndAReplayDoesNotReopenAnAnsweredApproval() {
        var history = ConversationHistory()
        let evs = events(Fixtures.sseCreatedAndConfirmed)
        for e in evs { history.fold(e) }
        let once = history
        for e in evs { XCTAssertNil(history.fold(e), "replaying \(e.type) changes nothing") }
        XCTAssertEqual(history, once)
        XCTAssertEqual(history.approval(pendingID: "ap-tlsnbh-1")?.resolution, .confirmed)
    }

    func testFoldFollowsTheDaemonsRules() {
        var history = ConversationHistory()
        func fold(_ type: String, _ id: UInt64, _ payload: [String: JSONValue]) {
            history.fold(DaemonEvent(seq: id, ts: Date(), source: "t", type: type, payload: payload))
        }
        fold("web.utterance", 1, ["text": .string("via voice"), "via": .string("voice")])
        fold("web.utterance", 2, ["text": .string("typed")])
        fold("voice.tool_result", 3, ["spoken": .string("model said"), "via": .string("voice")])
        fold("voice.tool_result", 4, ["spoken": .string("router said"), "via": .string("text_router")])
        fold("voice.push", 5, ["text": .string("hush"), "urgency": .string("silent")])
        fold("voice.push", 6, ["text": .string("loud"), "urgency": .string("interrupt")])
        fold("daemon.start", 7, [:])
        fold("pending.created", 8, ["id": .string("c1"), "kind": .string("confirm_send"), "summary": .string("send?")])
        fold("herdr.state", 9, [:])
        XCTAssertEqual(history.items.map(\.id), [2, 4, 6, 7])
        XCTAssertEqual(history.items.map(\.kind), [.shepherd, .dog, .push, .notice])
        XCTAssertTrue(history.items[2].needsUser)
    }

    func testResolutionsOtherThanConfirmedDoNotOverwriteAnAnswer() {
        var history = ConversationHistory()
        history.upsert(ConversationItem(id: 1, ts: Date(), kind: .approval, text: "q", pendingID: "p1", answered: false, resolution: .open))
        history.fold(DaemonEvent(seq: 2, source: "s", type: "pending.replaced", payload: ["old_id": .string("p1")]))
        XCTAssertEqual(history.items.first?.resolution, .replaced)
        history.upsert(ConversationItem(id: 3, ts: Date(), kind: .approval, text: "q", pendingID: "p2", answered: true, answer: "Yes", resolution: .confirmed))
        history.fold(DaemonEvent(seq: 4, source: "s", type: "pending.expired", payload: ["id": .string("p2")]))
        XCTAssertEqual(history.approval(pendingID: "p2")?.resolution, .confirmed)
    }
}

final class ConversationTimelineTests: XCTestCase {
    private var calendar: Calendar = {
        var c = Calendar(identifier: .gregorian)
        c.timeZone = TimeZone(identifier: "America/Los_Angeles")!
        return c
    }()

    private var formatter: DateFormatter {
        let f = DateFormatter()
        f.locale = Locale(identifier: "en_US_POSIX")
        f.timeZone = calendar.timeZone
        f.dateFormat = "h:mm a"
        return f
    }

    func testHistoryThenLiveWithDayAndRestartDividers() throws {
        let page = try DaemonClient.decoder.decode(ConversationPage.self, from: Data(Fixtures.conversationPage.utf8))
        let history = ConversationHistory(items: page.items).items
        let live = [ConversationReducer.Bubble(id: 1, speaker: .shepherd, text: "and now?", isFinal: true, at: Date(timeIntervalSince1970: 1_790_127_700))]
        let rows = ConversationTimeline.rows(history: history, live: live, calendar: calendar, timeFormatter: formatter)

        let days = rows.compactMap { if case let .day(d) = $0 { return d } else { return nil } }
        XCTAssertEqual(days.count, 1, "01:31 and 18:41 PDT on the 22nd are one day in Los Angeles")
        let dividers = rows.compactMap { if case let .divider(_, title) = $0 { return title } else { return nil } }
        XCTAssertEqual(dividers, ["Sheepdog started · 1:31 AM", "Sheepdog restarted · 6:40 PM"])
        guard case .live? = rows.last else { return XCTFail("the live bubble comes last") }
        // The push that repeats the approval's words is folded into it.
        let pushes = rows.filter { if case let .item(i) = $0 { return i.kind == .push } else { return false } }
        XCTAssertEqual(pushes.count, 1)
        XCTAssertEqual(Set(rows.map(\.id)).count, rows.count, "row ids are unique")
    }

    func testADayChangeGetsItsOwnDivider() {
        let items = [
            ConversationItem(id: 1, ts: Date(timeIntervalSince1970: 1_790_060_000), kind: .shepherd, text: "late"),
            ConversationItem(id: 2, ts: Date(timeIntervalSince1970: 1_790_160_000), kind: .dog, text: "next day"),
        ]
        let rows = ConversationTimeline.rows(history: items, live: [], calendar: calendar, timeFormatter: formatter)
        XCTAssertEqual(rows.map { if case .day = $0 { return "day" } else { return "row" } }, ["day", "row", "day", "row"])
    }

    func testDayTitles() {
        let now = Date(timeIntervalSince1970: 1_790_127_700)
        let today = calendar.startOfDay(for: now)
        let yesterday = calendar.date(byAdding: .day, value: -1, to: today)!
        let older = calendar.date(byAdding: .day, value: -3, to: today)!
        let en = Locale(identifier: "en_US")
        XCTAssertEqual(ConversationTimeline.dayTitle(today, now: now, calendar: calendar, locale: en), "Today")
        XCTAssertEqual(ConversationTimeline.dayTitle(yesterday, now: now, calendar: calendar, locale: en), "Yesterday")
        XCTAssertEqual(ConversationTimeline.dayTitle(older, now: now, calendar: calendar, locale: en), "Saturday, September 19")
    }

    func testReducerDropsSettledSpeechTheServerNowHas() {
        var reducer = ConversationReducer(now: { Date(timeIntervalSince1970: 100) })
        reducer.apply(.transcript(.incoming, text: "hello", final: true))
        reducer.apply(.notice("local notice"))
        reducer.apply(.transcript(.outgoing, text: "still talk", final: false))
        reducer.dropSettledSpeech(before: Date(timeIntervalSince1970: 200))
        XCTAssertEqual(reducer.bubbles.map(\.text), ["local notice", "still talk"])
    }
}
