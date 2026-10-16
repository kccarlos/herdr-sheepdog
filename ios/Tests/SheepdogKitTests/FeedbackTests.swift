import XCTest
@testable import SheepdogKit

/// The feedback mark (`POST /api/feedback`) and the `drain` message of
/// the daemon's docs/ws-contract.md "Feedback and drains".
final class FeedbackTests: XCTestCase {
    override func tearDown() {
        StubProtocol.handler = nil
        StubProtocol.seen = []
    }

    private func chunk(_ milliseconds: Int) -> [Int16] {
        [Int16](repeating: 1, count: Int(AudioContract.playbackRate) * milliseconds / 1000)
    }

    // MARK: - The drain message

    func testTheContractsDrainLiteralsDecode() throws {
        let dry = try WSMessage.decode(Data(#"{"type":"drain","at":"2026-10-08T00:48:51.950Z","buffer_ms":0,"played_ms":3850,"received_ms":3850,"reason":"dry"}"#.utf8))
        XCTAssertEqual(dry.type, .drain)
        XCTAssertEqual(dry.at, "2026-10-08T00:48:51.950Z")
        XCTAssertEqual(dry.receivedMs, 3_850)
        let early = try WSMessage.decode(Data(#"{"type":"drain","at":"2026-10-08T00:48:51.950Z","buffer_ms":3900,"played_ms":3850,"received_ms":7800,"reason":"early"}"#.utf8))
        XCTAssertEqual(early.bufferMs, 3_900)
        XCTAssertEqual(early.reason, "early")
        XCTAssertThrowsError(try WSMessage.decode(Data(#"{"type":"drain","buffer_ms":0,"played_ms":1}"#.utf8)), "at is required")
        XCTAssertThrowsError(try WSMessage.decode(Data(#"{"type":"drain","at":"2026-10-08T00:48:51Z","played_ms":1}"#.utf8)), "buffer_ms is required")
    }

    func testADrainEncodesAsTheDaemonParsesIt() throws {
        // 2026-10-08T00:48:51.950Z
        let at = Date(timeIntervalSince1970: 1_791_420_531.95)
        let message = WSMessage.drain(at: at, bufferMs: 3_900, playedMs: 3_850, receivedMs: 7_800, reason: "early")
        XCTAssertEqual(
            try message.encodedString(),
            #"{"at":"2026-10-08T00:48:51.950Z","buffer_ms":3900,"played_ms":3850,"reason":"early","received_ms":7800,"type":"drain"}"#
        )
        XCTAssertEqual(try WSMessage.decode(try message.encode()), message)
    }

    // MARK: - Drains from the queue

    func testEveryDryRingIsADrain() {
        var queue = PlaybackQueue(prebufferMilliseconds: 50)
        _ = queue.push(chunk(100), now: 0)
        // Dry at 0.1; more of the reply at 0.3 (an underrun), then dry again.
        _ = queue.push(chunk(200), now: 0.3)
        var drains = queue.takeDrains()
        XCTAssertEqual(drains, [.init(at: 0.1, bufferMilliseconds: 0, playedMilliseconds: 100, receivedMilliseconds: 100, reason: .dry)])
        _ = queue.tick(now: 0.81)
        drains = queue.takeDrains()
        XCTAssertEqual(drains.count, 1)
        XCTAssertEqual(drains.first?.playedMilliseconds, 300)
        XCTAssertEqual(drains.first?.reason, .dry)
        XCTAssertEqual(queue.takeDrains(), [], "taken once")
    }

    /// The pause of 2026-10-08 00:48:49Z: the player said it was done
    /// seconds before its playhead (an engine restart drops what was
    /// scheduled). The drain says how much never played.
    func testANodeThatFinishesEarlyIsAnEarlyDrainWithTheLostAudio() {
        var queue = PlaybackQueue(prebufferMilliseconds: 50)
        let released = queue.push(chunk(8_000), now: 0)
        queue.completed(released[0].id, now: 4)
        let drains = queue.takeDrains()
        XCTAssertEqual(drains.count, 1)
        XCTAssertEqual(drains.first?.reason, .early)
        XCTAssertEqual(drains.first?.at, 4)
        XCTAssertEqual(drains.first?.bufferMilliseconds, 4_000)
        XCTAssertEqual(drains.first?.playedMilliseconds, 4_000)
        XCTAssertEqual(drains.first?.receivedMilliseconds, 8_000)
    }

    func testANodeThatFinishesOnTimeIsAnOrdinaryDrain() {
        var queue = PlaybackQueue(prebufferMilliseconds: 50)
        let released = queue.push(chunk(100), now: 0)
        queue.completed(released[0].id, now: 0.06)
        XCTAssertEqual(queue.takeDrains().first?.reason, .dry, "40 ms early is the prediction's slack")
    }

    func testADrainRecordBecomesTheMessage() throws {
        let record = PlaybackQueue.DrainRecord(at: 0, bufferMilliseconds: 10, playedMilliseconds: 20, receivedMilliseconds: 30, reason: .early)
        let message = record.message
        XCTAssertEqual(message.type, .drain)
        XCTAssertEqual(message.at, "2001-01-01T00:00:00.000Z", "the queue's clock is seconds since the reference date")
        XCTAssertEqual(message.bufferMs, 10)
        XCTAssertEqual(message.playedMs, 20)
        XCTAssertEqual(message.receivedMs, 30)
        XCTAssertEqual(message.reason, "early")
    }

    // MARK: - POST /api/feedback

    func testAMarkPostsTheTagNoteAndTime() async throws {
        StubProtocol.handler = { _ in (200, Data(#"{"ok":true,"id":"fb-abc","ts":"2026-10-08T00:49:01Z"}"#.utf8)) }
        let at = Date(timeIntervalSince1970: 1_791_420_541.25)
        let mark = FeedbackMark(tag: .paused, note: "  that paused  ", at: at, conversationTurn: "b-12")
        let id = try await StubProtocol.client(token: "tok").sendFeedback(mark)
        XCTAssertEqual(id, "fb-abc")
        let request = try XCTUnwrap(StubProtocol.seen.first)
        XCTAssertEqual(request.httpMethod, "POST")
        XCTAssertEqual(request.url?.path, "/api/feedback")
        XCTAssertEqual(request.value(forHTTPHeaderField: "Authorization"), "Bearer tok")
        let body = try XCTUnwrap(JSONSerialization.jsonObject(with: request.httpBody ?? Data()) as? [String: Any])
        XCTAssertEqual(body["tag"] as? String, "paused")
        XCTAssertEqual(body["note"] as? String, "that paused")
        XCTAssertEqual(body["client_ts"] as? String, "2026-10-08T00:49:01.250Z")
        XCTAssertEqual(body["conversation_turn"] as? String, "b-12")
    }

    func testAnEmptyNoteIsLeftOut() throws {
        let data = try JSONEncoder().encode(FeedbackMark(tag: .misheard, note: "   ", at: Date(timeIntervalSince1970: 0)))
        let body = try XCTUnwrap(JSONSerialization.jsonObject(with: data) as? [String: Any])
        XCTAssertNil(body["note"])
        XCTAssertNil(body["conversation_turn"])
        XCTAssertEqual(body["tag"] as? String, "misheard")
    }

    func testAnOlderDaemonAnswersNil() async throws {
        StubProtocol.handler = { _ in (404, Data()) }
        let id = try await StubProtocol.client().sendFeedback(FeedbackMark(tag: .other, at: Date()))
        XCTAssertNil(id)
    }

    func testTheTagsMatchTheDaemon() {
        XCTAssertEqual(FeedbackTag.allCases.map(\.rawValue), ["paused", "wrong_answer", "too_short", "too_long", "misheard", "other"])
        XCTAssertEqual(FeedbackTag.feature, "feedback")
    }
}
