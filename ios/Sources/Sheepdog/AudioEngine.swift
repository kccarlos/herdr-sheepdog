import AVFoundation
import Foundation
import SheepdogKit

/// The microphone and the speaker on iOS.
///
/// Capture: an `AVAudioEngine` input tap at the hardware format →
/// `AudioCapture` (`PCMConverter`, `FrameChunker`, `CaptureGate` and
/// `MicSignalWatchdog` in `SheepdogKit`) → the caller, which writes each
/// 40 ms frame to `/ws` as a binary frame.
///
/// Playback: `AudioPlayback` (`AVAudioPlayerNode` → `AVAudioUnitTimePitch` →
/// `mainMixerNode`, driven by `PlaybackQueue` and its 20 ms rate-steering
/// loop in `SheepdogKit`).
///
/// The session is `.playAndRecord` / `.voiceChat`, which turns on the
/// system's echo canceller: without it the voice hears itself out of the
/// speaker and interrupts its own sentence.
@MainActor
final class AudioEngine {
    enum Failure: LocalizedError {
        case microphoneDenied
        case sessionFailed(String)
        case engineFailed(String)
        case format(String)

        var errorDescription: String? {
            switch self {
            case .microphoneDenied:
                return "Sheepdog needs the microphone to hear you. Turn it on in Settings ▸ Sheepdog."
            case let .sessionFailed(m):
                return "iOS would not start the audio session (\(m))."
            case let .engineFailed(m):
                return "The audio engine would not start (\(m))."
            case let .format(m):
                return m
            }
        }
    }

    /// A 40 ms frame ready for the socket, with the peak that decided the gate.
    var onFrame: ((Data) -> Void)? {
        get { capture.onFrame }
        set { capture.onFrame = newValue }
    }
    /// The silence gate flipped: `true` when it closed.
    var onGate: ((Bool) -> Void)? {
        get { capture.onGate }
        set { capture.onGate = newValue }
    }
    /// Input level, 0…1, for the orb's ring.
    var onLevel: ((Float) -> Void)? {
        get { capture.onLevel }
        set { capture.onLevel = newValue }
    }
    /// Playback started or stopped (the voice speaking, for the orb).
    var onSpeakingChanged: ((Bool) -> Void)? {
        get { playback.onSpeakingChanged }
        set { playback.onSpeakingChanged = newValue }
    }
    /// Output level, 0…1, as each chunk finishes playing: the speaking
    /// orb's waveform follows it. 0 when the voice stops.
    var onOutputLevel: ((Float) -> Void)? {
        get { playback.onOutputLevel }
        set { playback.onOutputLevel = newValue }
    }
    /// A playback report for the daemon (`started`, `drained`, `flushed`;
    /// docs/ws-contract.md "Playback reports"), in the order they happened.
    var onPlaybackReport: ((PlaybackQueue.Report) -> Void)? {
        get { playback.onPlaybackReport }
        set { playback.onPlaybackReport = newValue }
    }
    /// Every time the player ran dry, for the daemon's `drain` message.
    var onDrain: ((PlaybackQueue.DrainRecord) -> Void)? {
        get { playback.onDrain }
        set { playback.onDrain = newValue }
    }
    /// An interruption (a call, Siri) or a route change: the voice bar, a
    /// banner or the log say it, not the conversation (`LocalNotice`).
    var onNotice: ((LocalNotice) -> Void)?
    /// Where the sound goes now, when the engine starts and on each route
    /// change; nil once it stops.
    var onRoute: ((AudioRoute?) -> Void)?
    /// The microphone stopped (true) or resumed (false) delivering signal.
    var onMicSignal: ((Bool) -> Void)? {
        get { capture.onMicSignal }
        set { capture.onMicSignal = newValue }
    }
    /// iOS paused the audio and would not resume it (true), or it was
    /// resumed or stopped (false).
    var onPausedChanged: ((Bool) -> Void)?
    /// The engine stopped by itself: a route change it could not rebuild
    /// the capture chain for. The words say why.
    var onStopped: ((String) -> Void)?
    /// iOS gave the audio back (an interruption ended) or the route moved
    /// again after the engine stopped by itself: the moment to start it
    /// again.
    var onAvailable: (() -> Void)?

