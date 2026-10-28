import XCTest
@testable import SheepdogKit

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
        XCTAssertEqual(route("sheepdog://window/w1:p2"), .window("w1:p2", computer: nil))
        XCTAssertEqual(route("sheepdog://window/w1:p2?computer=home"), .window("w1:p2", computer: "home"))
        XCTAssertEqual(route("sheepdog://window/w1:p2?computer="), .window("w1:p2", computer: nil))
    }

    func testRejectsWhatIsNotARoute() {
        XCTAssertNil(route("sheepdog://window/"))
        XCTAssertNil(route("https://talk"))
        XCTAssertNil(route("sheepdog://"))
        XCTAssertNil(route("sheepdog://agent"))
        XCTAssertNil(route("sheepdog://agent/"))
        XCTAssertNil(route("sheepdog://mute?on=maybe"))
        XCTAssertNil(route("sheepdog://launch-missiles"))
    }

    func testURLsRoundTrip() {
        let all: [AppRoute] = [.talk, .agent("w1:p2"), .agent("pane with space"), .window("w1:p2", computer: nil), .window("w1:p2", computer: "a b&c"), .mute(true), .mute(false), .toggleMute, .stop, .end]
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

    func testToggleTalkIsHoldToTalkOnly() {
        XCTAssertEqual(route("sheepdog://toggle-talk"), .toggleTalk)
        XCTAssertEqual(AppRoute.toggleTalk.url.absoluteString, "sheepdog://toggle-talk")
        XCTAssertEqual(AppRoute.toggleTalk.actions(wantsConnection: true, muted: false, talkMode: .holdToTalk), [.toggleTalk])
        XCTAssertEqual(AppRoute.toggleTalk.actions(wantsConnection: true, muted: false, talkMode: .openMic), [])
        XCTAssertEqual(AppRoute.toggleTalk.actions(wantsConnection: false, muted: false, talkMode: .holdToTalk), [])
        // End works in both modes.
        XCTAssertEqual(AppRoute.end.actions(wantsConnection: true, muted: false, talkMode: .holdToTalk), [.disconnect])
        // Mute stays a no-op in hold mode: the core never mutes there.
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
        XCTAssertEqual(AppRoute.window("w1:p2", computer: "home").actions(wantsConnection: false, muted: false), [.openWindow("w1:p2", computer: "home")])
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
