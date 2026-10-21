import XCTest
@testable import SheepdogKit

/// Talks to a real daemon on 127.0.0.1:7799 (or `SHEEPDOG_FIXTURE_PORT`,
/// passed as `TEST_RUNNER_SHEEPDOG_FIXTURE_PORT`) when one is running
/// (`--brain=fake --herdr=mock --voice=off --dev`), and skips otherwise.
/// ios.yml runs it against its fixture daemon. It is the only place the
/// SSE stream is exercised end to end.
final class LiveDaemonTests: XCTestCase {
    private let host = "127.0.0.1:" + (ProcessInfo.processInfo.environment["SHEEPDOG_FIXTURE_PORT"] ?? "7799")
    private var endpoint: DaemonEndpoint { DaemonEndpoint(text: host)! }

    private func skipUnlessRunning() async throws {
        do {
            _ = try await DaemonClient(endpoint: endpoint).hello()
        } catch {
            throw XCTSkip("no sheepdog daemon on \(host)")
        }
    }

    func testHelloAndState() async throws {
        try await skipUnlessRunning()
        let client = DaemonClient(endpoint: endpoint)
        let hello = try await client.hello()
        XCTAssertEqual(hello.name, "sheepdog")
        XCTAssertEqual(hello.audio.inHz, 16_000)
        let state = try await client.state()
        XCTAssertTrue(state.voice.isOff, "this fixture daemon runs --voice=off")
    }

    func testEventStreamDeliversAnInjectedEvent() async throws {
        try await skipUnlessRunning()
        let client = DaemonClient(endpoint: endpoint)
        let received = expectation(description: "an event arrived")
        var seen: [String] = []

        let task = Task {
            do {
                for try await output in client.events() {
                    if case let .event(event) = output {
                        seen.append(event.type)
                        if event.type == "web.utterance" { received.fulfill() }
                    }
                }
            } catch {
                XCTFail("event stream failed: \(error)")
            }
        }
        // Give the stream a moment to attach before making something happen.
        try await Task.sleep(nanoseconds: 1_000_000_000)

        var request = URLRequest(url: endpoint.url(path: "/api/inject/utterance")!)
        request.httpMethod = "POST"
        request.setValue("application/json", forHTTPHeaderField: "Content-Type")
        request.httpBody = try JSONSerialization.data(withJSONObject: ["text": "what's going on"])
        _ = try await URLSession.shared.data(for: request)

        await fulfillment(of: [received], timeout: 20)
        task.cancel()
        XCTAssertTrue(seen.contains("web.utterance"), "saw \(seen)")
    }
}
