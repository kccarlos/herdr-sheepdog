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
    /// The brand accent as words on light surfaces (links, text buttons,
    /// labels): the accent itself is about 2.7:1 on white, this is 4.5:1 or
    /// better in both modes. Fills and glyphs keep `AccentColor`; text on an
    /// orange fill uses `OnAccent` (ink).
    static let primaryText = Color("PrimaryText")
    /// Brand cream: the outline ring on the listening orb.
    static let cream = Color("Cream")
    /// The orb's "thinking" purple, which is the orchestrator's colour too.
    static let thinking = Color("Thinking")

    // MARK: - Surfaces (assets/brand/colors.json "surface")

    /// The screen behind everything: warm off-white, near-black in dark.
    static let surface = Color("Surface")
    /// Cards, chips and menus on top of the surface.
    static let card = Color("Card")
    /// Code blocks, wells and quiet fills.
    static let fill = Color("Fill")
    /// The user's own bubbles: a warm neutral, never the accent.
    static let bubble = Color("Bubble")
    /// Hairlines and card borders.
    static let line = Color("Line")
    /// Secondary words.
    static let muted = Color("Muted")
    /// The segmented control's track.
    static let track = Color("Track")
    /// The selected segment.
    static let selected = Color("Selected")
    /// Ink: primary words and glyphs on the surfaces (cream in dark).
    static let ink = Color("Ink")
    /// The accent's soft tint: a waiting card's question area.
    static let accentSoft = Color("AccentSoft")
    static let workingSoft = Color("WorkingSoft")
    static let thinkingSoft = Color("ThinkingSoft")
    static let doneSoft = Color("DoneSoft")
    /// Red for words and the muted mic: "No" options, destructive rows.
    static let danger = Color("Danger")
    /// Ink on an orange fill, in both modes. Text on orange is never white.
    static let onAccent = Color("OnAccent")

    // MARK: - Shape and motion

    enum Radius {
        static let small: CGFloat = 8
        /// Cards.
        static let card: CGFloat = 14
        static let large: CGFloat = 22
    }

    /// The app's one easing, cubic-bezier(.2, .8, .2, 1) over 200 ms.
    static let ease = Animation.timingCurve(0.2, 0.8, 0.2, 1, duration: 0.2)

    /// The easing, or a plain fade-length animation under Reduce Motion
    /// (callers swap movement for opacity there).
    static func ease(reduceMotion: Bool) -> Animation {
        reduceMotion ? .linear(duration: 0.15) : ease
    }

    /// The state's colour for words ("Blocked · 4 min").
    static func textColor(for state: Agent.State) -> Color {
        state == .blocked ? blockedText : color(for: state)
    }

    static func color(for state: Agent.State) -> Color {
        switch state {
        case .blocked: return blocked
        case .working: return working
        case .idle: return idle
        case .done: return done
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

/// The app's pill button: the accent fill with ink words for the one
/// primary action on a screen, else the card with a hairline. At least
/// 44 pt tall; the label wraps rather than truncates.
struct PillButtonStyle: ButtonStyle {
    var prominent = false
    var fullWidth = true
    @Environment(\.isEnabled) private var isEnabled

    func makeBody(configuration: Configuration) -> some View {
        configuration.label
            .font(.body.weight(.semibold))
            .foregroundStyle(prominent ? Theme.onAccent : Theme.ink)
            .multilineTextAlignment(.center)
            .padding(.horizontal, 18)
            .padding(.vertical, 11)
            .frame(maxWidth: fullWidth ? .infinity : nil, minHeight: 44)
            .background(Capsule().fill(prominent ? Color.accentColor : Theme.card))
            .overlay { if !prominent { Capsule().strokeBorder(Theme.line, lineWidth: 1) } }
            .contentShape(Capsule())
            .opacity(configuration.isPressed ? 0.7 : (isEnabled ? 1 : 0.45))
    }
}
