import SheepdogKit
import SwiftUI

/// The app's small palette. The agent-state colours follow the daemon's
/// `/debug` page, with one deliberate change: blocked is amber rather than
/// red, because on a phone a red dot reads as "broken" and a blocked agent
/// is only waiting for you.
enum Theme {
    // The colours live in the asset catalog with a light and a dark
    // variant each, chosen for contrast (WCAG AA): every colour used for
    // text reads at 4.5:1 or better on the system backgrounds and on its
    // own tinted card. Amber is the exception — bright enough for a dot, a
    // border or a fill, too light for text on white — so it has a darker
    // twin, `blockedText`, for words.

    /// Amber, for dots, borders and fills.
    static let blocked = Color("Blocked")
    /// Amber for words: darker in light mode, lighter in dark.
    static let blockedText = Color("BlockedText")
    /// Text and glyphs on an amber fill.
    static let onBlocked = Color.black
    static let working = Color("Working")
    static let done = Color("Done")
    static let idle = Color("Idle")
    static let unknown = Color("Unknown")
    /// The orb's "thinking" purple, which is the orchestrator's colour too.
    static let thinking = Color("Thinking")

    /// The state's colour for words ("Blocked · 4 min").
    static func textColor(for state: Agent.State) -> Color {
        state == .blocked ? blockedText : color(for: state)
    }

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
        // The mic button is the one loud "muted" (red); the orb stays quiet.
        case .muted: return idle
        case .stalled: return blocked
        case .voiceOff: return unknown
        case .noMicrophone: return blocked
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
        case .muted: return "waveform"
        case .stalled: return "hourglass"
        case .voiceOff: return "mic.slash"
        case .noMicrophone: return "mic.slash"
        case .audioPaused: return "play.fill"
        case .idle: return "checkmark"
        case .connecting, .reconnecting: return "arrow.triangle.2.circlepath"
        case .disconnected: return "waveform"
        }
    }
}
