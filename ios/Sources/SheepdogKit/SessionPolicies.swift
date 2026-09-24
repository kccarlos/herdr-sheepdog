import Foundation

/// What the session does when the `/ws` socket cannot reach the daemon.
///
/// A daemon restart used to end the session: the first failed retry was
/// treated like a failed Connect, which stopped the socket's retry loop
/// while the orb went on saying "Reconnecting…". Now a failure while the
/// user still wants the session and the socket is retrying keeps retrying
/// (the socket backs off to 10 s): quietly for the first `quietPeriod`,
/// then with a banner once, and on until the user taps End. A refused
/// token, or a failure the socket will not retry, still stops.
public struct ReconnectPolicy: Equatable, Sendable {
    public static let defaultQuietPeriod: TimeInterval = 30

    public enum Decision: Equatable, Sendable {
        /// End the session and say why (the old behaviour, now only for
        /// failures that retrying cannot fix).
        case stop
        /// Keep retrying. `showBanner` is true exactly once per outage, the
        /// first failure after the quiet period.
        case keepRetrying(showBanner: Bool)
    }

    public let quietPeriod: TimeInterval
    /// When the current outage started (the socket dropped, or the first
    /// attempt failed); nil while connected.
    public private(set) var failingSince: TimeInterval?
    public private(set) var bannerShown = false

    public init(quietPeriod: TimeInterval = ReconnectPolicy.defaultQuietPeriod) {
        self.quietPeriod = quietPeriod
    }

    /// The socket dropped and will retry: the outage clock starts, unless
    /// it is already running.
    public mutating func connectionLost(at now: TimeInterval) {
        if failingSince == nil { failingSince = now }
    }

    /// The socket opened: the outage is over.
    public mutating func opened() {
        failingSince = nil
        bannerShown = false
    }

    /// A connection attempt failed.
    public mutating func failure(_ error: DaemonError, retrying: Bool, wantsConnection: Bool, at now: TimeInterval) -> Decision {
        if case .unauthorized = error {
            reset()
            return .stop
        }
        guard wantsConnection, retrying else {
            reset()
            return .stop
        }
        connectionLost(at: now)
        let since = failingSince ?? now
        guard !bannerShown, now - since >= quietPeriod else { return .keepRetrying(showBanner: false) }
        bannerShown = true
        return .keepRetrying(showBanner: true)
    }

    public mutating func reset() {
        failingSince = nil
        bannerShown = false
    }
}

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
/// leave); the dog can always be heard. The engine is stopped while the
/// app is paused in the background (Settings ▸ Keep listening with the
/// screen locked, off).
public enum AudioLifecycle {
    public static func engineShouldRun(socketOpen: Bool, voice: WSMessage.Voice?, suspended: Bool) -> Bool {
        guard socketOpen, !suspended, let voice else { return false }
        return voice != .closed
    }

    /// The watchdog expects signal only from a running, unmuted microphone
    /// that iOS has not paused (a call, Siri, or an interruption iOS would
    /// not let us resume from).
    public static func watchdogShouldRun(running: Bool, muted: Bool, interrupted: Bool) -> Bool {
        running && !muted && !interrupted
    }

    /// Whether going to the background pauses the session.
    public static func suspendsInBackground(keepListeningWhenLocked: Bool, connected: Bool) -> Bool {
        connected && !keepListeningWhenLocked
    }
}
