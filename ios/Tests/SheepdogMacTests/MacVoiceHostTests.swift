import XCTest
import SheepdogKit
@testable import SheepdogMac

/// The Mac's voice session with fake audio, socket and permission: events
/// in, effects out (the style of Kit's `SessionCoreTests`).
@MainActor
final class MacVoiceHostTests: XCTestCase {
    @MainActor
    private struct Rig {
        let host: MacVoiceHost
        let audio = FakeAudio()
        let channel = FakeChannel()
        let probe = FakeProbe()
        let microphone: FakeMicrophone
        let settings = FakeSettings()
        let timers = FakeTimers()
        let log = FakeLog()

        init(access: MicrophoneAccess = .granted) {
            microphone = FakeMicrophone(access: access)
            host = MacVoiceHost(channel: channel, audio: audio, prober: probe, microphone: microphone, settings: settings, timers: timers, log: log)
        }
    }

    /// Use here, the daemon's `status: connected` and the engine running.
    private func connect(_ rig: Rig) async {
        rig.host.useHere()
        await rig.host.opening?.value
        rig.channel.emit(.opened)
        rig.channel.emit(.control(.status(.connected)))
    }

    // MARK: - Connecting

    func testTheHandshakeSaysItIsTheMac() {
        let rig = Rig()
        XCTAssertEqual(rig.channel.client.name, "sheepdog-mac")
        XCTAssertEqual(rig.channel.client.platform, "macos")
    }

    func testUseHereProbesAsksForTheMicrophoneThenOpensTheSocketAndStartsAudio() async {
        let rig = Rig(access: .notDetermined)
        rig.host.useHere()
        XCTAssertTrue(rig.host.state.heldHere)
        await rig.host.opening?.value
        XCTAssertEqual(rig.probe.probes, 1)
        XCTAssertEqual(rig.microphone.requests, 1)
        XCTAssertEqual(rig.channel.connects, 1)
        XCTAssertTrue(rig.timers.ticking)
        XCTAssertEqual(rig.audio.starts, 0, "the engine waits for the daemon's voice")
        rig.channel.emit(.opened)
        rig.channel.emit(.control(.status(.connected)))
        XCTAssertEqual(rig.audio.starts, 1)
        XCTAssertTrue(rig.audio.isRunning)
        XCTAssertNil(rig.host.state.problem)
    }

    func testUseHereWhileConnectedDoesNothing() async {
        let rig = Rig()
        await connect(rig)
        rig.host.useHere()
        await rig.host.opening?.value
        XCTAssertEqual(rig.probe.probes, 1)
        XCTAssertEqual(rig.channel.connects, 1)
    }

    func testADaemonThatDoesNotAnswerEndsTheSessionWithItsWords() async {
        let rig = Rig()
        let error = DaemonError.unreachable(endpoint: "127.0.0.1:7777", detail: "refused")
        rig.probe.failure = error
        rig.host.useHere()
        await rig.host.opening?.value
        XCTAssertEqual(rig.channel.connects, 0)
        XCTAssertFalse(rig.host.state.heldHere)
        XCTAssertEqual(rig.host.state.problem, error.message)
    }

    func testTheSocketIsToldWhatToSayOnEveryOpen() async {
        let rig = Rig()
        await connect(rig)
        rig.host.handle(route: .mute(true))
        XCTAssertTrue(rig.channel.muted)
        rig.audio.onGate?(true)
        XCTAssertFalse(rig.channel.gateClosed, "a muted client sends no gate changes")
    }

    // MARK: - Talk, Mute, Stop

    func testTalkStartsTheSessionAndThenOnlyUnmutes() async {
        let rig = Rig()
        rig.host.handle(route: .talk)
        await rig.host.opening?.value
        XCTAssertEqual(rig.channel.connects, 1)
        rig.channel.emit(.opened)
        rig.channel.emit(.control(.status(.connected)))

        rig.host.handle(route: .talk)
        XCTAssertEqual(rig.audio.muted, [], "already listening")
        rig.host.handle(route: .toggleMute)
        XCTAssertTrue(rig.host.state.muted)
        rig.host.handle(route: .talk)
        XCTAssertFalse(rig.host.state.muted)
        XCTAssertEqual(rig.audio.muted, [true, false])
    }

    func testMuteIsTheWSMuteAndCapturesNothing() async {
        let rig = Rig()
        await connect(rig)
        rig.host.handle(route: .toggleMute)
        XCTAssertTrue(rig.channel.sent.contains(.mute(true)))
        XCTAssertEqual(rig.audio.muted, [true])
        XCTAssertTrue(rig.host.state.muted)
    }

