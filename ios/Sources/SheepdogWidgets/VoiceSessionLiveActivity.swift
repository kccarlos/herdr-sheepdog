import ActivityKit
import AppIntents
import SheepdogKit
import SwiftUI
import WidgetKit

/// The voice session on the lock screen: what the voice is doing, the
/// current computer (only with several), up to three agents waiting for an
/// answer with their questions (tap one to open its window), how many are
/// working and done, and Mute / End. The data and every word come from
/// SheepdogKit's `LiveActivityContent`; this file only draws them. The
/// Dynamic Island keeps a plain, system-style layout.
struct VoiceSessionLiveActivity: Widget {
    var body: some WidgetConfiguration {
        ActivityConfiguration(for: VoiceSessionAttributes.self) { context in
            VoiceSessionLockScreenView(state: context.state.openedWithStoredKey(), voiceName: context.attributes.voiceName, isStale: context.isStale)
                .activityBackgroundTint(ActivityPalette.card)
                .activitySystemActionForegroundColor(ActivityPalette.ink)
        } dynamicIsland: { context in
            let state = context.state
            let look = PhaseLook(state)
            return DynamicIsland {
                DynamicIslandExpandedRegion(.leading) {
                    Image(systemName: look.symbol)
                        .font(.title2)
                        .foregroundStyle(look.tint)
                        .accessibilityHidden(true)
                }
                DynamicIslandExpandedRegion(.trailing) {
                    if state.waiting > 0 {
                        Label("\(state.waiting)", systemImage: "person.fill.questionmark")
                            .font(.headline.monospacedDigit())
                            .foregroundStyle(ActivityPalette.waiting)
                            .accessibilityHidden(true)
                    }
                }
                DynamicIslandExpandedRegion(.center) {
                    Text(state.title)
                        .font(.headline)
                        .lineLimit(1)
                        .accessibilityLabel(context.isStale ? LiveActivityContent.staleAccessibilityLabel : state.accessibilityLabel(voiceName: context.attributes.voiceName))
                }
                DynamicIslandExpandedRegion(.bottom) {
                    SessionButtons(muted: state.muted)
                }
            } compactLeading: {
                Image(systemName: look.symbol)
                    .foregroundStyle(look.tint)
                    .accessibilityLabel(state.compactAccessibilityLabel)
            } compactTrailing: {
                Text(state.waiting > 0 ? "\(state.waiting)" : "")
                    .font(.caption.weight(.bold).monospacedDigit())
                    .foregroundStyle(ActivityPalette.waiting)
                    .accessibilityHidden(true)
            } minimal: {
                Image(systemName: state.waiting > 0 ? "person.fill.questionmark" : look.symbol)
                    .foregroundStyle(state.waiting > 0 ? ActivityPalette.waiting : look.tint)
                    .accessibilityLabel(state.compactAccessibilityLabel)
            }
            .keylineTint(look.tint)
        }
    }
}

/// The lock-screen banner. The lock screen gives a Live Activity about
/// 160 pt of height, so the rows shrink to two at the largest standard
/// text sizes and to one at the accessibility sizes; the last slot then
/// says how many more agents are waiting.
struct VoiceSessionLockScreenView: View {
    let state: LiveActivityContent
    let voiceName: String
    let isStale: Bool

    @Environment(\.dynamicTypeSize) private var typeSize

    /// How many lines the waiting agents get at this text size.
    private var slots: Int {
        if typeSize.isAccessibilitySize { return 1 }
        return typeSize >= .xxLarge ? 2 : LiveActivityContent.maxRows
    }

    var body: some View {
        let visible = state.visibleRows(slots: slots)
        VStack(alignment: .leading, spacing: 8) {
            header
            if !visible.rows.isEmpty || visible.more > 0 {
                Rectangle()
                    .fill(ActivityPalette.line)
                    .frame(height: 1)
                    .accessibilityHidden(true)
                VStack(alignment: .leading, spacing: 6) {
                    ForEach(visible.rows) { WaitingRow(row: $0) }
                    if visible.more > 0 {
                        MoreWaitingLine(text: LiveActivityContent.moreText(visible.more, afterRows: !visible.rows.isEmpty))
                    }
                }
                .opacity(isStale ? 0.5 : 1)
            }
        }
        .padding(.horizontal, 14)
        .padding(.vertical, 12)
        // Past AX2 the header's words would push the buttons off; the
        // headline and rows stay one line each and truncate instead.
        .dynamicTypeSize(...DynamicTypeSize.accessibility2)
    }

