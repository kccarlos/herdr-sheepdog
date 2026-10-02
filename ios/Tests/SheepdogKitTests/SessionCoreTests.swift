import XCTest
@testable import SheepdogKit

/// Event sequences through `SessionCore`, pinning the session's behaviour
/// as it was when the logic lived in `SessionViewModel`: the state after
/// each event and the exact effects it asks for, in order.
final class SessionCoreTests: XCTestCase {
    private let t0 = Date(timeIntervalSinceReferenceDate: 1_000)
    private let unreachable = DaemonError.unreachable(endpoint: "127.0.0.1:7799", detail: "Could not connect to the server.")

    private func at(_ seconds: TimeInterval) -> Date { t0.addingTimeInterval(seconds) }

    /// Connect, probe, open, and the daemon's first `status`.
    private func connected(talkMode: TalkMode = .openMic, muted: Bool = false, voice: WSMessage.Voice = .connected) -> SessionCore {
        var core = SessionCore(talkMode: talkMode)
        _ = core.apply(.connectTapped, now: t0)
        _ = core.apply(.probeSucceeded(micGranted: true), now: t0)
        _ = core.apply(.socketOpened, now: t0)
        _ = core.apply(.status(.status(voice, muted: muted)), now: t0)
        _ = core.apply(.audioStarted(gateClosed: talkMode == .holdToTalk), now: t0)
        return core
    }

    // MARK: - The golden walk

    func testGoldenWalkConnectOpenStatusTalkToolCallSpeakStopEnd() {
        var core = SessionCore()
        XCTAssertEqual(core.sessionState, .disconnected)
        XCTAssertEqual(core.voiceBar.orbAction, .connect)

        // Connect: the probe first.
        XCTAssertEqual(core.apply(.connectTapped, now: at(0)), [.clearBanner, .probe])
        XCTAssertEqual(core.sessionState, .connecting)
        XCTAssertEqual(core.connectionChip.status, .connecting)
        XCTAssertTrue(core.connection.wantsConnection)

        // The probe passed and the microphone is allowed: open everything.
        XCTAssertEqual(core.apply(.probeSucceeded(micGranted: true), now: at(0.1)), [.openSocket, .startStreams, .startTicker])
        XCTAssertFalse(core.connection.socketMuted, "the socket says mute(false) on open")
        XCTAssertFalse(core.connection.socketGateClosed)

        XCTAssertEqual(core.apply(.socketOpened, now: at(0.2)), [.clearBanner, .haptic(.connected)])
        XCTAssertEqual(core.sessionState, .idle, "open, no voice status yet")
        XCTAssertEqual(core.apply(.eventsChanged(.open), now: at(0.2)), [])
        XCTAssertEqual(core.connectionChip.status, .connected)

        // The daemon's voice is up: the engine starts.
        XCTAssertEqual(core.apply(.status(.status(.connected, muted: false, provider: "gemini")), now: at(0.3)), [.startAudio])
        XCTAssertEqual(core.voice.provider, "gemini")
        XCTAssertEqual(core.apply(.audioStarted(gateClosed: false), now: at(0.4)), [])
        XCTAssertEqual(core.sessionState, .listening)
        XCTAssertEqual(core.statusLine, "Listening — just talk")

        // Talk: the gate closes on silence and opens on speech.
        XCTAssertEqual(core.apply(.gateChanged(closed: true), now: at(1)), [.send(.silence(true))])
        XCTAssertEqual(core.sessionState, .silent)
        XCTAssertEqual(core.apply(.gateChanged(closed: false), now: at(2)), [.send(.silence(false)), .send(.speaking(true))])
        XCTAssertEqual(core.sessionState, .listening)

        // A tool call: thinking.
        XCTAssertEqual(core.apply(.toolCall, now: at(3)), [])
        XCTAssertEqual(core.sessionState, .thinking)
        XCTAssertEqual(core.voiceBar.orbAction, .stop)
        XCTAssertEqual(core.apply(.toolResult, now: at(4)), [.refreshState])
        XCTAssertEqual(core.sessionState, .listening)

        // The dog speaks.
        XCTAssertEqual(core.apply(.playbackChanged(speaking: true), now: at(5)), [])
        XCTAssertEqual(core.sessionState, .speaking)
        XCTAssertTrue(core.playbackBusy)

        // Stop.
        XCTAssertEqual(core.apply(.stopTapped, now: at(6)), [.flushPlayback, .flushTranscript, .send(.cancel), .haptic(.stop)])
        XCTAssertEqual(core.sessionState, .listening, "a stop is not a playback gap: no hold")
        XCTAssertFalse(core.playbackBusy)
        // The engine's own report of the flush changes nothing more.
        XCTAssertEqual(core.apply(.playbackChanged(speaking: false), now: at(6)), [])
        XCTAssertEqual(core.sessionState, .listening)

        // End.
        XCTAssertEqual(core.apply(.endTapped, now: at(7)), [.closeSocket, .stopAudio, .stopStreams, .stopTicker])
        XCTAssertEqual(core.sessionState, .disconnected)
        XCTAssertEqual(core.connectionChip.status, .notConnected)
        XCTAssertNil(core.voice.status)
        XCTAssertNil(core.voice.provider)
        XCTAssertFalse(core.connection.wantsConnection)
    }

