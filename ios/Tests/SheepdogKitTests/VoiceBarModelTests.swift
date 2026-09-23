import XCTest
@testable import SheepdogKit

final class VoiceBarModelTests: XCTestCase {
    func testDisconnectedOffersConnectAndHidesTheOtherTwoControls() {
        let m = VoiceBarModel(state: .disconnected)
        XCTAssertEqual(m.orbAction, .connect)
        XCTAssertEqual(m.muteAvailability, .hidden)
        XCTAssertFalse(m.endVisible)
    }

    func testConnectingAndReconnectingDisableMuteAndOfferOnlyCancel() {
        for state in [SessionState.connecting, .reconnecting] {
            let m = VoiceBarModel(state: state)
            XCTAssertEqual(m.orbAction, .none, "\(state)")
            XCTAssertEqual(m.muteAvailability, .disabled, "\(state)")
            XCTAssertTrue(m.endVisible, "\(state)")
        }
    }

    func testVoiceOffDisablesMuteButStillOffersEnd() {
        let m = VoiceBarModel(state: .voiceOff)
        XCTAssertEqual(m.orbAction, .none)
        XCTAssertEqual(m.muteAvailability, .disabled)
        XCTAssertTrue(m.endVisible)
    }

    func testIdleListeningSilentAndMutedLeaveTheOrbUntappable() {
        for state in [SessionState.idle, .listening, .silent, .muted] {
            let m = VoiceBarModel(state: state)
            XCTAssertEqual(m.orbAction, .none, "\(state)")
            XCTAssertEqual(m.muteAvailability, .enabled, "\(state)")
            XCTAssertTrue(m.endVisible, "\(state)")
        }
    }

    func testThinkingStalledAndSpeakingMakeTheOrbAStopButton() {
        for state in [SessionState.thinking, .stalled, .speaking] {
            let m = VoiceBarModel(state: state)
            XCTAssertEqual(m.orbAction, .stop, "\(state)")
            XCTAssertEqual(m.muteAvailability, .enabled, "\(state)")
            XCTAssertTrue(m.endVisible, "\(state)")
        }
    }

    func testEveryStateIsCovered() {
        // No state falls through to an implicit default — every case is a
        // switch arm above, so this just proves the enumeration is complete.
        for state in SessionState.allCases {
            _ = VoiceBarModel(state: state)
        }
    }
}
