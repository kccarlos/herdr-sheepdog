import XCTest
@testable import SheepdogKit

/// The pure half of the LiveKit transport, against herdr-sheepdog-bridge's
/// docs/client-contract.md: where the bridge is, its token endpoint, and
/// the control packets on `sheepdog.control`.
final class LiveKitBridgeTests: XCTestCase {
    // MARK: - The bridge's address

    func testAnEmptyBridgeAddressMeansTheDaemonsHostOnPort7778() throws {
        let daemon = try XCTUnwrap(DaemonEndpoint(text: "100.64.1.2:7777", token: "secret"))
        let bridge = try XCTUnwrap(LiveKitBridge.endpoint(bridgeAddress: "  ", daemon: daemon))
        XCTAssertEqual(bridge.host, "100.64.1.2")
        XCTAssertEqual(bridge.port, 7778)
        XCTAssertEqual(bridge.token, "secret", "the daemon token is the bridge's bearer")
        XCTAssertFalse(bridge.secure)
    }

    func testTheBridgeInheritsHTTPSFromTheDaemon() throws {
        let daemon = try XCTUnwrap(DaemonEndpoint(text: "https://mac.example:443"))
        let bridge = try XCTUnwrap(LiveKitBridge.endpoint(bridgeAddress: "", daemon: daemon))
        XCTAssertTrue(bridge.secure)
        XCTAssertEqual(bridge.display, "mac.example:7778")
    }

    func testATypedBridgeAddressWinsAndDefaultsToPort7778() throws {
        let daemon = try XCTUnwrap(DaemonEndpoint(text: "192.168.1.20:7777", token: "t"))
        let typed = try XCTUnwrap(LiveKitBridge.endpoint(bridgeAddress: "vm.tailnet", daemon: daemon))
        XCTAssertEqual(typed.display, "vm.tailnet:7778")
        XCTAssertEqual(typed.token, "t")
        let withPort = try XCTUnwrap(LiveKitBridge.endpoint(bridgeAddress: "http://10.0.0.5:9000", daemon: daemon))
        XCTAssertEqual(withPort.display, "10.0.0.5:9000")
    }

    func testNoBridgeWithoutEitherAddress() {
        XCTAssertNil(LiveKitBridge.endpoint(bridgeAddress: "", daemon: nil))
        XCTAssertNil(LiveKitBridge.endpoint(bridgeAddress: "http://", daemon: nil))
        XCTAssertEqual(LiveKitBridge.defaultAddress(daemon: DaemonEndpoint(text: "mac.local")), "mac.local:7778")
    }

    // MARK: - The token request

    func testTheTokenRequestCarriesTheBearerAndTheIdentity() throws {
        let bridge = DaemonEndpoint(host: "127.0.0.1", port: 7778, token: "dev")
        let request = try XCTUnwrap(LiveKitBridge.tokenRequest(bridge: bridge, identity: "phone-1a2b3c4d"))
        XCTAssertEqual(request.httpMethod, "GET")
        XCTAssertEqual(request.url?.absoluteString, "http://127.0.0.1:7778/livekit/token?identity=phone-1a2b3c4d")
        XCTAssertEqual(request.value(forHTTPHeaderField: "Authorization"), "Bearer dev")
    }

    func testAnInvalidIdentityIsLeftToTheBridge() throws {
        let bridge = DaemonEndpoint(host: "127.0.0.1", port: 7778, token: "")
        let request = try XCTUnwrap(LiveKitBridge.tokenRequest(bridge: bridge, identity: "sheepdog-bridge-2"))
        XCTAssertEqual(request.url?.absoluteString, "http://127.0.0.1:7778/livekit/token")
        XCTAssertNil(request.value(forHTTPHeaderField: "Authorization"), "no token, no header")
    }

