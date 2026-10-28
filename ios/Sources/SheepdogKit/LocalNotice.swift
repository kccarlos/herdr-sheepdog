import AVFoundation
import Foundation

/// The notices this phone makes about itself (never on the daemon): the
/// audio engine's route and interruption lines, the voice session closing,
/// and "Talking to …". Their words are defined here once; the audio engine
/// and the session core build them with `text`, and the conversation
/// recognises them with `init(text:)` until they are tagged (design doc,
/// phase 3). Where each one is shown is `placement`: the timeline keeps
/// only the context ones, the rest go to the voice bar's route glyph or a
/// banner (docs/dev/design/conversation-timeline.md, section 5.5).
public enum LocalNotice: Equatable, Sendable {
    /// You switched computers.
    case talkingTo(String)
    /// Headphones in or out, a car, AirPods: the voice bar's route glyph
    /// shows it. `output` is the new route's name, if iOS gave one.
    case routeChanged(output: String?)
    /// A call or Siri took the audio.
    case pausedForInterruption
    /// The interruption ended and listening resumed.
    case listeningAgain
    /// iOS paused the audio and would not give it back by itself.
    case audioNotGivenBack
    /// Taking the audio back threw.
    case audioFailedToResume(String)
    /// iOS reset its media services under the app.
    case mediaServicesReset
    /// The engine could not be rebuilt after a route change; the payload
    /// is the whole sentence (`microphoneWentAway`, `audioStoppedAfterRouteChange`).
    case audioStopped(String)
    /// The daemon runs without a voice (`--voice=off`).
    case voiceOff
    /// The daemon's voice session closed.
    case voiceClosed(reason: String)
    /// Not a notice this build knows: kept in the list, in case it asks
    /// for a tap.
    case unrecognised(String)

    /// Where the timeline puts it.
    public enum Placement: Equatable, Sendable {
        /// A divider in the conversation.
        case timeline
        /// The voice bar's glyph or its paused state says it: not in the list.
        case voiceBar
        /// A banner with the fix, not in the list.
        case banner
    }

    public var placement: Placement {
        switch self {
        case .talkingTo, .unrecognised: return .timeline
        case .routeChanged, .pausedForInterruption, .listeningAgain: return .voiceBar
        case .audioNotGivenBack, .audioFailedToResume, .mediaServicesReset, .audioStopped, .voiceOff, .voiceClosed: return .banner
        }
    }

    /// The notice as the technical view and the log keep it.
    public var text: String {
        switch self {
        case let .talkingTo(name): return "Talking to \(name)"
        case let .routeChanged(output): return output.map { "Audio moved to \($0)." } ?? "The audio route changed."
        case .pausedForInterruption: return Self.pausedText
        case .listeningAgain: return Self.listeningText
        case .audioNotGivenBack: return "iOS kept the audio for something else. Tap the orb to resume."
        case let .audioFailedToResume(error): return "Could not pick the audio back up (\(error)). Tap the orb to try again."
        case .mediaServicesReset: return "Audio was reset by iOS — reconnect to start listening again."
        case let .audioStopped(why): return why
        case .voiceOff:
            return "Voice is off on the daemon (`--voice=off`), so it cannot hear the microphone. Status, transcripts and pushes still work."
        case let .voiceClosed(reason): return "The daemon's voice session closed (\(reason))."
        case let .unrecognised(text): return text
        }
    }

    /// The notice for the unified log, which keeps it public: without the
    /// names a person gave (a computer, a pair of AirPods).
    public var logText: String {
        switch self {
        case .talkingTo: return "Switched computer."
        case .routeChanged: return "The audio route changed."
        default: return text
        }
    }

    /// The sentences the audio engine reports when it cannot rebuild itself.
    public static let microphoneWentAway = "The microphone went away with the route change."
    public static func audioStoppedAfterRouteChange(_ error: String) -> String { "Audio stopped after the route change (\(error))." }

    private static let pausedText = "Paused for a call or Siri."
    private static let listeningText = "Listening again."

