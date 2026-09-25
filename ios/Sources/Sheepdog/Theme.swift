import SheepdogKit
import SwiftUI

/// The app's small palette. The agent-state colours follow the daemon's
/// `/debug` page, with one deliberate change: blocked is amber rather than
/// red, because on a phone a red dot reads as "broken" and a blocked agent
/// is only waiting for you.
enum Theme {
    static let blocked = Color(red: 0.85, green: 0.58, blue: 0.09)
    static let working = Color(red: 0.17, green: 0.42, blue: 0.69)
    static let done = Color(red: 0.18, green: 0.52, blue: 0.36)
    static let idle = Color(red: 0.44, green: 0.47, blue: 0.53)
    static let unknown = Color(red: 0.63, green: 0.67, blue: 0.72)
    /// The orb's "thinking" purple, which is the brain's colour too.
    static let thinking = Color(red: 0.49, green: 0.36, blue: 0.75)

    static func color(for state: Agent.State) -> Color {
        switch state {
        case .blocked: return blocked
        case .working: return working
        case .idle, .done: return done
        case .unknown, .exited: return unknown
        }
    }

    static func word(for state: Agent.State) -> String {
        switch state {
        case .blocked: return "Blocked"
        case .working: return "Working"
        case .idle: return "Idle"
        case .done: return "Done"
        case .unknown: return "Unknown"
        case .exited: return "Exited"
        }
    }

    static func color(for session: SessionState) -> Color {
        switch session {
        case .listening: return .accentColor
        case .speaking: return working
        case .thinking: return thinking
        case .silent: return idle
        case .muted: return Color(red: 0.72, green: 0.27, blue: 0.27)
        case .stalled: return blocked
        case .voiceOff: return unknown
        case .audioPaused: return blocked
        case .idle: return done
        case .connecting, .reconnecting: return blocked
        // The voice bar's orb is a Connect button here, so it wears the
        // app's own primary tint rather than reading as "broken".
        case .disconnected: return .accentColor
        }
    }

    static func symbol(for session: SessionState) -> String {
        switch session {
        case .listening: return "waveform"
        case .silent: return "waveform.badge.minus"
        // A waveform, not a speaker: the bar's small stop badge already
        // says "tap to interrupt", the icon just needs to say "audio".
        case .speaking: return "waveform"
        case .thinking: return "ellipsis"
        case .muted: return "mic.slash.fill"
        case .stalled: return "hourglass"
        case .voiceOff: return "mic.slash"
        case .audioPaused: return "play.fill"
        case .idle: return "checkmark"
        case .connecting, .reconnecting: return "arrow.triangle.2.circlepath"
        case .disconnected: return "waveform"
        }
    }
}