    func testIdentityRules() {
        XCTAssertTrue(LiveKitBridge.isValidIdentity("phone-1a2b3c4d"))
        XCTAssertTrue(LiveKitBridge.isValidIdentity("a_b.c:d-E9"))
        XCTAssertFalse(LiveKitBridge.isValidIdentity(""))
        XCTAssertFalse(LiveKitBridge.isValidIdentity(String(repeating: "a", count: 65)))
        XCTAssertFalse(LiveKitBridge.isValidIdentity("has space"))
        XCTAssertFalse(LiveKitBridge.isValidIdentity("phöne"))
        XCTAssertFalse(LiveKitBridge.isValidIdentity("sheepdog-bridge"))
        let id = LiveKitBridge.identity(deviceID: UUID(uuidString: "1A2B3C4D-0000-0000-0000-000000000000"))
        XCTAssertEqual(id, "phone-1a2b3c4d")
        XCTAssertTrue(LiveKitBridge.isValidIdentity(id ?? ""))
        XCTAssertNil(LiveKitBridge.identity(deviceID: nil))
    }

    // MARK: - The token response

    private let bridge = DaemonEndpoint(host: "100.64.1.2", port: 7778, token: "dev")

    func testTheContractsTokenResponseDecodes() throws {
        let body = Data("""
        {"url":"ws://100.64.1.2:7880","token":"eyJhbGciOi","room":"sheepdog","identity":"phone-1a2b3c4d","expires_at":"2026-10-02T13:03:36Z"}
        """.utf8)
        let token = try LiveKitBridge.tokenResponse(status: 200, body: body, bridge: bridge)
        XCTAssertEqual(token.url, "ws://100.64.1.2:7880")
        XCTAssertEqual(token.token, "eyJhbGciOi")
        XCTAssertEqual(token.room, "sheepdog")
        XCTAssertEqual(token.identity, "phone-1a2b3c4d")
        XCTAssertEqual(token.expiresAt, ISO8601DateFormatter().date(from: "2026-10-02T13:03:36Z"))
        XCTAssertTrue(token.isFresh(at: token.expiresAt.addingTimeInterval(-3600)))
        XCTAssertFalse(token.isFresh(at: token.expiresAt.addingTimeInterval(-30)), "a minute to spare")
    }