    var metrics: AudioMetrics {
        var combined = engineMetrics
        capture.apply(to: &combined)
        playback.apply(to: &combined)
        return combined
    }
    private var engineMetrics = AudioMetrics()
    private(set) var isRunning = false
    /// Running, but paused after an interruption iOS would not let us
    /// resume from by ourselves: `resume()` is the way back.
    private(set) var needsResume = false

    private let engine = AVAudioEngine()
    private let capture = AudioCapture()
    private let playback = AudioPlayback()

    private var interrupted = false
    /// The `.playAndRecord` session is active and ours: false until
    /// `configureSession` succeeds, and again while iOS holds the audio.
    /// Installing a tap on an input iOS will not let us record from throws
    /// an `NSException`, which Swift cannot catch.
    private var sessionActive = false
    /// The route changed while iOS held the audio: the tap's format is
    /// stale, so the capture chain is rebuilt when the audio comes back.
    private var routeChangedWhileHeld = false
    /// The engine stopped by itself (`onStopped`) and has not started since.
    private var stoppedByItself = false
    private var observers: [NSObjectProtocol] = []

    init() {
        // Attach only. The player is connected in `start()`, after the
        // `.playAndRecord` session is active and voice processing is on:
        // a graph wired against the launch-time session (`.soloAmbient`,
        // output only) played nothing once the session changed under it.
        playback.attach(to: engine)
        capture.onSignalChange = { [weak self] change in
            self?.signalChanged(change)
        }
        observe()
    }

    deinit {
        for observer in observers { NotificationCenter.default.removeObserver(observer) }
    }

    // MARK: - Permission

    static func requestMicrophone() async -> Bool {
        if AVAudioApplication.shared.recordPermission == .granted { return true }
        return await AVAudioApplication.requestRecordPermission()
    }

    // MARK: - Lifecycle

    /// Starts capture and playback. With the Info.plist `audio` background
    /// mode a running engine keeps the app alive with the screen locked;
    /// the view model stops it on backgrounding when Settings ▸ Keep
    /// listening with the screen locked is off.
    func start() throws {
        guard !isRunning else { return }
        guard !holdsAudio else { throw Failure.sessionFailed("iOS has the audio right now") }
        try configureSession()

        let input = engine.inputNode
        enableVoiceProcessing()
        // A voice-processing unit carries its own input mute, and the system has an
        // app-wide one (AirPods' press-to-mute drives it). Either left on
        // gives exactly the symptom seen on the phone — unmuted in the app,
        // digital zero from the tap — so start from "not muted" and log it.
        if input.isVoiceProcessingEnabled, input.isVoiceProcessingInputMuted {
            SheepdogLog.audio.error("voice processing input was muted; unmuting")
            input.isVoiceProcessingInputMuted = false
        }
        if AVAudioApplication.shared.isInputMuted {
            SheepdogLog.audio.error("app input was muted at the system level; unmuting")
            try? AVAudioApplication.shared.setInputMuted(false)
        }
        playback.connect(in: engine)
        engine.mainMixerNode.outputVolume = 1
        let format = input.inputFormat(forBus: 0)
        try requireCapturable(format)
        try capture.prepare(inputFormat: format, isStart: true)
        engineMetrics.hardwareSampleRate = format.sampleRate

        // 4 096 frames is a hint; iOS hands over what it likes, which is why
        // the chunker exists.
        input.installTap(onBus: 0, bufferSize: 4_096, format: format) { [weak self] buffer, _ in
            self?.handleCaptured(buffer)
        }
        engine.prepare()
        do {
            try engine.start()
        } catch {
            input.removeTap(onBus: 0)
            throw Failure.engineFailed(error.localizedDescription)
        }
        playback.start()
        isRunning = true
        stoppedByItself = false
        capture.didStart()
        recordVoiceProcessing()
        reportRoute()
        SheepdogLog.audio.info("audio started: input \(format.sampleRate, privacy: .public) Hz, \(format.channelCount, privacy: .public) ch, echo cancellation \(self.engineMetrics.voiceProcessing.summary, privacy: .public), time-pitch latency \(self.playback.timePitchLatency * 1000, privacy: .public) ms")
    }

