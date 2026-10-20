import XCTest
@testable import SheepdogKit

/// Cards (docs/ws-contract.md "Cards"): the `visibility` reports, the
/// `card` message, the `card` conversation item, and the rule that a card
/// pops up by itself once, and never from history or a replay.
final class CardsTests: XCTestCase {
    private static let tableCard = #"{"type":"card","card":{"card_id":"cmh2x1","kind":"card","at":"2026-10-20T09:12:03Z","title":"Failing tests","markdown":"| Test | Error |\n|---|---|\n| TestLogin | timeout |"}}"#
    private static let approvalCard = #"{"type":"card","card":{"card_id":"cmh2x2","kind":"approval","at":"2026-10-20T09:13:10Z","title":"Auth fix is asking","agent":"auth","spoken":"Auth fix","pane_id":"w1:p2","pending_id":"p7","question":"to run the migration","command":"make migrate","options":[{"index":1,"label":"Yes","keys":["y"],"default":true},{"index":2,"label":"No","keys":["n"],"default":false}]}}"#

    private func card(_ id: String, at seconds: TimeInterval = 0, kind: ScreenCard.Kind = .card) -> ScreenCard {
        ScreenCard(cardID: id, kind: kind, at: Date(timeIntervalSince1970: 1_800_000_000 + seconds), title: "Card \(id)", markdown: "- one")
    }

    private func cardItem(id: Int64, cardID: String, card: ScreenCard? = nil) -> ConversationItem {
        ConversationItem(id: id, ts: Date(timeIntervalSince1970: TimeInterval(id) / 1_000_000), kind: .card,
                         text: card?.title ?? "A card", cardID: cardID, card: card)
    }

    // MARK: - Wire

    func testTheContractsCardMessagesDecode() throws {
        let m = try WSMessage.decode(Data(Self.tableCard.utf8))
        XCTAssertEqual(m.type, .card)
        let card = try XCTUnwrap(m.card)
        XCTAssertEqual(card.cardID, "cmh2x1")
        XCTAssertEqual(card.kind, .card)
        XCTAssertEqual(card.title, "Failing tests")
        XCTAssertEqual(card.at, ISO8601DateFormatter().date(from: "2026-10-20T09:12:03Z"))
        XCTAssertTrue(card.markdown?.contains("TestLogin") == true)

        let a = try XCTUnwrap(try WSMessage.decode(Data(Self.approvalCard.utf8)).card)
        XCTAssertEqual(a.kind, .approval)
        XCTAssertEqual(a.agentName, "Auth fix")
        XCTAssertEqual(a.windowPaneID, "w1:p2")
        XCTAssertEqual(a.pendingID, "p7")
        XCTAssertEqual(a.command, "make migrate")
        XCTAssertEqual(a.options?.map(\.label), ["Yes", "No"])
        XCTAssertEqual(a.options?.first?.isDefault, true)
    }

