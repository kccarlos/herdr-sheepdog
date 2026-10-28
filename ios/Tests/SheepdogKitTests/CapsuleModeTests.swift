import XCTest
@testable import SheepdogKit

final class CapsuleModeTests: XCTestCase {
    func testOpenMicTable() {
        let table: [(SessionState, CapsuleMode)] = [
            (.disconnected, .stopped),
            (.connecting, .reconnecting),
            (.reconnecting, .reconnecting),
            (.voiceOff, .voiceOff),
            (.audioPaused, .paused),
            (.stalled, .stalled),
            (.speaking, .speaking),
            (.thinking, .thinking),
            (.noMicrophone, .noMicrophone),
            (.muted, .muted),
            (.silent, .live),
            (.listening, .live),
            (.idle, .live),
        ]
        for (state, mode) in table {
            XCTAssertEqual(CapsuleMode.resolve(state: state), mode, "\(state)")
        }
        XCTAssertEqual(table.count, SessionState.allCases.count, "every state is mapped")
    }

    func testAnOpenQuestionQuietsTheListeningOrbOnly() {
        XCTAssertEqual(CapsuleMode.resolve(state: .listening, questionOpen: true), .quiet)
        XCTAssertEqual(CapsuleMode.resolve(state: .speaking, questionOpen: true), .speaking)
        XCTAssertEqual(CapsuleMode.resolve(state: .muted, questionOpen: true), .muted)
    }

    func testHoldToTalk() {
        XCTAssertEqual(CapsuleMode.resolve(state: .listening, talkMode: .holdToTalk), .hold)
        XCTAssertEqual(CapsuleMode.resolve(state: .muted, talkMode: .holdToTalk), .hold)
        XCTAssertEqual(CapsuleMode.resolve(state: .listening, talkMode: .holdToTalk, holding: true), .holding)
        XCTAssertEqual(CapsuleMode.resolve(state: .speaking, talkMode: .holdToTalk, holding: true), .holding)
        XCTAssertEqual(CapsuleMode.resolve(state: .speaking, talkMode: .holdToTalk), .speaking)
        XCTAssertEqual(CapsuleMode.resolve(state: .disconnected, talkMode: .holdToTalk), .stopped)
    }

    func testControls() {
        XCTAssertTrue(CapsuleMode.live.hasAccent)
        XCTAssertEqual(Set(CapsuleMode.allCases.filter(\.hasAccent)), [.live, .stopped], "one accent: these two never show together")
        XCTAssertEqual(Set(CapsuleMode.allCases.filter(\.showsStop)), [.speaking, .thinking, .stalled])
        XCTAssertEqual(Set(CapsuleMode.allCases.filter { !$0.showsMic }), [.noMicrophone, .offline, .voiceOff, .hold, .holding, .stopped, .stoppedQuiet, .elsewhere])
        XCTAssertTrue(CapsuleMode.reconnecting.micDimmed)
        XCTAssertEqual(CapsuleMode.allCases.count, 16)
    }

    // MARK: - Stopped, offline, elsewhere

    func testNoSessionIsStoppedWhenTheComputerAnswersAndOfflineWhenItDoesNot() {
        XCTAssertEqual(CapsuleMode.resolve(state: .disconnected), .stopped)
        XCTAssertEqual(CapsuleMode.resolve(state: .disconnected, questionOpen: true), .stoppedQuiet)
        XCTAssertEqual(CapsuleMode.resolve(state: .disconnected, computerUnreachable: true), .offline)
        XCTAssertEqual(CapsuleMode.resolve(state: .disconnected, questionOpen: true, computerUnreachable: true), .offline)
    }

    func testReachabilityOnlyChangesTheNoSessionLook() {
        for state in SessionState.allCases where state != .disconnected {
            XCTAssertEqual(
                CapsuleMode.resolve(state: state, computerUnreachable: true),
                CapsuleMode.resolve(state: state),
                "\(state): a live session keeps its own look"
            )
        }
    }

    func testAnotherWindowHoldingTheAudioWinsOverEveryState() {
        for state in SessionState.allCases {
            XCTAssertEqual(CapsuleMode.resolve(state: state, audioElsewhere: true), .elsewhere, "\(state)")
        }
        XCTAssertEqual(CapsuleMode.resolve(state: .speaking, talkMode: .holdToTalk, holding: true, audioElsewhere: true), .elsewhere)
    }

    func testStatusOverridesAndUseHere() {
        XCTAssertEqual(CapsuleMode.stopped.statusOverride, "Not talking · tap the orb")
        XCTAssertEqual(CapsuleMode.stoppedQuiet.statusOverride, "Not talking · tap the orb")
        XCTAssertEqual(CapsuleMode.elsewhere.statusOverride, "In another window")
        XCTAssertEqual(Set(CapsuleMode.allCases.filter { $0.statusOverride != nil }), [.stopped, .stoppedQuiet, .elsewhere])
        XCTAssertEqual(CapsuleMode.allCases.filter(\.showsUseHere), [.elsewhere])
    }
}
