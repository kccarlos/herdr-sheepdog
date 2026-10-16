import ActivityKit
import AppIntents
import SwiftUI
import WidgetKit

/// The voice session on the lock screen and in the Dynamic Island: what
/// the voice is doing (listening, speaking, muted…), how many agents are
/// waiting for an answer, and Mute / End. The words, VoiceOver labels
/// included, come ready-made in the content state
/// (SheepdogKit `VoiceActivityState`); this file only draws them.
struct VoiceSessionLiveActivity: Widget {
    var body: some WidgetConfiguration {
        ActivityConfiguration(for: VoiceSessionAttributes.self) { context in
            VoiceSessionLockScreenView(state: context.state, voiceName: context.attributes.voiceName, isStale: context.isStale)
                // The system background, not a tint of our own: white in
                // light mode, black in dark, like Heeler's banner.
                .activityBackgroundTint(Color(uiColor: .systemBackground))
                .activitySystemActionForegroundColor(Color(uiColor: .label))
        } dynamicIsland: { context in
            let state = context.state
            let look = PhaseLook(state)
            return DynamicIsland {
                DynamicIslandExpandedRegion(.leading) {
                    PhaseBadge(look: look, size: 44)
                        .accessibilityHidden(true)
                }
                DynamicIslandExpandedRegion(.center) {
                    VStack(alignment: .leading, spacing: 2) {
                        Text(state.title)
                            .font(.headline)
                            .lineLimit(1)
                        Text(state.waitingText ?? context.attributes.voiceName)
                            .font(.caption)
                            .foregroundStyle(state.waiting > 0 ? PhaseLook.waitingInk : .secondary)
                            .lineLimit(1)
                    }
                    .frame(maxWidth: .infinity, alignment: .leading)
                    .accessibilityElement(children: .ignore)
                    .accessibilityLabel(context.isStale ? VoiceSessionLockScreenView.staleLabel : state.accessibilityLabel)
                }
                DynamicIslandExpandedRegion(.trailing) {
                    if state.waiting > 0 {
                        WaitingBadge(count: state.waiting)
                            .accessibilityHidden(true)
                    }
                }
                DynamicIslandExpandedRegion(.bottom) {
                    SessionButtons(muted: state.muted)
                        .padding(.top, 4)
                }
            } compactLeading: {
                Image(systemName: look.symbol)
                    .foregroundStyle(look.tint)
                    .accessibilityLabel(state.compactAccessibilityLabel)
            } compactTrailing: {
                Group {
                    if state.waiting > 0 {
                        Text("\(state.waiting)")
                            .font(.caption.weight(.bold).monospacedDigit())
                            .foregroundStyle(PhaseLook.waitingInk)
                    } else {
                        Image(systemName: state.muted ? "mic.slash.fill" : "mic.fill")
                            .foregroundStyle(state.muted ? Color.red : Color.secondary)
                    }
                }
                .accessibilityHidden(true)
            } minimal: {
                Group {
                    if state.waiting > 0 {
                        Text("\(state.waiting)")
                            .font(.caption.weight(.bold).monospacedDigit())
                            .foregroundStyle(PhaseLook.waitingInk)
                    } else {
                        Image(systemName: look.symbol)
                            .foregroundStyle(look.tint)
                    }
                }
                .accessibilityLabel(state.compactAccessibilityLabel)
            }
            .keylineTint(state.waiting > 0 ? PhaseLook.waitingInk : look.tint)
        }
    }
}

struct VoiceSessionLockScreenView: View {
    let state: VoiceSessionAttributes.ContentState
    let voiceName: String
    let isStale: Bool

    static let staleLabel = "The voice session may have ended. Open Sheepdog to check."

    var body: some View {
        let look = PhaseLook(state)
        HStack(spacing: 12) {
            PhaseBadge(look: look, size: 44)
                .opacity(isStale ? 0.5 : 1)
                .accessibilityHidden(true)
            VStack(alignment: .leading, spacing: 2) {
                Text(isStale ? "May have ended" : state.title)
                    .font(.headline)
                    .foregroundStyle(Color(uiColor: .label))
                    .lineLimit(1)
                Text(isStale ? "Open \(voiceName) to check" : state.waitingText ?? voiceName)
                    .font(.subheadline)
                    .foregroundStyle(!isStale && state.waiting > 0 ? PhaseLook.waitingInk : Color(uiColor: .secondaryLabel))
                    .lineLimit(2)
            }
            .accessibilityElement(children: .ignore)
            .accessibilityLabel(isStale ? Self.staleLabel : state.accessibilityLabel)
            Spacer(minLength: 4)
            SessionButtons(muted: state.muted)
        }
        .padding(.horizontal, 16)
        .padding(.vertical, 14)
    }
}

/// Mute / Unmute and End, run in the app's process (`LiveActivityIntent`).
struct SessionButtons: View {
    let muted: Bool

    var body: some View {
        HStack(spacing: 10) {
            Button(intent: SetVoiceMuteIntent(muted: !muted)) {
                Image(systemName: muted ? "mic.slash.fill" : "mic.fill")
                    .font(.body.weight(.semibold))
                    .frame(width: 44, height: 44)
                    .foregroundStyle(muted ? Color.white : Color(uiColor: .label))
                    .background(Circle().fill(muted ? Color.red : Color(uiColor: .tertiarySystemFill)))
            }
            .buttonStyle(.plain)
            .accessibilityLabel(muted ? "Unmute" : "Mute")
            .accessibilityHint(muted ? "Turns the microphone back on" : "Turns the microphone off; Sheepdog can still talk")

            Button(intent: EndVoiceSessionIntent()) {
                Image(systemName: "xmark")
                    .font(.body.weight(.bold))
                    .frame(width: 44, height: 44)
                    .foregroundStyle(Color.white)
                    .background(Circle().fill(Color.red.opacity(0.9)))
            }
            .buttonStyle(.plain)
            .accessibilityLabel("End session")
            .accessibilityHint("Ends the voice session with Sheepdog")
        }
    }
}

struct PhaseBadge: View {
    let look: PhaseLook
    let size: CGFloat

    var body: some View {
        Image(systemName: look.symbol)
            .font(.system(size: size * 0.42, weight: .semibold))
            .foregroundStyle(look.tint)
            .frame(width: size, height: size)
            .background(Circle().fill(look.tint.opacity(0.16)))
    }
}

struct WaitingBadge: View {
    let count: Int

    var body: some View {
        HStack(spacing: 3) {
            Image(systemName: "person.fill.questionmark")
            Text("\(count)").monospacedDigit()
        }
        .font(.caption.weight(.bold))
        .foregroundStyle(PhaseLook.waitingInk)
        .padding(.horizontal, 8)
        .padding(.vertical, 4)
        .background(Capsule().fill(PhaseLook.waitingInk.opacity(0.18)))
    }
}

/// The symbol and colour for each phase. The app's amber "blocked" colour
/// marks agents waiting on the user here too.
struct PhaseLook {
    let symbol: String
    let tint: Color

    static let waitingInk = Color(red: 0.96, green: 0.62, blue: 0.04)

    init(_ state: VoiceSessionAttributes.ContentState) {
        switch state.phase {
        case "connecting":
            symbol = "antenna.radiowaves.left.and.right"; tint = .gray
        case "speaking":
            symbol = "speaker.wave.2.fill"; tint = .blue
        case "thinking":
            symbol = "ellipsis"; tint = .purple
        case "muted":
            symbol = "mic.slash.fill"; tint = .red
        case "paused":
            symbol = "pause.fill"; tint = .orange
        default:
            symbol = "waveform"; tint = .green
        }
    }
}
