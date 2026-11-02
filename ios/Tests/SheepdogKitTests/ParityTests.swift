import XCTest
@testable import SheepdogKit

final class TypedTurnTests: XCTestCase {
    override func tearDown() {
        StubProtocol.handler = nil
        StubProtocol.seen = []
    }

    func testTypedTurnPostsTextWithTheActionKeyAndDecodesTheReply() async throws {
        StubProtocol.handler = { _ in
            (200, Data(#"{"reply":"Two agents are working.","turn_id":"turn-7","acted":true}"#.utf8))
        }
        let reply = try await DaemonClient.$idempotencyKey.withValue("typed-1") {
            try await StubProtocol.client().sendTurn("what is going on")
        }
        XCTAssertEqual(reply, TypedTurnReply(reply: "Two agents are working.", turnID: "turn-7", acted: true))
        let request = try XCTUnwrap(StubProtocol.seen.first)
        XCTAssertEqual(request.httpMethod, "POST")
        XCTAssertEqual(request.url?.path, "/api/turn")
        XCTAssertEqual(request.value(forHTTPHeaderField: "Idempotency-Key"), "typed-1")
        let body = try XCTUnwrap(JSONSerialization.jsonObject(with: request.httpBody ?? Data()) as? [String: Any])
        XCTAssertEqual(body["text"] as? String, "what is going on")
    }

    func testTypedTurnRefusalsUsePlainWords() async {
        StubProtocol.handler = { _ in
            (503, Data(#"{"error":"the orchestrator is off (--brain=off)"}"#.utf8))
        }
        do {
            _ = try await StubProtocol.client().sendTurn("status")
            XCTFail("expected unavailable")
        } catch let failure as TypedTurnFailure {
            XCTAssertEqual(failure, .unavailable(reason: "the orchestrator is off (--brain=off)"))
            XCTAssertEqual(failure.errorDescription, "The orchestrator is off. Turn it on, then try again.")
        } catch {
            XCTFail("unexpected error: \(error)")
        }

        StubProtocol.handler = { _ in (409, Data(#"{"error":"a spoken turn is already answering this"}"#.utf8)) }
        do {
            _ = try await StubProtocol.client().sendTurn("status")
            XCTFail("expected claimed")
        } catch let failure as TypedTurnFailure {
            XCTAssertEqual(failure, .alreadyAnswered)
            XCTAssertEqual(failure.errorDescription, "The voice answered that first. Try again.")
        } catch {
            XCTFail("unexpected error: \(error)")
        }
    }

    func testTheTypedReplyIsWrittenRatherThanSpoken() throws {
        let json = #"{"id":4,"ts":"2026-09-22T08:31:20.3Z","kind":"dog","text":"Two agents are working.","via":"typed"}"#
        let item = try DaemonClient.decoder.decode(ConversationItem.self, from: Data(json.utf8))
        XCTAssertEqual(item.kind, .dog)
        XCTAssertEqual(item.via, "typed")
        XCTAssertTrue(item.isWritten)
    }
}

final class VoiceCommandTests: XCTestCase {
    func testEveryVoiceCommandHasADecision() {
        XCTAssertEqual(VoiceCommandAction.resolve(.voiceStop), .stopPlayback)
        XCTAssertEqual(VoiceCommandAction.resolve(.voicePTT(true)), .pushToTalk(true))
        XCTAssertEqual(VoiceCommandAction.resolve(.voicePTT(false)), .pushToTalk(false))
        XCTAssertEqual(VoiceCommandAction.resolve(.voiceMute(true)), .mute(true))
        XCTAssertEqual(VoiceCommandAction.resolve(.voiceMute(false)), .mute(false))
        XCTAssertEqual(VoiceCommandAction.resolve(WSMessage(type: .voiceCommand, action: .ptt)), .ignore)
        XCTAssertEqual(VoiceCommandAction.resolve(WSMessage(type: .voiceCommand)), .ignore)
        XCTAssertEqual(VoiceCommandAction.resolve(.pong), .ignore)
    }
}

final class VoiceHolderTests: XCTestCase {
    private func client(_ platform: String) -> DaemonState.VoiceInfo.Client {
        .init(name: "sheepdog-\(platform)", platform: platform)
    }

    func testAMacHolderAsksForUseHere() {
        let holder = VoiceHolderPresentation(client: client("macos"), localVoiceActive: false)
        XCTAssertTrue(holder.isElsewhere)
        XCTAssertTrue(holder.showsUseHere)
        XCTAssertEqual(holder.status, "Voice on Mac")
    }

    func testThisIPhoneOrAnActiveLocalClaimIsNotElsewhere() {
        XCTAssertFalse(VoiceHolderPresentation(client: client("ios"), localVoiceActive: false).isElsewhere)
        XCTAssertFalse(VoiceHolderPresentation(client: client("macos"), localVoiceActive: true).isElsewhere)
        XCTAssertFalse(VoiceHolderPresentation(client: nil, localVoiceActive: false).isElsewhere)
    }

    func testOtherPlatformsHavePlainWords() {
        XCTAssertEqual(VoiceHolderPresentation(client: client("web"), localVoiceActive: false).status, "Voice in the browser")
        XCTAssertEqual(VoiceHolderPresentation(client: client("windows"), localVoiceActive: false).status, "Voice on Windows")
        XCTAssertEqual(VoiceHolderPresentation(client: client("plan9"), localVoiceActive: false).status, "Voice on another device")
    }
}

final class DaemonUpgradeParityTests: XCTestCase {
    func testAChangedDaemonVersionForgetsTheOldShellAnswer() {
        var offer = PhoneShellOffer()
        offer.hello(version: "0.1.18")
        offer.ready(shell: false)
        XCTAssertEqual(offer.offered, false)

        offer.hello(version: "0.1.18")
        XCTAssertEqual(offer.offered, false, "the same daemon keeps the answer")

        offer.hello(version: "0.1.19")
        XCTAssertNil(offer.offered, "an upgraded daemon is asked again")
        XCTAssertEqual(offer.daemonVersion, "0.1.19")
        offer.ready(shell: true)
        XCTAssertEqual(offer.offered, true)
    }

    func testHelloCarriesTheDaemonsTerminalReason() throws {
        let json = #"""
        {"name":"sheepdog","version":"0.1.19","auth":"device","ws":"/ws",
         "audio":{"in_hz":16000,"out_hz":24000,"format":"pcm16le mono"},
         "term_unavailable":"the live terminal needs herdr-core with terminal.attach"}
        """#
        let hello = try DaemonClient.decoder.decode(Hello.self, from: Data(json.utf8))
        XCTAssertEqual(hello.termUnavailable, "the live terminal needs herdr-core with terminal.attach")
    }

    func testTheSnapshotUsesTheDaemonsReason() {
        var window = AgentWindowState(paneID: "w1:p2")
        _ = window.start(.no, termUnavailable: "the live terminal needs herdr 0.9.1 or newer")
        XCTAssertEqual(window.notice, .snapshotOnly)
        XCTAssertEqual(window.noticeText, "Snapshot only. The live terminal needs herdr 0.9.1 or newer")
        XCTAssertFalse(window.noticeText?.contains("0.9.2") == true)
    }
}
