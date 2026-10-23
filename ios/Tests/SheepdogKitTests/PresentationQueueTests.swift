import SheepdogKit
import XCTest

final class PresentationQueueTests: XCTestCase {
    private struct Sheet: Identifiable, Equatable {
        var id: String
        var payload = 0
    }

    private typealias Queue = PresentationQueue<Sheet, String>

    /// Present and report it on screen, as the view does.
    private func up(_ q: inout Queue, _ id: String) {
        q.present(Sheet(id: id))
        q.appeared(id)
    }

    func testPresentShowsAtOnceWhenClear() {
        var q = Queue()
        XCTAssertTrue(q.isIdle)
        q.present(Sheet(id: "settings"))
        XCTAssertEqual(q.current?.id, "settings")
        XCTAssertFalse(q.isIdle)
    }

    func testARequestWhilePresentingIsQueued() {
        var q = Queue()
        up(&q, "settings")
        q.present(Sheet(id: "pairing.confirm.c"))
        XCTAssertEqual(q.current?.id, "settings")
        XCTAssertEqual(q.pending.map(\.id), ["pairing.confirm.c"])
    }

    func testDismissalAdvancesTheQueueOnlyWhenReported() {
        var q = Queue()
        up(&q, "settings")
        q.present(Sheet(id: "pairing.confirm.c"))
        q.dismiss("settings")
        // Still sliding away: nothing new on top of it yet.
        XCTAssertNil(q.current)
        XCTAssertEqual(q.closing?.id, "settings")
        q.present(Sheet(id: "spaces"))
        XCTAssertNil(q.current, "a request while closing waits too")
        q.didDismiss()
        XCTAssertEqual(q.current?.id, "pairing.confirm.c")
        XCTAssertEqual(q.pending.map(\.id), ["spaces"])
        XCTAssertNil(q.closing)
    }

    func testTheSameIDTwiceIsIdempotent() {
        var q = Queue()
        up(&q, "card.1")
        q.present(Sheet(id: "card.1", payload: 2))
        XCTAssertEqual(q.current, Sheet(id: "card.1", payload: 2), "the payload refreshes in place")
        XCTAssertTrue(q.pending.isEmpty)
        q.present(Sheet(id: "spaces"))
        q.present(Sheet(id: "spaces", payload: 3))
        XCTAssertEqual(q.pending, [Sheet(id: "spaces", payload: 3)], "queued once")
    }

    /// The bug class this replaces: an item rebuilt on every render keeps
    /// its id, so asking again is never a new sheet.
    func testIDsAreStableAcrossRebuilds() {
        var q = Queue()
        for _ in 0 ..< 100 {
            q.present(Sheet(id: "pairing.start"))
            q.appeared("pairing.start")
        }
        XCTAssertEqual(q.current?.id, "pairing.start")
        XCTAssertTrue(q.pending.isEmpty)
        XCTAssertNil(q.closing)
    }

    /// A pairing link while Settings is open: Settings closes, then the
    /// pairing sheet shows, ahead of anything already queued.
    func testPresentInsteadClosesWhatIsUpFirst() {
        var q = Queue()
        up(&q, "settings")
        q.present(Sheet(id: "spaces"))
        q.presentInstead(Sheet(id: "pairing.confirm.c"))
        XCTAssertNil(q.current)
        XCTAssertEqual(q.closing?.id, "settings")
        q.didDismiss()
        XCTAssertEqual(q.current?.id, "pairing.confirm.c")
        XCTAssertEqual(q.pending.map(\.id), ["spaces"])

        var clear = Queue()
        clear.presentInstead(Sheet(id: "demo"))
        XCTAssertEqual(clear.current?.id, "demo", "nothing up: shown at once")
    }

    func testAnUnaskedDismissalChangesNothing() {
        var q = Queue()
        up(&q, "card.1")
        q.replace(Sheet(id: "card.2"))
        XCTAssertEqual(q.current?.id, "card.2")
        // The presentation swapped the cards and reported card 1's close.
        XCTAssertTrue(q.didDismiss().isEmpty)
        XCTAssertEqual(q.current?.id, "card.2")
    }

    func testDismissingAStaleOrQueuedIDLeavesTheCurrentOne() {
        var q = Queue()
        up(&q, "settings")
        q.present(Sheet(id: "spaces"))
        q.dismiss("newAgent")
        XCTAssertEqual(q.current?.id, "settings")
        q.dismiss("spaces")
        XCTAssertEqual(q.current?.id, "settings")
        XCTAssertTrue(q.pending.isEmpty, "a queued one is withdrawn")
    }

    func testAnItemNeverOnScreenClosesAtOnce() {
        var q = Queue()
        q.present(Sheet(id: "settings")) // held back (the splash), never appeared
        q.present(Sheet(id: "spaces"))
        q.dismiss()
        XCTAssertNil(q.closing, "no dismissal to wait for")
        XCTAssertEqual(q.current?.id, "spaces")
    }

    func testFollowupsWaitForAClearScreen() {
        var q = Queue()
        up(&q, "card.1")
        XCTAssertTrue(q.dismiss(then: "open window").isEmpty)
        XCTAssertNil(q.current)
        XCTAssertEqual(q.didDismiss(), ["open window"])
        XCTAssertTrue(q.isIdle)
        XCTAssertTrue(q.didDismiss().isEmpty, "handed back once")
    }

    func testAFollowupWithNothingUpRunsNow() {
        var q = Queue()
        XCTAssertEqual(q.dismiss(then: "open window"), ["open window"])
    }

    func testAFollowupWhileClosingWaitsForThatClose() {
        var q = Queue()
        up(&q, "spaces")
        q.dismiss()
        XCTAssertTrue(q.dismiss(then: "open window").isEmpty)
        XCTAssertEqual(q.didDismiss(), ["open window"])
    }

    /// Seeded random interleavings: at most one item up, never one up while
    /// another closes, and no item both up and queued.
    func testRandomInterleavingsKeepTheInvariants() {
        var rng = SplitMix(seed: 7)
        let ids = ["settings", "spaces", "newAgent", "card.1", "card.2", "pairing.start"]
        for seed in 0 ..< 200 {
            var q = Queue()
            for _ in 0 ..< 200 {
                let id = ids[Int(rng.next() % UInt64(ids.count))]
                switch rng.next() % 5 {
                case 0: q.present(Sheet(id: id))
                case 1: if let c = q.current { q.appeared(c.id) }
                case 2: q.dismiss()
                case 3: q.didDismiss()
                default: q.dismiss(id)
                }
                XCTAssertFalse(q.current != nil && q.closing != nil, "seed \(seed): one up while another closes")
                XCTAssertEqual(Set(q.pending.map(\.id)).count, q.pending.count, "seed \(seed): queued twice")
                if let c = q.current { XCTAssertFalse(q.pending.contains { $0.id == c.id }, "seed \(seed): up and queued") }
            }
        }
    }
}

/// A small seeded generator for the interleavings.
private struct SplitMix {
    var state: UInt64
    init(seed: UInt64) { state = seed }
    mutating func next() -> UInt64 {
        state &+= 0x9E37_79B9_7F4A_7C15
        var z = state
        z = (z ^ (z >> 30)) &* 0xBF58_476D_1CE4_E5B9
        z = (z ^ (z >> 27)) &* 0x94D0_49BB_1331_11EB
        return z ^ (z >> 31)
    }
}