    /// The echo canceller. `.voiceChat` alone does not cancel echo for
    /// AVAudioEngine; the voice-processing I/O unit does, and it has to be
    /// on before the engine starts. Enabling it on the input node turns
    /// the engine's I/O into one voice-processing unit, so the output node
    /// the player → time-pitch → mixer chain feeds is the canceller's
    /// reference: everything the voice says is played through it, nothing
    /// around it. Without it the voice hears itself from the speaker.
    /// Called with the engine stopped (start, and the rebuild after a
    /// configuration change, which can come back without it).
    private func enableVoiceProcessing() {
        let input = engine.inputNode
        if !input.isVoiceProcessingEnabled {
            do {
                try input.setVoiceProcessingEnabled(true)
            } catch {
                SheepdogLog.audio.error("voice processing unavailable: \(error.localizedDescription, privacy: .public)")
            }
        }
        if input.isVoiceProcessingEnabled, input.isVoiceProcessingBypassed {
            SheepdogLog.audio.error("voice processing was bypassed; turning it back on")
            input.isVoiceProcessingBypassed = false
        }
    }

    /// Settings ▸ About's echo-cancellation line, and the routes.
    private func recordVoiceProcessing() {
        let session = AVAudioSession.sharedInstance()
        let status = VoiceProcessingStatus(
            inputEnabled: engine.inputNode.isVoiceProcessingEnabled,
            outputEnabled: engine.outputNode.isVoiceProcessingEnabled,
            bypassed: engine.inputNode.isVoiceProcessingBypassed,
            voiceChatMode: session.mode == .voiceChat && session.category == .playAndRecord,
            outputRoute: session.currentRoute.outputs.map(\.portName).joined(separator: ", "),
            inputRoute: session.currentRoute.inputs.map(\.portName).joined(separator: ", ")
        )
        engineMetrics.outputRoute = status.outputRoute
        if status != engineMetrics.voiceProcessing {
            engineMetrics.voiceProcessing = status
            SheepdogLog.audio.info("echo cancellation: \(status.summary, privacy: .public)")
        }
    }

    func stop() {
        guard isRunning else { return }
        engine.inputNode.removeTap(onBus: 0)
        playback.stop(in: engine) {
            capture.resetOnStop()
            isRunning = false
            interrupted = false
            sessionActive = false
            routeChangedWhileHeld = false
            stoppedByItself = false
            setNeedsResume(false)
            onRoute?(nil)
            capture.disarmWatchdog()
        }
        try? AVAudioSession.sharedInstance().setActive(false, options: .notifyOthersOnDeactivation)
    }

    func setMuted(_ muted: Bool) {
        capture.setMuted(muted, running: isRunning, interrupted: holdsAudio)
    }

    // MARK: - The gate

    /// Hold-to-talk: `true` pressed, `false` released, `nil` open mic. The
    /// transition reaches the caller through `onGate` with the next frame.
    func setManualGate(_ manual: Bool?) {
        capture.setManualGate(manual, running: isRunning, interrupted: holdsAudio)
    }

    var gateDiagnostics: CaptureGate.Diagnostics { capture.gateDiagnostics }

    /// The gate's own state right now (after a start or a reset, before any
    /// frame has told the caller).
    var gateIsClosed: Bool { capture.gateIsClosed }

    // MARK: - No signal

    var noSignal: Bool { capture.noSignal }

