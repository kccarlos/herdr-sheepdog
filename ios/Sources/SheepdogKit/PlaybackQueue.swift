import Foundation

/// Scheduling logic for the gapless player, with no hardware attached.
///
/// The daemon sends PCM16 at 24 kHz in chunks of whatever length the model
/// produced (40–730 ms observed). The contract says they must be played by
/// appending to a buffer, never by scheduling each one at "now": this queue
/// holds a small jitter buffer and then hands chunks out **in order**, one
/// `AVAudioPlayerNode.scheduleBuffer` each, so the node itself concatenates
/// them sample-accurately.
///
/// `flush` (server) and the Stop button drop everything at once.
public struct PlaybackQueue: Sendable {
    /// One chunk ready to be scheduled, in arrival order.
    public struct Chunk: Equatable, Identifiable, Sendable {
        public let id: Int
        public let samples: [Int16]
        public var frameCount: Int { samples.count }
        public init(id: Int, samples: [Int16]) {
            self.id = id
            self.samples = samples
        }
    }

    public enum Phase: Equatable, Sendable {
        /// Nothing queued and nothing playing.
        case idle
        /// Filling the jitter buffer; nothing has been handed out yet.
        case buffering
        /// Chunks are with the player node.
        case playing
    }

    public let sampleRate: Double
    /// Samples to collect before the first chunk is released.
    public let prebufferSamples: Int
    /// A lone short utterance still starts after this long rather than never.
    public let maximumWait: TimeInterval

    public private(set) var phase: Phase = .idle
    /// Samples held back, not yet handed to the player.
    public private(set) var bufferedSamples = 0
    /// Samples handed to the player and not yet reported complete.
    public private(set) var scheduledSamples = 0
    public private(set) var flushes = 0
    public private(set) var chunksDropped = 0

    private var pending: [Chunk] = []
    private var nextID = 1
    private var bufferingSince: TimeInterval = 0
    private var live: Set<Int> = []

    public init(
        sampleRate: Double = AudioContract.playbackRate,
        prebufferMilliseconds: Int = 150,
        maximumWait: TimeInterval = 0.4
    ) {
        self.sampleRate = sampleRate
        self.prebufferSamples = Int(sampleRate * Double(prebufferMilliseconds) / 1000.0)
        self.maximumWait = maximumWait
    }

    /// True while the player has audio to play — what the orb calls "speaking".
    public var isSpeaking: Bool { scheduledSamples > 0 }

    /// How much audio is waiting to be heard, in milliseconds. Logged so the
    /// user can see the conversion is not the bottleneck.
    public var queueDepthMilliseconds: Int {
        Int(Double(bufferedSamples + scheduledSamples) / sampleRate * 1000.0)
    }

    /// Appends a chunk. Returns the chunks to schedule now, in order: empty
    /// while the jitter buffer fills, then everything held plus this one.
    public mutating func push(_ samples: [Int16], now: TimeInterval) -> [Chunk] {
        guard !samples.isEmpty else { return [] }
        let chunk = Chunk(id: nextID, samples: samples)
        nextID += 1
        if phase == .playing {
            live.insert(chunk.id)
            scheduledLengths[chunk.id] = chunk.frameCount
            scheduledSamples += chunk.frameCount
            return [chunk]
        }
        if phase == .idle {
            phase = .buffering
            bufferingSince = now
        }
        pending.append(chunk)
        bufferedSamples += chunk.frameCount
        if bufferedSamples >= prebufferSamples { return release() }
        return []
    }

    /// Releases a short utterance that never reached the jitter buffer.
    /// Call it from the same timer that refreshes the UI.
    public mutating func tick(now: TimeInterval) -> [Chunk] {
        guard phase == .buffering, now - bufferingSince >= maximumWait else { return [] }
        return release()
    }

    /// Reports that the player finished a chunk.
    public mutating func completed(_ id: Int) {
        guard live.remove(id) != nil else { return }
        // The chunk's own length leaves the depth; a queue that has drained
        // goes back to idle so the next utterance re-buffers.
        if let index = scheduledLengths.index(forKey: id) {
            scheduledSamples -= scheduledLengths[index].value
            scheduledLengths.remove(at: index)
        }
        if scheduledSamples < 0 { scheduledSamples = 0 }
        if live.isEmpty && pending.isEmpty {
            scheduledSamples = 0
            phase = .idle
        }
    }

    /// Drop everything now: the queued chunks and the ones already with the
    /// player (the caller stops the node). Returns true when there was
    /// anything to drop, i.e. the node needs stopping.
    @discardableResult
    public mutating func flush() -> Bool {
        let hadAudio = !pending.isEmpty || !live.isEmpty
        chunksDropped += pending.count + live.count
        pending.removeAll()
        live.removeAll()
        scheduledLengths.removeAll()
        bufferedSamples = 0
        scheduledSamples = 0
        phase = .idle
        flushes += 1
        return hadAudio
    }

    private var scheduledLengths: [Int: Int] = [:]

    private mutating func release() -> [Chunk] {
        let out = pending
        pending.removeAll()
        bufferedSamples = 0
        phase = .playing
        for chunk in out {
            live.insert(chunk.id)
            scheduledLengths[chunk.id] = chunk.frameCount
            scheduledSamples += chunk.frameCount
        }
        return out
    }
}
