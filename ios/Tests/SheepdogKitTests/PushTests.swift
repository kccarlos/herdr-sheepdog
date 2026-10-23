@testable import SheepdogKit
import XCTest

final class PushTests: XCTestCase {
    override func setUp() {
        StubProtocol.seen = []
        StubProtocol.handler = nil
    }

    // MARK: - Token and environment

    func testTokenIsLowercaseHexTwoDigitsAByte() {
        XCTAssertEqual(PushToken.hex(Data([0x00, 0x0A, 0xFF, 0x10, 0xab])), "000aff10ab")
        XCTAssertEqual(PushToken.hex(Data(repeating: 0xC3, count: 32)).count, 64)
        XCTAssertEqual(PushToken.hex(Data()), "")
    }

    func testEnvironmentComesFromTheBuildNotAGuess() {
        XCTAssertEqual(PushEnvironment(infoValue: "sandbox"), .sandbox)
        XCTAssertEqual(PushEnvironment(infoValue: "production"), .production)
        // Unset (an older Info.plist) or unexpanded: what distributed builds use.
        XCTAssertEqual(PushEnvironment(infoValue: nil), .production)
        XCTAssertEqual(PushEnvironment(infoValue: "$(SHEEPDOG_PUSH_ENVIRONMENT)"), .production)
    }

    // MARK: - Requests

