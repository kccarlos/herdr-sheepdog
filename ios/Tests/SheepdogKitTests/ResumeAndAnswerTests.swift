import XCTest
@testable import SheepdogKit

/// Answers requests from a closure instead of the network.
final class StubProtocol: URLProtocol {
    nonisolated(unsafe) static var handler: ((URLRequest) -> (Int, Data))?
    nonisolated(unsafe) static var seen: [URLRequest] = []

    override class func canInit(with request: URLRequest) -> Bool { true }
    override class func canonicalRequest(for request: URLRequest) -> URLRequest { request }

    override func startLoading() {
        var request = request
        if request.httpBody == nil, let stream = request.httpBodyStream {
            stream.open()
            var data = Data()
            var buffer = [UInt8](repeating: 0, count: 4096)
            while stream.hasBytesAvailable {
                let n = stream.read(&buffer, maxLength: buffer.count)
                if n <= 0 { break }
                data.append(buffer, count: n)
            }
            stream.close()
            request.httpBody = data
        }
        StubProtocol.seen.append(request)
        let (status, body) = StubProtocol.handler?(request) ?? (500, Data())
        let response = HTTPURLResponse(url: request.url!, statusCode: status, httpVersion: "HTTP/1.1", headerFields: ["Content-Type": "application/json"])!
        client?.urlProtocol(self, didReceive: response, cacheStoragePolicy: .notAllowed)
        client?.urlProtocol(self, didLoad: body)
        client?.urlProtocolDidFinishLoading(self)
    }

    override func stopLoading() {}

    static func client(token: String = "") -> DaemonClient {
        let config = URLSessionConfiguration.ephemeral
        config.protocolClasses = [StubProtocol.self]
        return DaemonClient(endpoint: DaemonEndpoint(text: "127.0.0.1:7799", token: token)!, session: URLSession(configuration: config))
    }
}

final class EventResumeTests: XCTestCase {
    func testAReconnectSendsLastEventIDAndSince() throws {
        let client = DaemonClient(endpoint: DaemonEndpoint(text: "127.0.0.1:7799", token: "t")!)
        let request = try client.eventsRequest(since: 1_790_127_642_099_438)
        XCTAssertEqual(request.value(forHTTPHeaderField: "Last-Event-ID"), "1790127642099438")
        XCTAssertEqual(request.url?.absoluteString, "http://127.0.0.1:7799/events?since=1790127642099438")
        XCTAssertEqual(request.value(forHTTPHeaderField: "Accept"), "text/event-stream")
        XCTAssertEqual(request.value(forHTTPHeaderField: "Authorization"), "Bearer t")
    }

    func testAFirstConnectSendsNeither() throws {
        // A small ?since= is a count to the daemon, so 0 must not be sent.
        let request = try DaemonClient(endpoint: DaemonEndpoint(text: "127.0.0.1:7799")!).eventsRequest(since: 0)
        XCTAssertNil(request.value(forHTTPHeaderField: "Last-Event-ID"))
        XCTAssertEqual(request.url?.absoluteString, "http://127.0.0.1:7799/events")
    }

    func testTheParserTracksTheLastIDOfRealFrames() {
        var parser = SSEParser()
        var seqs: [UInt64] = []
        for line in Fixtures.sseCreatedAndConfirmed {
            if case let .event(e)? = parser.push(line: line) { seqs.append(e.seq) }
        }
        XCTAssertEqual(seqs, [1_790_127_629_425_157, 1_790_127_629_426_365, 1_790_127_642_099_438])
        XCTAssertEqual(parser.lastSeq, 1_790_127_642_099_438, "a keepalive comment does not move it")
    }

    func testAParserResumedFromAnIDNeverGoesBack() {
        var parser = SSEParser(lastSeq: 1_790_127_642_099_438)
        _ = parser.push(line: "id: 5")
        XCTAssertEqual(parser.lastSeq, 1_790_127_642_099_438)
    }
}

final class ConversationAndAnswerClientTests: XCTestCase {
    override func tearDown() {
        StubProtocol.handler = nil
        StubProtocol.seen = []
    }

    func testConversationAsksForTheNewestPageThenPagesBack() async throws {
        StubProtocol.handler = { _ in (200, Data(Fixtures.conversationPage.utf8)) }
        let client = StubProtocol.client(token: "tok")
        let page = try await client.conversation(limit: 200)
        XCTAssertEqual(page.items.count, 10)
        _ = try await client.conversation(limit: 100, before: page.nextBefore)
        let urls = StubProtocol.seen.map { $0.url!.absoluteString }
        XCTAssertEqual(urls, [
            "http://127.0.0.1:7799/api/conversation?limit=200",
            "http://127.0.0.1:7799/api/conversation?limit=100&before=1790065877490932",
        ])
        XCTAssertEqual(StubProtocol.seen.first?.value(forHTTPHeaderField: "Authorization"), "Bearer tok")
    }

    func testAnswerPostsTheOptionIndexAndReturnsSpoken() async throws {
        StubProtocol.handler = { _ in (200, Data(#"{"ok":true,"spoken":"Told billing Yes, proceed."}"#.utf8)) }
        let spoken = try await StubProtocol.client().answerPending(id: "ap-tlsnbh-1", option: 1)
        XCTAssertEqual(spoken, "Told billing Yes, proceed.")
        let request = try XCTUnwrap(StubProtocol.seen.first)
        XCTAssertEqual(request.httpMethod, "POST")
        XCTAssertEqual(request.url?.path, "/api/pending/answer")
        let body = try XCTUnwrap(JSONSerialization.jsonObject(with: request.httpBody ?? Data()) as? [String: Any])
        XCTAssertEqual(body["id"] as? String, "ap-tlsnbh-1")
        XCTAssertEqual(body["option"] as? Int, 1)
    }

    func testA409IsPendingGone() async {
        StubProtocol.handler = { _ in (409, Data(#"{"error":"that question is no longer open"}"#.utf8)) }
        do {
            _ = try await StubProtocol.client().answerPending(id: "ap-1", option: 1)
            XCTFail("expected pendingGone")
        } catch {
            XCTAssertEqual(error as? DaemonError, .pendingGone)
            XCTAssertEqual((error as? DaemonError)?.message, "That question was already answered or has gone.")
        }
    }

    func testOtherRefusalsCarryTheDaemonsWords() async {
        StubProtocol.handler = { _ in (400, Data(#"{"error":"no option 7"}"#.utf8)) }
        do {
            _ = try await StubProtocol.client().answerPending(id: "ap-1", option: 7)
            XCTFail("expected rejected")
        } catch {
            XCTAssertEqual(error as? DaemonError, .rejected(status: 400, message: "no option 7"))
        }
    }
}
