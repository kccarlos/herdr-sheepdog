import XCTest
@testable import SheepdogKit

/// The "card waiting" corner label: it shows for a card that arrived as a
/// row while the app is active, a newer card replaces it, and its timer
/// hides it.
final class CardLabelTests: XCTestCase {
    private func card(_ id: String, title: String = "") -> ScreenCard {
        ScreenCard(cardID: id, kind: .card, at: nil, title: title.isEmpty ? "Card \(id)" : title, markdown: "- one")
    }

    func testAppearsOnARowWhileActive() {
        var inbox = CardInbox()
        var label = CardLabelState()
        let c = card("a")
        XCTAssertEqual(inbox.receive(c, canPresent: false), .row)
        XCTAssertNotNil(label.show(c, appActive: true))
        XCTAssertEqual(label.current?.cardID, "a")
        XCTAssertEqual(inbox.live.map(\.cardID), ["a"], "the card stays in the conversation")
    }

    func testNotWhileInactive() {
        var label = CardLabelState()
        XCTAssertNil(label.show(card("a"), appActive: false))
        XCTAssertNil(label.current)
    }

    func testANewerCardReplacesIt() {
        var label = CardLabelState()
        let first = label.show(card("a"), appActive: true)!
        let second = label.show(card("b"), appActive: true)!
        XCTAssertNotEqual(first, second)
        XCTAssertEqual(label.current?.cardID, "b")
        // The first card's timer must not hide the second label.
        label.expire(generation: first)
        XCTAssertEqual(label.current?.cardID, "b")
    }

    func testAutoHidesWhenItsTimerFires() {
        var label = CardLabelState()
        let g = label.show(card("a"), appActive: true)!
        label.expire(generation: g)
        XCTAssertNil(label.current)
        XCTAssertEqual(CardLabelState.duration, 5)
    }

    func testHideAndTheAnnouncement() {
        var label = CardLabelState()
        label.show(card("a", title: "Failing tests"), appActive: true)
        label.hide()
        XCTAssertNil(label.current)
        XCTAssertEqual(CardLabelState.announcement(for: card("a", title: "Failing tests")), "Card ready: Failing tests")
    }
}