    // MARK: - Connecting

    func testAProbeThatLandsAfterEndDoesNothing() {
        var core = SessionCore()
        _ = core.apply(.connectTapped, now: t0)
        _ = core.apply(.endTapped, now: t0)
        XCTAssertEqual(core.apply(.probeSucceeded(micGranted: true), now: t0), [])
        XCTAssertEqual(core.sessionState, .disconnected)
    }

    func testADeniedMicrophoneStillOpensTheSession() {
        var core = SessionCore()
        _ = core.apply(.connectTapped, now: t0)
        XCTAssertEqual(core.apply(.probeSucceeded(micGranted: false), now: t0), [.showBanner(.microphoneDenied), .openSocket, .startStreams, .startTicker])
    }

    func testAFailedProbeEndsTheSessionWithABanner() {
        var core = SessionCore()
        _ = core.apply(.connectTapped, now: t0)
        XCTAssertEqual(core.apply(.probeFailed(unreachable), now: t0), [.showBanner(.connectionFailed(unreachable)), .closeSocket, .stopAudio, .stopStreams, .stopTicker])
        XCTAssertFalse(core.connection.wantsConnection)
        XCTAssertEqual(core.connectionChip.status, .offline)
        XCTAssertEqual(core.sessionState, .disconnected)
    }

    // MARK: - Rule: reconnect keeps retrying (quiet 30 s, one banner, on)

    func testADaemonRestartReconnectsQuietlyThenOneBannerThenKeepsGoing() {
        var core = connected()
        XCTAssertEqual(core.apply(.socketClosed(reason: "connection lost", willRetry: true), now: at(10)), [.stopAudio])
        XCTAssertEqual(core.sessionState, .reconnecting)
        XCTAssertTrue(core.connection.wantsConnection)

        for s in [11.0, 13, 17, 25] {
            XCTAssertEqual(core.apply(.socketFailed(unreachable, willRetry: true), now: at(s)), [.log(.reconnectRetry(unreachable.message))], "t=\(s)")
            XCTAssertEqual(core.apply(.socketClosed(reason: "could not connect", willRetry: true), now: at(s)), [.stopAudio])
        }
        XCTAssertEqual(core.sessionState, .reconnecting)

        XCTAssertEqual(core.apply(.socketFailed(unreachable, willRetry: true), now: at(41)), [.showBanner(.stillReconnecting(unreachable))], "one banner after 30 s")
        XCTAssertEqual(core.apply(.socketFailed(unreachable, willRetry: true), now: at(51)), [.log(.reconnectRetry(unreachable.message))], "then quiet again")
        XCTAssertEqual(core.apply(.socketFailed(unreachable, willRetry: true), now: at(600)), [.log(.reconnectRetry(unreachable.message))], "until End")
        XCTAssertTrue(core.connection.wantsConnection)

        XCTAssertEqual(core.apply(.socketOpened, now: at(601)), [.clearBanner, .haptic(.connected)])
        XCTAssertNil(core.connection.reconnect.failingSince)
        XCTAssertEqual(core.apply(.status(.status(.connected)), now: at(601)), [.startAudio], "the engine comes back with the status")
    }

    func testBeingReplacedIsSaidOnceByTheBannerNotAlsoByANotice() {
        var core = connected()
        // VoiceSocket hands over the closing status first, then the close.
        let status = core.apply(.status(.status(.closed, reason: "replaced")), now: t0)
        XCTAssertFalse(status.contains { if case .notice = $0 { return true } else { return false } }, "no timeline notice")
        XCTAssertEqual(core.apply(.socketClosed(reason: "replaced", willRetry: false), now: t0).last, .showBanner(.replaced))
        // Any other close still gets its notice.
        var other = connected()
        XCTAssertTrue(other.apply(.status(.status(.closed, reason: "voice_off")), now: t0).contains { if case .notice = $0 { return true } else { return false } })
    }

