import AVFoundation
import Foundation
import SheepdogKit

/// The microphone and the speaker.
///
/// Capture: an `AVAudioEngine` input tap at the hardware format →
/// `PCMConverter` (16 kHz mono Int16) → `FrameChunker` (40 ms frames) →
/// `CaptureGate` (the voice-activity detector, or hold-to-talk) → the
/// caller, which writes each frame to `/ws` as a binary frame. A
/// `MicSignalWatchdog` watches for a microphone that delivers only digital
/// zero while unmuted.
///
/// Playback: one `AVAudioPlayerNode` fed 24 kHz chunks back to back through
/// `PlaybackQueue`, so the node concatenates them itself — gapless, and
/// never "play this now". `PlaybackQueue` also owns the adaptive jitter
/// buffer: it grows its target on a real underrun and reports the counters
/// through `AudioMetrics`. Between the player and the mixer sits an
/// `AVAudioUnitTimePitch` (pitch 0): every 20 ms the queue's
/// `PlaybackRatePolicy` picks a rate between 0.85 and 1.05 from how much
/// audio is buffered, so a provider that delivers slower than real time
/// (GPT-Live) is stretched a little instead of running dry.
///
/// The session is `.playAndRecord` / `.voiceChat`, which turns on the
/// system's echo canceller: without it the dog hears itself out of the
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
    var onFrame: ((Data) -> Void)?
    /// The silence gate flipped: `true` when it closed.
    var onGate: ((Bool) -> Void)?
    /// Input level, 0…1, for the orb's ring.
    var onLevel: ((Float) -> Void)?
    /// Playback started or stopped (the dog's voice, for the orb).
    var onSpeakingChanged: ((Bool) -> Void)?
    /// A playback report for the daemon (`started`, `drained`, `flushed`;
    /// docs/ws-contract.md "Playback reports"), in the order they happened.
    var onPlaybackReport: ((PlaybackQueue.Report) -> Void)?
    /// An interruption (a call, Siri) or a route change the user should see.
    var onNotice: ((String) -> Void)?
    /// The microphone stopped (true) or resumed (false) delivering signal.
    var onMicSignal: ((Bool) -> Void)?
    /// iOS paused the audio and would not resume it (true), or it was
    /// resumed or stopped (false).
    var onPausedChanged: ((Bool) -> Void)?
    /// The engine stopped by itself: a route change it could not rebuild
    /// the capture chain for. The words say why.
    var onStopped: ((String) -> Void)?

    private(set) var metrics = AudioMetrics()
    private(set) var isRunning = false
    /// The capture-side switch `setMuted` throws: gates `captureOnMain` on
    /// the render thread and arms/disarms the watchdog. Not a second source
    /// of truth for "muted" — the view model's own `isMuted` (and what it
    /// tells the daemon) is that; nothing outside `AudioEngine` reads this.
    private var isMuted = false
    /// Debounces `onGate`: only the *transition* into "closed" is reported
    /// (from `setMuted` and `captureOnMain`). The view model reads
    /// `gateIsClosed` for the gate's state itself.
    private var isGateClosed = false
    /// Running, but paused after an interruption iOS would not let us
    /// resume from by ourselves: `resume()` is the way back.
    private(set) var needsResume = false

    private let engine = AVAudioEngine()
    private let player = AVAudioPlayerNode()
    /// Pitch-preserving time-stretch between the player and the mixer.
    private let timePitch = AVAudioUnitTimePitch()
    /// The 20 ms rate control loop; runs while the engine does.
    private var steerTimer: Timer?
    private static let steerInterval: TimeInterval = 0.02
    private let playbackFormat = AVAudioFormat(commonFormat: .pcmFormatFloat32, sampleRate: AudioContract.playbackRate, channels: 1, interleaved: false)!

    private var converter: PCMConverter?
    private var chunker = FrameChunker()
    private var gate = CaptureGate()
    private var watchdog = MicSignalWatchdog()
    private var queue = PlaybackQueue()
    private var wasSpeaking = false
    private var interrupted = false
    private var observers: [NSObjectProtocol] = []

    init() {
        // Attach only. The player is connected in `start()`, after the
        // `.playAndRecord` session is active and voice processing is on:
        // a graph wired against the launch-time session (`.soloAmbient`,
        // output only) played nothing once the session changed under it.
        engine.attach(player)
        engine.attach(timePitch)
        timePitch.pitch = 0
        timePitch.rate = 1
        observe()
    }

    deinit {
        for observer in observers { NotificationCenter.default.removeObserver(observer) }
    }

    // MARK: - Permission

    static func requestMicrophone() async -> Bool {
        if #available(iOS 17.0, *) {
            if AVAudioApplication.shared.recordPermission == .granted { return true }
            return await AVAudioApplication.requestRecordPermission()
        }
        return await withCheckedContinuation { continuation in
            AVAudioSession.sharedInstance().requestRecordPermission { continuation.resume(returning: $0) }
        }
    }

    // MARK: - Lifecycle

    /// Starts capture and playback. With the Info.plist `audio` background
    /// mode a running engine keeps the app alive with the screen locked;
    /// the view model stops it on backgrounding when Settings ▸ Keep
    /// listening with the screen locked is off.
    func start() throws {
        guard !isRunning else { return }
        try configureSession()

        let input = engine.inputNode
        enableVoiceProcessing()
        // A voice-processing unit carries its own input mute, and iOS 17 an
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
        connectPlayback()
        engine.mainMixerNode.outputVolume = 1
        let format = input.inputFormat(forBus: 0)
        guard format.channelCount > 0, format.sampleRate > 0 else {
            // The simulator does this while a route settles; so does a device
            // whose microphone another app holds.
            throw Failure.format("No microphone is available right now — check that nothing else is using it.")
        }
        let converter = try PCMConverter(inputFormat: format)
        self.converter = converter
        chunker.reset()
        gate.reset()
        isGateClosed = false
        metrics.hardwareSampleRate = format.sampleRate

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
        player.play()
        isRunning = true
        startSteering()
        restartedForSignal = false
        if !isMuted { watchdog.arm(at: CFAbsoluteTimeGetCurrent()) }
        recordVoiceProcessing()
        SheepdogLog.audio.info("audio started: input \(format.sampleRate, privacy: .public) Hz, \(format.channelCount, privacy: .public) ch, echo cancellation \(self.metrics.voiceProcessing.summary, privacy: .public), time-pitch latency \(self.timePitch.latency * 1000, privacy: .public) ms")
    }

    /// The echo canceller. `.voiceChat` alone does not cancel echo for
    /// AVAudioEngine; the voice-processing I/O unit does, and it has to be
    /// on before the engine starts. Enabling it on the input node turns
    /// the engine's I/O into one voice-processing unit, so the output node
    /// the player → time-pitch → mixer chain feeds is the canceller's
    /// reference: everything the dog says is played through it, nothing
    /// around it. Without it the dog hears itself from the speaker.
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
        metrics.outputRoute = status.outputRoute
        if status != metrics.voiceProcessing {
            metrics.voiceProcessing = status
            SheepdogLog.audio.info("echo cancellation: \(status.summary, privacy: .public)")
        }
    }

    func stop() {
        guard isRunning else { return }
        engine.inputNode.removeTap(onBus: 0)
        stopSteering()
        player.stop()
        engine.stop()
        queue.flush(now: CFAbsoluteTimeGetCurrent())
        deliverReports()
        applyRate(1)
        chunker.reset()
        gate.reset()
        converter?.reset()
        isRunning = false
        isGateClosed = false
        interrupted = false
        setNeedsResume(false)
        watchdog.disarm()
        reportSignal()
        setSpeaking(false)
        try? AVAudioSession.sharedInstance().setActive(false, options: .notifyOthersOnDeactivation)
    }

    func setMuted(_ muted: Bool) {
        isMuted = muted
        if muted {
            watchdog.disarm()
            reportSignal()
            // Muted means silence, not zeros: nothing leaves the mic at all,
            // and the gate starts its bookkeeping again on unmute.
            chunker.reset()
            gate.reset()
            converter?.reset()
            if isGateClosed {
                isGateClosed = false
                onGate?(false)
            }
            onLevel?(0)
        } else if AudioLifecycle.watchdogShouldRun(running: isRunning, muted: false, interrupted: interrupted || needsResume) {
            watchdog.arm(at: CFAbsoluteTimeGetCurrent())
            restartedForSignal = false
        }
    }

    // MARK: - The gate

    /// Hold-to-talk: `true` pressed, `false` released, `nil` open mic. The
    /// transition reaches the caller through `onGate` with the next frame.
    func setManualGate(_ manual: Bool?) {
        gate.manual = manual
    }

    var gateDiagnostics: CaptureGate.Diagnostics { gate.diagnostics }

    /// The gate's own state right now (after a start or a reset, before any
    /// frame has told the caller).
    var gateIsClosed: Bool { gate.isGated }

    // MARK: - No signal

    private var reportedNoSignal = false
    private var restartedForSignal = false

    var noSignal: Bool { watchdog.noSignal }

    private func reportSignal() {
        guard watchdog.noSignal != reportedNoSignal else { return }
        reportedNoSignal = watchdog.noSignal
        onMicSignal?(watchdog.noSignal)
    }

    private func signalChanged(_ noSignal: Bool) {
        reportSignal()
        guard noSignal else {
            SheepdogLog.audio.info("microphone signal is back")
            return
        }
        let input = engine.inputNode
        let format = input.inputFormat(forBus: 0)
        SheepdogLog.audio.error("no microphone signal for \(self.watchdog.threshold, privacy: .public) s: input \(format.sampleRate, privacy: .public) Hz \(format.channelCount, privacy: .public) ch, voice processing \(input.isVoiceProcessingEnabled, privacy: .public), vp input muted \(input.isVoiceProcessingInputMuted, privacy: .public), app input muted \(AVAudioApplication.shared.isInputMuted, privacy: .public), route \(AVAudioSession.sharedInstance().currentRoute.inputs.map(\.portName).joined(separator: ","), privacy: .public)")
        // One automatic restart per arm: a capture chain that came up
        // silent usually comes back when it is rebuilt.
        if !restartedForSignal {
            restartedForSignal = true
            if input.isVoiceProcessingEnabled, input.isVoiceProcessingInputMuted { input.isVoiceProcessingInputMuted = false }
            // The watchdog stays tripped until a real sample arrives, so the
            // screen does not flicker back to "listening" on a guess.
            restartCapture()
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
        } catch {
            throw Failure.sessionFailed(error.localizedDescription)
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
        guard isRunning, !isMuted, let converter else { return }
        let started = CFAbsoluteTimeGetCurrent()
        let samples: [Int16]
        do {
            samples = try converter.convert(buffer)
        } catch {
            SheepdogLog.audio.error("convert failed: \(String(describing: error), privacy: .public)")
            return
        }
        guard !samples.isEmpty else { return }
        if let change = watchdog.observe(peak: PCMLevel.peak(of: samples), at: CFAbsoluteTimeGetCurrent()) {
            signalChanged(change)
        }

        var level: Float = 0
        for frame in chunker.push(samples) {
            let peak = PCMLevel.peak(of: frame)
            level = max(level, Float(peak) / 32_768)
            let out = gate.push(frame)
            if let transition = out.transition {
                let closed = transition == .closed
                isGateClosed = closed
                onGate?(closed)
            }
            for send in out.send {
                onFrame?(send.pcm16LEData)
            }
        }
        metrics.gatedFrames = gate.gatedFrames
        metrics.recordCapture(latency: CFAbsoluteTimeGetCurrent() - started, frames: 1)
        onLevel?(level)
    }

    var inputPeak: Int { gate.peak }
    var gatedFrames: Int { gate.gatedFrames }

    // MARK: - Playback

    /// One binary frame from the daemon: PCM16 LE mono at 24 kHz.
    func enqueue(_ data: Data) {
        let samples = [Int16](pcm16LE: data)
        guard !samples.isEmpty else { return }
        schedule(queue.push(samples, now: CFAbsoluteTimeGetCurrent()))
        deliverReports()
    }

    /// Hands the queue's playback reports to the caller, oldest first.
    /// Called after every queue mutation that can produce one.
    private func deliverReports() {
        for report in queue.takeReports() {
            onPlaybackReport?(report)
        }
    }

    /// What the `ping` says about the engine (the browser sends its
    /// AudioContext state): `running`, `interrupted` (iOS holds the audio)
    /// or `suspended` (stopped: voice closed, backgrounded, or not started).
    var audioState: String {
        guard isRunning else { return "suspended" }
        return interrupted || needsResume ? "interrupted" : "running"
    }

    /// The watchdog and the playback metrics, on the UI timer's cadence
    /// (100 ms while audio is moving, 1 s idle). Releasing a short
    /// utterance that never filled the jitter buffer (`queue.tick`) is
    /// `steer()`'s job while the engine runs, at its finer 20 ms cadence;
    /// this only calls it itself when the engine is *not* running, since
    /// `enqueue` has no `isRunning` guard — audio can arrive (and need
    /// releasing) before `start()` — and that is the one case `steer()`
    /// never sees.
    func tick() {
        if AudioLifecycle.watchdogShouldRun(running: isRunning, muted: isMuted, interrupted: interrupted || needsResume),
           let change = watchdog.tick(at: CFAbsoluteTimeGetCurrent()) {
            signalChanged(change)
        }
        let now = CFAbsoluteTimeGetCurrent()
        if !isRunning {
            schedule(queue.tick(now: now))
            deliverReports()
        }
        metrics.recordPlayback(depthMilliseconds: queue.queueDepthMilliseconds)
        metrics.recordTimeStretch(rate: queue.rate, stretchedSeconds: queue.stretchedSeconds, bufferedMilliseconds: queue.bufferedMilliseconds(at: now))
        recordJitterBufferMetrics()
        setSpeaking(queue.isSpeaking)
    }

    /// `flush` from the daemon, or the Stop button: drop everything queued,
    /// now. Reports `flushed` every time, even with nothing playing.
    func flushPlayback() {
        let hadAudio = queue.flush(now: CFAbsoluteTimeGetCurrent())
        deliverReports()
        if hadAudio {
            player.stop()
            if isRunning { player.play() }
        }
        applyRate(1)
        setSpeaking(false)
    }

    /// The voice provider changed (from `/ws` `status.provider`): switch
    /// the jitter-buffer target range. A no-op when it is already right.
    func setProvider(_ provider: String?) {
        let profile = PlaybackProfile.forProvider(provider)
        guard profile != queue.profile else { return }
        queue.setProfile(profile)
        SheepdogLog.audio.info("playback profile for \(provider ?? "default", privacy: .public): target \(profile.floorMilliseconds, privacy: .public)–\(profile.ceilingMilliseconds, privacy: .public) ms")
        recordJitterBufferMetrics()
    }

    // MARK: - Time-stretch

    /// player → time-pitch → mixer, both edges at the playback format.
    private func connectPlayback() {
        engine.disconnectNodeOutput(player)
        engine.disconnectNodeOutput(timePitch)
        engine.connect(player, to: timePitch, format: playbackFormat)
        engine.connect(timePitch, to: engine.mainMixerNode, format: playbackFormat)
    }

    private func startSteering() {
        steerTimer?.invalidate()
        let timer = Timer(timeInterval: Self.steerInterval, repeats: true) { [weak self] _ in
            MainActor.assumeIsolated { self?.steer() }
        }
        RunLoop.main.add(timer, forMode: .common)
        steerTimer = timer
    }

    private func stopSteering() {
        steerTimer?.invalidate()
        steerTimer = nil
    }

    /// One control-loop step, every 20 ms while the engine runs: release
    /// anything due (`PlaybackQueue.tick`, which also notices a drain when
    /// nothing has arrived), then move the rate one slew step and hand it
    /// to the time-pitch unit. The one exception — audio that arrived
    /// before the engine started — is `tick()`'s job; see its doc comment.
    private func steer() {
        let now = CFAbsoluteTimeGetCurrent()
        schedule(queue.tick(now: now))
        applyRate(queue.steer(now: now))
        deliverReports()
    }

    private func applyRate(_ rate: Double) {
        let value = Float(rate)
        if timePitch.rate != value { timePitch.rate = value }
    }

    private func schedule(_ chunks: [PlaybackQueue.Chunk]) {
        guard !chunks.isEmpty else { return }
        for chunk in chunks {
            guard let buffer = AudioEngine.buffer(chunk.samples, format: playbackFormat) else { continue }
            metrics.scheduledChunks += 1
            // No `at:`: each buffer starts where the last one ended, which is
            // what "gapless" means here.
            player.scheduleBuffer(buffer, completionCallbackType: .dataPlayedBack) { [weak self] _ in
                Task { @MainActor in
                    guard let self else { return }
                    self.queue.completed(chunk.id)
                    self.deliverReports()
                    self.metrics.playedChunks += 1
                    self.setSpeaking(self.queue.isSpeaking)
                }
            }
        }
        if isRunning, !player.isPlaying { player.play() }
        metrics.recordPlayback(depthMilliseconds: queue.queueDepthMilliseconds)
        recordJitterBufferMetrics()
        setSpeaking(queue.isSpeaking)
    }

    private func recordJitterBufferMetrics() {
        metrics.recordJitterBuffer(
            underruns: queue.underruns,
            targetMilliseconds: queue.targetMilliseconds,
            drains: queue.drains,
            largestArrivalGapMilliseconds: queue.largestArrivalGapMilliseconds
        )
    }

    private func setSpeaking(_ speaking: Bool) {
        guard speaking != wasSpeaking else { return }
        wasSpeaking = speaking
        onSpeakingChanged?(speaking)
    }

    private static func buffer(_ samples: [Int16], format: AVAudioFormat) -> AVAudioPCMBuffer? {
        guard let buffer = AVAudioPCMBuffer(pcmFormat: format, frameCapacity: AVAudioFrameCount(samples.count)),
              let channel = buffer.floatChannelData else { return nil }
        buffer.frameLength = AVAudioFrameCount(samples.count)
        for (index, sample) in samples.enumerated() {
            channel[0][index] = Float(sample) / 32_768
        }
        return buffer
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
            MainActor.assumeIsolated { self?.onNotice?("Audio was reset by iOS — reconnect to start listening again.") }
        })
    }

    private func handleInterruption(_ note: Notification) {
        guard let raw = note.userInfo?[AVAudioSessionInterruptionTypeKey] as? UInt,
              let type = AVAudioSession.InterruptionType(rawValue: raw) else { return }
        switch type {
        case .began:
            interrupted = true
            engine.pause()
            flushPlayback()
            // Nothing can arrive while iOS holds the audio: silence now is
            // not a broken microphone.
            watchdog.disarm()
            reportSignal()
            onNotice?("Paused for a call or Siri.")
        case .ended:
            guard interrupted else { return }
            interrupted = false
            let options = (note.userInfo?[AVAudioSessionInterruptionOptionKey] as? UInt).map(AVAudioSession.InterruptionOptions.init)
            guard options?.contains(.shouldResume) ?? true else {
                // Still "running" but paused: say so, and let the orb take
                // it back.
                setNeedsResume(true)
                onNotice?("iOS kept the audio for something else. Tap the orb to resume.")
                return
            }
            resumeAfterInterruption()
        @unknown default:
            break
        }
    }

    private func handleRouteChange(_ note: Notification) {
        guard isRunning,
              let raw = note.userInfo?[AVAudioSessionRouteChangeReasonKey] as? UInt,
              let reason = AVAudioSession.RouteChangeReason(rawValue: raw) else { return }
        switch reason {
        case .oldDeviceUnavailable, .newDeviceAvailable, .override, .categoryChange:
            // Headphones in or out: the hardware format can change under the
            // tap, so rebuild the capture chain rather than convert from a
            // rate that no longer exists.
            restartCapture()
            let output = AVAudioSession.sharedInstance().currentRoute.outputs.first?.portName
            onNotice?(output.map { "Audio moved to \($0)." } ?? "The audio route changed.")
        default:
            break
        }
    }

    private func resumeAfterInterruption() {
        do {
            try reactivate()
            onNotice?("Listening again.")
        } catch {
            setNeedsResume(true)
            onNotice?("Could not pick the audio back up (\(error.localizedDescription)). Tap the orb to try again.")
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
        try engine.start()
        player.play()
        setNeedsResume(false)
        if AudioLifecycle.watchdogShouldRun(running: isRunning, muted: isMuted, interrupted: false) {
            watchdog.arm(at: CFAbsoluteTimeGetCurrent())
            restartedForSignal = false
        }
    }

    private func setNeedsResume(_ paused: Bool) {
        guard paused != needsResume else { return }
        needsResume = paused
        onPausedChanged?(paused)
    }

    private func restartCapture() {
        guard isRunning else { return }
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
        guard format.channelCount > 0, format.sampleRate > 0, let converter = try? PCMConverter(inputFormat: format) else {
            isRunning = false
            stopSteering()
            onStopped?("The microphone went away with the route change.")
            return
        }
        self.converter = converter
        metrics.hardwareSampleRate = format.sampleRate
        chunker.reset()
        gate.reset()
        input.installTap(onBus: 0, bufferSize: 4_096, format: format) { [weak self] buffer, _ in
            self?.handleCaptured(buffer)
        }
        connectPlayback()
        do {
            try engine.start()
            player.play()
            recordVoiceProcessing()
        } catch {
            isRunning = false
            stopSteering()
            onStopped?("Audio stopped after the route change (\(error.localizedDescription)).")
        }
    }
}
