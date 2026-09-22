import XCTest
@testable import SheepdogKit

/// One test per control message of docs/ws-contract.md. The JSON literals
/// are the ones the Go side produces (`internal/voice/wsproto`), so a drift
/// in either direction fails here.
final class WSMessageTests: XCTestCase {
    private func decode(_ json: String) throws -> WSMessage {
        try WSMessage.decode(Data(json.utf8))
    }

    func testMuteRoundTrip() throws {
        XCTAssertEqual(try WSMessage.mute(true).encodedString(), #"{"on":true,"type":"mute"}"#)
        let m = try decode(#"{"type":"mute","on":false}"#)
        XCTAssertEqual(m.type, .mute)
        XCTAssertEqual(m.on, false)
    }

    func testSilenceRoundTrip() throws {
        XCTAssertEqual(try WSMessage.silence(true).encodedString(), #"{"on":true,"type":"silence"}"#)
        XCTAssertEqual(try decode(#"{"type":"silence","on":true}"#).on, true)
    }

    func testCancelCarriesNothingElse() throws {
        XCTAssertEqual(try WSMessage.cancel.encodedString(), #"{"type":"cancel"}"#)
    }

    func testTextRequiresItsText() throws {
        XCTAssertEqual(try WSMessage.simulateText("hello").encodedString(), #"{"text":"hello","type":"text"}"#)
        XCTAssertThrowsError(try WSMessage(type: .text).encode())
    }

    func testPingCarriesTheGateCounters() throws {
        XCTAssertEqual(
            try WSMessage.ping(gatedFrames: 12, peak: 900).encodedString(),
            #"{"gated_frames":12,"peak":900,"type":"ping"}"#
        )
    }

    func testStatusDecodesEveryVoiceState() throws {
        for raw in ["connecting", "connected", "reconnecting", "closed"] {
            let m = try decode(#"{"type":"status","voice":"\#(raw)","muted":false}"#)
            XCTAssertEqual(m.voice?.rawValue, raw)
            XCTAssertEqual(m.muted, false)
        }
    }

    func testStatusCarriesReasonAndStalled() throws {
        let m = try decode(#"{"type":"status","voice":"closed","muted":false,"reason":"voice_off"}"#)
        XCTAssertEqual(m.voice, .closed)
        XCTAssertEqual(m.reason, "voice_off")
        XCTAssertNil(m.stalled)

        let stalled = try decode(#"{"type":"status","voice":"connected","muted":true,"stalled":true}"#)
        XCTAssertEqual(stalled.stalled, true)
        XCTAssertEqual(stalled.muted, true)
    }

    func testStatusWithAnUnknownVoiceIsRejected() {
        XCTAssertThrowsError(try decode(#"{"type":"status","voice":"napping"}"#))
    }

    func testTranscriptFragments() throws {
        let partial = try decode(#"{"type":"transcript","dir":"out","text":"on ","final":false}"#)
        XCTAssertEqual(partial.dir, .outgoing)
        XCTAssertEqual(partial.text, "on ")
        XCTAssertEqual(partial.final, false)

        let final = try decode(#"{"type":"transcript","dir":"in","text":"what's going on","final":true}"#)
        XCTAssertEqual(final.dir, .incoming)
        XCTAssertEqual(final.final, true)
    }

    func testTranscriptWithABadDirectionIsRejected() {
        XCTAssertThrowsError(try decode(#"{"type":"transcript","dir":"sideways","text":"x"}"#))
    }

    func testFlushAndPong() throws {
        XCTAssertEqual(try decode(#"{"type":"flush"}"#).type, .flush)
        XCTAssertEqual(try decode(#"{"type":"pong"}"#).type, .pong)
    }

    func testErrorRequiresAMessage() throws {
        XCTAssertEqual(try decode(#"{"type":"error","message":"rate limited"}"#).message, "rate limited")
        XCTAssertThrowsError(try WSMessage(type: .error).encode())
    }

    func testUnknownTypeIsRejectedSoTheCallerCanLogIt() {
        XCTAssertThrowsError(try decode(#"{"type":"bark"}"#))
    }

    func testAudioContractMatchesTheDaemon() {
        XCTAssertEqual(AudioContract.captureRate, 16_000)
        XCTAssertEqual(AudioContract.playbackRate, 24_000)
        // 40 ms at 16 kHz = 640 samples = 1 280 bytes, inside the
        // contract's 20–100 ms window.
        XCTAssertEqual(AudioContract.frameSamples, 640)
        XCTAssertEqual(AudioContract.frameSamples * 2, 1_280)
    }
}