    func testADeniedMicrophoneMakesTheOrbSayNoMicrophone() {
        var core = SessionCore()
        _ = core.apply(.connectTapped, now: t0)
        _ = core.apply(.probeSucceeded(micGranted: false), now: t0)
        _ = core.apply(.socketOpened, now: t0)
        _ = core.apply(.status(.status(.connected)), now: t0)
        XCTAssertEqual(core.sessionState, .noMicrophone, "never \"Listening\" without a microphone")
        XCTAssertEqual(core.statusLine, "No microphone")
        XCTAssertFalse(core.sessionState.capturing)
        // The dog talking still outranks it.
        _ = core.apply(.playbackChanged(speaking: true), now: t0)
        XCTAssertEqual(core.sessionState, .speaking)
        // End forgets it.
        _ = core.apply(.endTapped, now: t0)
        _ = core.apply(.connectTapped, now: t0)
        XCTAssertFalse(core.mic.unavailable)
    }

    func testAMicrophoneThatWouldNotStartSaysSoUntilItStarts() {
        var core = connected()
        XCTAssertEqual(core.sessionState, .listening)
        _ = core.apply(.audioStartFailed("busy"), now: t0)
        XCTAssertEqual(core.sessionState, .noMicrophone)
        _ = core.apply(.audioStarted(gateClosed: false), now: t0)
        XCTAssertEqual(core.sessionState, .listening)
    }

    func testAClosedSocketThatWillNotRetryEndsTheSessionAndReplacedSaysSo() {
        var core = connected()
        XCTAssertEqual(core.apply(.socketClosed(reason: "replaced", willRetry: false), now: t0), [.stopAudio, .stopStreams, .stopTicker, .showBanner(.replaced)])
        XCTAssertFalse(core.connection.wantsConnection)
        XCTAssertEqual(core.sessionState, .disconnected)

        var other = connected()
        XCTAssertEqual(other.apply(.socketClosed(reason: "going away", willRetry: false), now: t0), [.stopAudio, .stopStreams, .stopTicker])
    }

    func testARetryingCloseAfterEndIsNotReconnecting() {
        var core = connected()
        _ = core.apply(.endTapped, now: t0)
        XCTAssertEqual(core.apply(.socketClosed(reason: "connection lost", willRetry: true), now: t0), [.stopAudio, .stopStreams, .stopTicker])
        XCTAssertEqual(core.sessionState, .disconnected)
    }

    // MARK: - Rule: 401 stops

    func testARefusedTokenStopsAndOpensSettings() {
        let refused = DaemonError.unauthorized(endpoint: "mac:7777")
        let stop: [SessionEffect] = [.showBanner(.connectionFailed(refused)), .openSettings, .closeSocket, .stopAudio, .stopStreams, .stopTicker]

        var socket = connected()
        XCTAssertEqual(socket.apply(.socketFailed(refused, willRetry: true), now: t0), stop, "the socket would retry; a 401 does not")
        XCTAssertEqual(socket.connectionChip.status, .tokenRefused)
        XCTAssertFalse(socket.connection.wantsConnection)

        var probe = SessionCore()
        _ = probe.apply(.connectTapped, now: t0)
        XCTAssertEqual(probe.apply(.probeFailed(refused), now: t0), stop)

        var events = connected()
        XCTAssertEqual(events.apply(.tokenRefused(endpoint: "mac:7777"), now: t0), stop)
        XCTAssertEqual(events.connectionChip.status, .tokenRefused)

        // The next Connect starts clean.
        XCTAssertEqual(events.apply(.connectTapped, now: t0), [.clearBanner, .probe])
        XCTAssertEqual(events.connectionChip.status, .connecting)
    }

    // MARK: - Rule: the app owns mute

    func testEveryOpenSaysTheAppsMuteAndADisagreeingStatusIsAnsweredNotAdopted() {
        var core = SessionCore()
        _ = core.apply(.connectTapped, now: t0)
        _ = core.apply(.probeSucceeded(micGranted: true), now: t0)
        _ = core.apply(.socketOpened, now: t0)
        XCTAssertFalse(core.connection.socketMuted, "the socket sends mute(false) on open, not nothing")
        _ = core.apply(.status(.status(.connected)), now: t0)
        _ = core.apply(.audioStarted(gateClosed: false), now: t0)

        // The daemon remembers an old mute: told again, logged once.
        XCTAssertEqual(core.apply(.status(.status(.connected, muted: true)), now: t0), [.log(.muteDisagreement(daemonMuted: true, wanted: false)), .send(.mute(false)), .startAudio])
        XCTAssertFalse(core.mic.muted, "never adopted")
        XCTAssertEqual(core.sessionState, .listening)
        XCTAssertEqual(core.apply(.status(.status(.connected, muted: true)), now: t0), [.send(.mute(false)), .startAudio], "re-sent, not re-logged")
        XCTAssertEqual(core.apply(.status(.status(.connected, muted: false)), now: t0), [.startAudio], "agreement")
        XCTAssertEqual(core.apply(.status(.status(.connected, muted: true)), now: t0), [.log(.muteDisagreement(daemonMuted: true, wanted: false)), .send(.mute(false)), .startAudio], "a new disagreement is logged again")

        // A reopened socket (muted by the user meanwhile) says muted.
        _ = core.apply(.muteTapped, now: t0)
        _ = core.apply(.socketClosed(reason: "connection lost", willRetry: true), now: t0)
        XCTAssertTrue(core.connection.socketMuted)
        XCTAssertEqual(core.apply(.socketOpened, now: t0), [.clearBanner, .haptic(.connected)])
        XCTAssertEqual(core.connection.muteSync, MuteSync(), "a fresh socket's first status is a new conversation")
    }

