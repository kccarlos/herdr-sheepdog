import XCTest
@testable import SheepdogKit

/// The same cases as the browser's `web/voice/audio-util.test.mjs`, at the
/// app's 40 ms frame instead of the page's 20 ms one.
final class SilenceGateTests: XCTestCase {
    private func frame(_ peak: Int16, count: Int = AudioContract.frameSamples) -> [Int16] {
        var f = [Int16](repeating: 0, count: count)
        f[count / 2] = peak
        return f
    }

    func testClosesAfterHoldAndCountsWithheldFrames() {
        var gate = SilenceGate(floor: 8, holdMilliseconds: 300, prerollMilliseconds: 100, frameMilliseconds: 40)
        // 7 quiet frames (280 ms) still pass through.
        for i in 0 ..< 7 {
            let out = gate.push(frame(3))
            XCTAssertEqual(out.send.count, 1, "frame \(i) should be sent")
            XCTAssertNil(out.transition)
        }
        // The 8th (320 ms ≥ 300 ms) closes the gate and is withheld.
        var out = gate.push(frame(0))
        XCTAssertEqual(out.transition, .closed)
        XCTAssertTrue(out.send.isEmpty)
        XCTAssertTrue(gate.isGated)
        for _ in 0 ..< 50 {
            out = gate.push(frame(7))
            XCTAssertTrue(out.send.isEmpty)
            XCTAssertNil(out.transition)
        }
        XCTAssertEqual(gate.gatedFrames, 51)
    }

    func testReopensOnTheFirstLoudFrameWithPrerollInFront() {
        var gate = SilenceGate(floor: 20, holdMilliseconds: 300, prerollMilliseconds: 120, frameMilliseconds: 40)
        for _ in 0 ..< 20 { gate.push(frame(0)) }
        XCTAssertTrue(gate.isGated)
        // Tag the withheld frames (all below the floor) so the order shows.
        for i in 1 ... 9 { gate.push(frame(Int16(i))) }
        let loud = frame(500)
        let out = gate.push(loud)
        XCTAssertEqual(out.transition, .opened)
        XCTAssertFalse(gate.isGated)
        // 120 ms / 40 ms = 3 pre-roll frames — the most recent three — then
        // the loud one.
        XCTAssertEqual(out.send.count, 4)
        XCTAssertEqual(out.send.prefix(3).map(SilenceGate.peak(of:)), [7, 8, 9])
        XCTAssertEqual(out.send[3], loud)
        // Loud audio keeps flowing one frame at a time, quiet timer reset.
        XCTAssertEqual(gate.push(frame(300)).send.count, 1)
        XCTAssertEqual(gate.quietMilliseconds, 0)
    }

    func testAShortPauseBelowTheHoldNeverGates() {
        var gate = SilenceGate(floor: 8, holdMilliseconds: 300, frameMilliseconds: 40)
        var transitions = 0
        for _ in 0 ..< 5 {
            for _ in 0 ..< 5 where gate.push(frame(0)).transition != nil { transitions += 1 } // 200 ms quiet
            if gate.push(frame(1000)).transition != nil { transitions += 1 }
        }
        XCTAssertEqual(transitions, 0)
        XCTAssertEqual(gate.gatedFrames, 0)
    }

    func testFloorIsInclusiveAndZeroPrerollSendsNothingOld() {
        var gate = SilenceGate(floor: 8, holdMilliseconds: 80, prerollMilliseconds: 0, frameMilliseconds: 40)
        XCTAssertEqual(gate.push(frame(8)).send.count, 1, "a peak at the floor counts as sound")
        gate.push(frame(7))
        XCTAssertEqual(gate.push(frame(7)).transition, .closed)
        let out = gate.push(frame(-9)) // negative peaks count too
        XCTAssertEqual(out.transition, .opened)
        XCTAssertEqual(out.send.count, 1)
    }

    func testResetReopensWithoutATransition() {
        var gate = SilenceGate(frameMilliseconds: 40)
        for _ in 0 ..< 20 { gate.push(frame(0)) }
        XCTAssertTrue(gate.isGated)
        gate.reset()
        XCTAssertFalse(gate.isGated)
        XCTAssertEqual(gate.quietMilliseconds, 0)
        XCTAssertEqual(gate.push(frame(0)).send.count, 1, "the hold starts again from zero")
    }

    func testPeakHandlesTheInt16Minimum() {
        XCTAssertEqual(SilenceGate.peak(of: [0, -32_768, 5]), 32_768)
    }
}
