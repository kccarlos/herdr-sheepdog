import XCTest
@testable import SheepdogKit

/// `/api/state`'s `voice.speaking` and `voice.client` (docs/dev/design/desktop-client.md
/// section 4): additive, so an older daemon's state still decodes.
final class VoiceClientDecodeTests: XCTestCase {
    private func state(voice: String) throws -> DaemonState {
        let json = #"{"agents":[],"voice":"# + voice + "}"
        return try DaemonClient.decoder.decode(DaemonState.self, from: Data(json.utf8))
    }

    func testAnOlderDaemonHasNeitherField() throws {
        let v = try state(voice: #"{"mode":"gemini","status":"connected","muted":false,"stalled":false,"client_connected":true}"#).voice
        XCTAssertFalse(v.speaking)
        XCTAssertNil(v.client)
    }

    func testTheHolderAndSpeakingDecode() throws {
        let v = try state(voice: #"{"mode":"gemini","status":"connected","muted":true,"stalled":false,"speaking":true,"client_connected":true,"client":{"name":"sheepdog-mac","platform":"macos","since":"2026-01-02T03:04:05.678Z"}}"#).voice
        XCTAssertTrue(v.speaking)
        XCTAssertEqual(v.client?.name, "sheepdog-mac")
        XCTAssertEqual(v.client?.platform, "macos")
        XCTAssertNotNil(v.client?.since)
    }
}
