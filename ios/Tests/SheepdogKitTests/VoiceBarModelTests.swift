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

    func testVoiceOffAndNoMicrophoneHideMuteButStillOfferEnd() {
        // Nothing can hear the microphone: no mic button beside an orb that
        // already says so (two mic icons, review A4).
        for state in [SessionState.voiceOff, .noMicrophone] {
            let m = VoiceBarModel(state: state)
            XCTAssertEqual(m.orbAction, .none, "\(state)")
            XCTAssertEqual(m.muteAvailability, .hidden, "\(state)")
            XCTAssertTrue(m.endVisible, "\(state)")
        }
        XCTAssertEqual(VoiceBarModel.statusLine(state: .noMicrophone), "No microphone")
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

    // MARK: - Hold to talk

    func testHoldToTalkMakesTheOrbTheTalkButtonAndHidesMute() {
        for state in [SessionState.idle, .listening, .silent, .muted, .thinking, .stalled, .speaking] {
            let m = VoiceBarModel(state: state, talkMode: .holdToTalk)
            XCTAssertEqual(m.orbAction, .holdToTalk, "\(state)")
            XCTAssertEqual(m.muteAvailability, .hidden, "\(state): not holding is muted, so there is no Mute")
            XCTAssertTrue(m.endVisible, "\(state)")
        }
    }

    func testHoldToTalkLeavesConnectingAndDisconnectedAlone() {
        XCTAssertEqual(VoiceBarModel(state: .disconnected, talkMode: .holdToTalk), VoiceBarModel(state: .disconnected))
        for state in [SessionState.connecting, .reconnecting, .voiceOff] {
            let m = VoiceBarModel(state: state, talkMode: .holdToTalk)
            XCTAssertEqual(m.orbAction, .none, "\(state)")
            XCTAssertEqual(m.muteAvailability, .hidden, "\(state)")
        }
    }

    func testOpenMicIsTheDefaultAndUnchanged() {
        for state in SessionState.allCases {
            XCTAssertEqual(VoiceBarModel(state: state, talkMode: .openMic), VoiceBarModel(state: state))
        }
    }

    // MARK: - Words

    func testTheStatusLineSaysWhatToDo() {
        XCTAssertEqual(VoiceBarModel.statusLine(state: .listening), "Listening — just talk")
        XCTAssertEqual(VoiceBarModel.statusLine(state: .silent), "Listening — just talk", "a closed gate is still listening")
        XCTAssertEqual(VoiceBarModel.statusLine(state: .muted), "Muted — tap to unmute")
        XCTAssertEqual(VoiceBarModel.statusLine(state: .voiceOff), "Voice is off on the daemon")
        XCTAssertEqual(VoiceBarModel.statusLine(state: .speaking), "Speaking")
        XCTAssertEqual(VoiceBarModel.statusLine(state: .disconnected), "Tap the orb to connect")
        XCTAssertEqual(VoiceBarModel.statusLine(state: .disconnected, talkMode: .holdToTalk), "Tap the orb to connect")
    }

    func testTheStatusLineInHoldToTalk() {
        XCTAssertEqual(VoiceBarModel.statusLine(state: .silent, talkMode: .holdToTalk), "Hold the orb to talk")
        XCTAssertEqual(VoiceBarModel.statusLine(state: .listening, talkMode: .holdToTalk, holding: true), "Talking — release to send")
        XCTAssertEqual(VoiceBarModel.statusLine(state: .speaking, talkMode: .holdToTalk), "Speaking")
        XCTAssertEqual(VoiceBarModel.statusLine(state: .speaking, talkMode: .holdToTalk, holding: true), "Talking — release to send")
        XCTAssertEqual(VoiceBarModel.statusLine(state: .connecting, talkMode: .holdToTalk, holding: true), "Connecting…")
    }

    func testNoMicrophoneSignalOutranksListening() {
        for state in [SessionState.listening, .silent, .idle] {
            XCTAssertEqual(VoiceBarModel.statusLine(state: state, noSignal: true), "No microphone signal — check the mic", "\(state)")
            XCTAssertEqual(VoiceBarModel.statusLine(state: state, talkMode: .holdToTalk, noSignal: true), "No microphone signal — check the mic", "\(state)")
        }
        XCTAssertEqual(VoiceBarModel.statusLine(state: .muted, noSignal: true), "Muted — tap to unmute", "muted: silence is expected")
        XCTAssertEqual(VoiceBarModel.statusLine(state: .speaking, noSignal: true), "Speaking")
    }

    func testTheMicButtonAlwaysNamesItsAction() {
        XCTAssertEqual(VoiceBarModel.muteLabel(muted: false), "Mute microphone")
        XCTAssertEqual(VoiceBarModel.muteLabel(muted: true), "Unmute microphone")
        XCTAssertEqual(VoiceBarModel.coachMark(voiceName: "Sheepdog"), "Just talk — Sheepdog is listening. Tap the mic only to mute.")
        XCTAssertTrue(VoiceBarModel.isListening(.listening))
        XCTAssertTrue(VoiceBarModel.isListening(.silent))
        XCTAssertFalse(VoiceBarModel.isListening(.muted))
        XCTAssertFalse(VoiceBarModel.isListening(.voiceOff))
    }
}
