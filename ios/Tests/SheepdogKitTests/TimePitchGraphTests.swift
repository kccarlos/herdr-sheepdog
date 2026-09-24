import AVFoundation
import XCTest
@testable import SheepdogKit

/// The playback graph `AudioEngine.connectPlayback()` builds — player →
/// `AVAudioUnitTimePitch` → main mixer at the 24 kHz mono Float32 playback
/// format — rendered offline, no hardware: it negotiates the format, makes
/// sound, and at rate 0.85 takes longer than the audio it was given.
final class TimePitchGraphTests: XCTestCase {
    func testTheStretchedGraphPlaysAndLengthensTheAudio() throws {
        let format = try XCTUnwrap(AVAudioFormat(commonFormat: .pcmFormatFloat32, sampleRate: AudioContract.playbackRate, channels: 1, interleaved: false))
        let engine = AVAudioEngine()
        let player = AVAudioPlayerNode()
        let timePitch = AVAudioUnitTimePitch()
        engine.attach(player)
        engine.attach(timePitch)
        timePitch.pitch = 0
        timePitch.rate = 0.85
        engine.connect(player, to: timePitch, format: format)
        engine.connect(timePitch, to: engine.mainMixerNode, format: format)
        try engine.enableManualRenderingMode(.offline, format: format, maximumFrameCount: 4_096)
        try engine.start()

        // One second of a 440 Hz tone.
        let frames = AVAudioFrameCount(AudioContract.playbackRate)
        let tone = try XCTUnwrap(AVAudioPCMBuffer(pcmFormat: format, frameCapacity: frames))
        tone.frameLength = frames
        for i in 0..<Int(frames) {
            tone.floatChannelData![0][i] = 0.5 * sin(2 * .pi * 440 * Float(i) / Float(AudioContract.playbackRate))
        }
        player.scheduleBuffer(tone, at: nil, options: [])
        player.play()

        let out = try XCTUnwrap(AVAudioPCMBuffer(pcmFormat: engine.manualRenderingFormat, frameCapacity: 4_096))
        var rendered = 0
        var lastAudible = 0
        var peak: Float = 0
        while rendered < Int(frames) * 3 {
            let status = try engine.renderOffline(4_096, to: out)
            XCTAssertEqual(status, .success)
            for i in 0..<Int(out.frameLength) {
                let v = abs(out.floatChannelData![0][i])
                peak = max(peak, v)
                if v > 0.01 { lastAudible = rendered + i }
            }
            rendered += Int(out.frameLength)
        }
        engine.stop()

        XCTAssertGreaterThan(peak, 0.1, "the graph makes sound")
        let heardSeconds = Double(lastAudible) / AudioContract.playbackRate
        // 1 s at 0.85× lasts ~1.18 s (plus the unit's own small latency).
        XCTAssertGreaterThan(heardSeconds, 1.1, "slowed down, not cut short")
        XCTAssertLessThan(heardSeconds, 1.4)
        print(String(format: "TIMEPITCH | 1.000 s of audio at 0.85x played for %.3f s, peak %.2f, unit latency %.1f ms", heardSeconds, peak, timePitch.latency * 1000))
    }
}