    // MARK: - Rule: the engine runs whenever voice is up; mute gates only capture

    func testMuteGatesCaptureAndNeverStopsTheEngine() {
        var core = connected()
        XCTAssertEqual(core.apply(.muteTapped, now: t0), [.setCaptureMuted(true), .send(.mute(true)), .haptic(.tap)])
        XCTAssertTrue(core.mic.muted)
        XCTAssertTrue(core.connection.socketMuted)
        XCTAssertEqual(core.sessionState, .muted)
        XCTAssertEqual(core.statusLine, "Muted — tap to unmute")

        // The dog still talks while muted: speaking outranks muted.
        _ = core.apply(.playbackChanged(speaking: true), now: t0)
        XCTAssertEqual(core.sessionState, .speaking)
        XCTAssertTrue(SessionState.showsMuteBadge(core.sessionState, muted: core.mic.muted))
        _ = core.apply(.playbackChanged(speaking: false), now: at(1))

        XCTAssertEqual(core.apply(.muteTapped, now: at(2)), [.send(.mute(false)), .setCaptureMuted(false), .startAudio, .haptic(.tap)])
        XCTAssertFalse(core.connection.socketMuted)
        XCTAssertEqual(core.sessionState, .listening)
    }

    func testAMutedStartStillStartsTheEngine() {
        var core = SessionCore()
        _ = core.apply(.connectTapped, now: t0)
        _ = core.apply(.muteTapped, now: t0)
        XCTAssertEqual(core.apply(.probeSucceeded(micGranted: true), now: t0), [.openSocket, .startStreams, .startTicker])
        XCTAssertTrue(core.connection.socketMuted, "the open says mute(true)")
        _ = core.apply(.socketOpened, now: t0)
        XCTAssertEqual(core.apply(.status(.status(.connected, muted: true)), now: t0), [.startAudio])
        XCTAssertEqual(core.sessionState, .muted)
    }

    func testAClosedVoiceStopsTheEngineAndSaysSoOnce() {
        var core = SessionCore()
        _ = core.apply(.connectTapped, now: t0)
        _ = core.apply(.probeSucceeded(micGranted: true), now: t0)
        _ = core.apply(.socketOpened, now: t0)
        let off = WSMessage.status(.closed, reason: "voice_off")
        XCTAssertEqual(core.apply(.status(off), now: t0), [.stopAudio, .notice("Voice is off on the daemon (`--voice=off`), so it cannot hear the microphone. Status, transcripts and pushes still work.")])
        XCTAssertEqual(core.sessionState, .voiceOff)
        XCTAssertEqual(core.apply(.status(off), now: t0), [.stopAudio], "once per session")
        XCTAssertEqual(core.apply(.muteTapped, now: t0), [.setCaptureMuted(true), .send(.mute(true)), .haptic(.tap)])
        XCTAssertEqual(core.apply(.muteTapped, now: t0), [.send(.mute(false)), .setCaptureMuted(false), .haptic(.tap)], "no engine while the voice is closed")
        XCTAssertEqual(core.apply(.status(.status(.closed, reason: "error")), now: t0), [.stopAudio])
        _ = core.apply(.status(.status(.connected)), now: t0)
        XCTAssertEqual(core.apply(.status(.status(.closed)), now: t0), [.stopAudio, .notice("The daemon's voice session closed (no reason given).")], "said again after the voice came back")
    }

    // MARK: - Rule: lock toggle off → background suspends and mutes; active resumes