    func testStopCancelsOnTheDaemonAndDropsWhatPlays() async {
        let rig = Rig()
        await connect(rig)
        rig.host.handle(route: .stop)
        XCTAssertEqual(rig.channel.sent.last, .cancel)
        XCTAssertEqual(rig.audio.flushes, 1)
    }

    func testEndClosesTheSocketAndStopsTheAudio() async {
        let rig = Rig()
        await connect(rig)
        rig.host.handle(route: .end)
        XCTAssertEqual(rig.channel.disconnects, ["user disconnect"])
        XCTAssertFalse(rig.audio.isRunning)
        XCTAssertFalse(rig.timers.ticking)
        XCTAssertFalse(rig.host.state.heldHere)
    }

    func testTheControlsDoNothingWithoutASession() {
        let rig = Rig()
        rig.host.handle(route: .stop)
        rig.host.handle(route: .toggleMute)
        rig.host.handle(route: .end)
        XCTAssertTrue(rig.channel.sent.isEmpty)
        XCTAssertEqual(rig.audio.stops, 0)
    }

    // MARK: - Audio

    func testFramesGoToTheSocketAndAudioFromTheDaemonPlays() async {
        let rig = Rig()
        await connect(rig)
        rig.audio.onFrame?(Data([1, 2, 3]))
        rig.channel.emit(.audio(Data([4, 5])))
        XCTAssertEqual(rig.channel.frames, [Data([1, 2, 3])])
        XCTAssertEqual(rig.audio.enqueued, [Data([4, 5])])
    }

    func testPlaybackReportsGoToTheDaemonAndDrainsOnlyWhenItTakesThem() async {
        let rig = Rig()
        await connect(rig)
        let drain = PlaybackQueue.DrainRecord(at: 0, bufferMilliseconds: 0, playedMilliseconds: 10, receivedMilliseconds: 10, reason: .dry)
        rig.audio.onPlaybackReport?(PlaybackQueue.Report(.started, playedMilliseconds: 0))
        rig.audio.onDrain?(drain)
        XCTAssertEqual(rig.channel.sent, [.playback(.started, playedMs: 0)])

        rig.channel.emit(.handshake(.ready(protocol: 1, daemonVersion: "test", features: [FeedbackTag.feature])))
        rig.audio.onDrain?(drain)
        XCTAssertEqual(rig.channel.sent.last, drain.message)
    }

    func testTheGateTellsTheDaemonSilenceAndSpeech() async {
        let rig = Rig()
        await connect(rig)
        rig.audio.onGate?(false)
        XCTAssertTrue(rig.host.state.userTalking)
        rig.audio.onGate?(true)
        XCTAssertFalse(rig.host.state.userTalking)
        XCTAssertEqual(rig.channel.sent, [.silence(false), .speaking(true), .silence(true), .speaking(false)])
    }

    func testTheProviderSetsThePlaybackProfile() async {
        let rig = Rig()
        await connect(rig)
        rig.channel.emit(.control(.status(.connected, provider: "openai")))
        XCTAssertEqual(rig.audio.providers.last ?? nil, "openai")
    }

    // MARK: - Replaced ("Take it back")

    func testBeingReplacedStopsEverythingAndDoesNotReconnect() async {
        let rig = Rig()
        await connect(rig)
        rig.channel.emit(.control(.status(.closed, reason: "replaced")))
        rig.channel.emit(.closed(reason: "replaced", retrying: false))
        XCTAssertFalse(rig.audio.isRunning)
        XCTAssertFalse(rig.timers.ticking)
        XCTAssertEqual(rig.timers.reopens, 0)
        XCTAssertFalse(rig.host.state.heldHere)
        XCTAssertEqual(rig.host.state.problem, "Another client took the voice session.")
    }

    func testUseHereTakesItBackAndClearsTheProblem() async {
        let rig = Rig()
        await connect(rig)
        rig.channel.emit(.control(.status(.closed, reason: "replaced")))
        rig.channel.emit(.closed(reason: "replaced", retrying: false))
        rig.host.useHere()
        XCTAssertNil(rig.host.state.problem)
        await rig.host.opening?.value
        XCTAssertEqual(rig.channel.connects, 2)
        rig.channel.emit(.opened)
        rig.channel.emit(.control(.status(.connected)))
        XCTAssertTrue(rig.audio.isRunning)
        XCTAssertTrue(rig.host.state.heldHere)
    }