    func testACardWithoutAnIDIsRefusedAndAnUnknownKindStillShows() throws {
        XCTAssertThrowsError(try WSMessage.decode(Data(#"{"type":"card"}"#.utf8)))
        XCTAssertThrowsError(try WSMessage.decode(Data(#"{"type":"card","card":{"card_id":"","kind":"card"}}"#.utf8)))
        let m = try WSMessage.decode(Data(#"{"type":"card","card":{"card_id":"c1","kind":"hologram","title":"Later"}}"#.utf8))
        XCTAssertEqual(m.card?.kind, .unknown)
        XCTAssertEqual(m.card?.displayTitle, "Later")
    }

    func testVisibilitySendsBothFieldsEvenWhenFalse() throws {
        XCTAssertEqual(
            try WSMessage.visibility(visible: false, voiceScreen: false).encodedString(),
            #"{"type":"visibility","visible":false,"voice_screen":false}"#
        )
        XCTAssertEqual(
            try WSMessage.visibility(visible: true, voiceScreen: true).encodedString(),
            #"{"type":"visibility","visible":true,"voice_screen":true}"#
        )
        XCTAssertThrowsError(try WSMessage(type: .visibility).encode(), "visible is required")
    }

    // MARK: - Visibility reports

    private let ready = ProtocolHandshake.State.ready(protocol: 1, daemonVersion: "0.9.0", features: ["screen", "cards"])
    private let readyWithout = ProtocolHandshake.State.ready(protocol: 1, daemonVersion: "0.8.0", features: ["screen"])

    func testNothingIsSentToADaemonWithoutCards() {
        var v = CardVisibility()
        XCTAssertNil(v.update(visible: true, voiceScreen: true))
        XCTAssertNil(v.handshake(readyWithout))
        XCTAssertNil(v.update(visible: false, voiceScreen: false))
        XCTAssertFalse(v.showing)
    }

    func testTheStateGoesOutRightAfterTheHandshakeAndOnEveryChange() {
        var v = CardVisibility()
        XCTAssertNil(v.update(visible: true, voiceScreen: true), "no connection yet")
        XCTAssertEqual(v.handshake(ready), .visibility(visible: true, voiceScreen: true))
        XCTAssertTrue(v.showing)
        XCTAssertNil(v.handshake(ready), "the same connection, nothing new")
        XCTAssertNil(v.update(visible: true, voiceScreen: true), "unchanged")
        XCTAssertEqual(v.update(visible: true, voiceScreen: false), .visibility(visible: true, voiceScreen: false))
        XCTAssertEqual(v.update(visible: false, voiceScreen: true), .visibility(visible: false, voiceScreen: false),
                       "a hidden app has no voice screen in front")
        XCTAssertFalse(v.showing)
    }

    func testANewConnectionHearsTheStateAgain() {
        var v = CardVisibility()
        _ = v.update(visible: true, voiceScreen: true)
        _ = v.handshake(ready)
        v.socketClosed()
        XCTAssertNil(v.update(visible: true, voiceScreen: true), "no socket")
        XCTAssertNil(v.handshake(.waiting))
        XCTAssertEqual(v.handshake(ready), .visibility(visible: true, voiceScreen: true),
                       "the daemon starts every client not visible")
    }

    // MARK: - The inbox

    func testALiveCardOpensOnceAndAReplayNever() {
        var inbox = CardInbox()
        XCTAssertEqual(inbox.receive(card("c1"), canPresent: true), .open)
        XCTAssertEqual(inbox.receive(card("c1"), canPresent: true), .duplicate)
        XCTAssertEqual(inbox.live.map(\.cardID), ["c1"])
    }

    func testACardWhileSomethingElseIsInFrontIsOnlyARow() {
        var inbox = CardInbox()
        XCTAssertEqual(inbox.receive(card("c1"), canPresent: false), .row)
        XCTAssertEqual(inbox.live.count, 1)
    }

    func testACardFromHistoryOrTheCacheNeverPopsUp() {
        var inbox = CardInbox()
        inbox.absorb([cardItem(id: 10, cardID: "c1", card: card("c1"))])
        XCTAssertEqual(inbox.receive(card("c1"), canPresent: true), .duplicate)
        XCTAssertTrue(inbox.live.isEmpty)
        XCTAssertEqual(inbox.card(id: "c1")?.title, "Card c1")
    }

    func testTheHistoryCopyReplacesTheLiveOneAndKeepsItsContent() {
        var inbox = CardInbox()
        let live = card("c1")
        _ = inbox.receive(live, canPresent: true)
        // The store lost the card: the item has the id only.
        let item = cardItem(id: 10, cardID: "c1")
        inbox.absorb([item])
        XCTAssertTrue(inbox.live.isEmpty)
        XCTAssertEqual(inbox.card(for: item), live)
        XCTAssertNil(CardInbox().card(for: item), "nothing to open: a plain row")
    }

    // MARK: - History and the timeline

    func testTheConversationItemDecodesWithAndWithoutItsCard() throws {
        let json = #"{"items":[{"id":1800000000000000,"ts":"2027-01-15T08:00:00Z","kind":"card","text":"Failing tests","card_id":"cmh2x1","agent":"","card":{"card_id":"cmh2x1","kind":"card","at":"2027-01-15T08:00:00Z","title":"Failing tests","markdown":"- a"}},{"id":1800000001000000,"kind":"card","text":"(card no longer stored)","card_id":"cgone"},{"id":1800000002000000,"kind":"card","text":"Odd","card_id":"codd","card":{"kind":7}}],"next_before":null}"#
        let page = try DaemonClient.decoder.decode(ConversationPage.self, from: Data(json.utf8))
        XCTAssertEqual(page.items.count, 3, "one unreadable card must not lose the page")
        XCTAssertEqual(page.items[0].kind, .card)
        XCTAssertEqual(page.items[0].card?.markdown, "- a")
        XCTAssertEqual(page.items[1].cardID, "cgone")
        XCTAssertNil(page.items[1].card)
        XCTAssertEqual(page.items[2].cardID, "codd")
        XCTAssertNil(page.items[2].card)
    }

    func testTheCacheKeepsTheCard() throws {
        let item = cardItem(id: 1_800_000_000_000_000, cardID: "c1", card: card("c1"))
        let data = try ConversationCache.encoder.encode(item)
        let back = try ConversationCache.decoder.decode(ConversationItem.self, from: data)
        XCTAssertEqual(back.cardID, "c1")
        XCTAssertEqual(back.card, item.card)
    }

    func testALiveCardSitsInTimeAndIsShownOnceWhenTheHistoryHasIt() {
        let t0 = Date(timeIntervalSince1970: 1_800_000_000)
        let said = ConversationItem(id: 1_800_000_000_000_000, ts: t0, kind: .shepherd, text: "show me the failing tests")
        let reply = ConversationItem(id: 1_800_000_010_000_000, ts: t0.addingTimeInterval(10), kind: .dog, text: "It's on your screen.")
        let live = card("c1", at: 5)

        let rows = ConversationTimeline.rows(history: [said, reply], live: [], cards: [live])
        XCTAssertEqual(rows.dropFirst().map(\.id), ["item.\(said.id)", "card.c1", "item.\(reply.id)"])

        let item = cardItem(id: 1_800_000_005_000_000, cardID: "c1", card: live)
        let synced = ConversationTimeline.rows(history: [said, item, reply], live: [], cards: [live])
        XCTAssertEqual(synced.dropFirst().map(\.id), ["item.\(said.id)", "item.\(item.id)", "item.\(reply.id)"])
    }

    func testCardsNeverFoldIntoUpdates() {
        let t0 = Date(timeIntervalSince1970: 1_800_000_000)
        let push1 = ConversationItem(id: 1_800_000_000_000_000, ts: t0, kind: .push, text: "auth finished")
        let item = cardItem(id: 1_800_000_001_000_000, cardID: "c1")
        let push2 = ConversationItem(id: 1_800_000_002_000_000, ts: t0.addingTimeInterval(2), kind: .push, text: "docs finished")
        let rows = ConversationTimeline.rows(history: [push1, item, push2], live: [])
        XCTAssertFalse(ConversationTimeline.isUpdate(.item(item)))
        XCTAssertFalse(ConversationTimeline.isUpdate(.card(card("c2"))))
        let grouped = ConversationTimeline.grouped(rows)
        XCTAssertTrue(grouped.contains { $0.id == "item.\(item.id)" })
    }

    // MARK: - The demo

    func testTheDemoShowsItsAgentsAsACard() throws {
        var world = DemoWorld(now: Date(timeIntervalSince1970: 1_800_000_000))
        let reply = world.ask("Show me the agents")
        XCTAssertTrue(reply.hasPrefix("It's on your screen"), reply)
        let cards = world.takeCards()
        XCTAssertEqual(cards.count, 1)
        XCTAssertTrue(world.takeCards().isEmpty)
        let card = try XCTUnwrap(cards.first)
        let blocks = MarkdownRenderer.blocks(card.markdown ?? "")
        XCTAssertTrue(blocks.contains { if case .table = $0 { return true } else { return false } }, "a table")
        let item = try XCTUnwrap(world.history.items.last { $0.kind == .card })
        XCTAssertEqual(item.cardID, card.cardID)
        XCTAssertEqual(item.card, card)
        XCTAssertNil(world.history.items.first { $0.kind == .unknown }, "the card.shown event folds to nothing else")
    }

    // MARK: - Tables

    func testATableIsABlockAndSurvivesTheOneTextRendering() {
        let text = "Two failed:\n\n| Test | Error |\n|:---|---:|\n| TestLogin | timeout |\n| TestPay \\| v2 | nil map |\n\nThen more."
        let blocks = MarkdownRenderer.blocks(text)
        XCTAssertEqual(blocks, [
            .paragraph("Two failed:"),
            .table(MarkdownRenderer.Table(header: ["Test", "Error"], alignments: [.leading, .trailing],
                                          rows: [["TestLogin", "timeout"], ["TestPay | v2", "nil map"]])),
            .paragraph("Then more."),
        ])
        let plain = MarkdownRenderer.plain(text)
        XCTAssertTrue(plain.contains("Test · Error"))
        XCTAssertTrue(plain.contains("TestLogin · timeout"))
    }

    func testShortRowsArePaddedAndAPipeInProseIsNotATable() {
        let blocks = MarkdownRenderer.blocks("| a | b | c |\n|---|---|---|\n| 1 |")
        XCTAssertEqual(blocks, [.table(MarkdownRenderer.Table(header: ["a", "b", "c"], rows: [["1", "", ""]]))])
        XCTAssertEqual(MarkdownRenderer.blocks("cats | dogs\nare fine"), [.paragraph("cats | dogs\nare fine")])
        XCTAssertEqual(MarkdownRenderer.blocks("a | b\n---"), [.paragraph("a | b\n---")])
    }
}
