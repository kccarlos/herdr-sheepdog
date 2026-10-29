import XCTest
@testable import Sheepdog
@testable import SheepdogKit

/// `VoiceEngine` without a microphone or a socket: a host that runs every
/// effect itself (as the demo does) records what the engine hands over,
/// and the tests check the view state the engine publishes.
@MainActor
final class VoiceEngineTests: XCTestCase {
    private final class Host: VoiceEngineHost {
        var effects: [SessionEffect] = []
        var wantsConnectionChanges: [Bool] = []
        var handshakes: [ProtocolHandshake.State] = []
        var publishes = 0
        var controls: [WSMessage.Kind] = []
        var notices: [LocalNotice] = []
        var canShowTip = true
        var tipsShown = 0

        func voiceEngine(_ engine: VoiceEngine, run effect: SessionEffect) -> Bool {
            effects.append(effect)
            return true
        }

        func voiceEngineWantsConnectionChanged(_ engine: VoiceEngine) {
            wantsConnectionChanges.append(engine.wantsConnection)
        }

        func voiceEngine(_ engine: VoiceEngine, handshakeChanged handshake: ProtocolHandshake.State) {
            if handshakes.last != handshake { handshakes.append(handshake) }
        }

        func voiceEngineDidPublish(_ engine: VoiceEngine) { publishes += 1 }
        func voiceEngine(_ engine: VoiceEngine, socket event: VoiceSocket.Event) {}
        func voiceEngine(_ engine: VoiceEngine, control message: WSMessage) { controls.append(message.type) }
        func voiceEngine(_ engine: VoiceEngine, notice: LocalNotice) { notices.append(notice) }
        var voiceEngineSendsDrains: Bool { false }
        var voiceEngineCanShowTip: Bool { canShowTip }
        func voiceEngineShowMicrophoneTip(_ engine: VoiceEngine) { tipsShown += 1 }
    }

    private var defaults: UserDefaults!
    private let suite = "sheepdog.tests.voice-engine"
    private var modes: (active: MicrophoneModeName, preferred: MicrophoneModeName) = (.standard, .standard)

    override func setUp() {
        super.setUp()
        UserDefaults().removePersistentDomain(forName: suite)
        defaults = UserDefaults(suiteName: suite)
        modes = (.standard, .standard)
    }

    override func tearDown() {
        UserDefaults().removePersistentDomain(forName: suite)
        super.tearDown()
    }

    private func engine(talkMode: TalkMode = .openMic, host: Host) -> VoiceEngine {
        let engine = VoiceEngine(endpoint: DaemonEndpoint(host: "127.0.0.1", port: 9), talkMode: talkMode,
                                 keepListeningWhenLocked: false, defaults: defaults,
                                 microphoneModes: { [unowned self] in self.modes })
        engine.host = host
        return engine
    }

    /// Connect, the probe, the socket, the daemon's voice and the engine.
    private func connect(_ engine: VoiceEngine, muted: Bool = false) {
        engine.send(.connectTapped)
        engine.send(.probeSucceeded(micGranted: true))
        engine.send(.socketOpened)
        engine.send(.status(.status(.connected, muted: muted)))
        engine.send(.audioStarted(gateClosed: engine.talkMode == .holdToTalk))
    }

    func testConnectAndEndPublishTheStateAndTellTheHost() {
        let host = Host()
        let engine = engine(host: host)
        XCTAssertEqual(engine.state, .disconnected)
        XCTAssertFalse(engine.wantsConnection)

        engine.send(.connectTapped)
        XCTAssertEqual(engine.state, .connecting)
        XCTAssertTrue(engine.wantsConnection)
        XCTAssertEqual(host.wantsConnectionChanges, [true], "the host learns it once, after the engine says so")
        XCTAssertEqual(host.effects, [.clearBanner, .probe], "the probe and the banner are the host's")

        engine.send(.probeSucceeded(micGranted: true))
        engine.send(.socketOpened)
        engine.send(.status(.status(.connected, muted: false, provider: "gemini")))
        engine.send(.audioStarted(gateClosed: false))
        XCTAssertEqual(engine.state, .listening)
        XCTAssertEqual(engine.liveVoiceProvider, "gemini")
        XCTAssertEqual(host.publishes, 5, "every event ends with the host's publish")

        engine.send(.endTapped)
        XCTAssertEqual(engine.state, .disconnected)
        XCTAssertFalse(engine.wantsConnection)
        XCTAssertEqual(host.wantsConnectionChanges, [true, false])
        XCTAssertEqual(Array(host.effects.suffix(3)), [.closeSocket, .stopAudio, .stopTicker])
    }

