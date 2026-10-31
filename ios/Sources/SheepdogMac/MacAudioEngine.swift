import AVFoundation
import Foundation
import SheepdogKit

/// The microphone and the speaker on the Mac: the counterpart of the
/// phone's `AudioEngine` (Sources/Sheepdog/AudioEngine.swift), on the same
/// Kit pieces.
///
/// Capture: an `AVAudioEngine` input tap → `MicMixdown` (one mono channel,
/// whatever the voice-processing input delivers) → `PCMConverter` (16 kHz
/// mono Int16) → `FrameChunker` (40 ms frames) → `CaptureGate` (the
/// voice-activity detector, or hold-to-talk) → the caller, which writes each
/// frame to `/ws`. A `MicSignalWatchdog` watches for a microphone that
/// delivers only digital zero while unmuted.
///
/// Playback: one `AVAudioPlayerNode` fed 24 kHz chunks back to back through
/// `PlaybackQueue`, with the same adaptive jitter buffer and the
/// `AVAudioUnitTimePitch` rate control as the phone.
///
/// Echo cancellation is the voice-processing I/O unit on the input node, on
/// before the engine starts; the output node the player feeds is its
/// reference. There is no `AVAudioSession` on the Mac: a route change (the
/// AirPods' headset microphone, a display's speakers) arrives as
/// `AVAudioEngineConfigurationChange`, which stops the engine, and the
/// engine is rebuilt (`restartCapture`).
@MainActor
final class MacAudioEngine: VoiceAudio {
    enum Failure: LocalizedError {
        case engineFailed(String)
        case format(String)

        var errorDescription: String? {
            switch self {
            case let .engineFailed(m): return "The audio engine would not start (\(m))."
            case let .format(m): return m
            }
        }
    }

    var onFrame: ((Data) -> Void)?
    var onGate: ((Bool) -> Void)?
    var onSpeakingChanged: ((Bool) -> Void)?
    var onPlaybackReport: ((PlaybackQueue.Report) -> Void)?
    var onDrain: ((PlaybackQueue.DrainRecord) -> Void)?
    var onNotice: ((LocalNotice) -> Void)?
    var onMicSignal: ((Bool) -> Void)?
    var onStopped: ((String) -> Void)?

    private(set) var metrics = AudioMetrics()
    private(set) var isRunning = false
    /// The capture-side switch `setMuted` throws (see `AudioEngine`): not a
    /// second source of truth for "muted", which is the session core's.
    private var isMuted = false
    /// Debounces `onGate`: only the transition into "closed" is reported.
    private var isGateClosed = false

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
    private var observer: NSObjectProtocol?
    private let log: AppLogging

    init(log: AppLogging) {
        self.log = log
        // Attach only; the player is connected in `start()`, after voice
        // processing is on (the phone's reason, and the same here: a graph
        // wired before the I/O unit changed can play nothing).
        engine.attach(player)
        engine.attach(timePitch)
        timePitch.pitch = 0
        timePitch.rate = 1
        // The engine stops itself when the hardware format changes under it.
        observer = NotificationCenter.default.addObserver(forName: .AVAudioEngineConfigurationChange, object: engine, queue: .main) { [weak self] _ in
            MainActor.assumeIsolated { self?.configurationChanged() }
        }
    }

    deinit {
        if let observer { NotificationCenter.default.removeObserver(observer) }
    }

    // MARK: - Lifecycle

    func start() throws {
        guard !isRunning else { return }
        let input = engine.inputNode
        enableVoiceProcessing()
        // A voice-processing unit carries its own input mute; left on it
        // gives unmuted in the app and digital zero from the tap.
        if input.isVoiceProcessingEnabled, input.isVoiceProcessingInputMuted {
            log.log("voice processing input was muted; unmuting")
            input.isVoiceProcessingInputMuted = false
        }
        connectPlayback()
        engine.mainMixerNode.outputVolume = 1
        let format = input.outputFormat(forBus: 0)
        guard format.channelCount > 0, format.sampleRate > 0 else {
            throw Failure.format("No microphone is available right now — check that nothing else is using it.")
        }
        resetCapture()
        isGateClosed = false
        metrics.hardwareSampleRate = format.sampleRate

        // 4 096 frames is a hint; the system hands over what it likes, which
        // is why the chunker exists.
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
        if !isMuted, gate.manual != false { watchdog.arm(at: CFAbsoluteTimeGetCurrent()) }
        recordVoiceProcessing()
        log.log("audio started: input \(format.sampleRate) Hz, \(format.channelCount) ch, echo cancellation \(metrics.voiceProcessing.summary), time-pitch latency \(timePitch.latency * 1000) ms")
    }