    private var header: some View {
        HStack(spacing: 12) {
            PhaseOrb(look: PhaseLook(state), size: 40)
                .opacity(isStale ? 0.5 : 1)
                .accessibilityHidden(true)
            VStack(alignment: .leading, spacing: 1) {
                Text(isStale ? LiveActivityContent.staleTitle : state.title)
                    .font(.headline)
                    .foregroundStyle(ActivityPalette.ink)
                Text(isStale ? LiveActivityContent.staleSubtitle(voiceName: voiceName) : state.subtitle(voiceName: voiceName))
                    .font(.footnote)
                    .foregroundStyle(ActivityPalette.muted)
            }
            .lineLimit(1)
            .frame(maxWidth: .infinity, alignment: .leading)
            .accessibilityElement(children: .ignore)
            .accessibilityLabel(isStale ? LiveActivityContent.staleAccessibilityLabel : state.accessibilityLabel(voiceName: voiceName))
            SessionButtons(muted: state.muted)
        }
    }
}

/// One waiting agent: amber dot, name, the other computer's name when it
/// is not the current one, and the question. Opens the agent's window.
struct WaitingRow: View {
    let row: LiveActivityContent.Row

    var body: some View {
        Link(destination: row.url) {
            HStack(spacing: 8) {
                Circle()
                    .fill(ActivityPalette.waiting)
                    .frame(width: 7, height: 7)
                Text(row.agent)
                    .font(.subheadline.weight(.semibold))
                    .foregroundStyle(ActivityPalette.ink)
                    .layoutPriority(2)
                if let computer = row.computer {
                    Text(computer)
                        .font(.caption)
                        .foregroundStyle(ActivityPalette.muted)
                        .padding(.horizontal, 6)
                        .padding(.vertical, 1)
                        .background(ActivityPalette.chip, in: RoundedRectangle(cornerRadius: 6, style: .continuous))
                        .layoutPriority(1)
                }
                Text(row.question)
                    .font(.subheadline)
                    .foregroundStyle(ActivityPalette.muted)
            }
            .lineLimit(1)
            .frame(maxWidth: .infinity, alignment: .leading)
            .contentShape(Rectangle())
        }
        .accessibilityElement(children: .ignore)
        .accessibilityLabel(row.accessibilityLabel)
        .accessibilityHint("Opens the agent's window")
        .accessibilityAddTraits(.isLink)
    }
}

/// "3 more waiting": what did not fit.
struct MoreWaitingLine: View {
    let text: String

    var body: some View {
        HStack(spacing: 8) {
            Circle()
                .strokeBorder(ActivityPalette.waiting, lineWidth: 1.5)
                .frame(width: 7, height: 7)
                .accessibilityHidden(true)
            Text(text)
                .font(.subheadline)
                .foregroundStyle(ActivityPalette.muted)
                .lineLimit(1)
        }
    }
}

/// Mute / Unmute and End, run in the app's process (`LiveActivityIntent`).
/// 44 pt targets.
struct SessionButtons: View {
    let muted: Bool

    var body: some View {
        HStack(spacing: 8) {
            Button(intent: SetVoiceMuteIntent(muted: !muted)) {
                Image(systemName: muted ? "mic.slash.fill" : "mic.fill")
                    .font(.system(size: 17, weight: .semibold))
                    .frame(width: 44, height: 44)
                    .foregroundStyle(muted ? Color.white : ActivityPalette.ink)
                    .background(Circle().fill(muted ? ActivityPalette.danger : ActivityPalette.button))
            }
            .buttonStyle(.plain)
            .accessibilityLabel(muted ? "Unmute" : "Mute")
            .accessibilityHint(muted ? "Turns the microphone back on" : "Turns the microphone off; Sheepdog can still talk")

            Button(intent: EndVoiceSessionIntent()) {
                Image(systemName: "xmark")
                    .font(.system(size: 15, weight: .bold))
                    .frame(width: 44, height: 44)
                    .foregroundStyle(ActivityPalette.ink)
                    .background(Circle().fill(ActivityPalette.button))
            }
            .buttonStyle(.plain)
            .accessibilityLabel("End session")
            .accessibilityHint("Ends the voice session. Your agents keep working.")
        }
    }
}

