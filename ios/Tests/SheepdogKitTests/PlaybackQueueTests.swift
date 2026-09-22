import XCTest
@testable import SheepdogKit

final class PlaybackQueueTests: XCTestCase {
    private func chunk(_ milliseconds: Int) -> [Int16] {
        [Int16](repeating: 1, count: Int(AudioContract.playbackRate) * milliseconds / 1000)
    }

    func testHoldsTheJitterBufferThenReleasesInOrder() {
        var queue = PlaybackQueue(prebufferMilliseconds: 150)
        XCTAssertEqual(queue.phase, .idle)

        XCTAssertTrue(queue.push(chunk(40), now: 0).isEmpty, "40 ms is not enough to start")
        XCTAssertEqual(queue.phase, .buffering)
        XCTAssertTrue(queue.push(chunk(60), now: 0.05).isEmpty)

        let released = queue.push(chunk(60), now: 0.1)
        XCTAssertEqual(released.map(\.id), [1, 2, 3], "chunks come out in arrival order")
        XCTAssertEqual(queue.phase, .playing)
        XCTAssertEqual(queue.queueDepthMilliseconds, 160)
    }

    func testOncePlayingEveryChunkGoesStraightToThePlayer() {
        var queue = PlaybackQueue(prebufferMilliseconds: 50)
        _ = queue.push(chunk(60), now: 0)
        XCTAssertEqual(queue.phase, .playing)
        let next = queue.push(chunk(700), now: 0.1)
        XCTAssertEqual(next.map(\.id), [2], "no re-buffering mid-utterance: that would be a gap")
        XCTAssertEqual(queue.queueDepthMilliseconds, 760)
    }

    func testAShortUtteranceStartsAfterTheMaximumWait() {
        var queue = PlaybackQueue(prebufferMilliseconds: 150, maximumWait: 0.4)
        XCTAssertTrue(queue.push(chunk(40), now: 0).isEmpty)
        XCTAssertTrue(queue.tick(now: 0.2).isEmpty, "still inside the wait")
        let released = queue.tick(now: 0.45)
        XCTAssertEqual(released.map(\.id), [1])
        XCTAssertEqual(queue.phase, .playing)
    }

    func testCompletionDrainsTheDepthAndReturnsToIdle() {
        var queue = PlaybackQueue(prebufferMilliseconds: 50)
        let first = queue.push(chunk(60), now: 0)
        let second = queue.push(chunk(60), now: 0.01)
        XCTAssertTrue(queue.isSpeaking)
        queue.completed(first[0].id)
        XCTAssertEqual(queue.queueDepthMilliseconds, 60)
        XCTAssertTrue(queue.isSpeaking)
        queue.completed(second[0].id)
        XCTAssertFalse(queue.isSpeaking)
        XCTAssertEqual(queue.phase, .idle, "the next utterance re-buffers")
        XCTAssertEqual(queue.queueDepthMilliseconds, 0)
    }

    func testFlushEmptiesEverythingAtOnce() {
        var queue = PlaybackQueue(prebufferMilliseconds: 50)
        let scheduled = queue.push(chunk(200), now: 0)
        _ = queue.push(chunk(200), now: 0.05)
        XCTAssertTrue(queue.flush(), "there was audio, so the node needs stopping")
        XCTAssertEqual(queue.phase, .idle)
        XCTAssertEqual(queue.queueDepthMilliseconds, 0)
        XCTAssertFalse(queue.isSpeaking)
        XCTAssertEqual(queue.chunksDropped, 2)
        XCTAssertEqual(queue.flushes, 1)
        // A completion arriving after the flush (the node was mid-callback)
        // must not resurrect the depth.
        queue.completed(scheduled[0].id)
        XCTAssertEqual(queue.queueDepthMilliseconds, 0)
    }

    func testFlushOnAnEmptyQueueSaysThereWasNothingToStop() {
        var queue = PlaybackQueue()
        XCTAssertFalse(queue.flush())
        XCTAssertEqual(queue.flushes, 1)
    }

    func testBufferedChunksAreDroppedByAFlushToo() {
        var queue = PlaybackQueue(prebufferMilliseconds: 500)
        XCTAssertTrue(queue.push(chunk(40), now: 0).isEmpty)
        XCTAssertTrue(queue.flush())
        XCTAssertTrue(queue.tick(now: 10).isEmpty, "nothing is left to release")
    }

    func testEmptyChunksAreIgnored() {
        var queue = PlaybackQueue()
        XCTAssertTrue(queue.push([], now: 0).isEmpty)
        XCTAssertEqual(queue.phase, .idle)
    }
}
