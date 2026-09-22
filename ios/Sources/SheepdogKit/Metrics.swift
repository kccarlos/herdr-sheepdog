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
}

public enum SheepdogLog {
    public static let audio = Logger(subsystem: "com.kccarlos.sheepdog", category: "audio")
    public static let socket = Logger(subsystem: "com.kccarlos.sheepdog", category: "socket")
    public static let session = Logger(subsystem: "com.kccarlos.sheepdog", category: "session")
}
