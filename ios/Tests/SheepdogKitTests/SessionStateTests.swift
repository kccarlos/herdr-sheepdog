import XCTest
@testable import SheepdogKit

final class SessionStateTests: XCTestCase {
    func testASocketThatIsNotOpenBeatsEverythingElse() {
        XCTAssertEqual(SessionState.resolve(.init(socket: .closed, voice: .connected)), .disconnected)
        XCTAssertEqual(SessionState.resolve(.init(socket: .connecting)), .connecting)
        XCTAssertEqual(SessionState.resolve(.init(socket: .reconnecting, voice: .connected, speaking: true)), .reconnecting)
    }

    func testVoiceOffIsSaidHonestlyRatherThanShownAsListening() {
        let state = SessionState.resolve(.init(socket: .open, voice: .closed, closedReason: "voice_off"))
        XCTAssertEqual(state, .voiceOff)
        XCTAssertFalse(state.capturing, "the microphone stays shut when nothing can hear it")
    }

    func testAVoiceThatIsComingBackReadsAsConnecting() {
        XCTAssertEqual(SessionState.resolve(.init(socket: .open, voice: .reconnecting)), .connecting)
        XCTAssertEqual(SessionState.resolve(.init(socket: .open, voice: .connecting)), .connecting)
    }

    func testSpeakingWinsOverEverythingTheSessionIsAlsoDoing() {
        let inputs = SessionState.Inputs(socket: .open, voice: .connected, stalled: true, muted: true, gateClosed: true, speaking: true, thinking: true)
        XCTAssertEqual(SessionState.resolve(inputs), .speaking)
    }

    func testTheRestOfThePriorityOrder() {
        func state(_ change: (inout SessionState.Inputs) -> Void) -> SessionState {
            var i = SessionState.Inputs(socket: .open, voice: .connected)
            change(&i)
            return SessionState.resolve(i)
        }
        XCTAssertEqual(state { $0.stalled = true; $0.thinking = true }, .stalled)
        XCTAssertEqual(state { $0.thinking = true; $0.muted = true }, .thinking)
        XCTAssertEqual(state { $0.muted = true; $0.gateClosed = true }, .muted)
        XCTAssertEqual(state { $0.gateClosed = true }, .silent)
        XCTAssertEqual(state { _ in }, .listening)
    }

    func testAnOpenSocketWithNoStatusYetIsIdleNotListening() {
        XCTAssertEqual(SessionState.resolve(.init(socket: .open)), .idle)
    }

    func testCapturingStatesAreExactlyTheOnesThatCanHear() {
        let capturing = SessionState.allCases.filter(\.capturing)
        XCTAssertEqual(Set(capturing), [.listening, .silent, .speaking, .thinking, .stalled, .idle])
    }

    func testEveryStateHasItsOwnVoiceOverSentence() {
        let labels = SessionState.allCases.map { $0.accessibilityLabel(voiceName: "Rex") }
        XCTAssertEqual(Set(labels).count, SessionState.allCases.count)
        XCTAssertTrue(labels.allSatisfy { !$0.isEmpty })
        XCTAssertEqual(Set(SessionState.allCases.map(\.title)).count, SessionState.allCases.count)
    }

    // MARK: - The orb while muted

    func testMutedWhileTheDogTalksStaysSpeakingWithABadge() {
        let talking = SessionState.resolve(.init(socket: .open, voice: .connected, muted: true, speaking: true))
        XCTAssertEqual(talking, .speaking)
        XCTAssertTrue(SessionState.showsMuteBadge(talking, muted: true))
        let thinking = SessionState.resolve(.init(socket: .open, voice: .connected, muted: true, thinking: true))
        XCTAssertEqual(thinking, .thinking)
        XCTAssertTrue(SessionState.showsMuteBadge(thinking, muted: true))
    }

    func testTheBadgeOnlyAppearsWhenMuteIsNotAlreadyTheOrb() {
        XCTAssertFalse(SessionState.showsMuteBadge(.muted, muted: true), "the muted orb already says so")
        XCTAssertFalse(SessionState.showsMuteBadge(.speaking, muted: false))
        XCTAssertFalse(SessionState.showsMuteBadge(.thinking, muted: false))
        for state in SessionState.allCases where state != .speaking && state != .thinking {
            XCTAssertFalse(SessionState.showsMuteBadge(state, muted: true), "\(state)")
        }
    }

    // MARK: - Speaking hold

    func testABriefPlaybackGapKeepsTheOrbSpeaking() {
        var hold = SpeakingHold()
        var inputs = SessionState.Inputs(socket: .open, voice: .connected, muted: true)
        func orb(playing: Bool, at t: TimeInterval) -> SessionState {
            inputs.speaking = hold.update(playing: playing, now: t)
            return SessionState.resolve(inputs)
        }
        XCTAssertEqual(orb(playing: true, at: 0), .speaking)
        XCTAssertEqual(orb(playing: true, at: 2), .speaking)
        // An underrun: 300 ms of nothing, then more of the reply.
        XCTAssertEqual(orb(playing: false, at: 2.0), .speaking, "no flick to Muted")
        XCTAssertEqual(orb(playing: false, at: 2.3), .speaking)
        XCTAssertEqual(orb(playing: true, at: 2.3), .speaking)
        // The reply really ends: the orb follows 800 ms later.
        XCTAssertEqual(orb(playing: false, at: 4.0), .speaking)
        XCTAssertEqual(orb(playing: false, at: 4.79), .speaking)
        XCTAssertEqual(orb(playing: false, at: 4.81), .muted)
        XCTAssertEqual(orb(playing: false, at: 9), .muted)
    }

    func testTheHoldIsMeasuredFromWhenPlaybackStoppedNotStarted() {
        var hold = SpeakingHold()
        XCTAssertTrue(hold.update(playing: true, now: 0))
        // Stopped long after it started: the hold still runs its full 800 ms.
        XCTAssertTrue(hold.update(playing: false, now: 10))
        XCTAssertTrue(hold.speaking(at: 10.5))
        XCTAssertFalse(hold.speaking(at: 10.81))
    }

    func testStopOrFlushDropsTheHoldAtOnce() {
        var hold = SpeakingHold()
        hold.update(playing: true, now: 0)
        hold.update(playing: false, now: 1)
        hold.reset()
        XCTAssertFalse(hold.speaking(at: 1.01), "Stop means stop: the orb does not linger")
        XCTAssertNil(hold.stoppedAt)
    }

    func testNothingPlayedMeansNothingHeld() {
        var hold = SpeakingHold()
        XCTAssertFalse(hold.update(playing: false, now: 0))
    }
}
