import XCTest
@testable import SheepdogKit

/// The Live Activity's state builder (`VoiceActivityState`).
final class VoiceActivityStateTests: XCTestCase {
    private func make(_ state: SessionState, muted: Bool = false, waiting: Int = 0, suspended: Bool = false) -> VoiceActivityState? {
        VoiceActivityState.make(state: state, muted: muted, waiting: waiting, voiceName: "Rex", suspendedInBackground: suspended)
    }

    func testNoSessionMeansNoActivity() {
        XCTAssertNil(make(.disconnected))
        XCTAssertNil(make(.disconnected, suspended: true))
    }

    func testEverySessionStateMapsToAPhase() {
        let expected: [SessionState: VoiceActivityState.Phase] = [
            .connecting: .connecting, .reconnecting: .connecting,
            .voiceOff: .paused, .audioPaused: .paused, .noMicrophone: .paused,
            .stalled: .thinking, .thinking: .thinking, .speaking: .speaking,
            .muted: .muted, .silent: .listening, .listening: .listening, .idle: .listening,
        ]
        for state in SessionState.allCases where state != .disconnected {
            XCTAssertEqual(make(state)?.phase, expected[state], "\(state)")
        }
    }

    func testTitles() {
        XCTAssertEqual(make(.listening)?.title, "Listening")
        XCTAssertEqual(make(.silent)?.title, "Listening")
        XCTAssertEqual(make(.speaking)?.title, "Speaking")
        XCTAssertEqual(make(.muted, muted: true)?.title, "Muted")
        XCTAssertEqual(make(.reconnecting)?.title, "Reconnecting…")
        XCTAssertEqual(make(.connecting)?.title, "Connecting…")
    }

    func testWaitingAgentsAreCountedAndWorded() {
        XCTAssertNil(make(.listening)?.waitingText)
        XCTAssertEqual(make(.listening, waiting: 1)?.waitingText, "1 agent waiting for an answer")
        let two = make(.listening, waiting: 2)
        XCTAssertEqual(two?.waiting, 2)
        XCTAssertEqual(two?.waitingText, "2 agents waiting for an answer")
        XCTAssertEqual(two?.accessibilityLabel, "Rex is listening. 2 agents waiting for an answer")
        XCTAssertEqual(two?.compactAccessibilityLabel, "Listening, 2 waiting")
        XCTAssertEqual(make(.listening, waiting: -3)?.waiting, 0)
    }

    func testMuteIsKeptWhileSpeakingOutranksIt() {
        let speaking = make(.speaking, muted: true)
        XCTAssertEqual(speaking?.phase, .speaking)
        XCTAssertEqual(speaking?.muted, true)
        XCTAssertEqual(speaking?.accessibilityLabel, "Rex is speaking. Microphone muted")
        XCTAssertEqual(speaking?.compactAccessibilityLabel, "Speaking, muted")

        let muted = make(.muted, muted: true)
        XCTAssertEqual(muted?.accessibilityLabel, "Microphone muted. Rex can still talk")
        XCTAssertEqual(muted?.compactAccessibilityLabel, "Muted")
    }

    func testPausedInTheBackgroundSaysSo() {
        let paused = make(.listening, suspended: true)
        XCTAssertEqual(paused?.phase, .paused)
        XCTAssertEqual(paused?.title, "Paused while locked")
        XCTAssertEqual(paused?.accessibilityLabel, "Paused while the phone is locked. Open Sheepdog to talk again")
        // Reconnecting is still reconnecting.
        XCTAssertEqual(make(.reconnecting, suspended: true)?.phase, .connecting)
    }

    func testPhaseRawValuesAreTheWireStrings() {
        // ContentState.phase carries these; the widget switches on them.
        XCTAssertEqual(VoiceActivityState.Phase.allCases.map(\.rawValue),
                       ["connecting", "listening", "speaking", "thinking", "muted", "paused"])
    }

    func testRefreshComesWellBeforeStale() {
        XCTAssertLessThan(VoiceActivityState.refreshInterval * 2, VoiceActivityState.staleAfter)
    }
}

/// `sheepdog://` links, the App Intents and the earbud remote (`AppRoute`).
final class AppRouteTests: XCTestCase {
    private func route(_ s: String) -> AppRoute? { AppRoute(url: URL(string: s)!) }

