import Foundation
import os

/// The numbers the About row shows and `os_log` records, so it is obvious
/// whether the sample-rate conversion is the bottleneck (it is not).
///
/// * `captureLatency` — one tap buffer arriving to its last frame being
///   handed to the socket: convert + gate + chunk.
/// * `playbackDepth` — how much audio is waiting to be heard.
public struct AudioMetrics: Equatable, Sendable {
    public var captureFrames = 0
    public var captureLatencyMilliseconds: Double = 0
    public var peakCaptureLatencyMilliseconds: Double = 0
    public var playbackDepthMilliseconds = 0
    public var peakPlaybackDepthMilliseconds = 0
    public var gatedFrames = 0
    public var silenceTransitions = 0
    public var droppedFrames = 0
    public var hardwareSampleRate: Double = 0
    /// Where playback goes ("Speaker", "Receiver", AirPods…), for Settings ▸ About.
    public var outputRoute = ""
    /// Chunks handed to the player and chunks it reported played back:
    /// received-but-silent audio shows up as scheduled ≫ played.
    public var scheduledChunks = 0
    public var playedChunks = 0

    /// The adaptive jitter buffer (`PlaybackQueue`): audible gaps, the
    /// current target, every time the node ran dry (underrun or not), and
    /// the worst arrival gap seen while one was worth caring about.
    public var underruns = 0
    public var bufferTargetMilliseconds = 0
    public var drains = 0
    public var largestArrivalGapMilliseconds = 0
    /// Audio buffered right now (predicted playhead + held back), in ms.
    public var bufferedMilliseconds = 0
    /// The time-stretch rate right now, and the total wall-clock time
    /// played at a rate other than 1.0.
    public var playbackRate = 1.0
    public var stretchedMilliseconds = 0

    public init() {}

    public mutating func recordCapture(latency: TimeInterval, frames: Int) {
        captureFrames += frames
        captureLatencyMilliseconds = latency * 1000
        peakCaptureLatencyMilliseconds = max(peakCaptureLatencyMilliseconds, captureLatencyMilliseconds)
    }

    public mutating func recordPlayback(depthMilliseconds: Int) {
        playbackDepthMilliseconds = depthMilliseconds
        peakPlaybackDepthMilliseconds = max(peakPlaybackDepthMilliseconds, depthMilliseconds)
    }

    public mutating func recordJitterBuffer(underruns: Int, targetMilliseconds: Int, drains: Int, largestArrivalGapMilliseconds: Int) {
        self.underruns = underruns
        self.bufferTargetMilliseconds = targetMilliseconds
        self.drains = drains
        self.largestArrivalGapMilliseconds = largestArrivalGapMilliseconds
    }

    public mutating func recordTimeStretch(rate: Double, stretchedSeconds: TimeInterval, bufferedMilliseconds: Double) {
        playbackRate = rate
        stretchedMilliseconds = Int((stretchedSeconds * 1000).rounded())
        self.bufferedMilliseconds = Int(bufferedMilliseconds.rounded())
    }

    /// The one-line summary under Settings ▸ About.
    public var summary: String {
        String(
            format: "capture %.1f ms (peak %.1f) · queue %d ms (peak %d) · %d frames, %d gated",
            captureLatencyMilliseconds,
            peakCaptureLatencyMilliseconds,
            playbackDepthMilliseconds,
            peakPlaybackDepthMilliseconds,
            captureFrames,
            gatedFrames
        )
    }

    /// The jitter-buffer line under Settings ▸ About.
    public var jitterSummary: String {
        String(
            format: "jitter target %d ms · %d underruns, %d drains · worst gap %d ms",
            bufferTargetMilliseconds,
            underruns,
            drains,
            largestArrivalGapMilliseconds
        )
    }

    /// The time-stretch line under Settings ▸ About.
    public var stretchSummary: String {
        String(
            format: "rate %.2f× · target %d ms · %d underruns · stretched %.1f s",
            playbackRate,
            bufferTargetMilliseconds,
            underruns,
            Double(stretchedMilliseconds) / 1000
        )
    }
}

public enum SheepdogLog {
    public static let audio = Logger(subsystem: "com.kccarlos.sheepdog", category: "audio")
    public static let socket = Logger(subsystem: "com.kccarlos.sheepdog", category: "socket")
    public static let session = Logger(subsystem: "com.kccarlos.sheepdog", category: "session")
}