    /// The echo canceller: the voice-processing I/O unit, on before the
    /// engine starts. Called with the engine stopped (start, and the rebuild
    /// after a configuration change, which can come back without it).
    private func enableVoiceProcessing() {
        let input = engine.inputNode
        if !input.isVoiceProcessingEnabled {
            do {
                try input.setVoiceProcessingEnabled(true)
            } catch {
                log.log("voice processing unavailable: \(error.localizedDescription)")
            }
        }
        if input.isVoiceProcessingEnabled, input.isVoiceProcessingBypassed {
            log.log("voice processing was bypassed; turning it back on")
            input.isVoiceProcessingBypassed = false
        }
    }

    private func recordVoiceProcessing() {
        // No session mode on macOS: voice processing is the whole of it.
        let status = VoiceProcessingStatus(
            inputEnabled: engine.inputNode.isVoiceProcessingEnabled,
            outputEnabled: engine.outputNode.isVoiceProcessingEnabled,
            bypassed: engine.inputNode.isVoiceProcessingBypassed,
            voiceChatMode: engine.inputNode.isVoiceProcessingEnabled
        )
        if status != metrics.voiceProcessing {
            metrics.voiceProcessing = status
            log.log("echo cancellation: \(status.summary)")
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
        resetCapture()
        isRunning = false
        isGateClosed = false
        watchdog.disarm()
        reportSignal()
        setSpeaking(false)
    }

    func setMuted(_ muted: Bool) {
        isMuted = muted
        if muted {
            watchdog.disarm()
            reportSignal()
            // Muted means silence, not zeros: nothing leaves the mic at all,
            // and the gate starts its bookkeeping again on unmute.
            resetCapture()
            if isGateClosed {
                isGateClosed = false
                onGate?(false)
            }
        } else if AudioLifecycle.watchdogShouldRun(running: isRunning, muted: false, interrupted: false, gateClosed: gate.manual == false) {
            watchdog.arm(at: CFAbsoluteTimeGetCurrent())
            restartedForSignal = false
        }
    }

    private func resetCapture() {
        chunker.reset()
        gate.reset()
        converter = nil
    }

    // MARK: - The gate

    /// Hold-to-talk: `true` pressed, `false` released, `nil` open mic.
    func setManualGate(_ manual: Bool?) {
        let wasClosed = gate.manual == false
        gate.manual = manual
        if manual == false {
            watchdog.disarm()
            reportSignal()
        } else if wasClosed,
                  AudioLifecycle.watchdogShouldRun(running: isRunning, muted: isMuted, interrupted: false, gateClosed: false) {
            watchdog.arm(at: CFAbsoluteTimeGetCurrent())
            restartedForSignal = false
        }
    }

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
            log.log("microphone signal is back")
            return
        }
        let input = engine.inputNode
        let format = input.outputFormat(forBus: 0)
        log.log("no microphone signal for \(watchdog.threshold) s: input \(format.sampleRate) Hz \(format.channelCount) ch, voice processing \(input.isVoiceProcessingEnabled), vp input muted \(input.isVoiceProcessingInputMuted)")
        // One automatic restart per arm: a capture chain that came up silent
        // usually comes back when it is rebuilt.
        if !restartedForSignal {
            restartedForSignal = true
            if input.isVoiceProcessingEnabled, input.isVoiceProcessingInputMuted { input.isVoiceProcessingInputMuted = false }
            restartCapture(routeChanged: false)
        }
    }

    // MARK: - Capture

    /// Called on the audio render thread. The buffer is only valid for the
    /// length of the callback, so it is mixed down into a mono copy and
    /// handed to the main actor, which owns the converter, the chunker and
    /// the gate. ~40 ms of float samples, 25 times a second, is nothing next
    /// to keeping the render thread lock-free.
    private nonisolated func handleCaptured(_ buffer: AVAudioPCMBuffer) {
        guard let mono = MicMixdown.mono(buffer) else { return }
        DispatchQueue.main.async { [weak self] in
            self?.captureOnMain(mono)
        }
    }

    /// One mono buffer from the tap. The converter follows the buffer's rate,
    /// so a route that changed it needs no special case here.
    private func captureOnMain(_ buffer: AVAudioPCMBuffer) {
        guard isRunning, !isMuted else { return }
        let started = CFAbsoluteTimeGetCurrent()
        let samples: [Int16]
        do {
            if converter?.inputFormat.sampleRate != buffer.format.sampleRate {
                converter = try PCMConverter(inputFormat: buffer.format)
            }
            samples = try converter?.convert(buffer) ?? []
        } catch {
            log.log("convert failed: \(String(describing: error))")
            return
        }
        guard !samples.isEmpty else { return }
        if let change = watchdog.observe(peak: PCMLevel.peak(of: samples), at: CFAbsoluteTimeGetCurrent()) {
            signalChanged(change)
        }

        for frame in chunker.push(samples) {
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
    private func deliverReports() {
        for report in queue.takeReports() {
            onPlaybackReport?(report)
        }
        for drain in queue.takeDrains() {
            onDrain?(drain)
        }
    }

    /// What the `ping` says about the engine: `running` or `suspended`.
    var audioState: String { isRunning ? "running" : "suspended" }

    /// The watchdog and the playback metrics, on the host timer's cadence.
    /// Releasing a short utterance that never filled the jitter buffer is
    /// `steer()`'s job while the engine runs; this only does it when the
    /// engine is not running (audio can arrive before `start()`).
    func tick() {
        if AudioLifecycle.watchdogShouldRun(running: isRunning, muted: isMuted, interrupted: false, gateClosed: gate.manual == false),
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

    /// `flush` from the daemon, a `voice-command` stop or the Stop item:
    /// drop everything queued, now. Reports `flushed` every time.
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

    /// The voice provider changed (from `/ws` `status.provider`): switch the
    /// jitter-buffer target range. A no-op when it is already right.
    func setProvider(_ provider: String?) {
        let profile = PlaybackProfile.forProvider(provider)
        guard profile != queue.profile else { return }
        queue.setProfile(profile)
        log.log("playback profile for \(provider ?? "default"): target \(profile.floorMilliseconds)–\(profile.ceilingMilliseconds) ms")
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
    /// anything due, then move the rate one slew step.
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
            guard let buffer = Self.buffer(chunk.samples, format: playbackFormat) else { continue }
            metrics.scheduledChunks += 1
            // No `at:`: each buffer starts where the last one ended, which is
            // what "gapless" means here.
            player.scheduleBuffer(buffer, completionCallbackType: .dataPlayedBack) { [weak self] _ in
                Task { @MainActor in
                    guard let self else { return }
                    self.queue.completed(chunk.id, now: CFAbsoluteTimeGetCurrent())
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

    // MARK: - Route changes

    /// The engine stopped under us: the hardware format changed (headphones
    /// or a headset microphone came or went, the sample rate moved). Say so
    /// and rebuild.
    private func configurationChanged() {
        guard isRunning else { return }
        restartCapture(routeChanged: true)
    }

    /// Rebuilds the capture chain on the new hardware format, with voice
    /// processing back on (a rebuilt I/O unit can come up without it), and
    /// starts the engine again. A failure ends the engine (`onStopped`); the
    /// session core starts it once more.
    private func restartCapture(routeChanged: Bool) {
        guard isRunning else { return }
        engine.inputNode.removeTap(onBus: 0)
        engine.stop()
        enableVoiceProcessing()
        let input = engine.inputNode
        let format = input.outputFormat(forBus: 0)
        guard format.channelCount > 0, format.sampleRate > 0 else {
            isRunning = false
            stopSteering()
            onStopped?(LocalNotice.microphoneWentAway)
            return
        }
        metrics.hardwareSampleRate = format.sampleRate
        resetCapture()
        input.installTap(onBus: 0, bufferSize: 4_096, format: format) { [weak self] buffer, _ in
            self?.handleCaptured(buffer)
        }
        connectPlayback()
        do {
            try engine.start()
            player.play()
            recordVoiceProcessing()
            if routeChanged { onNotice?(.routeChanged(output: nil)) }
        } catch {
            isRunning = false
            stopSteering()
            onStopped?(LocalNotice.audioStoppedAfterRouteChange(error.localizedDescription))
        }
    }
}