    func testBackgroundWithTheLockToggleOffSuspendsAndMutesAndActiveResumes() {
        var core = connected()
        XCTAssertEqual(core.apply(.keepListeningLockedChanged(false), now: t0), [])
        XCTAssertEqual(core.apply(.scenePhase(.background), now: t0), [.suspendAudio, .send(.mute(true)), .log(.pausedInBackground)])
        XCTAssertTrue(core.suspension.backgroundSuspended)
        XCTAssertTrue(core.connection.socketMuted)
        XCTAssertFalse(core.mic.muted, "the user's mute is untouched")
        XCTAssertFalse(core.acceptsPlayback, "nothing piles up to play stale on return")
        XCTAssertEqual(core.apply(.scenePhase(.background), now: t0), [], "once")

        // A status meanwhile: the daemon is told we are muted, no engine.
        XCTAssertEqual(core.apply(.status(.status(.connected, muted: true)), now: t0), [])

        XCTAssertEqual(core.apply(.scenePhase(.active), now: t0), [.send(.mute(false)), .startAudio, .log(.resumedFromBackground)])
        XCTAssertFalse(core.suspension.backgroundSuspended)
        XCTAssertTrue(core.acceptsPlayback)
        XCTAssertEqual(core.apply(.scenePhase(.active), now: t0), [])
    }

    func testBackgroundResumesMutedWhenTheUserHadMuted() {
        var core = connected()
        _ = core.apply(.keepListeningLockedChanged(false), now: t0)
        _ = core.apply(.muteTapped, now: t0)
        XCTAssertEqual(core.apply(.scenePhase(.background), now: t0), [.suspendAudio, .send(.mute(true)), .log(.pausedInBackground)])
        XCTAssertEqual(core.apply(.scenePhase(.active), now: t0), [.send(.mute(true)), .startAudio, .log(.resumedFromBackground)])
    }

    func testBackgroundWithTheLockToggleOnOrDisconnectedDoesNothing() {
        var on = connected()
        XCTAssertEqual(on.apply(.scenePhase(.background), now: t0), [])
        XCTAssertFalse(on.suspension.backgroundSuspended)

        var idle = SessionCore(keepListeningWhenLocked: false)
        XCTAssertEqual(idle.apply(.scenePhase(.background), now: t0), [])
    }

    func testEndOrAFailureClearsTheBackgroundPause() {
        var core = connected()
        _ = core.apply(.keepListeningLockedChanged(false), now: t0)
        _ = core.apply(.scenePhase(.background), now: t0)
        _ = core.apply(.socketFailed(.malformed("x"), willRetry: false), now: t0)
        XCTAssertFalse(core.suspension.backgroundSuspended)
    }

    // MARK: - Rule: audio paused by iOS

    func testAudioPausedByIOSIsTheOrbsResumeButton() {
        var core = connected()
        XCTAssertEqual(core.apply(.interruption(paused: true), now: t0), [])
        XCTAssertEqual(core.sessionState, .audioPaused)
        XCTAssertEqual(core.voiceBar.orbAction, .resume)
        XCTAssertEqual(core.statusLine, "Audio paused by iOS — tap to resume")

        XCTAssertEqual(core.apply(.resumeTapped, now: t0), [.resumeAudio])
        XCTAssertEqual(core.apply(.audioResumeFailed("busy"), now: t0), [.showBanner(.audioResumeFailed("busy"))])
        XCTAssertEqual(core.sessionState, .audioPaused)

        XCTAssertEqual(core.apply(.interruption(paused: false), now: t0), [])
        XCTAssertEqual(core.sessionState, .listening)

        _ = core.apply(.interruption(paused: true), now: t0)
        _ = core.apply(.endTapped, now: t0)
        XCTAssertFalse(core.suspension.pausedByIOS)
    }

    // MARK: - Rule: hold-to-talk is never muted

    func testSwitchingToHoldToTalkUnmutesAndMuteDoesNothingThere() {
        var core = connected()
        _ = core.apply(.muteTapped, now: t0)
        XCTAssertEqual(core.apply(.talkModeChanged(.holdToTalk), now: t0), [.send(.mute(false)), .setCaptureMuted(false), .startAudio, .haptic(.tap), .setManualGate(false)])
        XCTAssertFalse(core.mic.muted)
        XCTAssertEqual(core.voiceBar.muteAvailability, .hidden)
        XCTAssertEqual(core.apply(.muteTapped, now: t0), [], "a mute here could never be undone")
        XCTAssertFalse(core.mic.muted)
        XCTAssertEqual(core.apply(.talkModeChanged(.holdToTalk), now: t0), [])
        XCTAssertEqual(core.apply(.talkModeChanged(.openMic), now: t0), [.setManualGate(nil)])
    }