    func testALostSocketOpensAgainUntilTheSessionEnds() async {
        let rig = Rig()
        await connect(rig)
        rig.channel.emit(.closed(reason: "connection lost", retrying: true))
        XCTAssertEqual(rig.timers.pendingReopen?.delay, MacVoiceHost.reopenDelay)
        XCTAssertTrue(rig.host.state.heldHere)
        rig.timers.fireReopen()
        XCTAssertEqual(rig.channel.connects, 2)

        rig.channel.emit(.failed(.unreachable(endpoint: "127.0.0.1:7777", detail: ""), retrying: true))
        XCTAssertNotNil(rig.timers.pendingReopen)
        rig.host.handle(route: .end)
        XCTAssertNil(rig.timers.pendingReopen, "End cancels the reopen")
    }

    func testAFailureThatCannotBeRetriedEndsTheSession() async {
        let rig = Rig()
        await connect(rig)
        let error = DaemonError.unauthorized(endpoint: "127.0.0.1:7777")
        rig.channel.emit(.failed(error, retrying: false))
        XCTAssertFalse(rig.host.state.heldHere)
        XCTAssertEqual(rig.host.state.problem, error.message)
        XCTAssertNil(rig.timers.pendingReopen)
    }

    // MARK: - voice-command

    func testPTTOnUnmutesWhileHeldAndTheReleaseMutesAgain() async {
        let rig = Rig()
        await connect(rig)
        rig.host.handle(route: .mute(true))
        rig.channel.emit(.control(.voicePTT(true)))
        XCTAssertFalse(rig.host.state.muted)
        XCTAssertEqual(rig.audio.muted, [true, false])
        rig.channel.emit(.control(.voicePTT(false)))
        XCTAssertTrue(rig.host.state.muted)
        XCTAssertEqual(rig.audio.muted, [true, false, true])
    }

    func testPTTWhileTheMicrophoneIsOpenChangesNothing() async {
        let rig = Rig()
        await connect(rig)
        rig.channel.emit(.control(.voicePTT(true)))
        rig.channel.emit(.control(.voicePTT(false)))
        XCTAssertFalse(rig.host.state.muted)
        XCTAssertEqual(rig.audio.muted, [])
    }

    func testAReleaseThatWasNeverPressedDoesNotMute() async {
        let rig = Rig()
        await connect(rig)
        rig.channel.emit(.control(.voicePTT(false)))
        XCTAssertFalse(rig.host.state.muted)
    }

    func testPTTBeforeASessionDoesNothing() {
        let rig = Rig()
        rig.channel.emit(.control(.voicePTT(true)))
        XCTAssertTrue(rig.channel.sent.isEmpty)
        XCTAssertFalse(rig.host.state.heldHere)
    }

    func testAPTTHeldThroughEndDoesNotMuteTheNextSession() async {
        let rig = Rig()
        await connect(rig)
        rig.host.handle(route: .mute(true))
        rig.channel.emit(.control(.voicePTT(true)))
        rig.host.handle(route: .end)
        await connect(rig)
        rig.channel.emit(.control(.voicePTT(false)))
        XCTAssertFalse(rig.host.state.muted)
    }

    /// The panel's Mute (`POST /api/voice/mute`): the daemon sends the
    /// command, then the status that confirms it. The app mutes and does
    /// not answer the status by unmuting again.
    func testAMuteFromThePanelSticks() async {
        let rig = Rig()
        await connect(rig)
        rig.channel.emit(.control(.voiceMute(true)))
        rig.channel.emit(.control(.status(.connected, muted: true)))
        XCTAssertTrue(rig.host.state.muted)
        XCTAssertEqual(rig.audio.muted, [true])
        XCTAssertFalse(rig.channel.sent.contains(.mute(false)), "the panel's mute is not undone")

        rig.channel.emit(.control(.voiceMute(false)))
        rig.channel.emit(.control(.status(.connected, muted: false)))
        XCTAssertFalse(rig.host.state.muted)
        XCTAssertEqual(rig.audio.muted, [true, false])
    }

    func testAPanelMuteDuringHoldToTalkIsNotUndoneByTheRelease() async {
        let rig = Rig()
        await connect(rig)
        rig.host.handle(route: .mute(true))
        rig.channel.emit(.control(.voicePTT(true)))
        rig.channel.emit(.control(.voiceMute(false)))
        rig.channel.emit(.control(.voicePTT(false)))
        XCTAssertFalse(rig.host.state.muted, "the panel unmuted; the release leaves it")
    }

    func testVoiceCommandStopDropsThePlayback() async {
        let rig = Rig()
        await connect(rig)
        rig.audio.onSpeakingChanged?(true)
        rig.channel.emit(.control(.voiceStop))
        XCTAssertEqual(rig.audio.flushes, 1)
        XCTAssertTrue(rig.host.state.heldHere, "the session goes on")
        XCTAssertFalse(rig.channel.sent.contains(.cancel), "the daemon already cut the speech")
    }

