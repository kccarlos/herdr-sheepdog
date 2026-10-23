import XCTest
@testable import SheepdogKit

final class CapsuleModeTests: XCTestCase {
    func testOpenMicTable() {
        let table: [(SessionState, CapsuleMode)] = [
            (.disconnected, .offline),
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
        XCTAssertEqual(CapsuleMode.resolve(state: .disconnected, talkMode: .holdToTalk), .offline)
    }

    func testControls() {
        XCTAssertTrue(CapsuleMode.live.hasAccent)
        XCTAssertEqual(CapsuleMode.allCases.filter(\.hasAccent), [.live], "one accent")
        XCTAssertEqual(Set(CapsuleMode.allCases.filter(\.showsStop)), [.speaking, .thinking, .stalled])
        XCTAssertEqual(Set(CapsuleMode.allCases.filter { !$0.showsMic }), [.noMicrophone, .offline, .voiceOff, .hold, .holding])
        XCTAssertTrue(CapsuleMode.reconnecting.micDimmed)
        XCTAssertEqual(CapsuleMode.allCases.count, 13)
    }
}
