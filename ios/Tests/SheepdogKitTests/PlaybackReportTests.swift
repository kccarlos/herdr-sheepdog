import XCTest
@testable import SheepdogKit

/// The playback reports of docs/ws-contract.md "Playback reports", as
/// `PlaybackQueue` produces them: `started` / `drained` / `flushed`, the
/// 300 ms underrun rule, and `played_ms` in stream time.
final class PlaybackReportTests: XCTestCase {
    private func chunk(_ milliseconds: Int) -> [Int16] {
        [Int16](repeating: 1, count: Int(AudioContract.playbackRate) * milliseconds / 1000)
    }

    private typealias Report = PlaybackQueue.Report

    // MARK: - The wire

    func testTheContractsLiteralsDecode() throws {
        let started = try WSMessage.decode(Data(#"{"type":"playback","state":"started","played_ms":0}"#.utf8))
        XCTAssertEqual(started.type, .playback)
        XCTAssertEqual(started.state, .started)
        XCTAssertEqual(started.playedMs, 0)
        let drained = try WSMessage.decode(Data(#"{"type":"playback","state":"drained","played_ms":4200}"#.utf8))
        XCTAssertEqual(drained, .playback(.drained, playedMs: 4_200))
        let flushed = try WSMessage.decode(Data(#"{"type":"playback","state":"flushed","played_ms":1850}"#.utf8))
        XCTAssertEqual(flushed, .playback(.flushed, playedMs: 1_850))
        XCTAssertEqual(try WSMessage.decode(Data(#"{"type":"speaking","on":true}"#.utf8)), .speaking(true))
        XCTAssertEqual(try WSMessage.decode(Data(#"{"type":"speaking","on":false}"#.utf8)), .speaking(false))
    }

    func testReportsEncodeToTheSameFields() throws {
        for (message, literal) in [
            (WSMessage.playback(.started, playedMs: 0), #"{"type":"playback","state":"started","played_ms":0}"#),
            (.playback(.drained, playedMs: 4_200), #"{"type":"playback","state":"drained","played_ms":4200}"#),
            (.playback(.flushed, playedMs: 1_850), #"{"type":"playback","state":"flushed","played_ms":1850}"#),
            (.speaking(true), #"{"type":"speaking","on":true}"#),
            (.speaking(false), #"{"type":"speaking","on":false}"#),
        ] {
            let ours = try JSONSerialization.jsonObject(with: try message.encode()) as? NSDictionary
            let theirs = try JSONSerialization.jsonObject(with: Data(literal.utf8)) as? NSDictionary
            XCTAssertEqual(ours, theirs, literal)
            XCTAssertEqual(try WSMessage.decode(try message.encode()), message)
        }
        XCTAssertEqual(try WSMessage.playback(.flushed, playedMs: 0).encodedString(), #"{"played_ms":0,"state":"flushed","type":"playback"}"#)
    }

    func testPlaybackValidationMirrorsTheGoSide() {
        // `started` may omit played_ms; `drained` and `flushed` may not.
        XCTAssertNoThrow(try WSMessage.decode(Data(#"{"type":"playback","state":"started"}"#.utf8)))
        XCTAssertThrowsError(try WSMessage.decode(Data(#"{"type":"playback","state":"drained"}"#.utf8)))
        XCTAssertThrowsError(try WSMessage.decode(Data(#"{"type":"playback","state":"flushed"}"#.utf8)))
        XCTAssertThrowsError(try WSMessage.decode(Data(#"{"type":"playback","state":"paused","played_ms":1}"#.utf8)))
        XCTAssertThrowsError(try WSMessage.decode(Data(#"{"type":"playback","played_ms":1}"#.utf8)))
        XCTAssertThrowsError(try WSMessage(type: .playback, state: .drained, playedMs: -1).encode())
        XCTAssertThrowsError(try WSMessage(type: .speaking).encode(), "speaking needs on")
        XCTAssertEqual(WSMessage.playback(.flushed, playedMs: -5).playedMs, 0, "the constructor never sends a negative")
    }

    func testThePingCarriesTheAudioState() throws {
        let ping = WSMessage.ping(gatedFrames: 0, peak: 0, audioState: "running")
        XCTAssertEqual(try ping.encodedString(), #"{"audio_state":"running","gated_frames":0,"peak":0,"type":"ping"}"#)
    }

    // MARK: - started / drained

    func testARunStartsOnceAndDrainsWithItsWholeLength() {
        var queue = PlaybackQueue(prebufferMilliseconds: 150)
        _ = queue.push(chunk(100), now: 0)
        XCTAssertEqual(queue.takeReports(), [], "still buffering: nothing is playing yet")
        _ = queue.push(chunk(100), now: 0.05)
        XCTAssertEqual(queue.takeReports(), [Report(.started, playedMilliseconds: 0)])
        _ = queue.push(chunk(300), now: 0.1)
        XCTAssertEqual(queue.takeReports(), [], "more of the same run")

        // 500 ms played from 0.05: the playhead runs out at 0.55.
        _ = queue.tick(now: 0.6)
        XCTAssertEqual(queue.takeReports(), [], "dry, but still inside the underrun window")
        _ = queue.tick(now: 0.86)
        XCTAssertEqual(queue.takeReports(), [Report(.drained, playedMilliseconds: 500)])
        _ = queue.tick(now: 2)
        XCTAssertEqual(queue.takeReports(), [], "drained once")
    }

    func testAnUnderrunThatResumesWithin300msIsTheSameRun() {
        var queue = PlaybackQueue(prebufferMilliseconds: 50)
        _ = queue.push(chunk(100), now: 0)
        XCTAssertEqual(queue.takeReports(), [Report(.started, playedMilliseconds: 0)])
        // Dry at 0.1; more of the reply at 0.3, inside the window.
        _ = queue.push(chunk(200), now: 0.3)
        XCTAssertEqual(queue.underruns, 1)
        XCTAssertEqual(queue.phase, .playing, "the refill target is still 150 ms, 200 ms releases at once")
        XCTAssertEqual(queue.takeReports(), [], "no drained, no second started")
        XCTAssertEqual(queue.runSamples, chunk(300).count, "played_ms keeps counting across the underrun")

        // The resumed audio ends at 0.5; nothing follows.
        _ = queue.tick(now: 0.81)
        XCTAssertEqual(queue.takeReports(), [Report(.drained, playedMilliseconds: 300)])
    }

    func testAudioAfterTheWindowIsANewRun() {
        var queue = PlaybackQueue(prebufferMilliseconds: 50)
        _ = queue.push(chunk(100), now: 0)
        _ = queue.takeReports()
        // Dry at 0.1; the next audio at 0.5 (400 ms later) is a new reply.
        _ = queue.push(chunk(100), now: 0.5)
        XCTAssertEqual(queue.underruns, 0)
        XCTAssertEqual(queue.takeReports(), [
            Report(.drained, playedMilliseconds: 100),
            Report(.started, playedMilliseconds: 0),
        ], "the old run drains before the new one starts")
    }

    func testAShortReplyThatNeverFillsTheBufferStillStartsAndDrains() {
        var queue = PlaybackQueue(prebufferMilliseconds: 150)
        _ = queue.push(chunk(40), now: 0)
        _ = queue.tick(now: 0.45)
        XCTAssertEqual(queue.takeReports(), [Report(.started, playedMilliseconds: 0)])
        _ = queue.tick(now: 0.9)
        XCTAssertEqual(queue.takeReports(), [Report(.drained, playedMilliseconds: 40)])
    }

    func testTheNodeReportingDryEarlyStillDrainsOnce() {
        var queue = PlaybackQueue(prebufferMilliseconds: 50)
        let released = queue.push(chunk(100), now: 0)
        _ = queue.takeReports()
        queue.completed(released[0].id)
        XCTAssertEqual(queue.takeReports(), [])
        _ = queue.tick(now: 0.5)
        XCTAssertEqual(queue.takeReports(), [Report(.drained, playedMilliseconds: 100)])
    }

    // MARK: - flushed

    func testEveryFlushReportsEvenWithNothingPlaying() {
        var queue = PlaybackQueue()
        XCTAssertFalse(queue.flush(now: 0))
        XCTAssertEqual(queue.takeReports(), [Report(.flushed, playedMilliseconds: 0)])
        XCTAssertFalse(queue.flush(now: 1))
        XCTAssertEqual(queue.takeReports(), [Report(.flushed, playedMilliseconds: 0)], "every flush, not the first")
    }

    func testAFlushMidRunSaysHowMuchWasHeard() {
        var queue = PlaybackQueue(prebufferMilliseconds: 50)
        _ = queue.push(chunk(1_000), now: 0)
        _ = queue.takeReports()
        XCTAssertTrue(queue.flush(now: 0.4))
        XCTAssertEqual(queue.takeReports(), [Report(.flushed, playedMilliseconds: 400)])
        XCTAssertEqual(queue.runSamples, 0)

        // The next audio is a new run with its own started.
        _ = queue.push(chunk(100), now: 0.5)
        XCTAssertEqual(queue.takeReports(), [Report(.started, playedMilliseconds: 0)])
    }

    func testAFlushWhileBufferingHeardNothing() {
        var queue = PlaybackQueue(prebufferMilliseconds: 150)
        _ = queue.push(chunk(100), now: 0)
        XCTAssertTrue(queue.flush(now: 0.05))
        XCTAssertEqual(queue.takeReports(), [Report(.flushed, playedMilliseconds: 0)])
    }

    func testAFlushAfterTheRunRanDryButBeforeDrainedCountsAllOfIt() {
        var queue = PlaybackQueue(prebufferMilliseconds: 50)
        _ = queue.push(chunk(200), now: 0)
        _ = queue.takeReports()
        _ = queue.tick(now: 0.25)  // dry at 0.2, drained not reported yet
        queue.flush(now: 0.3)
        XCTAssertEqual(queue.takeReports(), [Report(.flushed, playedMilliseconds: 200)])
        _ = queue.tick(now: 1)
        XCTAssertEqual(queue.takeReports(), [], "a flushed run never drains afterwards")
    }

    func testAFlushWithoutAClockCountsTheUnplayedAsUnheard() {
        var queue = PlaybackQueue(prebufferMilliseconds: 50)
        _ = queue.push(chunk(300), now: 0)
        _ = queue.takeReports()
        queue.flush()
        XCTAssertEqual(queue.takeReports(), [Report(.flushed, playedMilliseconds: 0)])
    }

    // MARK: - Stream time under time-stretch

    func testOneSecondAtPoint85ReportsOneThousandNotTheWallClock() {
        var queue = PlaybackQueue(prebufferMilliseconds: 50)
        _ = queue.push(chunk(1_000), now: 0)
        _ = queue.takeReports()
        queue.setRate(0.85, now: 0)
        // At 0.85× one second of audio lasts 1 / 0.85 = 1.176 s.
        _ = queue.tick(now: 1.1)
        XCTAssertEqual(queue.phase, .playing, "still playing at 1.1 s: the stretch made it longer")
        _ = queue.tick(now: 1.18)
        _ = queue.tick(now: 1.6)
        XCTAssertEqual(queue.takeReports(), [Report(.drained, playedMilliseconds: 1_000)], "not 1 176")
    }

    func testAFlushUnderStretchCountsStreamTime() {
        var queue = PlaybackQueue(prebufferMilliseconds: 50)
        _ = queue.push(chunk(1_000), now: 0)
        _ = queue.takeReports()
        queue.setRate(0.85, now: 0)
        // 0.5 s of wall clock at 0.85× is 425 ms of the stream.
        queue.flush(now: 0.5)
        XCTAssertEqual(queue.takeReports(), [Report(.flushed, playedMilliseconds: 425)])
    }

    func testASlowProviderSteeredEvery20msStillDrainsItsStreamLength() {
        // GPT-Live-shaped: 1.2 s start target, 100 ms chunks arriving at
        // 0.75× real time, so the rate policy stretches for most of it.
        var queue = PlaybackQueue(profile: .openAI)
        var reports: [Report] = []
        var pushed = 0
        var t = 0.0
        var nextArrival = 0.0
        while t < 12 {
            if pushed < 6_000, t >= nextArrival {
                _ = queue.push(chunk(100), now: t)
                pushed += 100
                nextArrival += 0.1 / 0.75
            }
            _ = queue.tick(now: t)
            queue.steer(now: t)
            reports += queue.takeReports()
            t += 0.02
        }
        XCTAssertGreaterThan(queue.stretchedSeconds, 1, "the rate really moved")
        XCTAssertEqual(reports.filter { $0.state == .started }.count, reports.filter { $0.state == .drained }.count)
        let played = reports.filter { $0.state == .drained }.map(\.playedMilliseconds).reduce(0, +)
        XCTAssertEqual(played, pushed, "every drained run adds up to the stream received, in stream time")
    }
}