/// The voice's orb: a filled circle in the phase's colour with a cream
/// ring, as on the app's capsule.
struct PhaseOrb: View {
    let look: PhaseLook
    let size: CGFloat

    var body: some View {
        Image(systemName: look.symbol)
            .font(.system(size: size * 0.45, weight: .semibold))
            .foregroundStyle(look.glyph)
            .frame(width: size, height: size)
            .background(Circle().fill(look.tint))
            .padding(2.5)
            .background(Circle().fill(ActivityPalette.ring))
    }
}

/// The symbol and colours for each phase.
struct PhaseLook {
    let symbol: String
    let tint: Color
    let glyph: Color

    init(_ state: LiveActivityContent) {
        switch state.phase {
        case .listening:
            symbol = "waveform"; tint = ActivityPalette.accent; glyph = ActivityPalette.onAccent
        case .speaking:
            symbol = "waveform"; tint = ActivityPalette.working; glyph = .white
        case .thinking:
            symbol = "ellipsis"; tint = ActivityPalette.thinking; glyph = .white
        case .muted:
            symbol = "waveform"; tint = ActivityPalette.idle; glyph = .white
        case .connecting, .reconnecting:
            symbol = "arrow.triangle.2.circlepath"; tint = ActivityPalette.idle; glyph = .white
        case .locked, .paused, .away:
            symbol = "pause.fill"; tint = ActivityPalette.idle; glyph = .white
        case .noMicrophone, .voiceOff:
            symbol = "mic.slash"; tint = ActivityPalette.idle; glyph = .white
        }
    }
}

/// The app's palette (its asset catalog, which the widget does not ship),
/// in code: a light and a dark value each.
enum ActivityPalette {
    static let card = dynamic(0xFAF8F3, 0x1F2126)
    static let ink = dynamic(0x1F1F22, 0xF5F2EA)
    static let muted = dynamic(0x6B6E75, 0xA3A7AF)
    static let line = dynamic(0xE4E0D7, 0x2E3138)
    static let button = dynamic(0xECE8DF, 0x2E3138)
    static let chip = dynamic(0xECE8DF, 0x2E3138)
    static let ring = dynamic(0xF5F2EA, 0x2E3138)
    static let accent = dynamic(0xD98718, 0xF39C1F)
    static let onAccent = Color(red: 0x1F / 255, green: 0x1F / 255, blue: 0x22 / 255)
    static let waiting = dynamic(0xD99417, 0xF0A92A)
    static let working = dynamic(0x1F5F9E, 0x3D7BC4)
    static let thinking = dynamic(0x6A46B0, 0x8566C9)
    static let idle = dynamic(0x5C6370, 0x5C6370)
    static let danger = dynamic(0xB42318, 0xD9473A)

    private static func dynamic(_ light: UInt32, _ dark: UInt32) -> Color {
        Color(uiColor: UIColor { $0.userInterfaceStyle == .dark ? rgb(dark) : rgb(light) })
    }

    private static func rgb(_ hex: UInt32) -> UIColor {
        UIColor(red: CGFloat((hex >> 16) & 0xFF) / 255, green: CGFloat((hex >> 8) & 0xFF) / 255, blue: CGFloat(hex & 0xFF) / 255, alpha: 1)
    }
}

extension LiveActivityContent {
    /// A pushed update's rows, opened with the push content key the app
    /// stored in the App Group keychain (the widget shares the group).
    func openedWithStoredKey() -> LiveActivityContent {
        sealed == nil ? self : opened(key: PushContentKeyStore().key())
    }
}
