import XCTest
@testable import SheepdogKit

/// One test per control message of docs/ws-contract.md. The JSON literals
/// are the ones the Go side produces (`internal/voice/wsproto`), so a drift
/// in either direction fails here.
final class WSMessageTests: XCTestCase {
    private func decode(_ json: String) throws -> WSMessage {
        try WSMessage.decode(Data(json.utf8))
    }

    func testClientReadyRoundTrip() throws {
        let client = WSMessage.ClientInfo(name: "sheepdog-ios", version: "0.2.0 (12)", platform: "ios")
        XCTAssertEqual(
            try WSMessage.clientReady(client: client).encodedString(),
            #"{"client":{"name":"sheepdog-ios","platform":"ios","version":"0.2.0 (12)"},"protocol":1,"type":"client-ready"}"#
        )
        let m = try decode(#"{"type":"client-ready","protocol":1,"client":{"name":"sheepdog-ios","version":"0.2.0 (12)","platform":"ios"}}"#)
        XCTAssertEqual(m.type, .clientReady)
        XCTAssertEqual(m.client, client)
        XCTAssertThrowsError(try decode(#"{"type":"client-ready"}"#), "protocol is required")
    }

    func testBotReadyAsTheDaemonSendsIt() throws {
        let m = try decode(#"{"type":"bot-ready","protocol":1,"min_protocol":1,"daemon":{"version":"0.4.2"},"features":["playback","speaking","silence","provider","history","push","models"]}"#)
        XCTAssertEqual(m.type, .botReady)
        XCTAssertEqual(m.protocol, 1)
        XCTAssertEqual(m.minProtocol, 1)
        XCTAssertEqual(m.daemon?.version, "0.4.2")
        XCTAssertEqual(m.features?.count, 7)
        XCTAssertThrowsError(try decode(#"{"type":"bot-ready","protocol":1}"#), "daemon is required")
    }

    func testTheAppNamesItselfFromItsBundle() {
        let info = WSMessage.ClientInfo.app()
        XCTAssertEqual(info.name, "sheepdog-ios")
        XCTAssertEqual(info.platform, "ios")
        XCTAssertFalse(info.version.isEmpty)
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

    func testPingCarriesThePlaybackCountersTheDaemonLogs() throws {
        // `drains` read 0 in web.client.stats because the app never sent it.
        let ping = WSMessage.ping(gatedFrames: 1, peak: 2, underruns: 3, drains: 7, bufferMs: 640, rate: 0.93, stretchedMs: 4_200)
        XCTAssertEqual(
            try ping.encodedString(),
            #"{"buffer_ms":640,"drains":7,"gated_frames":1,"peak":2,"rate":0.93,"stretched_ms":4200,"type":"ping","underruns":3}"#
        )
        let back = try decode(try ping.encodedString())
        XCTAssertEqual(back, ping)
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
