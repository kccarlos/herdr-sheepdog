import XCTest
@testable import SheepdogKit

/// The session-lifecycle rules behind the view model: who owns mute, when
/// the audio engine runs, and the "paused by iOS" state. (Reconnecting is
/// the computer's link: `LinkTests`, `LinkChannelTests`.)
final class SessionPoliciesTests: XCTestCase {
    private let unreachable = DaemonError.unreachable(endpoint: "127.0.0.1:7799", detail: "Could not connect to the server.")

    // MARK: - MuteSync (bug: the daemon's view of mute won)

    func testADisagreeingStatusIsAnsweredNotAdopted() {
        var sync = MuteSync()
        XCTAssertEqual(sync.status(daemonMuted: false, intent: false), .none)
        XCTAssertEqual(sync.status(daemonMuted: true, intent: false), .resend(log: true), "a stale daemon mute is corrected")
        XCTAssertEqual(sync.status(daemonMuted: true, intent: false), .resend(log: false), "logged once per disagreement")
        XCTAssertEqual(sync.status(daemonMuted: false, intent: false), .none)
        XCTAssertEqual(sync.status(daemonMuted: false, intent: true), .resend(log: true), "a new disagreement is logged again")
        XCTAssertEqual(sync.status(daemonMuted: nil, intent: true), .none, "no view, nothing to correct")
    }

    func testHoldToTalkNeverMutes() {
        XCTAssertFalse(MuteSync.intent(requested: true, talkMode: .holdToTalk))
        XCTAssertTrue(MuteSync.intent(requested: true, talkMode: .openMic))
        XCTAssertFalse(MuteSync.intent(requested: false, talkMode: .openMic))
    }

    func testTheDaemonIsToldMutedWhilePausedInTheBackground() {
        XCTAssertFalse(MuteSync.daemonMute(intent: false, suspended: false))
        XCTAssertTrue(MuteSync.daemonMute(intent: false, suspended: true))
        XCTAssertTrue(MuteSync.daemonMute(intent: true, suspended: false))
    }

    // MARK: - AudioLifecycle (bugs: silent dog after a reconnect while muted; lock toggle)

    func testTheEngineRunsWheneverVoiceIsUpMutedOrNot() {
        // Mute is not an input at all: it gates capture, never playback.
        XCTAssertTrue(AudioLifecycle.engineShouldRun(socketOpen: true, voice: .connected, suspended: false))
        XCTAssertTrue(AudioLifecycle.engineShouldRun(socketOpen: true, voice: .reconnecting, suspended: false))
        XCTAssertFalse(AudioLifecycle.engineShouldRun(socketOpen: true, voice: .closed, suspended: false), "--voice=off")
        XCTAssertFalse(AudioLifecycle.engineShouldRun(socketOpen: true, voice: nil, suspended: false), "no status yet")
        XCTAssertFalse(AudioLifecycle.engineShouldRun(socketOpen: false, voice: .connected, suspended: false), "reconnecting")
        XCTAssertFalse(AudioLifecycle.engineShouldRun(socketOpen: true, voice: .connected, suspended: true), "paused in the background")
    }

    func testTheWatchdogSleepsWhileMutedOrInterrupted() {
        XCTAssertTrue(AudioLifecycle.watchdogShouldRun(running: true, muted: false, interrupted: false))
        XCTAssertFalse(AudioLifecycle.watchdogShouldRun(running: true, muted: true, interrupted: false))
        XCTAssertFalse(AudioLifecycle.watchdogShouldRun(running: true, muted: false, interrupted: true))
        XCTAssertFalse(AudioLifecycle.watchdogShouldRun(running: false, muted: false, interrupted: false))
    }

    func testOnlyTheLockToggleOffPausesInTheBackground() {
        XCTAssertFalse(AudioLifecycle.suspendsInBackground(keepListeningWhenLocked: true, connected: true), "on: today's behaviour")
        XCTAssertTrue(AudioLifecycle.suspendsInBackground(keepListeningWhenLocked: false, connected: true))
        XCTAssertFalse(AudioLifecycle.suspendsInBackground(keepListeningWhenLocked: false, connected: false), "nothing to pause")
    }

    // MARK: - Audio paused by iOS (bug: the orb kept saying "Listening")

    func testAPausedEngineSaysSoAndTheOrbResumesIt() {
        let state = SessionState.resolve(.init(socket: .open, voice: .connected, audioPaused: true))
        XCTAssertEqual(state, .audioPaused)
        XCTAssertEqual(state.title, "Audio paused by iOS — tap to resume")
        XCTAssertFalse(state.capturing)
        XCTAssertEqual(VoiceBarModel(state: state).orbAction, .resume)
        XCTAssertEqual(VoiceBarModel(state: state, talkMode: .holdToTalk).orbAction, .resume, "hold-to-talk resumes too")
        XCTAssertEqual(VoiceBarModel.statusLine(state: state, talkMode: .holdToTalk), state.title)
        XCTAssertEqual(SessionState.resolve(.init(socket: .open, voice: .closed, audioPaused: true)), .voiceOff, "voice off outranks it")
        XCTAssertEqual(SessionState.resolve(.init(socket: .reconnecting, audioPaused: true)), .reconnecting)
    }
}
