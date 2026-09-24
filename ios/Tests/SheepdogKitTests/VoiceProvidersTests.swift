import AVFoundation
import XCTest
@testable import SheepdogKit

/// The voice provider contract (`GET /api/voice/providers`,
/// `PUT /api/voice/provider`, the new `voice_provider`/`voice_model` and
/// `status.provider` fields) — and that a daemon without any of it still
/// works.
final class VoiceProvidersTests: XCTestCase {
    static let list = #"""
    {"provider":"gemini","model":"gemini-3.8-live","providers":[
      {"id":"gemini","label":"Gemini Live","model":"gemini-3.8-live","available":true},
      {"id":"openai","label":"GPT-Live","model":"gpt-live-1","available":false,"reason":"no OPENAI_API_KEY in the daemon's environment","billing":"per_minute"}
    ]}
    """#

    override func setUp() {
        super.setUp()
        StubProtocol.seen = []
        StubProtocol.handler = nil
    }

    func testDecodesTheProviderList() throws {
        let list = try JSONDecoder().decode(VoiceProviders.self, from: Data(Self.list.utf8))
        XCTAssertEqual(list.provider, "gemini")
        XCTAssertEqual(list.model, "gemini-3.8-live")
        XCTAssertEqual(list.providers.map(\.id), ["gemini", "openai"])
        XCTAssertEqual(list.current?.displayName, "Gemini Live")
        XCTAssertEqual(list.display, "Gemini Live · gemini-3.8-live")
        let openai = list.providers[1]
        XCTAssertFalse(openai.isAvailable)
        XCTAssertEqual(openai.reason, "no OPENAI_API_KEY in the daemon's environment")
        XCTAssertTrue(openai.billedPerMinute)
        XCTAssertFalse(list.providers[0].billedPerMinute)
    }

    func testAMinimalProviderEntryStillDecodes() throws {
        let list = try JSONDecoder().decode(VoiceProviders.self, from: Data(#"{"providers":[{"id":"gemini"}],"extra":1}"#.utf8))
        XCTAssertNil(list.provider)
        XCTAssertTrue(list.providers[0].isAvailable, "no flag means available")
        XCTAssertEqual(list.providers[0].displayName, "gemini")
        XCTAssertNil(list.display)
        XCTAssertEqual(try JSONDecoder().decode(VoiceProviders.self, from: Data("{}".utf8)).providers, [])
    }

    func testGetReturnsTheList() async throws {
        StubProtocol.handler = { _ in (200, Data(Self.list.utf8)) }
        let list = try await StubProtocol.client().voiceProviders()
        XCTAssertEqual(list?.providers.count, 2)
        XCTAssertEqual(StubProtocol.seen.first?.url?.path, "/api/voice/providers")
        XCTAssertEqual(StubProtocol.seen.first?.httpMethod, "GET")
    }

    func testAnOlderDaemonAnswers404AndThePickerHides() async throws {
        StubProtocol.handler = { _ in (404, Data("404 page not found".utf8)) }
        let list = try await StubProtocol.client().voiceProviders()
        XCTAssertNil(list)
    }

    func testPutSwitchesAndReturnsTheSameShape() async throws {
        StubProtocol.handler = { _ in
            (200, Data(#"{"provider":"openai","model":"gpt-live-1","providers":[{"id":"gemini","available":true},{"id":"openai","available":true,"billing":"per_minute"}]}"#.utf8))
        }
        let list = try await StubProtocol.client(token: "t").setVoiceProvider("openai")
        XCTAssertEqual(list.provider, "openai")
        let request = try XCTUnwrap(StubProtocol.seen.first)
        XCTAssertEqual(request.httpMethod, "PUT")
        XCTAssertEqual(request.url?.path, "/api/voice/provider")
        XCTAssertEqual(request.value(forHTTPHeaderField: "Authorization"), "Bearer t")
        let body = try XCTUnwrap(JSONSerialization.jsonObject(with: request.httpBody ?? Data()) as? [String: Any])
        XCTAssertEqual(body["provider"] as? String, "openai")
    }

    func testPutOfAnUnavailableProviderCarriesTheDaemonsReason() async {
        StubProtocol.handler = { _ in (400, Data(#"{"error":"openai is not available: no OPENAI_API_KEY"}"#.utf8)) }
        do {
            _ = try await StubProtocol.client().setVoiceProvider("openai")
            XCTFail("expected a refusal")
        } catch {
            XCTAssertEqual(error as? DaemonError, .rejected(status: 400, message: "openai is not available: no OPENAI_API_KEY"))
        }
    }

    func testHelloAndStateCarryTheProviderWhenTheDaemonSendsIt() throws {
        let hello = try DaemonClient.decoder.decode(Hello.self, from: Data(#"""
        {"name":"sheepdog","version":"0.9","voice_name":"Sheepdog","auth":"none","ws":"/ws",
         "audio":{"in_hz":16000,"out_hz":24000,"format":"pcm16le mono"},
         "voice_provider":"openai","voice_model":"gpt-live-1"}
        """#.utf8))
        XCTAssertEqual(hello.voiceProvider, "openai")
        XCTAssertEqual(hello.voiceModel, "gpt-live-1")

        let state = try DaemonClient.decoder.decode(DaemonState.self, from: Data(#"""
        {"agents":[],"voice":{"mode":"gemini","status":"connected","muted":false,"stalled":false,"client_connected":true},
         "voice_provider":"gemini","voice_model":"gemini-3.8-live"}
        """#.utf8))
        XCTAssertEqual(state.voiceProvider, "gemini")
        XCTAssertEqual(state.voiceModel, "gemini-3.8-live")
    }

    func testOlderPayloadsWithoutTheFieldsStillDecode() throws {
        let hello = try DaemonClient.decoder.decode(Hello.self, from: Data(#"{"name":"sheepdog","version":"0.8","auth":"none","ws":"/ws","audio":{"in_hz":16000,"out_hz":24000,"format":"pcm16le mono"}}"#.utf8))
        XCTAssertNil(hello.voiceProvider)
        let state = try DaemonClient.decoder.decode(DaemonState.self, from: Data(#"{"agents":[],"voice":{"mode":"off","status":"closed","muted":false,"stalled":false,"client_connected":false}}"#.utf8))
        XCTAssertNil(state.voiceModel)
    }

    func testTheStatusMessageCarriesTheProvider() throws {
        let m = try WSMessage.decode(Data(#"{"type":"status","voice":"connected","muted":false,"provider":"openai"}"#.utf8))
        XCTAssertEqual(m.provider, "openai")
        XCTAssertEqual(try WSMessage.status(.connected, provider: "gemini").encodedString(), #"{"muted":false,"provider":"gemini","type":"status","voice":"connected"}"#)
        let old = try WSMessage.decode(Data(#"{"type":"status","voice":"connected"}"#.utf8))
        XCTAssertNil(old.provider)
        XCTAssertEqual(try WSMessage.status(.connected).encodedString(), #"{"muted":false,"type":"status","voice":"connected"}"#, "no provider, no key — same bytes as before")
    }

    // MARK: - Microphone mode

    func testMicrophoneModesAreNamedAndVoiceIsolationIsRecommended() {
        XCTAssertEqual(MicrophoneModeName(rawValue: 0), .standard)
        XCTAssertEqual(MicrophoneModeName(rawValue: 1), .wideSpectrum)
        XCTAssertEqual(MicrophoneModeName(rawValue: 2), .voiceIsolation)
        XCTAssertEqual(MicrophoneModeName(rawValue: 9), .unknown)
        XCTAssertEqual(MicrophoneModeName.voiceIsolation.label, "Voice Isolation")
        XCTAssertEqual(MicrophoneModeName.wideSpectrum.label, "Wide Spectrum")
        XCTAssertTrue(MicrophoneModeName.standard.advice.contains("Voice Isolation"))
    }

    func testTheAVFoundationRawValuesMatch() {
        XCTAssertEqual(MicrophoneModeName(rawValue: AVCaptureDevice.MicrophoneMode.standard.rawValue), .standard)
        XCTAssertEqual(MicrophoneModeName(rawValue: AVCaptureDevice.MicrophoneMode.voiceIsolation.rawValue), .voiceIsolation)
        XCTAssertEqual(MicrophoneModeName(rawValue: AVCaptureDevice.MicrophoneMode.wideSpectrum.rawValue), .wideSpectrum)
    }

    // MARK: - Settings

    func testTalkModeDefaultsAndRoundTrip() {
        let defaults = UserDefaults(suiteName: "voice-settings-\(UUID().uuidString)")!
        let store = SettingsStore(defaults: defaults, secrets: InMemorySecretStore())
        XCTAssertEqual(store.talkMode, .openMic, "hold-to-talk is off by default")
        store.talkMode = .holdToTalk
        XCTAssertEqual(store.talkMode, .holdToTalk)
    }
}