    func testTokenFailuresUseTheDaemonsWords() {
        XCTAssertThrowsError(try LiveKitBridge.tokenResponse(status: 401, body: Data(#"{"error":"unauthorized"}"#.utf8), bridge: bridge)) {
            XCTAssertEqual($0 as? DaemonError, .unauthorized(endpoint: "100.64.1.2:7778"))
        }
        XCTAssertThrowsError(try LiveKitBridge.tokenResponse(status: 400, body: Data(#"{"error":"bad identity"}"#.utf8), bridge: bridge)) {
            XCTAssertEqual($0 as? DaemonError, .rejected(status: 400, message: "bridge: bad identity"))
        }
        XCTAssertThrowsError(try LiveKitBridge.tokenResponse(status: 404, body: Data(), bridge: bridge)) {
            XCTAssertEqual($0 as? DaemonError, .http(status: 404, endpoint: "100.64.1.2:7778"))
        }
        XCTAssertThrowsError(try LiveKitBridge.tokenResponse(status: 200, body: Data(#"{"url":"ws://x"}"#.utf8), bridge: bridge)) {
            guard case .malformed? = $0 as? DaemonError else { return XCTFail("\($0)") }
        }
    }

    // MARK: - Control packets

    func testTheBridgesOwnMessageDecodes() throws {
        XCTAssertEqual(try ControlPacket.decode(Data(#"{"type":"bridge","daemon":"connected"}"#.utf8)), .bridge(.connected))
        XCTAssertEqual(try ControlPacket.decode(Data(#"{"type":"bridge","daemon":"disconnected"}"#.utf8)), .bridge(.disconnected))
        XCTAssertEqual(try ControlPacket.decode(Data(#"{"type":"bridge","daemon":"replaced"}"#.utf8)), .bridge(.replaced))
        XCTAssertThrowsError(try ControlPacket.decode(Data(#"{"type":"bridge","daemon":"sideways"}"#.utf8)))
        XCTAssertThrowsError(try ControlPacket.decode(Data(#"{"type":"bridge"}"#.utf8)))
        XCTAssertEqual(BridgeLink.replaced.word, "In use by the browser")
    }

    func testDaemonFramesDecodeExactlyAsOverTheWebSocket() throws {
        let frames = [
            #"{"type":"status","voice":"connected","muted":false,"provider":"gemini"}"#,
            #"{"type":"transcript","dir":"out","text":"echo: hello","final":true}"#,
            #"{"type":"flush"}"#,
            #"{"type":"error","message":"bridge: bad control message: x"}"#,
            #"{"type":"pong"}"#,
        ]
        for frame in frames {
            let data = Data(frame.utf8)
            XCTAssertEqual(try ControlPacket.decode(data), .message(try WSMessage.decode(data)), frame)
        }
    }

    func testUnknownTypesAndNonObjectsAreRejected() {
        XCTAssertThrowsError(try ControlPacket.decode(Data(#"{"type":"teleport"}"#.utf8)))
        XCTAssertThrowsError(try ControlPacket.decode(Data("[1,2]".utf8)))
        XCTAssertThrowsError(try ControlPacket.decode(Data("not json".utf8)))
    }

    func testOutgoingIsTheWebSocketsBytes() throws {
        for message in [WSMessage.mute(true), .silence(false), .cancel, WSMessage(type: .text, text: "hello"), WSMessage(type: .ping)] {
            XCTAssertEqual(ControlPacket.outgoing(message), try message.encode())
        }
        XCTAssertEqual(ControlPacket.outgoing(.mute(true)).map { String(decoding: $0, as: UTF8.self) }, #"{"on":true,"type":"mute"}"#)
    }

    func testPlaybackAndSpeakingNeverGoOverTheBridge() {
        XCTAssertNil(ControlPacket.outgoing(.playback(.started, playedMs: 0)), "the bridge owns playback reports")
        XCTAssertNil(ControlPacket.outgoing(.playback(.flushed, playedMs: 120)))
        XCTAssertNil(ControlPacket.outgoing(.speaking(true)), "one speaking would switch off the bridge's speaker detection")
        XCTAssertNil(ControlPacket.outgoing(.speaking(false)))
    }

    func testAnInvalidMessageIsNotSent() {
        XCTAssertNil(ControlPacket.outgoing(WSMessage(type: .text, text: "")))
    }

    // MARK: - Settings

    func testTransportSettingsPersistAndDefaultToTheWebSocket() throws {
        let suite = "LiveKitBridgeTests.\(UUID().uuidString)"
        let defaults = try XCTUnwrap(UserDefaults(suiteName: suite))
        defer { defaults.removePersistentDomain(forName: suite) }
        let secrets = InMemorySecretStore()
        let store = SettingsStore(defaults: defaults, secrets: secrets)
        XCTAssertEqual(store.voiceTransport, .webSocket)
        XCTAssertEqual(store.bridgeAddress, "")
        XCTAssertNil(store.bridgeEndpoint)

        store.address = "100.64.1.2:7777"
        store.token = "secret"
        XCTAssertEqual(store.bridgeEndpoint?.display, "100.64.1.2:7778")
        XCTAssertEqual(store.bridgeEndpoint?.token, "secret")

        store.voiceTransport = .liveKit
        store.bridgeAddress = "vm.tailnet:7790"
        let reread = SettingsStore(defaults: defaults, secrets: secrets)
        XCTAssertEqual(reread.voiceTransport, .liveKit)
        XCTAssertEqual(reread.bridgeAddress, "vm.tailnet:7790")
        XCTAssertEqual(reread.bridgeEndpoint?.display, "vm.tailnet:7790")
        XCTAssertNil(defaults.string(forKey: "daemon.token"), "the token stays out of defaults")

        defaults.set("carrier-pigeon", forKey: "voice.transport")
        XCTAssertEqual(reread.voiceTransport, .webSocket, "an unknown value falls back to the WebSocket")
    }
}
