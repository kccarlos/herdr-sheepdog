import UserNotifications
import XCTest
import SheepdogKit
@testable import SheepdogMac

final class NotificationServiceTests: XCTestCase {
    func testAQuestionUsesThePendingActionsExistingWords() throws {
        let event = DaemonEvent(
            seq: 7,
            source: "supervisor",
            type: "pending.created",
            paneID: "w1:p2",
            payload: [
                "id": .string("pending-1"),
                "kind": .string("answer_prompt"),
                "agent": .string("auth"),
                "summary": .string("auth asks to run the tests."),
                "pane_id": .string("w1:p2"),
                "options": .array([
                    .object(["index": .number(1), "label": .string("Yes")]),
                    .object(["index": .number(2), "label": .string("No")]),
                ]),
            ]
        )
        let notice = try XCTUnwrap(NotificationNotice(event: event))
        XCTAssertEqual(notice.eventID, 7)
        XCTAssertEqual(notice.paneID, "w1:p2")
        XCTAssertEqual(notice.title, "auth")
        XCTAssertEqual(notice.body, "auth asks to run the tests.")
    }

    func testAWorkerFinishUsesTheTransitionAndIgnoresOtherStates() throws {
        func transition(from: String, to: String) -> DaemonEvent {
            DaemonEvent(
                seq: 8,
                source: "herdr",
                type: "herdr.transition",
                paneID: "w1:p2",
                payload: ["from": .string(from), "to": .string(to), "name": .string("auth")]
            )
        }
        let notice = try XCTUnwrap(NotificationNotice(event: transition(from: "working", to: "done")))
        XCTAssertEqual(notice.paneID, "w1:p2")
        XCTAssertEqual(notice.title, "auth")
        XCTAssertEqual(notice.body, "auth finished.")
        XCTAssertNotNil(NotificationNotice(event: transition(from: "working", to: "idle")))
        XCTAssertNil(NotificationNotice(event: transition(from: "working", to: "blocked")))
        XCTAssertNil(NotificationNotice(event: transition(from: "blocked", to: "done")))
    }

    func testAVoiceProblemUsesTheLocalNoticeWords() throws {
        let event = DaemonEvent(
            seq: 9,
            source: "voice",
            type: "voice.closed",
            payload: ["reason": .string("error")]
        )
        let notice = try XCTUnwrap(NotificationNotice(event: event))
        XCTAssertEqual(notice.body, LocalNotice.voiceClosed(reason: "error").text)
        XCTAssertNil(NotificationNotice(event: DaemonEvent(
            seq: 10,
            source: "voice",
            type: "voice.closed",
            payload: ["reason": .string("replaced")]
        )))
    }

    func testSuppressionNeedsTheVoiceHereAndTheUserTalking() {
        XCTAssertTrue(NotificationPolicy.suppresses(voice: VoiceMenuState(heldHere: true, userTalking: true)))
        XCTAssertFalse(NotificationPolicy.suppresses(voice: VoiceMenuState(heldHere: true)))
        XCTAssertFalse(NotificationPolicy.suppresses(voice: VoiceMenuState(userTalking: true)))
    }
}

@MainActor
final class NotificationDeliveryTests: XCTestCase {
    private final class FakeCenter: NotificationCenterClient {
        var status: UNAuthorizationStatus = .notDetermined
        var grants = true
        private(set) var requests = 0
        private(set) var added: [UNNotificationRequest] = []

        func authorizationStatus() async -> UNAuthorizationStatus { status }

        func requestAuthorization() async -> Bool {
            requests += 1
            if grants { status = .authorized }
            return grants
        }

        func add(_ request: UNNotificationRequest) async throws {
            added.append(request)
        }
    }

    func testPermissionIsAskedOnTheFirstNeedAndOnlyOnce() async {
        let center = FakeCenter()
        let service = NotificationService(panel: FakePanel(), log: FakeLog(), center: center)
        let event = DaemonEvent(
            seq: 11,
            source: "web",
            type: "voice.connect_error",
            payload: ["message": .string("The connection failed.")]
        )
        service.receive(event, voice: VoiceMenuState())
        await eventually { center.added.count == 1 }
        XCTAssertEqual(center.requests, 1)
        XCTAssertEqual(center.added.first?.content.body, LocalNotice.voiceClosed(reason: "The connection failed.").text)

        service.receive(DaemonEvent(seq: 12, source: "web", type: "voice.connect_error"), voice: VoiceMenuState())
        await eventually { center.added.count == 2 }
        XCTAssertEqual(center.requests, 1)
    }

    func testSuppressedEventsDoNotAskOrDeliver() async {
        let center = FakeCenter()
        let service = NotificationService(panel: FakePanel(), log: FakeLog(), center: center)
        service.receive(
            DaemonEvent(seq: 13, source: "voice", type: "voice.connect_error"),
            voice: VoiceMenuState(heldHere: true, userTalking: true)
        )
        await Task.yield()
        XCTAssertEqual(center.requests, 0)
        XCTAssertTrue(center.added.isEmpty)
    }

    func testANotificationTapUsesTheExistingPanelOpener() {
        let panel = FakePanel()
        let log = FakeLog()
        let service = NotificationService(panel: panel, log: log, center: FakeCenter())
        service.openPanelFromNotification()
        XCTAssertEqual(panel.opened, 1)

        panel.failure = Boom()
        service.openPanelFromNotification()
        XCTAssertEqual(panel.opened, 2)
        XCTAssertEqual(log.lines.count, 1)
    }

    private func eventually(_ condition: @escaping @MainActor () -> Bool) async {
        for _ in 0 ..< 100 {
            if condition() { return }
            try? await Task.sleep(nanoseconds: 10_000_000)
        }
        XCTFail("condition did not become true")
    }
}
