import Foundation

/// Who owns the mute state: the app. The daemon only echoes it.
///
/// The app used to adopt the `muted` of every `status`, so a daemon that
/// remembered an old mute (or a status crossing a `mute` in flight)
/// silently muted the phone — in hold-to-talk, where the Mute button is
/// hidden, with no way out. Now the app says what it wants on every open
/// (including "not muted") and answers a disagreeing status by saying it
/// again.
public struct MuteSync: Equatable, Sendable {
    public enum Action: Equatable, Sendable {
        case none
        /// Send `mute(intent)` again. `log` is true once per disagreement.
        case resend(log: Bool)
    }

    /// The last disagreement was already logged.
    public private(set) var disagreementLogged = false

    public init() {}

    /// What the daemon should believe: muted when the user muted, or while
    /// the app is paused in the background.
    public static func daemonMute(intent: Bool, suspended: Bool) -> Bool {
        intent || suspended
    }

    /// The user's mute, as far as the talk mode allows: hold-to-talk hides
    /// Mute (not holding *is* muted), so a mute there would be a dead end.
    public static func intent(requested: Bool, talkMode: TalkMode) -> Bool {
        talkMode == .holdToTalk ? false : requested
    }

    /// A `status` arrived carrying the daemon's view.
    public mutating func status(daemonMuted: Bool?, intent: Bool) -> Action {
        guard let daemonMuted else { return .none }
        guard daemonMuted != intent else {
            disagreementLogged = false
            return .none
        }
        let log = !disagreementLogged
        disagreementLogged = true
        return .resend(log: log)
    }

    /// A fresh socket: its first status is a new conversation.
    public mutating func reset() {
        disagreementLogged = false
    }
}

/// When the audio engine runs, and when the microphone watchdog listens.
///
/// The engine does capture *and* playback, so it runs whenever the voice
/// session is up — muted or not. Mute only gates capture (no frames
/// leave); the voice can always be heard. The engine is stopped while the
/// app is paused in the background (Settings ▸ Keep listening with the
/// screen locked, off).
public enum AudioLifecycle {
    public static func engineShouldRun(socketOpen: Bool, voice: WSMessage.Voice?, suspended: Bool) -> Bool {
        guard socketOpen, !suspended, let voice else { return false }
        return voice != .closed
    }

    /// The watchdog expects signal only from a running, unmuted microphone
    /// that iOS has not paused (a call, Siri, or an interruption iOS would
    /// not let us resume from). In hold-to-talk the microphone is only meant
    /// to be live while the gate is open, so a closed gate expects nothing.
    public static func watchdogShouldRun(running: Bool, muted: Bool, interrupted: Bool, gateClosed: Bool = false) -> Bool {
        running && !muted && !interrupted && !gateClosed
    }

    /// Whether going to the background pauses the session.
    public static func suspendsInBackground(keepListeningWhenLocked: Bool, connected: Bool) -> Bool {
        connected && !keepListeningWhenLocked
    }
}