    func testMuteMirrorsTheCoreAndShowsTheHintOncePerInstall() {
        let host = Host()
        let engine = engine(host: host)
        connect(engine)
        XCTAssertFalse(engine.isMuted)

        engine.toggleMute()
        XCTAssertTrue(engine.isMuted)
        XCTAssertEqual(engine.state, .muted)
        XCTAssertTrue(engine.showMuteHint)
        XCTAssertTrue(host.effects.contains(.setCaptureMuted(true)))

        engine.dismissMuteHint()
        engine.toggleMute()
        XCTAssertFalse(engine.isMuted)
        engine.toggleMute()
        XCTAssertTrue(engine.isMuted)
        XCTAssertFalse(engine.showMuteHint, "the hint is once per install")
    }

    func testHoldToTalkOpensWhileHeldAndTalkModeFollowsTheCore() {
        let host = Host()
        let engine = engine(host: host)
        connect(engine)

        engine.send(.talkModeChanged(.holdToTalk))
        XCTAssertEqual(engine.talkMode, .holdToTalk)
        XCTAssertFalse(engine.holding)

        engine.pressToTalk()
        XCTAssertTrue(engine.holding)
        engine.releaseToTalk()
        XCTAssertFalse(engine.holding)

        engine.send(.talkModeChanged(.openMic))
        XCTAssertEqual(engine.talkMode, .openMic)
    }

    func testLockScreenTalkToggleOpensThenSends() {
        let engine = engine(talkMode: .holdToTalk, host: Host())
        connect(engine)
        engine.toggleTalk()
        XCTAssertTrue(engine.holding, "the first tap opens the gate")
        engine.toggleTalk()
        XCTAssertFalse(engine.holding, "the second tap closes it and sends")
    }

    func testLockScreenTalkClosesByItselfAfterTheLimit() async throws {
        let engine = engine(talkMode: .holdToTalk, host: Host())
        engine.lockScreenTalkLimit = .milliseconds(50)
        connect(engine)
        engine.toggleTalk()
        XCTAssertTrue(engine.holding)
        let deadline = Date().addingTimeInterval(3)
        while engine.holding, Date() < deadline {
            try await Task.sleep(for: .milliseconds(20))
        }
        XCTAssertFalse(engine.holding, "a forgotten tap does not leave the mic open")
    }

    func testLockScreenTalkDoesNothingInOpenMic() {
        let engine = engine(host: Host())
        connect(engine)
        engine.toggleTalk()
        XCTAssertFalse(engine.holding)
    }

    func testAnEngineMadeInHoldToTalkStartsThere() {
        let engine = engine(talkMode: .holdToTalk, host: Host())
        XCTAssertEqual(engine.talkMode, .holdToTalk)
        XCTAssertFalse(engine.holding)
    }

    func testTheMicrophoneModesAreAssignedOnlyOnChange() {
        let engine = engine(host: Host())
        XCTAssertEqual(engine.microphoneMode, .standard)

        modes = (.voiceIsolation, .wideSpectrum)
        engine.refreshMicrophoneMode()
        XCTAssertEqual(engine.microphoneMode, .voiceIsolation)
        XCTAssertEqual(engine.preferredMicrophoneMode, .wideSpectrum)

        var changes = 0
        withObservationTracking { _ = engine.microphoneMode } onChange: { changes += 1 }
        engine.refreshMicrophoneMode()
        XCTAssertEqual(changes, 0, "the same mode again does not re-render")
    }

    func testTheCoachMarkFirstThenTheVoiceIsolationTipNextTime() {
        let host = Host()
        let first = engine(host: host)
        connect(first)
        XCTAssertTrue(first.showCoachMark, "the first time it listens: just talk")
        XCTAssertEqual(host.tipsShown, 0)
        first.dismissCoachMark()
        XCTAssertFalse(first.showCoachMark)
        first.send(.endTapped)

        // The next session: the tip, once, and not while a banner is up.
        host.canShowTip = false
        connect(first)
        XCTAssertEqual(host.tipsShown, 0, "a banner is up")
        first.send(.endTapped)
        host.canShowTip = true
        connect(first)
        XCTAssertFalse(first.showCoachMark)
        XCTAssertEqual(host.tipsShown, 1)
        first.send(.endTapped)
        connect(first)
        XCTAssertEqual(host.tipsShown, 1, "once per install")
    }