    private func signalChanged(_ change: AudioCapture.SignalChange) {
        switch change {
        case .recovered:
            SheepdogLog.audio.info("microphone signal is back")
        case let .lost(restart):
            let input = engine.inputNode
            let format = input.inputFormat(forBus: 0)
            SheepdogLog.audio.error("no microphone signal for \(self.capture.watchdogThreshold, privacy: .public) s: input \(format.sampleRate, privacy: .public) Hz \(format.channelCount, privacy: .public) ch, voice processing \(input.isVoiceProcessingEnabled, privacy: .public), vp input muted \(input.isVoiceProcessingInputMuted, privacy: .public), app input muted \(AVAudioApplication.shared.isInputMuted, privacy: .public), route \(AVAudioSession.sharedInstance().currentRoute.inputs.map(\.portName).joined(separator: ","), privacy: .public)")
            // One automatic restart per arm: a capture chain that came up
            // silent usually comes back when it is rebuilt.
            if restart {
                if input.isVoiceProcessingEnabled, input.isVoiceProcessingInputMuted { input.isVoiceProcessingInputMuted = false }
                // The watchdog stays tripped until a real sample arrives, so the
                // screen does not flicker back to "listening" on a guess.
                restartCapture()
            }
        }
    }

    private func configureSession() throws {
        let session = AVAudioSession.sharedInstance()
        do {
            try session.setCategory(
                .playAndRecord,
                mode: .voiceChat,
                options: [.allowBluetooth, .allowBluetoothA2DP, .defaultToSpeaker]
            )
            // 40 ms of hardware buffer keeps the tap callbacks in step with
            // the frames the contract wants.
            try session.setPreferredIOBufferDuration(0.04)
            try session.setActive(true, options: [])
            sessionActive = true
        } catch {
            sessionActive = false
            throw Failure.sessionFailed(error.localizedDescription)
        }
    }

    /// iOS has the audio: an interruption is on, or it ended without letting
    /// us resume. Nothing may touch the input then.
    private var holdsAudio: Bool { interrupted || needsResume }

    /// The one check in front of every `installTap`: the session is ours and
    /// the input has a format. Refusing here is a Swift error; reaching
    /// AVFoundation without it is a crash.
    private func requireCapturable(_ format: AVAudioFormat) throws {
        guard sessionActive, !holdsAudio else { throw Failure.sessionFailed("iOS has the audio right now") }
        guard format.channelCount > 0, format.sampleRate > 0 else {
            // The simulator does this while a route settles; so does a device
            // whose microphone another app holds.
            throw Failure.format(AudioCapture.noMicrophoneMessage)
        }
    }

    // MARK: - Capture

    /// Called on the audio render thread. The buffer is only valid for the
    /// length of the callback, so it is copied and handed to the main actor,
    /// which owns the converter, the chunker and the gate. One copy of ~40 ms
    /// of float samples, 25 times a second, is nothing next to keeping the
    /// render thread lock-free.
    private nonisolated func handleCaptured(_ buffer: AVAudioPCMBuffer) {
        guard let copy = AudioEngine.copy(buffer) else { return }
        DispatchQueue.main.async { [weak self] in
            self?.captureOnMain(copy)
        }
    }

    private nonisolated static func copy(_ buffer: AVAudioPCMBuffer) -> AVAudioPCMBuffer? {
        guard buffer.frameLength > 0,
              let out = AVAudioPCMBuffer(pcmFormat: buffer.format, frameCapacity: buffer.frameLength) else { return nil }
        out.frameLength = buffer.frameLength
        let source = UnsafeMutableAudioBufferListPointer(UnsafeMutablePointer(mutating: buffer.audioBufferList))
        let destination = UnsafeMutableAudioBufferListPointer(out.mutableAudioBufferList)
        for index in 0 ..< min(source.count, destination.count) {
            guard let from = source[index].mData, let to = destination[index].mData else { continue }
            memcpy(to, from, Int(source[index].mDataByteSize))
            destination[index].mDataByteSize = source[index].mDataByteSize
        }
        return out
    }

    private func captureOnMain(_ buffer: AVAudioPCMBuffer) {
        guard isRunning else { return }
        do {
            try capture.process(buffer)
        } catch {
            SheepdogLog.audio.error("convert failed: \(String(describing: error), privacy: .public)")
        }
    }

    var inputPeak: Int { capture.inputPeak }
    var gatedFrames: Int { capture.gatedFrames }

    // MARK: - Playback

    /// One binary frame from the daemon: PCM16 LE mono at 24 kHz.
    func enqueue(_ data: Data) {
        playback.enqueue(data)
    }

