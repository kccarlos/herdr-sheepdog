@testable import SheepdogKit
import XCTest

/// The words a stranger reads when the phone cannot reach their daemon:
/// each failure names a plain fix and, where one exists, the page that
/// explains it. And the app only promises notifications its daemon can send.
final class SetupHelpTests: XCTestCase {
    func testUnreachableNamesSameWiFiOrTailscale() {
        let refused = DaemonError.unreachable(endpoint: "192.168.1.20:7777", detail: "Nothing is listening on that port.")
        XCTAssertEqual(refused.hint, "Nothing is listening on that port. " + SetupHelp.unreachableFix)
        XCTAssertTrue(SetupHelp.unreachableFix.contains("same network"))
        XCTAssertTrue(SetupHelp.unreachableFix.contains("same Wi-Fi"))
        XCTAssertEqual(DaemonError.unreachable(endpoint: "x:1", detail: "").hint, SetupHelp.unreachableFix)
        XCTAssertEqual(SetupHelp.helpURL(for: refused), SetupHelp.remoteAccessURL)
    }

    func testARefusedPhoneSaysPairAgain() {
        let error = DaemonError.unauthorized(endpoint: "192.168.1.20:7777")
        XCTAssertEqual(error.hint, SetupHelp.pairFix)
        XCTAssertTrue(SetupHelp.pairFix.contains("sheepdog pair"))
        XCTAssertEqual(SetupHelp.helpURL(for: error), SetupHelp.remoteAccessURL)
    }

    func testNotSheepdogAndOtherErrors() {
        XCTAssertEqual(DaemonError.notSheepdog(endpoint: "x:1").hint, SetupHelp.notSheepdogFix)
        XCTAssertNil(SetupHelp.helpURL(for: .pendingGone))
        XCTAssertNil(SetupHelp.helpURL(for: .http(status: 500, endpoint: "x:1")))
    }

    /// No hint points at the old manual setup (`sheepdog serve`'s banner).
    func testHintsDoNotSendStrangersToTheOldSetup() {
        let errors: [DaemonError] = [
            .unreachable(endpoint: "x:1", detail: ""), .unauthorized(endpoint: "x:1"), .notSheepdog(endpoint: "x:1"),
        ]
        for error in errors {
            let hint = error.hint ?? ""
            XCTAssertFalse(hint.contains("lan url"), hint)
            XCTAssertFalse(hint.contains("web.token"), hint)
        }
        XCTAssertTrue(SetupHelp.requirement.contains("sheepdog setup"))
    }

    func testLinksPointAtTheDaemonRepositoryDocs() {
        for url in [SetupHelp.appGuideURL, SetupHelp.remoteAccessURL] {
            XCTAssertEqual(url.host, "github.com")
            XCTAssertTrue(url.path.hasPrefix("/kccarlos/herdr-sheepdog/blob/main/docs/"), url.path)
        }
    }

    func testNotificationsAreAskedForOnlyWhenTheDaemonCanSend() {
        XCTAssertFalse(PushPolicy.shouldAskPermission(daemon: nil))
        XCTAssertFalse(PushPolicy.shouldAskPermission(daemon: PushStatus(enabled: false, reason: "push.apns not configured")))
        XCTAssertTrue(PushPolicy.shouldAskPermission(daemon: PushStatus(enabled: true, devices: 1)))
        XCTAssertTrue(PushPolicy.shouldAskPermission(daemon: PushStatus(enabled: true, devices: 1, mode: .relay)))
        XCTAssertTrue(PushPolicy.shouldAskPermission(daemon: PushStatus(enabled: true, devices: 1, mode: .apns)))
        XCTAssertFalse(PushPolicy.shouldAskPermission(daemon: PushStatus(enabled: false, reason: "push.mode is off", mode: .off)))
    }

    func testNotificationCopyIsHonestWithoutPush() {
        let off = PushPolicy.explanation(daemon: PushStatus(enabled: false))
        XCTAssertTrue(off.contains("Live Activity"))
        XCTAssertTrue(off.contains("not sending notifications now"))
        XCTAssertTrue(off.contains("shared relay"))
        XCTAssertTrue(off.contains("your own push key"))
        XCTAssertFalse(off.contains("even with the app closed"))
        let relay = PushPolicy.explanation(daemon: PushStatus(enabled: true, devices: 1, mode: .relay))
        XCTAssertTrue(relay.contains("even with the app closed"))
        XCTAssertTrue(relay.contains("end-to-end encrypted"))
        XCTAssertTrue(relay.contains("never what it says"))
        let direct = PushPolicy.explanation(daemon: PushStatus(enabled: true, devices: 1, mode: .apns))
        XCTAssertTrue(direct.contains("your own push key"))
        XCTAssertFalse(direct.contains("relay"))
    }
}
