import AppKit
import AVFoundation
import Foundation
import SheepdogKit

/// What the voice host needs from the system, each behind a protocol so
/// `MacVoiceHostTests` runs with fakes (docs/dev/design/desktop-client.md
/// section 5.2).

/// The microphone and the speaker: what `MacAudioEngine` offers the host.
/// The same surface the iOS voice drives on `AudioEngine`, less what only a
/// phone has (interruptions, the route glyph, the level meters).
@MainActor
protocol VoiceAudio: AnyObject {
    /// A 40 ms frame ready for the socket.
    var onFrame: ((Data) -> Void)? { get set }
    /// The silence gate flipped: `true` when it closed.
    var onGate: ((Bool) -> Void)? { get set }
    /// Playback started or stopped (the voice speaking).
    var onSpeakingChanged: ((Bool) -> Void)? { get set }
    /// A playback report for the daemon, in the order they happened.
    var onPlaybackReport: ((PlaybackQueue.Report) -> Void)? { get set }
    /// Every time the player ran dry, for the daemon's `drain` message.
    var onDrain: ((PlaybackQueue.DrainRecord) -> Void)? { get set }
    /// A route change the engine rebuilt itself for (`LocalNotice`).
    var onNotice: ((LocalNotice) -> Void)? { get set }
    /// The microphone stopped (true) or resumed (false) delivering signal.
    var onMicSignal: ((Bool) -> Void)? { get set }
    /// The running engine died and could not be rebuilt; the words say why.
    var onStopped: ((String) -> Void)? { get set }
    /// The hardware moved again after the engine stopped by itself: the
    /// moment to start it again.
    var onAvailable: (() -> Void)? { get set }

    var metrics: AudioMetrics { get }
    var isRunning: Bool { get }
    var gateIsClosed: Bool { get }
    var gatedFrames: Int { get }
    var inputPeak: Int { get }
    var audioState: String { get }
    var noSignal: Bool { get }

    func start() throws
    func stop()
    func setMuted(_ muted: Bool)
    func setManualGate(_ manual: Bool?)
    func flushPlayback()
    /// One binary frame from the daemon: PCM16 LE mono at 24 kHz.
    func enqueue(_ data: Data)
    func tick()
    func setProvider(_ provider: String?)
}

/// The `/ws` socket, as `VoiceSocket` offers it to the voice.
@MainActor
protocol VoiceChannel: AnyObject {
    var onEvent: ((VoiceSocket.Event) -> Void)? { get set }
    var client: WSMessage.ClientInfo { get set }
    /// What the daemon is to believe on every (re)connection.
    var muted: Bool { get set }
    var gateClosed: Bool { get set }
    var speaking: Bool { get set }
    /// Counters copied onto the `ping`.
    var gatedFrames: Int { get set }
    var inputPeak: Int { get set }
    var underruns: Int { get set }
    var drains: Int { get set }
    var bufferMs: Int { get set }
    var rate: Double { get set }
    var stretchedMs: Int { get set }
    var audioState: String? { get set }

    func connect()
    func disconnect(reason: String)
    @discardableResult func send(_ message: WSMessage) -> Bool
    @discardableResult func send(frame: Data) -> Bool
}

extension VoiceSocket: VoiceChannel {}

/// The auth probe a session starts with (`GET /api/state`), as the phone's
/// `openSession` does: a daemon that is not there is told plainly here, not
/// as a failed WebSocket upgrade.
protocol DaemonProbing {
    func probe() async throws
}

struct DaemonProbe: DaemonProbing {
    let locator: DaemonLocating

    func probe() async throws {
        let client = DaemonClient(endpoint: locator.endpoint)
        defer { client.invalidate() }
        _ = try await client.state()
    }
}

/// Where the user stands with the microphone.
enum MicrophoneAccess: Equatable {
    case notDetermined
    case granted
    /// Refused in System Settings, or by a profile.
    case denied
}

protocol MicrophonePermission {
    var access: MicrophoneAccess { get }
    /// True when the microphone may be used: asks the first time only.
    func request() async -> Bool
}

struct SystemMicrophonePermission: MicrophonePermission {
    var access: MicrophoneAccess {
        switch AVCaptureDevice.authorizationStatus(for: .audio) {
        case .authorized: return .granted
        case .notDetermined: return .notDetermined
        case .denied, .restricted: return .denied
        @unknown default: return .denied
        }
    }

    func request() async -> Bool {
        switch access {
        case .granted: return true
        case .denied: return false
        case .notDetermined: return await AVCaptureDevice.requestAccess(for: .audio)
        }
    }
}

/// "Open System Settings" on the Microphone page of Privacy & Security.
protocol SystemSettingsOpening {
    func openMicrophonePrivacy()
}

struct SystemSettingsOpener: SystemSettingsOpening {
    static let microphoneURL = URL(string: "x-apple.systempreferences:com.apple.preference.security?Privacy_Microphone")!

    func openMicrophonePrivacy() {
        NSWorkspace.shared.open(Self.microphoneURL)
    }
}

/// The host's clocks: the ticker that drives the audio's periodic work and
/// the one-shot that opens `/ws` again after a loss. `VoiceSocket` never
/// retries by itself; on the phone the computer's `ComputerLink` does. The
/// Mac has one loopback daemon and no link until computers arrive (v2).
@MainActor
protocol VoiceTimers: AnyObject {
    /// Calls `fire` repeatedly, `interval()` apart (asked again each time).
    func startTicking(interval: @escaping @MainActor () -> TimeInterval, fire: @escaping @MainActor () -> Void)
    func stopTicking()
    /// Calls `work` once after `delay`, replacing a pending one.
    func reopen(after delay: TimeInterval, _ work: @escaping @MainActor () -> Void)
    func cancelReopen()
}

@MainActor
final class TaskTimers: VoiceTimers {
    private var ticker: Task<Void, Never>?
    private var reopening: Task<Void, Never>?

    func startTicking(interval: @escaping @MainActor () -> TimeInterval, fire: @escaping @MainActor () -> Void) {
        ticker?.cancel()
        ticker = Task {
            while !Task.isCancelled {
                try? await Task.sleep(nanoseconds: UInt64(interval() * 1_000_000_000))
                guard !Task.isCancelled else { return }
                fire()
            }
        }
    }

    func stopTicking() {
        ticker?.cancel()
        ticker = nil
    }

    func reopen(after delay: TimeInterval, _ work: @escaping @MainActor () -> Void) {
        reopening?.cancel()
        reopening = Task {
            try? await Task.sleep(nanoseconds: UInt64(delay * 1_000_000_000))
            guard !Task.isCancelled else { return }
            work()
        }
    }

    func cancelReopen() {
        reopening?.cancel()
        reopening = nil
    }
}
