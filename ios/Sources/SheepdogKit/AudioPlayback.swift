import AVFoundation
import Foundation

/// Gapless 24 kHz voice playback with the adaptive jitter buffer and
/// pitch-preserving time-stretch, shared by the iOS and macOS audio engines.
///
/// `PlaybackQueue` owns the jitter-buffer and rate-steering decisions; this
/// class wires `AVAudioPlayerNode` → `AVAudioUnitTimePitch` → `mainMixerNode`,
/// runs the 20 ms rate control loop while the engine is up, converts PCM16 LE
/// wire chunks into 24 kHz Float32 buffers, delivers playback and drain
/// reports, and records the playback counters on `AudioMetrics`.
@MainActor
public final class AudioPlayback {
    public static let format = AVAudioFormat(
        commonFormat: .pcmFormatFloat32,
        sampleRate: AudioContract.playbackRate,
        channels: 1,
        interleaved: false
    )!
    private static let steerInterval: TimeInterval = 0.02

    /// Playback started or stopped (`PlaybackQueue.isSpeaking`).
    public var onSpeakingChanged: ((Bool) -> Void)?
    /// Output level, 0…1, as each chunk finishes playing; 0 when playback stops.
    public var onOutputLevel: ((Float) -> Void)?
    /// A playback report for `/ws` (`started`, `drained`, `flushed`), in order.
    public var onPlaybackReport: ((PlaybackQueue.Report) -> Void)?
    /// Every time the player ran dry, for the daemon's `drain` message.
    public var onDrain: ((PlaybackQueue.DrainRecord) -> Void)?

    public private(set) var isRunning = false
    public private(set) var metrics = AudioMetrics()
    public var timePitchLatency: TimeInterval { timePitch.latency }
    public var currentRate: Float { timePitch.rate }

    private let player = AVAudioPlayerNode()
    private let timePitch = AVAudioUnitTimePitch()
    private var steerTimer: Timer?
    private var queue = PlaybackQueue()
    private var wasSpeaking = false
    private let clock: () -> TimeInterval

    public init(clock: @escaping () -> TimeInterval = { CFAbsoluteTimeGetCurrent() }) {
        self.clock = clock
        timePitch.pitch = 0
        timePitch.rate = 1
    }

    deinit {
        steerTimer?.invalidate()
    }

    // MARK: - Graph and lifecycle

    /// Attaches the player and time-pitch unit to `engine`. Connect them in
    /// `start()` with `connect(in:)`, after the session or voice-processing
    /// I/O unit is configured.
    public func attach(to engine: AVAudioEngine) {
        engine.attach(player)
        engine.attach(timePitch)
        timePitch.pitch = 0
        timePitch.rate = 1
    }

    /// Connects `player` → `timePitch` → `mainMixerNode` at the 24 kHz mono
    /// Float32 playback format.
    public func connect(in engine: AVAudioEngine) {
        engine.disconnectNodeOutput(player)
        engine.disconnectNodeOutput(timePitch)
        engine.connect(player, to: timePitch, format: Self.format)
        engine.connect(timePitch, to: engine.mainMixerNode, format: Self.format)
    }

    /// Starts the player node and the 20 ms rate-steering loop once `engine`
    /// is running.
    public func start() {
        player.play()
        isRunning = true
        startSteering()
    }

    /// Resumes the player node after `engine` restarts on an already-running
    /// session (route rebuild or interruption reactivation).
    public func play() {
        player.play()
    }

    /// Stops steering, the player node and `engine`, flushes the queue and
    /// delivers reports while still running, resets the rate, runs host
    /// teardown, and clears speaking state.
    public func stop(in engine: AVAudioEngine, teardown: () -> Void = {}) {
        stopSteering()
        player.stop()
        engine.stop()
        queue.flush(now: clock())
        deliverReports()
        applyRate(1)
        isRunning = false
        teardown()
        setSpeaking(false)
    }

    /// Marks playback stopped and halts the 20 ms steering timer when the
    /// engine died by itself during a route change.
    public func stopForFailure() {
        isRunning = false
        stopSteering()
    }

    // MARK: - Playback

    /// Enqueues one binary frame from `/ws`: PCM16 LE mono at 24 kHz.
    public func enqueue(_ data: Data) {
        let samples = [Int16](pcm16LE: data)
        guard !samples.isEmpty else { return }
        schedule(queue.push(samples, now: clock()))
        deliverReports()
    }

    /// Drops everything queued or scheduled and reports `flushed` every time.
    public func flush() {
        let hadAudio = queue.flush(now: clock())
        deliverReports()
        if hadAudio {
            player.stop()
            if isRunning { player.play() }
        }
        applyRate(1)
        setSpeaking(false)
    }

