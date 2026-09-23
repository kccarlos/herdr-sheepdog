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
/// through `AudioMetrics`.
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
    /// Playback started or stopped.
    var onSpeakingChanged: ((Bool) -> Void)?
    /// An interruption (a call, Siri) or a route change the user should see.
    var onNotice: ((String) -> Void)?
    /// The microphone stopped (true) or resumed (false) delivering signal.
    var onMicSignal: ((Bool) -> Void)?

    private(set) var metrics = AudioMetrics()
    private(set) var isRunning = false
    private(set) var isMuted = false
    private(set) var isGateClosed = false

    private let engine = AVAudioEngine()
    private let player = AVAudioPlayerNode()
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

    /// Starts capture and playback. `background` keeps the session alive
    /// with the screen locked (the Info.plist `audio` background mode plus
    /// the Settings toggle).
    func start(background: Bool) throws {
        guard !isRunning else { return }
        try configureSession(background: background)

        let input = engine.inputNode
        // `.voiceChat` alone does not cancel echo for AVAudioEngine; the
        // voice-processing I/O unit does, and it has to be on before the
        // engine starts. Without it the dog hears itself from the speaker.
        if !input.isVoiceProcessingEnabled {
            do {
                try input.setVoiceProcessingEnabled(true)
            } catch {
                SheepdogLog.audio.error("voice processing unavailable: \(error.localizedDescription, privacy: .public)")
            }
        }
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
        engine.disconnectNodeOutput(player)
        engine.connect(player, to: engine.mainMixerNode, format: playbackFormat)
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
        restartedForSignal = false
        if !isMuted { watchdog.arm(at: CFAbsoluteTimeGetCurrent()) }
        let route = AVAudioSession.sharedInstance().currentRoute.outputs.map(\.portName).joined(separator: ", ")
        metrics.outputRoute = route
        SheepdogLog.audio.info("audio started: input \(format.sampleRate, privacy: .public) Hz, \(format.channelCount, privacy: .public) ch, output \(route, privacy: .public), voice processing \(input.isVoiceProcessingEnabled, privacy: .public)")
    }

    func stop() {
        guard isRunning else { return }
        engine.inputNode.removeTap(onBus: 0)
        player.stop()
        engine.stop()
        queue.flush()
        chunker.reset()
        gate.reset()
        converter?.reset()
        isRunning = false
        isGateClosed = false
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
        } else if isRunning {
            watchdog.arm(at: CFAbsoluteTimeGetCurrent())
            restartedForSignal = false
        }
    }

    // MARK: - The gate

    /// Which detector decides (Settings ▸ Diagnostics).
    func setDetector(_ detector: CaptureGate.Detector) {
        gate.detector = detector
    }

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

    private func configureSession(background: Bool) throws {
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
            _ = background
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
        if let change = watchdog.observe(peak: SilenceGate.peak(of: samples), at: CFAbsoluteTimeGetCurrent()) {
            signalChanged(change)
        }

        var level: Float = 0
        for frame in chunker.push(samples) {
            let peak = SilenceGate.peak(of: frame)
            level = max(level, Float(peak) / 32_768)
            let out = gate.push(frame)
            if let transition = out.transition {
                let closed = transition == .closed
                isGateClosed = closed
                metrics.silenceTransitions += 1
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
    }

    /// Releases a short utterance that never filled the jitter buffer. Call
    /// from the UI timer.
    func tick() {
        if isRunning, !isMuted, let change = watchdog.tick(at: CFAbsoluteTimeGetCurrent()) {
            signalChanged(change)
        }
        schedule(queue.tick(now: CFAbsoluteTimeGetCurrent()))
        metrics.recordPlayback(depthMilliseconds: queue.queueDepthMilliseconds)
        recordJitterBufferMetrics()
        setSpeaking(queue.isSpeaking)
    }

    /// `flush` from the daemon, or the Stop button: drop everything queued,
    /// now.
    func flushPlayback() {
        if queue.flush() {
            player.stop()
            if isRunning { player.play() }
        }
        setSpeaking(false)
    }

    var queueDepthMilliseconds: Int { queue.queueDepthMilliseconds }

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
            onNotice?("Paused for a call or Siri.")
        case .ended:
            guard interrupted else { return }
            interrupted = false
            let options = (note.userInfo?[AVAudioSessionInterruptionOptionKey] as? UInt).map(AVAudioSession.InterruptionOptions.init)
            guard options?.contains(.shouldResume) ?? true else {
                onNotice?("iOS kept the audio for something else. Tap Connect to take it back.")
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
            try AVAudioSession.sharedInstance().setActive(true, options: [])
            try engine.start()
            player.play()
            onNotice?("Listening again.")
        } catch {
            onNotice?("Could not pick the audio back up (\(error.localizedDescription)).")
        }
    }

    private func restartCapture() {
        guard isRunning else { return }
        engine.inputNode.removeTap(onBus: 0)
        engine.stop()
        let input = engine.inputNode
        let format = input.inputFormat(forBus: 0)
        guard format.channelCount > 0, format.sampleRate > 0, let converter = try? PCMConverter(inputFormat: format) else {
            isRunning = false
            onNotice?("The microphone went away with the route change.")
            return
        }
        self.converter = converter
        metrics.hardwareSampleRate = format.sampleRate
        chunker.reset()
        gate.reset()
        input.installTap(onBus: 0, bufferSize: 4_096, format: format) { [weak self] buffer, _ in
            self?.handleCaptured(buffer)
        }
        engine.disconnectNodeOutput(player)
        engine.connect(player, to: engine.mainMixerNode, format: playbackFormat)
        do {
            try engine.start()
            player.play()
        } catch {
            isRunning = false
            onNotice?("Audio stopped after the route change (\(error.localizedDescription)).")
        }
    }
}