    /// What the `ping` says about the engine (the browser sends its
    /// AudioContext state): `running`, `interrupted` (iOS holds the audio)
    /// or `suspended` (stopped: voice closed, backgrounded, or not started).
    var audioState: String {
        guard isRunning else { return "suspended" }
        return interrupted || needsResume ? "interrupted" : "running"
    }

    /// The watchdog and the playback metrics, on the UI timer's cadence
    /// (100 ms while audio is moving, 1 s idle).
    func tick() {
        capture.tickWatchdog(running: isRunning, interrupted: holdsAudio)
        playback.tick()
    }

    /// `flush` from the daemon, or the Stop button: drop everything queued,
    /// now. Reports `flushed` every time, even with nothing playing.
    func flushPlayback() {
        playback.flush()
    }

    /// The voice provider changed (from `/ws` `status.provider`): switch
    /// the jitter-buffer target range. A no-op when it is already right.
    func setProvider(_ provider: String?) {
        guard let profile = playback.setProvider(provider) else { return }
        SheepdogLog.audio.info("playback profile for \(provider ?? "default", privacy: .public): target \(profile.floorMilliseconds, privacy: .public)–\(profile.ceilingMilliseconds, privacy: .public) ms")
    }

    // MARK: - Interruptions and routes

    private func observe() {
        let center = NotificationCenter.default
        observers.append(center.addObserver(forName: AVAudioSession.interruptionNotification, object: nil, queue: .main) { [weak self] note in
            MainActor.assumeIsolated { self?.handleInterruption(note) }
        })
        observers.append(center.addObserver(forName: AVAudioSession.routeChangeNotification, object: nil, queue: .main) { [weak self] note in
            MainActor.assumeIsolated { self?.handleRouteChange(note) }
        })
        // The engine stops itself when the hardware format changes under it
        // (a route or sample-rate change); pick playback back up.
        observers.append(center.addObserver(forName: .AVAudioEngineConfigurationChange, object: engine, queue: .main) { [weak self] _ in
            MainActor.assumeIsolated { self?.restartCapture() }
        })
        observers.append(center.addObserver(forName: AVAudioSession.mediaServicesWereResetNotification, object: nil, queue: .main) { [weak self] _ in
            MainActor.assumeIsolated { self?.onNotice?(.mediaServicesReset) }
        })
    }

    private func handleInterruption(_ note: Notification) {
        guard let raw = note.userInfo?[AVAudioSessionInterruptionTypeKey] as? UInt,
              let type = AVAudioSession.InterruptionType(rawValue: raw) else { return }
        switch type {
        case .began:
            interrupted = true
            sessionActive = false
            engine.pause()
            flushPlayback()
            // Nothing can arrive while iOS holds the audio: silence now is
            // not a broken microphone.
            capture.disarmWatchdog()
            onNotice?(.pausedForInterruption)
        case .ended:
            guard interrupted else { return }
            interrupted = false
            guard isRunning else {
                // The engine stopped by itself during the interruption:
                // nothing to resume, and now it can start again.
                if stoppedByItself { onAvailable?() }
                return
            }
            let options = (note.userInfo?[AVAudioSessionInterruptionOptionKey] as? UInt).map(AVAudioSession.InterruptionOptions.init)
            guard options?.contains(.shouldResume) ?? true else {
                // Still "running" but paused: say so, and let the orb take
                // it back.
                setNeedsResume(true)
                onNotice?(.audioNotGivenBack)
                return
            }
            resumeAfterInterruption()
        @unknown default:
            break
        }
    }