    /// Switches the jitter-buffer profile when the voice provider changes,
    /// returning the new profile when it moved (nil when unchanged).
    @discardableResult
    public func setProvider(_ provider: String?) -> PlaybackProfile? {
        let profile = PlaybackProfile.forProvider(provider)
        guard profile != queue.profile else { return nil }
        queue.setProfile(profile)
        recordJitterBufferMetrics()
        return profile
    }

    /// Updates playback metrics on the host timer's cadence, and releases a
    /// short pre-start utterance when the engine is not running yet (while the
    /// engine runs, `steer()` handles releases every 20 ms).
    public func tick() {
        let now = clock()
        if !isRunning {
            schedule(queue.tick(now: now))
            deliverReports()
        }
        metrics.recordPlayback(depthMilliseconds: queue.queueDepthMilliseconds)
        metrics.recordTimeStretch(
            rate: queue.rate,
            stretchedSeconds: queue.stretchedSeconds,
            bufferedMilliseconds: queue.bufferedMilliseconds(at: now)
        )
        recordJitterBufferMetrics()
        setSpeaking(queue.isSpeaking)
    }

    /// One 20 ms control-loop step: releases any due chunks, steps the
    /// time-stretch rate one slew increment, and delivers reports.
    public func steer() {
        let now = clock()
        schedule(queue.tick(now: now))
        applyRate(queue.steer(now: now))
        deliverReports()
    }

    /// Copies the playback counters onto an engine's `AudioMetrics`.
    public func apply(to target: inout AudioMetrics) {
        target.playbackDepthMilliseconds = metrics.playbackDepthMilliseconds
        target.peakPlaybackDepthMilliseconds = max(target.peakPlaybackDepthMilliseconds, metrics.peakPlaybackDepthMilliseconds)
        target.scheduledChunks = metrics.scheduledChunks
        target.playedChunks = metrics.playedChunks
        target.underruns = metrics.underruns
        target.bufferTargetMilliseconds = metrics.bufferTargetMilliseconds
        target.drains = metrics.drains
        target.largestArrivalGapMilliseconds = metrics.largestArrivalGapMilliseconds
        target.bufferedMilliseconds = metrics.bufferedMilliseconds
        target.playbackRate = metrics.playbackRate
        target.stretchedMilliseconds = metrics.stretchedMilliseconds
    }

    /// Converts 16-bit signed PCM samples into a Float32 mono buffer for the
    /// player node.
    public static func pcmBuffer(for samples: [Int16], format: AVAudioFormat = AudioPlayback.format) -> AVAudioPCMBuffer? {
        guard !samples.isEmpty,
              let buffer = AVAudioPCMBuffer(pcmFormat: format, frameCapacity: AVAudioFrameCount(samples.count)),
              let channel = buffer.floatChannelData else { return nil }
        buffer.frameLength = AVAudioFrameCount(samples.count)
        for (index, sample) in samples.enumerated() {
            channel[0][index] = Float(sample) / 32_768
        }
        return buffer
    }

    // MARK: - Internal

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

    private func applyRate(_ rate: Double) {
        let value = Float(rate)
        if timePitch.rate != value { timePitch.rate = value }
    }

    private func schedule(_ chunks: [PlaybackQueue.Chunk]) {
        guard !chunks.isEmpty else { return }
        for chunk in chunks {
            guard let buffer = Self.pcmBuffer(for: chunk.samples) else { continue }
            let level = Float(PCMLevel.peak(of: chunk.samples)) / 32_768
            metrics.scheduledChunks += 1
            // No `at:`: each buffer starts where the last one ended, which is
            // what "gapless" means here.
            player.scheduleBuffer(buffer, completionCallbackType: .dataPlayedBack) { [weak self] _ in
                Task { @MainActor in
                    self?.chunkCompleted(chunk.id, level: level)
                }
            }
        }
        if isRunning, !player.isPlaying { player.play() }
        metrics.recordPlayback(depthMilliseconds: queue.queueDepthMilliseconds)
        recordJitterBufferMetrics()
        setSpeaking(queue.isSpeaking)
    }

    func chunkCompleted(_ id: Int, level: Float) {
        queue.completed(id, now: clock())
        onOutputLevel?(level)
        deliverReports()
        metrics.playedChunks += 1
        setSpeaking(queue.isSpeaking)
    }

    private func deliverReports() {
        for report in queue.takeReports() {
            onPlaybackReport?(report)
        }
        for drain in queue.takeDrains() {
            onDrain?(drain)
        }
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
        if !speaking { onOutputLevel?(0) }
    }
}
