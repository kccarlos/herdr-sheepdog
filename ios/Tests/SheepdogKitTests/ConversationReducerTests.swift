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

    func testPushesBecomeSystemCardsAndCloseAnOpenReply() {
        var r = reducer()
        r.apply(.transcript(.outgoing, text: "working on it", final: false))
        r.apply(.push(text: "auth is asking whether to run the migration.", needsUser: true))
        XCTAssertEqual(r.bubbles.count, 2)
        XCTAssertTrue(r.bubbles[0].isFinal)
        XCTAssertEqual(r.bubbles[1].speaker, .system)
        XCTAssertEqual(r.bubbles[1].kind, .push(needsUser: true))
    }

    func testMirroredEventsBuildBothSidesWhenTheDaemonHasNoVoice() {
        var r = reducer()
        r.apply(.mirroredUtterance("what's going on"))
        r.apply(.mirroredReply("Two agents: auth is blocked, docs is working."))
        XCTAssertEqual(r.bubbles.map(\.speaker), [.shepherd, .sheepdog])
        XCTAssertTrue(r.bubbles.allSatisfy(\.isFinal))
    }

    func testMirroredRepliesNeverMergeWithEachOther() {
        var r = reducer()
        r.apply(.mirroredReply("first"))
        r.apply(.mirroredReply("second"))
        XCTAssertEqual(r.bubbles.map(\.text), ["first", "second"])
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
}