    func testHoldToTalkOpensAndClosesTheGate() {
        var core = connected(talkMode: .holdToTalk)
        XCTAssertTrue(core.mic.gateClosed, "starts closed")
        XCTAssertEqual(core.statusLine, "Hold the orb to talk")
        XCTAssertEqual(core.voiceBar.orbAction, .holdToTalk)

        XCTAssertEqual(core.apply(.holdBegan, now: t0), [.setManualGate(true), .haptic(.tap)])
        XCTAssertTrue(core.mic.holding)
        XCTAssertEqual(core.statusLine, "Talking — release to send")
        XCTAssertEqual(core.apply(.holdBegan, now: t0), [], "already holding")
        XCTAssertEqual(core.apply(.gateChanged(closed: false), now: t0), [.send(.silence(false)), .send(.speaking(true))], "holding is talking")

        XCTAssertEqual(core.apply(.holdEnded, now: t0), [.setManualGate(false)])
        XCTAssertFalse(core.mic.holding)
        XCTAssertEqual(core.apply(.holdEnded, now: t0), [])

        XCTAssertEqual(core.apply(.endTapped, now: t0), [.closeSocket, .stopAudio, .stopStreams, .stopTicker, .setManualGate(false)])
    }

    func testHoldingOverTheDogStopsIt() {
        var core = connected(talkMode: .holdToTalk)
        _ = core.apply(.playbackChanged(speaking: true), now: t0)
        XCTAssertEqual(core.apply(.holdBegan, now: t0), [.flushPlayback, .flushTranscript, .send(.cancel), .haptic(.stop), .setManualGate(true), .haptic(.tap)])
        XCTAssertFalse(core.playback.speaking)
    }

    func testTheEngineStartingClosedTellsTheDaemonOnce() {
        var core = SessionCore(talkMode: .holdToTalk)
        _ = core.apply(.connectTapped, now: t0)
        _ = core.apply(.probeSucceeded(micGranted: true), now: t0)
        _ = core.apply(.socketOpened, now: t0)
        _ = core.apply(.status(.status(.connected)), now: t0)
        XCTAssertEqual(core.apply(.audioStarted(gateClosed: true), now: t0), [.send(.silence(true))])
        XCTAssertTrue(core.connection.socketGateClosed)
        XCTAssertEqual(core.apply(.audioStarted(gateClosed: true), now: t0), [], "no transition, no message")
        XCTAssertEqual(core.apply(.audioStartFailed("no mic"), now: t0), [.showBanner(.microphoneFailed("no mic"))])
    }

    func testTheSimulatedGateNeverTellsTheDaemon() {
        var core = connected()
        XCTAssertEqual(core.apply(.simulatedGate(closed: true), now: t0), [])
        XCTAssertEqual(core.sessionState, .silent)
        XCTAssertTrue(core.connection.socketGateClosed)
    }

    // MARK: - Rule: stop clears thinking

    func testStopClearsThinking() {
        var core = connected()
        _ = core.apply(.toolCall, now: t0)
        XCTAssertEqual(core.sessionState, .thinking)
        XCTAssertEqual(core.apply(.stopTapped, now: at(1)), [.flushPlayback, .flushTranscript, .send(.cancel), .haptic(.stop)])
        XCTAssertNil(core.thinkingExpiry)
        XCTAssertEqual(core.sessionState, .listening)
    }

    func testThinkingTimesOutAfterThirtySecondsOnTheTicker() {
        var core = connected()
        _ = core.apply(.toolCall, now: t0)
        XCTAssertEqual(core.apply(.tick, now: at(29.9)), [])
        XCTAssertEqual(core.sessionState, .thinking)
        XCTAssertEqual(core.apply(.gateChanged(closed: false), now: at(31)), [.send(.silence(false)), .send(.speaking(true))])
        XCTAssertEqual(core.sessionState, .thinking, "only the ticker expires it")
        XCTAssertEqual(core.apply(.tick, now: at(31)), [])
        XCTAssertEqual(core.sessionState, .listening)
    }

    // MARK: - Playback

    func testTheSpeakingHoldBridgesAGapAndTheDaemonsFlushDropsIt() {
        var core = connected()
        _ = core.apply(.playbackChanged(speaking: true), now: at(0))
        _ = core.apply(.playbackChanged(speaking: false), now: at(1))
        XCTAssertEqual(core.sessionState, .speaking, "held across the gap")
        XCTAssertTrue(core.playbackBusy)
        _ = core.apply(.tick, now: at(1.5))
        XCTAssertEqual(core.sessionState, .speaking)
        _ = core.apply(.tick, now: at(1.9))
        XCTAssertEqual(core.sessionState, .listening)
        XCTAssertFalse(core.playbackBusy)

        _ = core.apply(.playbackChanged(speaking: true), now: at(2))
        XCTAssertEqual(core.apply(.flush, now: at(3)), [.flushPlayback, .flushTranscript, .syncHistory])
        XCTAssertEqual(core.sessionState, .listening, "a flush is not a gap")
    }

