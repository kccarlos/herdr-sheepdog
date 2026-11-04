import AVFoundation
import Foundation
import SheepdogKit

/// The microphone and the speaker on the Mac: the counterpart of the
/// phone's `AudioEngine` (Sources/Sheepdog/AudioEngine.swift), on the same
/// `AudioCapture` and `AudioPlayback` pipelines in `SheepdogKit`.
///
/// Capture: an `AVAudioEngine` input tap → `MicMixdown` (one mono channel,
/// whatever the voice-processing input delivers) → `AudioCapture`
/// (`PCMConverter`, `FrameChunker`, `CaptureGate` and `MicSignalWatchdog`) →
/// the caller, which writes each 40 ms frame to `/ws`.
///
/// Playback: `AudioPlayback` (`AVAudioPlayerNode` → `AVAudioUnitTimePitch` →
/// `mainMixerNode`), plus the macOS mixer → output link at the input's sample
/// rate that `AUVPAggregate` requires.
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

    var onFrame: ((Data) -> Void)? {
        get { capture.onFrame }
        set { capture.onFrame = newValue }
    }
    var onGate: ((Bool) -> Void)? {
        get { capture.onGate }
        set { capture.onGate = newValue }
    }
    var onSpeakingChanged: ((Bool) -> Void)? {
        get { playback.onSpeakingChanged }
        set { playback.onSpeakingChanged = newValue }
    }
    var onPlaybackReport: ((PlaybackQueue.Report) -> Void)? {
        get { playback.onPlaybackReport }
        set { playback.onPlaybackReport = newValue }
    }
    var onDrain: ((PlaybackQueue.DrainRecord) -> Void)? {
        get { playback.onDrain }
        set { playback.onDrain = newValue }
    }
    var onNotice: ((LocalNotice) -> Void)?
    var onMicSignal: ((Bool) -> Void)? {
        get { capture.onMicSignal }
        set { capture.onMicSignal = newValue }
    }
    var onStopped: ((String) -> Void)?
    var onAvailable: (() -> Void)?

    var metrics: AudioMetrics {
        var combined = engineMetrics
        playback.apply(to: &combined)
        return combined
    }
    private var engineMetrics = AudioMetrics()
    private(set) var isRunning = false
    /// The engine stopped by itself (`onStopped`) and has not started since.
    private var stoppedByItself = false

    private let engine = AVAudioEngine()
    private let capture = AudioCapture()
    private let playback = AudioPlayback()
    private var observer: NSObjectProtocol?
    private let log: AppLogging

    init(log: AppLogging) {
        self.log = log
        // Attach only; the player is connected in `start()`, after voice
        // processing is on (the phone's reason, and the same here: a graph
        // wired before the I/O unit changed can play nothing).
        playback.attach(to: engine)
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
            throw Failure.format(AudioCapture.noMicrophoneMessage)
        }
        capture.reset()
        engineMetrics.hardwareSampleRate = format.sampleRate

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
        playback.start()
        isRunning = true
        stoppedByItself = false
        capture.didStart()
        recordVoiceProcessing()
        log.log("audio started: input \(format.sampleRate) Hz, \(format.channelCount) ch, echo cancellation \(engineMetrics.voiceProcessing.summary), time-pitch latency \(playback.timePitchLatency * 1000) ms")
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
        if status != engineMetrics.voiceProcessing {
            engineMetrics.voiceProcessing = status
            log.log("echo cancellation: \(status.summary)")
        }
    }

    func stop() {
        guard isRunning else { return }
        isRunning = false
        engine.inputNode.removeTap(onBus: 0)
        playback.stop()
        engine.stop()
        capture.stop()
    }

    func setMuted(_ muted: Bool) {
        capture.setMuted(muted, running: isRunning)
    }

    // MARK: - The gate

    /// Hold-to-talk: `true` pressed, `false` released, `nil` open mic.
    func setManualGate(_ manual: Bool?) {
        capture.setManualGate(manual, running: isRunning)
    }

    var gateIsClosed: Bool { capture.gateIsClosed }

    // MARK: - No signal

    var noSignal: Bool { capture.noSignal }

    private func signalChanged(_ change: AudioCapture.SignalChange) {
        switch change {
        case .recovered:
            log.log("microphone signal is back")
        case let .lost(restart):
            let input = engine.inputNode
            let format = input.outputFormat(forBus: 0)
            log.log("no microphone signal for \(capture.watchdogThreshold) s: input \(format.sampleRate) Hz \(format.channelCount) ch, voice processing \(input.isVoiceProcessingEnabled), vp input muted \(input.isVoiceProcessingInputMuted)")
            // One automatic restart per arm: a capture chain that came up silent
            // usually comes back when it is rebuilt.
            if restart {
                if input.isVoiceProcessingEnabled, input.isVoiceProcessingInputMuted { input.isVoiceProcessingInputMuted = false }
                restartCapture(routeChanged: false)
            }
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
        guard isRunning else { return }
        do {
            if let change = try capture.process(buffer, metrics: &engineMetrics) {
                signalChanged(change)
            }
        } catch {
            log.log("convert failed: \(String(describing: error))")
        }
    }

    var inputPeak: Int { capture.inputPeak }
    var gatedFrames: Int { capture.gatedFrames }

    // MARK: - Playback

    /// One binary frame from the daemon: PCM16 LE mono at 24 kHz.
    func enqueue(_ data: Data) {
        playback.enqueue(data)
    }

    /// What the `ping` says about the engine: `running` or `suspended`.
    var audioState: String { isRunning ? "running" : "suspended" }

    /// The watchdog and the playback metrics, on the host timer's cadence.
    func tick() {
        if let change = capture.tickWatchdog(running: isRunning) {
            signalChanged(change)
        }
        playback.tick()
    }

    /// `flush` from the daemon, a `voice-command` stop or the Stop item:
    /// drop everything queued, now. Reports `flushed` every time.
    func flushPlayback() {
        playback.flush()
    }

    /// The voice provider changed (from `/ws` `status.provider`): switch the
    /// jitter-buffer target range. A no-op when it is already right.
    func setProvider(_ provider: String?) {
        guard let profile = playback.setProvider(provider) else { return }
        log.log("playback profile for \(provider ?? "default"): target \(profile.floorMilliseconds)–\(profile.ceilingMilliseconds) ms")
    }

    /// player → time-pitch → mixer at the playback format (`AudioPlayback`),
    /// and mixer → output at the input's sample rate. The voice-processing
    /// unit on the Mac will not initialize unless its two client sides share a
    /// rate (`AUVPAggregate`: "client-side input and output formats do not
    /// match", -10875), and the mixer's implicit link to the output can come
    /// up at 44.1 kHz against a 48 kHz microphone. Called after voice
    /// processing is on, so the input format is the one the unit delivers.
    private func connectPlayback() {
        let inputRate = engine.inputNode.outputFormat(forBus: 0).sampleRate
        let outputChannels = engine.outputNode.inputFormat(forBus: 0).channelCount
        playback.connect(in: engine)
        if inputRate > 0,
           let mixed = AVAudioFormat(standardFormatWithSampleRate: inputRate, channels: outputChannels > 0 ? outputChannels : 2) {
            engine.disconnectNodeOutput(engine.mainMixerNode)
            engine.connect(engine.mainMixerNode, to: engine.outputNode, format: mixed)
        }
    }

    // MARK: - Route changes

    /// The engine stopped under us: the hardware format changed (headphones
    /// or a headset microphone came or went, the sample rate moved). Say so
    /// and rebuild.
    private func configurationChanged() {
        guard isRunning else {
            // Stopped by itself: the hardware moved again, which is when a
            // microphone that went away may be back.
            if stoppedByItself { onAvailable?() }
            return
        }
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
            stopByItself(LocalNotice.microphoneWentAway)
            return
        }
        engineMetrics.hardwareSampleRate = format.sampleRate
        capture.reset()
        input.installTap(onBus: 0, bufferSize: 4_096, format: format) { [weak self] buffer, _ in
            self?.handleCaptured(buffer)
        }
        connectPlayback()
        do {
            try engine.start()
            playback.play()
            recordVoiceProcessing()
            if routeChanged { onNotice?(.routeChanged(output: nil)) }
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
}