    public init(text: String) {
        func between(_ prefix: String, _ suffix: String) -> String? {
            guard text.hasPrefix(prefix), text.hasSuffix(suffix), text.count >= prefix.count + suffix.count else { return nil }
            return String(text.dropFirst(prefix.count).dropLast(suffix.count))
        }
        if text.hasPrefix("Talking to ") {
            self = .talkingTo(String(text.dropFirst("Talking to ".count)))
        } else if let output = between("Audio moved to ", ".") {
            self = .routeChanged(output: output)
        } else if text == "The audio route changed." {
            self = .routeChanged(output: nil)
        } else if text == Self.pausedText {
            self = .pausedForInterruption
        } else if text == Self.listeningText {
            self = .listeningAgain
        } else if text == LocalNotice.audioNotGivenBack.text {
            self = .audioNotGivenBack
        } else if let error = between("Could not pick the audio back up (", "). Tap the orb to try again.") {
            self = .audioFailedToResume(error)
        } else if text == LocalNotice.mediaServicesReset.text {
            self = .mediaServicesReset
        } else if text == Self.microphoneWentAway || text.hasPrefix("Audio stopped after the route change (") {
            self = .audioStopped(text)
        } else if text.hasPrefix("Voice is off on the daemon") {
            self = .voiceOff
        } else if let reason = between("The daemon's voice session closed (", ")."), !reason.isEmpty {
            self = .voiceClosed(reason: reason)
        } else {
            self = .unrecognised(text)
        }
    }

    /// What the banner says and how to fix it, for the notices placed
    /// there; nil for the others.
    public var banner: (message: String, hint: String)? {
        switch self {
        case .audioNotGivenBack:
            return ("iOS kept the audio for something else.", "Tap the orb to resume listening.")
        case .audioFailedToResume:
            return ("Could not pick the audio back up.", "Tap the orb to try again.")
        case .mediaServicesReset:
            return ("Audio was reset by iOS.", "Reconnect to start listening again.")
        case let .audioStopped(why):
            return (why, "Tap the orb to start listening again.")
        case .voiceOff:
            return ("Voice is off on the daemon.", "Status, transcripts and pushes still work. Start the daemon without `--voice=off` to talk.")
        case let .voiceClosed(reason):
            return ("The daemon's voice session closed (\(reason)).", "Status, transcripts and pushes still work.")
        default:
            return nil
        }
    }

    /// Whether the banner is still true in `state`: iOS giving the audio
    /// back, or the voice returning, clears it. The others stay until the
    /// user dismisses them or reconnects.
    public func bannerStillApplies(in state: SessionState) -> Bool {
        switch self {
        case .audioNotGivenBack, .audioFailedToResume: return state == .audioPaused
        case .voiceOff, .voiceClosed: return state == .voiceOff
        default: return true
        }
    }
}

/// Where the sound goes now, for the voice bar's glyph.
public enum AudioRoute: Equatable, Sendable {
    case speaker
    /// The phone's earpiece.
    case receiver
    case headphones
    case bluetooth
    case car
    case airPlay

    /// The glyph for the route.
    public var symbol: String {
        switch self {
        case .speaker: return "speaker.wave.2"
        case .receiver: return "iphone"
        case .headphones: return "headphones"
        case .bluetooth: return "dot.radiowaves.left.and.right"
        case .car: return "car"
        case .airPlay: return "airplayaudio"
        }
    }

    /// For VoiceOver: "Audio plays through the speaker".
    public var title: String {
        switch self {
        case .speaker: return "the speaker"
        case .receiver: return "the earpiece"
        case .headphones: return "headphones"
        case .bluetooth: return "a Bluetooth device"
        case .car: return "the car"
        case .airPlay: return "AirPlay"
        }
    }

    /// The route for an output port type. Anything this build does not know
    /// is the speaker's glyph: a route is always shown while audio runs.
    public init(output port: AVAudioSession.Port) {
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