    func testTheDaemonsErrorAndAMissingMicSignal() {
        var core = connected()
        XCTAssertEqual(core.apply(.daemonError("boom"), now: t0), [.showBanner(.daemonSaid("boom"))])
        XCTAssertEqual(core.apply(.micSignal(ok: false), now: t0), [.haptic(.micLost)], "a warning, not an answer that failed")
        XCTAssertEqual(core.statusLine, "No microphone signal — check the mic")
        XCTAssertEqual(core.apply(.micSignal(ok: true), now: t0), [])
        XCTAssertEqual(core.statusLine, "Listening — just talk")
    }

    func testStalledAndProvider() {
        var core = connected()
        _ = core.apply(.status(.status(.connected, stalled: true, provider: "openai")), now: t0)
        XCTAssertEqual(core.sessionState, .stalled)
        XCTAssertEqual(core.voiceBar.orbAction, .stop)
        XCTAssertEqual(core.voice.provider, "openai")
        _ = core.apply(.providerSelected("gemini"), now: t0)
        XCTAssertEqual(core.voice.provider, "gemini")
        _ = core.apply(.status(.status(.connected)), now: t0)
        XCTAssertEqual(core.sessionState, .listening)
        XCTAssertEqual(core.voice.provider, "gemini", "a status without a provider keeps it")
    }

    // MARK: - The speech detector (`speaking`)

    func testTheGateIsTheSpeechDetectorOncePerChange() {
        var core = connected()
        XCTAssertFalse(core.mic.speaking, "a fresh engine hears nothing yet")
        XCTAssertEqual(core.apply(.gateChanged(closed: true), now: t0), [.send(.silence(true))], "never open: nothing to close")
        XCTAssertEqual(core.apply(.gateChanged(closed: false), now: at(1)), [.send(.silence(false)), .send(.speaking(true))])
        XCTAssertTrue(core.connection.socketSpeaking, "a reconnect says it again on open")
        XCTAssertEqual(core.apply(.gateChanged(closed: true), now: at(2)), [.send(.silence(true)), .send(.speaking(false))])
        XCTAssertFalse(core.connection.socketSpeaking)
    }

    func testMutingClosesTheDetectorAndItsGateResetIsNotSpeech() {
        var core = connected()
        _ = core.apply(.gateChanged(closed: false), now: t0)
        XCTAssertTrue(core.mic.speaking)
        XCTAssertEqual(core.apply(.muteTapped, now: at(1)), [.setCaptureMuted(true), .send(.mute(true)), .send(.speaking(false)), .haptic(.tap)])
        // `AudioEngine.setMuted` reports a closed gate as open again, with
        // no speech behind it: the daemon must not hold its pushes for it.
        XCTAssertEqual(core.apply(.gateChanged(closed: false), now: at(1)), [], "a muted client sends no silence transitions")
        XCTAssertFalse(core.mic.speaking)
    }

    func testTheDetectorIsQuietAfterTheSocketDropsOrTheVoiceCloses() {
        var core = connected()
        _ = core.apply(.gateChanged(closed: false), now: t0)
        _ = core.apply(.socketClosed(reason: "connection lost", willRetry: true), now: at(1))
        XCTAssertFalse(core.mic.speaking)
        XCTAssertFalse(core.connection.socketSpeaking, "the reopened socket starts from not speaking")

        var closed = connected()
        _ = closed.apply(.gateChanged(closed: false), now: t0)
        let effects = closed.apply(.status(.status(.closed, reason: "shutdown")), now: at(1))
        XCTAssertTrue(effects.contains(.send(.speaking(false))))
        XCTAssertFalse(closed.mic.speaking)
    }

    func testBackgroundPauseClosesTheDetector() {
        var core = SessionCore(keepListeningWhenLocked: false)
        _ = core.apply(.connectTapped, now: t0)
        _ = core.apply(.probeSucceeded(micGranted: true), now: t0)
        _ = core.apply(.socketOpened, now: t0)
        _ = core.apply(.status(.status(.connected)), now: t0)
        _ = core.apply(.gateChanged(closed: false), now: t0)
        let effects = core.apply(.scenePhase(.background), now: at(1))
        XCTAssertTrue(effects.contains(.send(.speaking(false))))
        XCTAssertFalse(core.mic.speaking)
    }

    func testTheSimulatedGateNeverSaysSpeaking() {
        var core = connected()
        XCTAssertEqual(core.apply(.simulatedGate(closed: true), now: t0), [])
        XCTAssertEqual(core.apply(.simulatedGate(closed: false), now: t0), [])
        XCTAssertFalse(core.mic.speaking)
    }

