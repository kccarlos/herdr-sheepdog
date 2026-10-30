import AVFoundation
import SheepdogKit

extension AudioRoute {
    /// The route for an output port type. Anything this build does not know
    /// is the speaker's glyph: a route is always shown while audio runs.
    init(port: AVAudioSession.Port) {
        switch port {
        case .builtInSpeaker: self = .speaker
        case .builtInReceiver: self = .receiver
        case .headphones, .headsetMic, .usbAudio, .lineOut, .HDMI, .displayPort: self = .headphones
        case .bluetoothA2DP, .bluetoothHFP, .bluetoothLE: self = .bluetooth
        case .carAudio: self = .car
        case .airPlay: self = .airPlay
        default: self = .speaker
        }
    }
}
