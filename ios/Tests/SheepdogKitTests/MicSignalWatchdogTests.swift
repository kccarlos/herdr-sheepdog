import XCTest
@testable import SheepdogKit

/// "Unmuted, but the microphone delivers digital zero" — the 25 s of
/// nothing on 2026-09-22 — is noticed after 3 s and cleared by the first
/// real sample.
final class MicSignalWatchdogTests: XCTestCase {
    func testThreeSecondsOfDigitalZeroIsNoSignal() {
        var dog = MicSignalWatchdog()
        dog.arm(at: 0)
        var t = 0.0
        while t < 2.9 {
            XCTAssertNil(dog.observe(peak: 0, at: t))
            t += 0.04
        }
        XCTAssertFalse(dog.noSignal)
        XCTAssertEqual(dog.observe(peak: 0, at: 3.0), true)
        XCTAssertTrue(dog.noSignal)
        XCTAssertEqual(dog.firings, 1)
        XCTAssertNil(dog.observe(peak: 0, at: 3.5), "reported once, not every buffer")
    }

    func testAnyRealSampleClearsIt() {
        var dog = MicSignalWatchdog()
        dog.arm(at: 0)
        dog.tick(at: 4)
        XCTAssertTrue(dog.noSignal)
        XCTAssertEqual(dog.observe(peak: 1, at: 4.1), false)
        XCTAssertFalse(dog.noSignal)
    }

    func testAQuietRoomIsNotNoSignal() {
        var dog = MicSignalWatchdog()
        dog.arm(at: 0)
        var t = 0.0
        while t < 20 {
            dog.observe(peak: 3, at: t) // a real room is never exactly zero
            t += 0.04
        }
        XCTAssertNil(dog.tick(at: 20))
        XCTAssertFalse(dog.noSignal)
    }

    func testNoBuffersAtAllIsNoSignalToo() {
        var dog = MicSignalWatchdog()
        dog.arm(at: 10)
        XCTAssertNil(dog.tick(at: 12))
        XCTAssertEqual(dog.tick(at: 13.1), true)
    }

    func testMutedOrStoppedNothingIsExpected() {
        var dog = MicSignalWatchdog()
        XCTAssertNil(dog.tick(at: 100), "never armed")
        dog.arm(at: 0)
        dog.tick(at: 5)
        XCTAssertTrue(dog.noSignal)
        dog.disarm()
        XCTAssertFalse(dog.noSignal)
        XCTAssertNil(dog.observe(peak: 0, at: 50))
        XCTAssertFalse(dog.isArmed)
    }

    func testUnmutingRestartsTheClock() {
        var dog = MicSignalWatchdog()
        dog.arm(at: 0)
        dog.observe(peak: 500, at: 1)
        dog.disarm() // mute at 1 s
        dog.arm(at: 10) // unmute at 10 s
        XCTAssertNil(dog.tick(at: 12), "the 9 s muted do not count")
        XCTAssertEqual(dog.tick(at: 13), true)
    }

    /// Hold-to-talk: the engine disarms when the gate closes and re-arms
    /// (fresh grace period) when it opens.
    func testClosedGateForTenSecondsIsNoAlarm() {
        var dog = MicSignalWatchdog()
        dog.arm(at: 0)
        dog.disarm() // gate closed
        var t = 0.0
        while t < 10 {
            XCTAssertNil(dog.observe(peak: 0, at: t))
            XCTAssertNil(dog.tick(at: t))
            t += 0.1
        }
        XCTAssertFalse(dog.noSignal)
    }

    func testOpenedGateWithZeroSamplesAlarmsAfterAFreshThreeSeconds() {
        var dog = MicSignalWatchdog()
        dog.arm(at: 0)
        dog.tick(at: 5)
        XCTAssertTrue(dog.noSignal)
        dog.disarm() // released: state cleared
        XCTAssertFalse(dog.noSignal)
        dog.arm(at: 100) // pressed again
        XCTAssertNil(dog.observe(peak: 0, at: 102.9))
        XCTAssertEqual(dog.observe(peak: 0, at: 103.0), true)
    }
}
