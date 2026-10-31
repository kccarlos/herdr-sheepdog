import Foundation
import SheepdogKit
@testable import SheepdogMac

/// One fake per platform service protocol of the app layer.

final class FakeLocator: DaemonLocating {
    var endpoint: DaemonEndpoint
    init(host: String = "127.0.0.1", port: Int = 1) {
        endpoint = DaemonEndpoint(host: host, port: port)
    }
}

final class FakeLoginItem: LoginItemControlling {
    var isEnabled = false
    var failure: Error?
    private(set) var calls: [Bool] = []

    func setEnabled(_ enabled: Bool) throws {
        calls.append(enabled)
        if let failure { throw failure }
        isEnabled = enabled
    }
}

final class FakeLauncher: ProcessLaunching {
    struct Launch: Equatable {
        var executable: String
        var arguments: [String]
        var path: String?
    }

    var failure: Error?
    private(set) var launches: [Launch] = []

    func launch(_ executable: String, arguments: [String], environment: [String: String]) throws {
        launches.append(Launch(executable: executable, arguments: arguments, path: environment["PATH"]))
        if let failure { throw failure }
    }
}

final class FakePanel: PanelOpening {
    var failure: Error?
    private(set) var opened = 0

    func openPanel() throws {
        opened += 1
        if let failure { throw failure }
    }
}

final class FakeLog: AppLogging {
    private(set) var lines: [String] = []
    func log(_ message: String) { lines.append(message) }
}

final class FakeStatusSource: DaemonStatusSource {
    var onChange: (@MainActor (DaemonState?) -> Void)?
    private(set) var running = false

    func start() { running = true }
    func stop() { running = false }
}

struct Boom: Error {}

// MARK: - The voice session

@MainActor
final class FakeAudio: VoiceAudio {
    var onFrame: ((Data) -> Void)?
    var onGate: ((Bool) -> Void)?
    var onSpeakingChanged: ((Bool) -> Void)?
    var onPlaybackReport: ((PlaybackQueue.Report) -> Void)?
    var onDrain: ((PlaybackQueue.DrainRecord) -> Void)?
    var onNotice: ((LocalNotice) -> Void)?
    var onMicSignal: ((Bool) -> Void)?
    var onStopped: ((String) -> Void)?

    var metrics = AudioMetrics()
    private(set) var isRunning = false
    var gateIsClosed = false
    var gatedFrames = 0
    var inputPeak = 0
    var audioState: String { isRunning ? "running" : "suspended" }
    var noSignal = false

    var startFailure: Error?
    private(set) var starts = 0
    private(set) var stops = 0
    private(set) var muted: [Bool] = []
    private(set) var gates: [Bool?] = []
    private(set) var flushes = 0
    private(set) var enqueued: [Data] = []
    private(set) var providers: [String?] = []

    func start() throws {
        starts += 1
        if let startFailure { throw startFailure }
        isRunning = true
    }

    func stop() {
        stops += 1
        isRunning = false
    }

    /// The engine died under the session (a route change it could not
    /// rebuild), and says so.
    func die(_ message: String) {
        isRunning = false
        onStopped?(message)
    }

    func setMuted(_ muted: Bool) { self.muted.append(muted) }
    func setManualGate(_ manual: Bool?) { gates.append(manual) }
    func flushPlayback() { flushes += 1 }
    func enqueue(_ data: Data) { enqueued.append(data) }
    func tick() {}
    func setProvider(_ provider: String?) { providers.append(provider) }
}

@MainActor
final class FakeChannel: VoiceChannel {
    var onEvent: ((VoiceSocket.Event) -> Void)?
    var client = WSMessage.ClientInfo(name: "", version: "", platform: "")
    var muted = false
    var gateClosed = false
    var speaking = false
    var gatedFrames = 0
    var inputPeak = 0
    var underruns = 0
    var drains = 0
    var bufferMs = 0
    var rate = 1.0
    var stretchedMs = 0
    var audioState: String?

    private(set) var connects = 0
    private(set) var disconnects: [String] = []
    private(set) var sent: [WSMessage] = []
    private(set) var frames: [Data] = []

    func connect() { connects += 1 }
    func disconnect(reason: String) { disconnects.append(reason) }

    func send(_ message: WSMessage) -> Bool {
        sent.append(message)
        return true
    }

    func send(frame: Data) -> Bool {
        frames.append(frame)
        return true
    }

    /// What the socket reports.
    func emit(_ event: VoiceSocket.Event) { onEvent?(event) }
}

final class FakeProbe: DaemonProbing {
    var failure: Error?
    private(set) var probes = 0

    func probe() async throws {
        probes += 1
        if let failure { throw failure }
    }
}

final class FakeMicrophone: MicrophonePermission {
    var access: MicrophoneAccess
    /// What the prompt answers when it is asked (`notDetermined`).
    var answer = true
    private(set) var requests = 0

    init(access: MicrophoneAccess = .granted) {
        self.access = access
    }

    func request() async -> Bool {
        requests += 1
        if access == .notDetermined { access = answer ? .granted : .denied }
        return access == .granted
    }
}

final class FakeSettings: SystemSettingsOpening {
    private(set) var opened = 0
    func openMicrophonePrivacy() { opened += 1 }
}

@MainActor
final class FakeTimers: VoiceTimers {
    private(set) var ticking = false
    private(set) var pendingReopen: (delay: TimeInterval, work: @MainActor () -> Void)?
    private(set) var reopens = 0
    private var fire: (@MainActor () -> Void)?

    func startTicking(interval: @escaping @MainActor () -> TimeInterval, fire: @escaping @MainActor () -> Void) {
        ticking = true
        self.fire = fire
    }

    func stopTicking() {
        ticking = false
        fire = nil
    }

    func reopen(after delay: TimeInterval, _ work: @escaping @MainActor () -> Void) {
        reopens += 1
        pendingReopen = (delay, work)
    }

    func cancelReopen() { pendingReopen = nil }

    func tick() { fire?() }

    /// The delay passes.
    func fireReopen() {
        let work = pendingReopen?.work
        pendingReopen = nil
        work?()
    }
}

/// The voice as the menu and the routes see it.
@MainActor
final class FakeVoice: VoiceControlling {
    var state = VoiceMenuState()
    var onChange: (@MainActor () -> Void)?
    private(set) var routes: [AppRoute] = []
    private(set) var useHeres = 0
    private(set) var settingsOpened = 0

    func handle(route: AppRoute) { routes.append(route) }
    func useHere() { useHeres += 1 }
    func openMicrophoneSettings() { settingsOpened += 1 }
}