    func testNoVoiceIsolationTipWhenItIsAlreadyOn() {
        defaults.set(true, forKey: "hint.coach.shown")
        modes = (.voiceIsolation, .voiceIsolation)
        let host = Host()
        let engine = engine(host: host)
        connect(engine)
        XCTAssertEqual(host.tipsShown, 0)
    }

    func testNoMicSignalMirrorsTheCore() {
        let engine = engine(host: Host())
        connect(engine)
        engine.send(.micSignal(ok: false))
        XCTAssertTrue(engine.noMicSignal)
        engine.send(.micSignal(ok: true))
        XCTAssertFalse(engine.noMicSignal)
    }

    /// The consent gate's path: the session's probe finds consent withdrawn
    /// and ends the voice before anything opens.
    func testAnEndFromTheProbeLeavesNothingOpen() {
        let host = Host()
        let engine = engine(host: host)
        engine.send(.connectTapped)
        host.effects.removeAll()
        engine.send(.endTapped)
        XCTAssertEqual(engine.state, .disconnected)
        XCTAssertFalse(engine.wantsConnection)
        XCTAssertFalse(host.effects.contains(.openSocket))
        // A probe answer that lands after it changes nothing.
        engine.send(.probeSucceeded(micGranted: true))
        XCTAssertFalse(engine.wantsConnection)
        XCTAssertFalse(host.effects.contains(.openSocket))
    }

    /// Switching computers: the voice ends on the old one, `/ws` moves,
    /// and it connects again on the new one, ending in a fresh probe.
    func testSwitchingComputersEndsThenReconnects() {
        let host = Host()
        let engine = engine(host: host)
        connect(engine)
        host.effects.removeAll()
        host.wantsConnectionChanges.removeAll()

        engine.send(.endTapped)
        engine.update(endpoint: DaemonEndpoint(host: "127.0.0.2", port: 9))
        engine.send(.connectTapped)
        XCTAssertEqual(host.effects, [.closeSocket, .stopAudio, .stopTicker, .clearBanner, .probe])
        XCTAssertEqual(host.wantsConnectionChanges, [false, true])
        XCTAssertEqual(engine.state, .connecting)
        XCTAssertFalse(engine.isMuted)
    }

    func testControlMessagesThatAreNotTheVoicesGoToTheHost() {
        let host = Host()
        let engine = engine(host: host)
        connect(engine)
        engine.receive(control: WSMessage(type: .transcript))
        engine.receive(control: WSMessage(type: .card))
        engine.receive(control: WSMessage(type: .pong))
        XCTAssertEqual(host.controls, [.transcript, .card])
    }
}

/// The consent gate in front of the voice (`SessionViewModel+Consent.swift`):
/// Connect without consent asks and sends nothing; agreeing connects.
@MainActor
final class VoiceConsentTests: XCTestCase {
    func testConnectWaitsForConsentAndAgreeingStartsTheVoice() throws {
        let suite = "sheepdog.tests.voice-consent"
        UserDefaults().removePersistentDomain(forName: suite)
        defer { UserDefaults().removePersistentDomain(forName: suite) }
        let defaults = try XCTUnwrap(UserDefaults(suiteName: suite))
        let store = SettingsStore(defaults: defaults, computers: ComputerStore(defaults: defaults, secrets: MemorySecretStore()))
        store.address = "127.0.0.1:9"
        store.hasBeenConfigured = true
        let directory = FileManager.default.temporaryDirectory.appendingPathComponent("VoiceConsentTests-\(UUID().uuidString)", isDirectory: true)
        defer { try? FileManager.default.removeItem(at: directory) }
        let model = SessionViewModel(settings: store, cache: ConversationCache(directory: directory))

        model.connect()
        XCTAssertFalse(model.voice.wantsConnection, "nothing starts before consent")
        XCTAssertTrue(model.presentation.isShowing(.consent))

        model.declineAIServices()
        XCTAssertFalse(model.voice.wantsConnection)

        model.connect()
        model.agreeToAIServices()
        XCTAssertTrue(model.voice.wantsConnection, "agreeing runs the connect that asked")
        model.withdrawFromAIServices()
        XCTAssertFalse(model.voice.wantsConnection, "withdrawing ends the voice")
    }
}