    func testParsesEveryRoute() {
        XCTAssertEqual(route("sheepdog://talk"), .talk)
        XCTAssertEqual(route("SHEEPDOG://Talk"), .talk)
        XCTAssertEqual(route("sheepdog:talk"), .talk)
        XCTAssertEqual(route("sheepdog://agent/w1:p2"), .agent("w1:p2"))
        XCTAssertEqual(route("sheepdog://agent/w1%3Ap2"), .agent("w1:p2"))
        XCTAssertEqual(route("sheepdog://mute?on=true"), .mute(true))
        XCTAssertEqual(route("sheepdog://mute?on=0"), .mute(false))
        XCTAssertEqual(route("sheepdog://mute"), .toggleMute)
        XCTAssertEqual(route("sheepdog://stop"), .stop)
        XCTAssertEqual(route("sheepdog://end"), .end)
    }

    func testRejectsWhatIsNotARoute() {
        XCTAssertNil(route("https://talk"))
        XCTAssertNil(route("sheepdog://"))
        XCTAssertNil(route("sheepdog://agent"))
        XCTAssertNil(route("sheepdog://agent/"))
        XCTAssertNil(route("sheepdog://mute?on=maybe"))
        XCTAssertNil(route("sheepdog://launch-missiles"))
    }

    func testURLsRoundTrip() {
        let all: [AppRoute] = [.talk, .agent("w1:p2"), .agent("pane with space"), .mute(true), .mute(false), .toggleMute, .stop, .end]
        for r in all {
            XCTAssertEqual(AppRoute(url: r.url), r, r.url.absoluteString)
        }
        XCTAssertEqual(AppRoute.talk.url.absoluteString, "sheepdog://talk")
    }

    func testTheIntentsURLsAreRoutes() {
        // What SheepdogIntents.swift sends (it cannot import SheepdogKit).
        XCTAssertEqual(route("sheepdog://talk"), .talk)
        XCTAssertEqual(route("sheepdog://mute?on=true"), .mute(true))
        XCTAssertEqual(route("sheepdog://mute?on=false"), .mute(false))
        XCTAssertEqual(route("sheepdog://end"), .end)
    }

    func testTalkStartsTheSessionOrUnmutesIt() {
        XCTAssertEqual(AppRoute.talk.actions(wantsConnection: false, muted: false), [.showConversation, .connect])
        XCTAssertEqual(AppRoute.talk.actions(wantsConnection: false, muted: true), [.showConversation, .connect])
        XCTAssertEqual(AppRoute.talk.actions(wantsConnection: true, muted: true), [.showConversation, .toggleMute])
        XCTAssertEqual(AppRoute.talk.actions(wantsConnection: true, muted: false), [.showConversation])
    }

    func testMuteIsIdempotentAndNeedsASession() {
        XCTAssertEqual(AppRoute.mute(true).actions(wantsConnection: true, muted: false), [.toggleMute])
        XCTAssertEqual(AppRoute.mute(true).actions(wantsConnection: true, muted: true), [])
        XCTAssertEqual(AppRoute.mute(false).actions(wantsConnection: true, muted: true), [.toggleMute])
        XCTAssertEqual(AppRoute.mute(false).actions(wantsConnection: true, muted: false), [])
        XCTAssertEqual(AppRoute.mute(true).actions(wantsConnection: false, muted: false), [])
        XCTAssertEqual(AppRoute.toggleMute.actions(wantsConnection: true, muted: true), [.toggleMute])
        XCTAssertEqual(AppRoute.toggleMute.actions(wantsConnection: false, muted: true), [])
    }

    func testStopEndAndAgent() {
        XCTAssertEqual(AppRoute.stop.actions(wantsConnection: true, muted: false), [.stop])
        XCTAssertEqual(AppRoute.stop.actions(wantsConnection: false, muted: false), [])
        XCTAssertEqual(AppRoute.end.actions(wantsConnection: true, muted: false), [.disconnect])
        XCTAssertEqual(AppRoute.end.actions(wantsConnection: false, muted: false), [])
        XCTAssertEqual(AppRoute.agent("w1:p2").actions(wantsConnection: false, muted: false), [.openAgent("w1:p2")])
    }

    func testEarbudRemote() {
        XCTAssertEqual(RemoteCommand.togglePlayPause.route, .toggleMute)
        XCTAssertEqual(RemoteCommand.pause.route, .mute(true))
        XCTAssertEqual(RemoteCommand.play.route, .mute(false))
        XCTAssertEqual(RemoteCommand.nextTrack.route, .stop)
        // Unmuted reads as playing, so the next press is "pause" = mute.
        XCTAssertEqual(RemoteCommand.playbackRate(muted: false), 1)
        XCTAssertEqual(RemoteCommand.playbackRate(muted: true), 0)
    }
}