    private func handleRouteChange(_ note: Notification) {
        guard let raw = note.userInfo?[AVAudioSessionRouteChangeReasonKey] as? UInt,
              let reason = AVAudioSession.RouteChangeReason(rawValue: raw) else { return }
        switch reason {
        case .oldDeviceUnavailable, .newDeviceAvailable, .override, .categoryChange:
            guard isRunning else {
                // Stopped by itself: the route moved again, which is when a
                // microphone that went away may be back.
                if stoppedByItself, !holdsAudio { onAvailable?() }
                return
            }
            // iOS holds the audio (a call, another app playing): the route
            // changes under an interruption are not ours to rebuild for.
            // Remember it; the way back from the interruption rebuilds.
            guard !holdsAudio else {
                routeChangedWhileHeld = true
                return
            }
            // Headphones in or out: the hardware format can change under the
            // tap, so rebuild the capture chain rather than convert from a
            // rate that no longer exists.
            restartCapture()
            onNotice?(.routeChanged(output: AVAudioSession.sharedInstance().currentRoute.outputs.first?.portName))
            reportRoute()
        default:
            break
        }
    }

    /// Tells the voice bar where the sound goes now.
    private func reportRoute() {
        let port = AVAudioSession.sharedInstance().currentRoute.outputs.first?.portType
        onRoute?(port.map(AudioRoute.init(port:)) ?? .speaker)
    }

    private func resumeAfterInterruption() {
        do {
            try reactivate()
            onNotice?(.listeningAgain)
        } catch {
            setNeedsResume(true)
            onNotice?(.audioFailedToResume(error.localizedDescription))
        }
    }

    /// The orb's "tap to resume" after iOS paused the audio.
    func resume() throws {
        guard isRunning, needsResume else { return }
        try reactivate()
    }

    private func reactivate() throws {
        guard isRunning else { return }
        try AVAudioSession.sharedInstance().setActive(true, options: [])
        sessionActive = true
        if routeChangedWhileHeld {
            // The route moved while iOS held the audio: rebuild the capture
            // chain on what it is now instead of restarting the old one.
            routeChangedWhileHeld = false
            setNeedsResume(false)
            restartCapture()
            guard isRunning else { return }
            reportRoute()
        } else {
            try engine.start()
            playback.play()
            setNeedsResume(false)
        }
        capture.armWatchdogIfNeeded(running: isRunning, interrupted: false)
    }

    private func setNeedsResume(_ paused: Bool) {
        guard paused != needsResume else { return }
        needsResume = paused
        onPausedChanged?(paused)
    }

    /// Rebuilds the capture chain; if that cannot be done the engine stops
    /// and says why (`onStopped`). While iOS holds the audio nothing is
    /// rebuilt: the way back from the interruption does it (`reactivate`).
    private func restartCapture() {
        guard isRunning else { return }
        guard !holdsAudio else {
            routeChangedWhileHeld = true
            return
        }
        do {
            try rebuildCapture()
        } catch Failure.format {
            stopByItself(LocalNotice.microphoneWentAway)
        } catch {
            stopByItself(LocalNotice.audioStoppedAfterRouteChange(error.localizedDescription))
        }
    }

    private func stopByItself(_ message: String) {
        isRunning = false
        stoppedByItself = true
        playback.stopForFailure()
        onStopped?(message)
    }

    private func rebuildCapture() throws {
        engine.inputNode.removeTap(onBus: 0)
        engine.stop()
        // A category change (another audio client, a system dialog) can
        // leave the session out of `.voiceChat`, and a rebuilt I/O unit
        // without voice processing: both switch the echo canceller off.
        let session = AVAudioSession.sharedInstance()
        if session.mode != .voiceChat || session.category != .playAndRecord {
            SheepdogLog.audio.error("audio session left voice chat (\(session.category.rawValue, privacy: .public)/\(session.mode.rawValue, privacy: .public)); restoring it")
            try? configureSession()
        }
        enableVoiceProcessing()
        let input = engine.inputNode
        let format = input.inputFormat(forBus: 0)
        try requireCapturable(format)
        try capture.prepare(inputFormat: format)
        engineMetrics.hardwareSampleRate = format.sampleRate
        input.installTap(onBus: 0, bufferSize: 4_096, format: format) { [weak self] buffer, _ in
            self?.handleCaptured(buffer)
        }
        playback.connect(in: engine)
        do {
            try engine.start()
        } catch {
            input.removeTap(onBus: 0)
            throw Failure.engineFailed(error.localizedDescription)
        }
        playback.play()
        recordVoiceProcessing()
    }
}
