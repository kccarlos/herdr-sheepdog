import XCTest
@testable import SheepdogKit

/// The gate as the audio engine drives it: detector choice, hold-to-talk,
/// and transitions that always match what the daemon was told.
final class CaptureGateTests: XCTestCase {
    private let quiet = [Int16](repeating: 0, count: AudioContract.frameSamples)
    private func noise(_ rng: inout VADTestAudio.RNG, _ amplitude: Double = 40) -> [Int16] {
        (0 ..< AudioContract.frameSamples).map { _ in Int16(rng.gaussian() * amplitude) }
    }

    private let tone = (0 ..< AudioContract.frameSamples).map { Int16(8_000 * sin(2 * Double.pi * 700 * Double($0) / 16_000)) }

    func testTheDefaultIsTheVoiceActivityDetector() {
        var gate = CaptureGate()
        XCTAssertEqual(gate.detector, .voiceActivity)
        var rng = VADTestAudio.RNG(seed: 1)
        var closes = 0
        // Room noise well above the old 8 LSB floor: the peak gate would
        // never close on this, the detector does.
        for _ in 0 ..< 50 where gate.push(noise(&rng, 400)).transition == .closed { closes += 1 }
        XCTAssertEqual(closes, 1)
        XCTAssertTrue(gate.isGated)
        XCTAssertFalse(gate.diagnostics.isOpen)
        XCTAssertNotNil(gate.diagnostics.floorDBFS)
    }

    func testThePeakGateIsStillThereForComparison() {
        var gate = CaptureGate(detector: .peak)
        var rng = VADTestAudio.RNG(seed: 1)
        for _ in 0 ..< 50 { XCTAssertNil(gate.push(noise(&rng, 400)).transition, "400 LSB of room noise keeps the peak gate open") }
        XCTAssertFalse(gate.isGated)
        XCTAssertNil(gate.diagnostics.floorDBFS)
        for _ in 0 ..< 7 { gate.push(quiet) }
        XCTAssertEqual(gate.push(quiet).transition, .closed, "and it closes on digital silence after 300 ms, as before")
    }

    func testSwitchingDetectorMidSessionNeverLeavesTheDaemonOutOfStep() {
        var gate = CaptureGate(detector: .voiceActivity)
        var rng = VADTestAudio.RNG(seed: 2)
        for _ in 0 ..< 50 { gate.push(noise(&rng, 400)) }
        XCTAssertTrue(gate.isGated)
        gate.detector = .peak
        // The peak gate starts open, and the daemon is told so.
        let out = gate.push(noise(&rng, 400))
        XCTAssertEqual(out.transition, .opened)
        XCTAssertEqual(out.send.count, 1)
        XCTAssertFalse(gate.isGated)
    }

    func testHoldToTalkOpensOnPressWithPrerollAndClosesOnRelease() {
        var gate = CaptureGate()
        gate.manual = false // hold-to-talk, not held
        var out = gate.push(tone)
        XCTAssertEqual(out.transition, .closed, "not holding means nothing is sent, and the daemon hears so")
        XCTAssertTrue(out.send.isEmpty)
        for _ in 0 ..< 10 { XCTAssertTrue(gate.push(tone).send.isEmpty, "even loud speech waits for the press") }

        gate.manual = true
        out = gate.push(tone)
        XCTAssertEqual(out.transition, .opened)
        XCTAssertEqual(out.send.count, 4, "100 ms of pre-roll, then the frame")
        out = gate.push(quiet)
        XCTAssertNil(out.transition)
        XCTAssertEqual(out.send.count, 1, "held open: silence is sent too, the user is mid-thought")

        gate.manual = false
        out = gate.push(tone)
        XCTAssertEqual(out.transition, .closed)
        XCTAssertTrue(out.send.isEmpty)
    }

    func testLeavingHoldToTalkHandsBackToTheDetector() {
        var gate = CaptureGate()
        gate.manual = false
        gate.push(quiet)
        XCTAssertTrue(gate.isGated)
        gate.manual = nil
        let out = gate.push(quiet)
        XCTAssertEqual(out.transition, .opened, "the detector starts open, like a new client")
        XCTAssertFalse(gate.isGated)
    }

    func testResetOpensWithoutATransitionUnlessHeldClosed() {
        var gate = CaptureGate()
        var rng = VADTestAudio.RNG(seed: 3)
        for _ in 0 ..< 50 { gate.push(noise(&rng, 400)) }
        XCTAssertTrue(gate.isGated)
        gate.reset()
        XCTAssertFalse(gate.isGated)
        XCTAssertNil(gate.push(noise(&rng, 400)).transition)

        gate.manual = false
        gate.reset()
        XCTAssertTrue(gate.isGated, "hold-to-talk not held stays closed across a reset")
    }

    func testDiagnosticsFormatReadsLikeAMeter() {
        XCTAssertEqual(CaptureGate.Diagnostics.format(dBFS: -41.4), "−41 dBFS")
        XCTAssertEqual(CaptureGate.Diagnostics.format(dBFS: nil), "—")
        XCTAssertEqual(CaptureGate.Diagnostics.format(dBFS: VoiceActivityDetector.silenceDBFS), "—")
    }
}