    // MARK: - The microphone

    func testADeniedMicrophoneIsSaidAndTheEngineStaysOff() async {
        let rig = Rig(access: .denied)
        await connect(rig)
        XCTAssertEqual(rig.microphone.requests, 1)
        XCTAssertEqual(rig.audio.starts, 0)
        XCTAssertTrue(rig.host.state.micDenied)
        XCTAssertEqual(rig.host.state.problem, "Microphone off: open System Settings")
        XCTAssertTrue(rig.host.core.mic.unavailable)
        XCTAssertTrue(rig.host.state.heldHere, "status and the voice's words still work")
    }

    func testRefusingThePromptIsTheSameAsDenied() async {
        let rig = Rig(access: .notDetermined)
        rig.microphone.answer = false
        await connect(rig)
        XCTAssertTrue(rig.host.state.micDenied)
        XCTAssertEqual(rig.audio.starts, 0)
    }

    func testTheMenuOpensThePrivacyPane() {
        let rig = Rig(access: .denied)
        rig.host.openMicrophoneSettings()
        XCTAssertEqual(rig.settings.opened, 1)
    }

    func testAnEngineThatWillNotStartIsSaid() async {
        let rig = Rig()
        rig.audio.startFailure = Boom()
        await connect(rig)
        XCTAssertEqual(rig.audio.starts, 1)
        XCTAssertNotNil(rig.host.state.problem)
        XCTAssertFalse(rig.host.state.micDenied)
    }

    func testAMicrophoneThatDeliversNothingIsTold() async {
        let rig = Rig()
        await connect(rig)
        rig.audio.onMicSignal?(true)
        XCTAssertTrue(rig.host.core.mic.noSignal)
        rig.audio.onMicSignal?(false)
        XCTAssertFalse(rig.host.core.mic.noSignal)
    }

    // MARK: - Route changes

    func testARouteChangeTheEngineRebuiltForIsOnlyLogged() async {
        let rig = Rig()
        await connect(rig)
        rig.audio.onNotice?(.routeChanged(output: nil))
        XCTAssertEqual(rig.log.lines.last, "The audio route changed.")
        XCTAssertEqual(rig.audio.starts, 1)
    }

    func testAnEngineThatDiedInARouteChangeIsRebuiltOnceTheHardwareSettles() async {
        let rig = Rig()
        await connect(rig)
        rig.audio.die(LocalNotice.microphoneWentAway)
        await Task.yield()
        XCTAssertTrue(rig.log.lines.contains(LocalNotice.microphoneWentAway))
        XCTAssertEqual(rig.audio.starts, 1, "not from inside the engine's own callback")
        rig.audio.onAvailable?()
        await Task.yield()
        XCTAssertEqual(rig.audio.starts, 2)
        XCTAssertTrue(rig.audio.isRunning)
        XCTAssertNil(rig.host.state.problem)
    }

    func testASecondFailureIsTheBannerNotALoop() async {
        let rig = Rig()
        await connect(rig)
        rig.audio.startFailure = Boom()
        rig.audio.die(LocalNotice.audioStoppedAfterRouteChange("boom"))
        await Task.yield()
        rig.audio.onAvailable?()
        await Task.yield()
        XCTAssertEqual(rig.audio.starts, 2)
        XCTAssertNotNil(rig.host.state.problem)
        XCTAssertTrue(rig.host.state.heldHere)
    }

    // MARK: - The menu follows

    func testTheMenuIsToldOnlyWhenTheStateChanges() async {
        let rig = Rig()
        var changes = 0
        rig.host.onChange = { changes += 1 }
        await connect(rig)
        let afterConnect = changes
        XCTAssertGreaterThan(afterConnect, 0)
        rig.channel.emit(.control(.status(.connected)))
        XCTAssertEqual(changes, afterConnect)
        rig.host.handle(route: .toggleMute)
        XCTAssertEqual(changes, afterConnect + 1)
    }

    func testTheDaemonsErrorIsTheLine() async {
        let rig = Rig()
        await connect(rig)
        rig.channel.emit(.control(.error("rate limited")))
        XCTAssertEqual(rig.host.state.problem, "The daemon said: rate limited")
    }

    func testTheTickerCopiesTheCountersToThePing() async {
        let rig = Rig()
        await connect(rig)
        rig.audio.gatedFrames = 7
        rig.audio.inputPeak = 900
        rig.timers.tick()
        XCTAssertEqual(rig.channel.gatedFrames, 7)
        XCTAssertEqual(rig.channel.inputPeak, 900)
        XCTAssertEqual(rig.channel.audioState, "running")
    }
}
