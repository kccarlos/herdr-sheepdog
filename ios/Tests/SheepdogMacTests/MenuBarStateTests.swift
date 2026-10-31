import XCTest
import SheepdogKit
@testable import SheepdogMac

/// The state table behind the menu-bar icon and the menu's first line.
final class MenuBarStateTests: XCTestCase {
    private func daemon(mode: String = "gemini", muted: Bool = false, speaking: Bool = false, connected: Bool = true, client: DaemonState.VoiceInfo.Client? = nil, questions: Int? = nil) -> DaemonState {
        let voice = DaemonState.VoiceInfo(mode: mode, status: "connected", muted: muted, speaking: speaking, clientConnected: connected, client: client)
        let open = questions.map { n in
            (0 ..< n).map { PendingAction(id: "q\($0)", paneID: "p\($0)", kind: "question") }
        }
        return DaemonState(agents: [], questions: open, voice: voice)
    }

    private let mac = DaemonState.VoiceInfo.Client(name: "sheepdog-mac", platform: "macos")
    private let phone = DaemonState.VoiceInfo.Client(name: "iPhone", platform: "ios")

    func testNoAnswerIsDaemonDown() {
        let s = MenuBarState(state: nil)
        XCTAssertEqual(s.kind, .daemonDown)
        XCTAssertEqual(s.line, "Sheepdog isn't running")
        XCTAssertTrue(s.isDimmed)
    }

    func testVoiceOffAndNobodyHoldingIt() {
        XCTAssertEqual(MenuBarState(state: daemon(mode: "off")).kind, .off)
        XCTAssertEqual(MenuBarState(state: daemon(connected: false)).kind, .off)
        XCTAssertEqual(MenuBarState(state: daemon(connected: false)).line, "Voice off")
    }

    func testVoiceElsewhereNamesTheHolder() {
        let s = MenuBarState(state: daemon(client: phone))
        XCTAssertEqual(s.kind, .voiceElsewhere(holder: "iPhone"))
        XCTAssertEqual(s.line, "Voice on iPhone")
        XCTAssertTrue(s.isDimmed)
    }

    func testAHolderFromADaemonThatDoesNotNameItIsStillElsewhere() {
        let s = MenuBarState(state: daemon())
        XCTAssertEqual(s.kind, .voiceElsewhere(holder: nil))
        XCTAssertEqual(s.line, "Voice on another device")
    }

    func testThisMacMutedSpeakingListening() {
        XCTAssertEqual(MenuBarState(state: daemon(muted: true, client: mac)).kind, .muted)
        XCTAssertEqual(MenuBarState(state: daemon(speaking: true, client: mac)).kind, .speaking)
        XCTAssertEqual(MenuBarState(state: daemon(client: mac)).kind, .listening)
        XCTAssertFalse(MenuBarState(state: daemon(client: mac)).isDimmed)
    }

    func testMutedWinsOverSpeaking() {
        XCTAssertEqual(MenuBarState(state: daemon(muted: true, speaking: true, client: mac)).kind, .muted)
    }

    func testElsewhereWinsOverThePhonesMute() {
        XCTAssertEqual(MenuBarState(state: daemon(muted: true, client: phone)).kind, .voiceElsewhere(holder: "iPhone"))
    }

    func testNeedsYouBadge() {
        XCTAssertEqual(MenuBarState(state: daemon(client: mac, questions: 0)).needsYou, 0)
        XCTAssertEqual(MenuBarState(state: daemon(client: mac, questions: 3)).needsYou, 3)
        XCTAssertEqual(MenuBarState(state: daemon(client: mac, questions: 3)).line, "Listening · 3 need you")
        XCTAssertEqual(MenuBarState(state: daemon(client: mac, questions: 1)).line, "Listening · 1 needs you")
    }

    func testTheBadgeFallsBackToThePendingAction() {
        var s = daemon(client: mac)
        s.pendingAction = PendingAction(id: "q", paneID: "p", kind: "question")
        XCTAssertEqual(MenuBarState(state: s).needsYou, 1)
    }

    func testTheBadgeShowsWithVoiceOffToo() {
        XCTAssertEqual(MenuBarState(state: daemon(mode: "off", questions: 2)).line, "Voice off · 2 need you")
    }

    func testDownHasNoBadge() {
        XCTAssertEqual(MenuBarState(state: nil).needsYou, 0)
    }
}
