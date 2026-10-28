import XCTest
@testable import SheepdogKit

/// The fragment-merging rules of docs/ws-contract.md, checked against the
/// same cases the browser page implements in `addTranscript`.
final class ConversationReducerTests: XCTestCase {
    private func reducer() -> ConversationReducer {
        ConversationReducer(now: { Date(timeIntervalSince1970: 0) })
    }

    func testFragmentsOfOneUtteranceMergeIntoOneBubble() {
        var r = reducer()
        r.apply(.transcript(.outgoing, text: "On it — ", final: false))
        r.apply(.transcript(.outgoing, text: "auth is blocked", final: false))
        XCTAssertEqual(r.bubbles.count, 1)
        XCTAssertEqual(r.bubbles[0].text, "On it — auth is blocked")
        XCTAssertFalse(r.bubbles[0].isFinal)
        XCTAssertEqual(r.openBubble?.id, r.bubbles[0].id)

        r.apply(.transcript(.outgoing, text: " on a prompt.", final: true))
        XCTAssertEqual(r.bubbles.count, 1)
        XCTAssertTrue(r.bubbles[0].isFinal)
        XCTAssertNil(r.openBubble)
    }

    func testTheOtherDirectionClosesAnOutputThatNeverCarriedFinal() {
        var r = reducer()
        r.apply(.transcript(.outgoing, text: "thinking…", final: false))
        r.apply(.transcript(.incoming, text: "stop", final: true))
        XCTAssertEqual(r.bubbles.count, 2)
        XCTAssertTrue(r.bubbles[0].isFinal, "the incoming transcript is the boundary")
        XCTAssertEqual(r.bubbles[0].speaker, .sheepdog)
        XCTAssertEqual(r.bubbles[1].speaker, .shepherd)
    }

    func testFlushClosesTheOpenReply() {
        var r = reducer()
        r.apply(.transcript(.outgoing, text: "half a senten", final: false))
        r.apply(.flush)
        XCTAssertTrue(r.bubbles[0].isFinal)
        // What arrives after a flush is a new utterance.
        r.apply(.transcript(.outgoing, text: "Never mind.", final: true))
        XCTAssertEqual(r.bubbles.count, 2)
    }

    func testAFinalisedBubbleIsNotAppendedTo() {
        var r = reducer()
        r.apply(.transcript(.incoming, text: "what's going on", final: true))
        r.apply(.transcript(.incoming, text: "hello?", final: true))
        XCTAssertEqual(r.bubbles.map(\.text), ["what's going on", "hello?"])
    }

    func testNoticesAreTheirOwnKind() {
        var r = reducer()
        r.apply(.notice("Voice is off on the daemon — it will not hear the microphone."))
        XCTAssertEqual(r.bubbles[0].kind, .notice)
        XCTAssertEqual(r.bubbles[0].speaker, .system)
    }

    func testAnEmptyFinalFragmentDoesNotOpenABubble() {
        var r = reducer()
        r.apply(.transcript(.outgoing, text: "", final: true))
        XCTAssertTrue(r.bubbles.isEmpty)
    }

    func testTheConversationIsCappedAndKeepsTheNewest() {
        var r = reducer()
        for i in 0 ..< (ConversationReducer.capacity + 10) {
            r.apply(.transcript(.incoming, text: "line \(i)", final: true))
        }
        XCTAssertEqual(r.bubbles.count, ConversationReducer.capacity)
        XCTAssertEqual(r.bubbles.last?.text, "line \(ConversationReducer.capacity + 9)")
    }

    func testClearEmptiesTheConversation() {
        var r = reducer()
        r.apply(.notice("hello"))
        r.clear()
        XCTAssertTrue(r.bubbles.isEmpty)
    }

    // MARK: - A bubble never stays open for good

    private final class Clock: @unchecked Sendable {
        var t: TimeInterval = 0
    }

    /// The stuck state of TestFlight 0.2.2: the voice's reply never carries
    /// `final` and no `in` follows, so it stayed open ("...") forever.
    func testReplyWithoutFinalStaysOpenUntilSettled() {
        let clock = Clock()
        var r = ConversationReducer(now: { Date(timeIntervalSince1970: clock.t) })
        r.apply(.transcript(.incoming, text: "status?", final: true))
        r.apply(.transcript(.outgoing, text: "All idle.", final: false))
        XCTAssertNotNil(r.openBubble)

        clock.t = 2
        r.apply(.settle(idle: 4))
        XCTAssertNotNil(r.openBubble, "still talking: a fragment came 2 s ago")

        clock.t = 5
        r.apply(.settle(idle: 4))
        XCTAssertNil(r.openBubble)
        XCTAssertTrue(r.bubbles.allSatisfy(\.isFinal))
    }

    func testFragmentsKeepTheBubbleOpenPastItsStart() {
        let clock = Clock()
        var r = ConversationReducer(now: { Date(timeIntervalSince1970: clock.t) })
        r.apply(.transcript(.outgoing, text: "One ", final: false))
        clock.t = 10
        r.apply(.transcript(.outgoing, text: "two", final: false))
        clock.t = 12
        r.apply(.settle(idle: 4))
        XCTAssertNotNil(r.openBubble)
    }

    func testEmptyFinalFromTheDaemonClosesTheReply() {
        var r = reducer()
        r.apply(.transcript(.outgoing, text: "Done.", final: false))
        r.apply(.transcript(.outgoing, text: "", final: true))
        XCTAssertEqual(r.bubbles.count, 1)
        XCTAssertEqual(r.bubbles[0].text, "Done.")
        XCTAssertNil(r.openBubble)
    }

    func testSettleZeroClosesEveryOpenBubbleNotJustTheLast() {
        var r = reducer()
        r.apply(.transcript(.outgoing, text: "Old reply", final: false))
        // A turn this phone only half saw: a notice lands in between.
        r.apply(.notice("Talking to Mac"))
        r.apply(.transcript(.outgoing, text: "New reply", final: false))
        r.apply(.settle(idle: 0))
        XCTAssertTrue(r.bubbles.allSatisfy(\.isFinal))
    }

    /// Live partial + the history's final for the same turn: once settled,
    /// the sync drops the live copy (one row). Two identical turns are two
    /// rows: the swap goes by time, never by matching text.
    func testSettledReplyIsDroppedByHistoryButALaterIdenticalTurnStays() {
        let clock = Clock()
        var r = ConversationReducer(now: { Date(timeIntervalSince1970: clock.t) })
        r.apply(.transcript(.outgoing, text: "Codex is idle.", final: false))
        clock.t = 10
        r.apply(.settle(idle: 4))
        // History fetched at t = 20 has the first turn.
        clock.t = 30
        r.apply(.transcript(.outgoing, text: "Codex is idle.", final: false)) // same words, a new turn
        r.dropSettledSpeech(before: Date(timeIntervalSince1970: 20))
        XCTAssertEqual(r.bubbles.count, 1)
        XCTAssertEqual(r.bubbles[0].at, Date(timeIntervalSince1970: 30))
    }
}