    // MARK: - The seven glitches the extraction found (PR #13)

    /// 1. A failure used to reset far less than End: the next Connect showed
    /// "Listening" off the dead session's status before any new one came.
    func testAFailedSessionLeavesNothingStaleForTheNextConnect() {
        var core = connected()
        _ = core.apply(.status(.status(.connected, stalled: true, provider: "openai")), now: t0)
        _ = core.apply(.toolCall, now: t0)
        _ = core.apply(.playbackChanged(speaking: true), now: t0)
        _ = core.apply(.micSignal(ok: false), now: t0)
        _ = core.apply(.socketFailed(.unreachable(endpoint: "x", detail: ""), willRetry: false), now: at(1))
        XCTAssertNil(core.voice.status)
        XCTAssertNil(core.voice.provider)
        XCTAssertFalse(core.voice.stalled)
        XCTAssertNil(core.thinkingExpiry)
        XCTAssertFalse(core.playback.speaking)
        XCTAssertFalse(core.mic.noSignal)

        _ = core.apply(.connectTapped, now: at(2))
        _ = core.apply(.probeSucceeded(micGranted: true), now: at(2))
        _ = core.apply(.socketOpened, now: at(2))
        XCTAssertNotEqual(core.sessionState, .listening, "no status yet: not Listening")
        XCTAssertNil(core.voice.status)
    }

    /// 2. A probe failing after End raised a banner and turned the chip red.
    func testAProbeFailureAfterEndIsIgnored() {
        var core = SessionCore()
        _ = core.apply(.connectTapped, now: t0)
        _ = core.apply(.endTapped, now: t0)
        XCTAssertEqual(core.apply(.probeFailed(.unreachable(endpoint: "x", detail: "")), now: at(1)), [])
        XCTAssertFalse(core.connection.failed)
        XCTAssertEqual(core.connectionChip.status, ConnectionChip(.init()).status, "still plain Not connected")
        XCTAssertEqual(core.apply(.tokenRefused(endpoint: "x"), now: at(1)), [])
    }

    /// 3. The `/events` task cancelled by End could still say "reconnecting".
    func testAStreamReportAfterEndLeavesTheStreamIdle() {
        var core = connected()
        _ = core.apply(.endTapped, now: t0)
        _ = core.apply(.eventsChanged(.reconnecting), now: at(1))
        XCTAssertEqual(core.connection.events, .idle)
    }

    /// 4. Connect marked the stream as connecting before it existed.
    func testTheStreamIsConnectingOnlyOnceItIsStarted() {
        var core = SessionCore()
        _ = core.apply(.connectTapped, now: t0)
        XCTAssertEqual(core.connection.events, .idle, "the probe runs first")
        _ = core.apply(.probeSucceeded(micGranted: true), now: t0)
        XCTAssertEqual(core.connection.events, .connecting)
    }

    /// 5. An engine that died after a route change stayed dead.
    func testAnEngineThatDiesIsSaidAndStartedAgain() {
        var core = connected()
        _ = core.apply(.gateChanged(closed: false), now: t0)
        let effects = core.apply(.audioStopped("The microphone went away with the route change."), now: at(1))
        XCTAssertEqual(effects, [.notice("The microphone went away with the route change."), .startAudio])
        XCTAssertFalse(core.mic.speaking)
    }

    /// 6. The unmute's gate reset and the daemon disagreed: a muted client
    /// now sends no silence transitions, and the unmute says "open" when the
    /// daemon last heard "closed".
    func testUnmuteTellsTheDaemonTheGateItLastHeardClosedIsOpen() {
        var core = connected()
        _ = core.apply(.gateChanged(closed: true), now: t0)
        XCTAssertEqual(core.apply(.muteTapped, now: at(1)), [.setCaptureMuted(true), .send(.mute(true)), .haptic(.tap)])
        XCTAssertEqual(core.apply(.gateChanged(closed: false), now: at(1)), [], "the reset muting reports")
        XCTAssertEqual(core.apply(.muteTapped, now: at(2)), [.send(.mute(false)), .send(.silence(false)), .setCaptureMuted(false), .startAudio, .haptic(.tap)])
        XCTAssertFalse(core.connection.socketGateClosed)
        XCTAssertFalse(core.mic.gateClosed)
    }

    /// 7. Losing the mic signal played the "answer failed" haptic: now a
    /// warning (see testTheDaemonsErrorAndAMissingMicSignal).
    func testMicLossIsAWarningHaptic() {
        var core = connected()
        XCTAssertEqual(core.apply(.micSignal(ok: false), now: t0), [.haptic(.micLost)])
    }
}