    func testRegisterRequestMatchesTheDaemonContract() throws {
        let client = StubProtocol.client(token: "s3cr3t")
        let request = try client.pushRegisterRequest(PushRegistration(token: "abcd", environment: .sandbox, device: "iPhone"))
        XCTAssertEqual(request.httpMethod, "POST")
        XCTAssertEqual(request.url?.path, "/api/push/register")
        XCTAssertEqual(request.value(forHTTPHeaderField: "Content-Type"), "application/json")
        XCTAssertEqual(String(decoding: request.httpBody ?? Data(), as: UTF8.self), #"{"device":"iPhone","environment":"sandbox","token":"abcd"}"#)
    }

    func testRegisterAnswersTrueOnOkAndFalseWithoutAPushService() async throws {
        let client = StubProtocol.client()
        StubProtocol.handler = { _ in (200, Data(#"{"ok":true}"#.utf8)) }
        let ok = try await client.registerPush(PushRegistration(token: "ab", environment: .production))
        XCTAssertTrue(ok)
        StubProtocol.handler = { _ in (503, Data(#"{"error":"push not available"}"#.utf8)) }
        let none = try await client.registerPush(PushRegistration(token: "ab", environment: .production))
        XCTAssertFalse(none)
        StubProtocol.handler = { _ in (401, Data()) }
        do {
            try await client.registerPush(PushRegistration(token: "ab", environment: .production))
            XCTFail("a refused token throws")
        } catch let error as DaemonError {
            XCTAssertEqual(error, .unauthorized(endpoint: "127.0.0.1:7799"))
        }
        StubProtocol.handler = { _ in (400, Data(#"{"error":"bad token"}"#.utf8)) }
        do {
            try await client.registerPush(PushRegistration(token: "zz", environment: .production))
            XCTFail("a 400 throws")
        } catch let error as DaemonError {
            XCTAssertEqual(error, .rejected(status: 400, message: "bad token"))
        }
    }

    func testUnregisterIsADeleteWithTheToken() async throws {
        let client = StubProtocol.client()
        StubProtocol.handler = { _ in (200, Data(#"{"ok":true}"#.utf8)) }
        try await client.unregisterPush(token: "abcd")
        let request = try XCTUnwrap(StubProtocol.seen.last)
        XCTAssertEqual(request.httpMethod, "DELETE")
        XCTAssertEqual(request.url?.path, "/api/push/register")
        XCTAssertEqual(try JSONSerialization.jsonObject(with: request.httpBody ?? Data()) as? [String: String], ["token": "abcd"])
    }

    func testStatusDecodesAndSummarises() async throws {
        let client = StubProtocol.client()
        StubProtocol.handler = { _ in (200, Data(#"{"enabled":false,"reason":"push.apns not configured","devices":1}"#.utf8)) }
        let status = try await client.pushStatus()
        XCTAssertEqual(status, PushStatus(enabled: false, reason: "push.apns not configured", devices: 1))
        XCTAssertEqual(status?.summary, "Off — push.apns not configured")
        XCTAssertEqual(PushStatus(enabled: true, devices: 2).summary, "On · 2 devices")
        XCTAssertEqual(PushStatus(enabled: true, devices: 1).summary, "On · 1 device")
        StubProtocol.handler = { _ in (404, Data()) }
        let missing = try await client.pushStatus()
        XCTAssertNil(missing, "an older daemon has no route")
    }

    func testStatusModeDecidesWhetherItCanSend() async throws {
        let client = StubProtocol.client()
        StubProtocol.handler = { _ in (200, Data(#"{"enabled":true,"devices":1,"mode":"relay"}"#.utf8)) }
        let relay = try await client.pushStatus()
        XCTAssertEqual(relay, PushStatus(enabled: true, devices: 1, mode: .relay))
        XCTAssertEqual(relay?.canSend, true)
        XCTAssertEqual(relay?.summary, "On · shared relay · 1 device")
        XCTAssertEqual(PushStatus(enabled: true, devices: 2, mode: .apns).summary, "On · your push key · 2 devices")
        XCTAssertFalse(PushStatus(enabled: true, mode: .off).canSend)
        StubProtocol.handler = { _ in (200, Data(#"{"enabled":true,"devices":1,"mode":"carrier-pigeon"}"#.utf8)) }
        let unknown = try await client.pushStatus()
        XCTAssertNil(unknown?.mode, "an unknown mode decodes as none")
        XCTAssertEqual(unknown?.canSend, true)
    }

    func testRegisterAnswerCarriesTheContentKey() async throws {
        let client = StubProtocol.client()
        StubProtocol.handler = { _ in (200, Data(#"{"ok":true,"content_key":"AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8=","encryption":"aes-256-gcm"}"#.utf8)) }
        let answer = try await client.registerPushDevice(PushRegistration(token: "ab", environment: .production))
        XCTAssertEqual(answer, PushRegistrationAnswer(contentKey: "AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8=", encryption: "aes-256-gcm"))
        StubProtocol.handler = { _ in (200, Data(#"{"ok":true}"#.utf8)) }
        let old = try await client.registerPushDevice(PushRegistration(token: "ab", environment: .production))
        XCTAssertEqual(old, PushRegistrationAnswer(), "a daemon from before the relay answers no key")
        StubProtocol.handler = { _ in (503, Data()) }
        let none = try await client.registerPushDevice(PushRegistration(token: "ab", environment: .production))
        XCTAssertNil(none)
    }

    // MARK: - Payload

    /// The payload exactly as herdr-sheepdog's docs/push.md shows it.
    static let approvalUserInfo: [AnyHashable: Any] = [
        "aps": [
            "alert": ["title": "auth", "body": "auth asks to run git push. Yes or no?"],
            "sound": "default",
            "thread-id": "w5:p1",
            "interruption-level": "time-sensitive",
            "category": "APPROVAL",
        ] as [String: Any],
        "sheepdog": [
            "kind": "approval",
            "pane_id": "w5:p1",
            "agent": "auth",
            "pending_id": "ap-tm9bu0-1",
            "event_id": "evt-12",
            "options": [["index": 1, "label": "Yes, proceed"], ["index": 2, "label": "Yes, and don't ask again"], ["index": 3, "label": "No"]],
        ] as [String: Any],
    ]

    func testParsesTheApprovalPayload() throws {
        let payload = try XCTUnwrap(PushPayload(userInfo: Self.approvalUserInfo))
        XCTAssertEqual(payload.kind, "approval")
        XCTAssertEqual(payload.paneID, "w5:p1")
        XCTAssertEqual(payload.agent, "auth")
        XCTAssertEqual(payload.pendingID, "ap-tm9bu0-1")
        XCTAssertEqual(payload.eventID, "evt-12")
        XCTAssertEqual(payload.options.map(\.index), [1, 2, 3])
        XCTAssertEqual(payload.options.map(\.label), ["Yes, proceed", "Yes, and don't ask again", "No"])
        XCTAssertEqual(payload.category, "APPROVAL")
        XCTAssertEqual(payload.title, "auth")
        XCTAssertEqual(payload.body, "auth asks to run git push. Yes or no?")
        XCTAssertTrue(payload.isAnswerable)
    }

    func testParsesAKeptPromiseAndIgnoresForeignNotifications() throws {
        let promise: [AnyHashable: Any] = [
            "aps": ["alert": ["title": "billing", "body": "billing finished."], "category": "AGENT"] as [String: Any],
            "sheepdog": ["kind": "done", "pane_id": "w1:p2", "agent": "billing"],
        ]
        let payload = try XCTUnwrap(PushPayload(userInfo: promise))
        XCTAssertEqual(payload.kind, "done")
        XCTAssertNil(payload.pendingID)
        XCTAssertTrue(payload.options.isEmpty)
        XCTAssertFalse(payload.isAnswerable)
        XCTAssertNil(PushPayload(userInfo: ["aps": ["alert": "hi"]]))
    }

    // MARK: - Actions

    private func options(_ labels: [String]) -> [PendingOption] {
        labels.enumerated().map { PendingOption(index: $0.offset + 1, label: $0.element) }
    }

    func testApproveIsTheFirstYesElseOptionOne() {
        XCTAssertEqual(PushActions.approveOption(options(["No", "Yes, proceed"]))?.index, 2)
        XCTAssertEqual(PushActions.approveOption(options(["Proceed", "Cancel"]))?.index, 1)
        XCTAssertNil(PushActions.approveOption(options(["No, stop"])), "never approve with a No")
        XCTAssertNil(PushActions.approveOption([]))
    }

    func testDeclineIsTheFirstNo() {
        XCTAssertEqual(PushActions.declineOption(options(["Yes", "No, and tell Codex what to do differently"]))?.index, 2)
        XCTAssertNil(PushActions.declineOption(options(["Yes", "Nothing else"])), "\"Nothing\" is not a No")
        XCTAssertNil(PushActions.declineOption(options(["Proceed", "Skip"])))
    }

    func testActionMapsToTheAnswer() throws {
        let payload = try XCTUnwrap(PushPayload(userInfo: Self.approvalUserInfo))
        let approve = try XCTUnwrap(PushActions.answer(actionIdentifier: PushActions.approve, payload: payload))
        XCTAssertEqual(approve.pendingID, "ap-tm9bu0-1")
        XCTAssertEqual(approve.option.index, 1)
        let decline = try XCTUnwrap(PushActions.answer(actionIdentifier: PushActions.decline, payload: payload))
        XCTAssertEqual(decline.option.index, 3)
        XCTAssertNil(PushActions.answer(actionIdentifier: "com.apple.UNNotificationDefaultActionIdentifier", payload: payload), "a plain tap opens the app")
        var noPending = payload
        noPending.pendingID = nil
        XCTAssertNil(PushActions.answer(actionIdentifier: PushActions.approve, payload: noPending))
    }
}
