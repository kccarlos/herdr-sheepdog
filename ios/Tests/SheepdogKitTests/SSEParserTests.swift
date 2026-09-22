import XCTest
@testable import SheepdogKit

final class SSEParserTests: XCTestCase {
    private func run(_ lines: [String], since: UInt64 = 0) -> ([SSEParser.Output], UInt64) {
        var parser = SSEParser(lastSeq: since)
        var out: [SSEParser.Output] = []
        for line in lines {
            if let o = parser.push(line: line) { out.append(o) }
        }
        return (out, parser.lastSeq)
    }

    func testParsesOneEventAndRemembersItsSequence() {
        let (out, seq) = run([
            ": sheepdog events 1.2.3",
            "id: 41",
            "event: log",
            #"data: {"ts":"2026-09-22T00:12:00.123456Z","source":"supervisor","type":"pending.created","pane_id":"w1:p1","payload":{"question":"run the migration?"}}"#,
            "",
        ])
        XCTAssertEqual(seq, 41)
        guard case let .event(event)? = out.first else { return XCTFail("no event, got \(out)") }
        XCTAssertEqual(event.seq, 41)
        XCTAssertEqual(event.source, "supervisor")
        XCTAssertEqual(event.type, "pending.created")
        XCTAssertEqual(event.paneID, "w1:p1")
        XCTAssertEqual(event.string("question"), "run the migration?")
        XCTAssertNotNil(event.ts)
    }

    func testKeepaliveCommentsAndBlankLinesProduceNothing() {
        let (out, _) = run([": keepalive", "", ": keepalive", ""])
        XCTAssertTrue(out.isEmpty)
    }

    func testSeveralEventsInOneStream() {
        let (out, seq) = run([
            "id: 1", "event: log", #"data: {"source":"web","type":"web.utterance","payload":{"text":"what's going on"}}"#, "",
            "id: 2", "event: log", #"data: {"source":"voice","type":"voice.push","payload":{"text":"auth is asking.","skipped":true}}"#, "",
        ])
        XCTAssertEqual(out.count, 2)
        XCTAssertEqual(seq, 2)
        guard case let .event(second) = out[1] else { return XCTFail("expected an event") }
        XCTAssertEqual(second.type, "voice.push")
        XCTAssertEqual(second.string("text"), "auth is asking.")
        XCTAssertEqual(second.bool("skipped"), true)
    }

    func testAClosedRecordCarriesItsReason() {
        let (out, _) = run(["event: closed", #"data: {"reason":"shutdown"}"#, ""])
        XCTAssertEqual(out, [.closed(reason: "shutdown")])
    }

    func testUndecodableDataIsReportedRatherThanSwallowed() {
        let (out, _) = run(["id: 9", "event: log", "data: not json", ""])
        XCTAssertEqual(out, [.undecodable("not json")])
    }

    func testMultiLineDataIsJoinedWithNewlines() {
        // A record split over two `data:` lines is joined with a newline,
        // which JSON tolerates between fields.
        let (out, _) = run([
            "event: log",
            #"data: {"source":"x","type":"y","#,
            #"data: "payload":{"text":"ab"}}"#,
            "",
        ])
        guard case let .event(event)? = out.first else { return XCTFail("no event") }
        XCTAssertEqual(event.string("text"), "ab")
    }

    func testTheSinceSequenceSurvivesAStreamWithNoIdLines() {
        let (_, seq) = run(["event: log", #"data: {"source":"x","type":"y"}"#, ""], since: 100)
        XCTAssertEqual(seq, 100)
    }
}
