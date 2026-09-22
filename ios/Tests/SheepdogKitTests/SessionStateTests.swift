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
}
