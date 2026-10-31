import AVFoundation
import Foundation

/// The microphone's buffer as one mono channel, whatever the hardware
/// delivers.
///
/// The voice-processing input on a Mac is not mono: an experiment saw six
/// channels at 48 kHz with channel 0 digital silence while the others
/// carried sound (docs/dev/design/desktop-client.md section 5.3). Reading
/// channel 0, or averaging over every channel, gives silence or a quiet
/// signal. So the mix is the average of the channels that carry something in
/// this buffer: one live channel passes at full level, identical channels
/// (stereo of one microphone) stay as they are, and an all-zero buffer stays
/// zero, which is what `MicSignalWatchdog` is there to notice. `PCMConverter`
/// then only changes the rate and the sample format.
///
/// Whether the live channel is the microphone on a real device needs a real
/// microphone (the maintainer's check 2).
enum MicMixdown {
    /// The mono Float32 format at `sampleRate`.
    static func format(sampleRate: Double) -> AVAudioFormat? {
        AVAudioFormat(commonFormat: .pcmFormatFloat32, sampleRate: sampleRate, channels: 1, interleaved: false)
    }

    /// A copy of `buffer` as mono, or nil for an empty buffer or one that
    /// is not Float32 (what the engine's tap delivers). Safe on the audio
    /// render thread: the result owns its memory.
    static func mono(_ buffer: AVAudioPCMBuffer) -> AVAudioPCMBuffer? {
        let frames = Int(buffer.frameLength)
        let channels = Int(buffer.format.channelCount)
        guard frames > 0, channels > 0, buffer.format.commonFormat == .pcmFormatFloat32,
              let source = buffer.floatChannelData,
              let format = format(sampleRate: buffer.format.sampleRate),
              let out = AVAudioPCMBuffer(pcmFormat: format, frameCapacity: buffer.frameLength),
              let target = out.floatChannelData?[0] else { return nil }
        out.frameLength = buffer.frameLength
        // Interleaved buffers have one pointer, `stride` samples per frame.
        let stride = buffer.stride
        func sample(_ channel: Int, _ frame: Int) -> Float {
            buffer.format.isInterleaved ? source[0][frame * stride + channel] : source[channel][frame]
        }
        var live: [Int] = []
        for channel in 0 ..< channels where (0 ..< frames).contains(where: { sample(channel, $0) != 0 }) {
            live.append(channel)
        }
        let divisor = Float(max(live.count, 1))
        for frame in 0 ..< frames {
            var sum: Float = 0
            for channel in live { sum += sample(channel, frame) }
            target[frame] = sum / divisor
        }
        return out
    }
}
