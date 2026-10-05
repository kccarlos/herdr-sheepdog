import SheepdogKit
import SwiftUI

/// The bottom bar, one row across the full width: the mic (mute) button,
/// the orb, the status line beside them, then Type and End (✕). Every
/// control sits in a fixed slot so a hidden one never shifts the others,
/// and the bar's height depends only on the text size, never on the state
/// (a bottom area that grows and shrinks with the state thrashes the
/// conversation's scroll tracking). Stopping the dog lives on the orb
/// itself (`VoiceBarModel.orbAction`).
///
/// The mic button is never a bare microphone that invites "press to talk":
/// while the mic is live it shows the input level as a ring and says
/// "Mute microphone"; muted, it is a red slashed mic — the one place that
/// says "muted" loudly. In hold-to-talk the orb is the talk button and the
/// mic button is gone; with voice off (or no microphone) it is gone too.
struct VoiceBarView: View {
    let state: SessionState
    /// Only this view (and the mic ring/orb it draws) observes the meter,
    /// so a micLevel tick re-renders the bar, not the whole main screen.
    @ObservedObject var meter: VoiceMeter
    let muted: Bool
    let voiceName: String
    var talkMode: TalkMode = .openMic
    var holding = false
    var noSignal = false
    /// What the status line says, and in which colour.
    var statusText = ""
    var statusTint: Color = .secondary
    var typing = false
    var canType = true
    let onConnect: () -> Void
    let onStop: () -> Void
    let onMute: () -> Void
    let onEnd: () -> Void
    var onResume: () -> Void = {}
    var onPress: () -> Void = {}
    var onRelease: () -> Void = {}
    var onType: () -> Void = {}

    /// Grows with Dynamic Type so two lines of status always fit; capped so
    /// the conversation keeps the screen at the largest sizes.
    @ScaledMetric(relativeTo: .footnote) private var scaledHeight: CGFloat = 68

    private var level: Float { meter.micLevel }
    private var model: VoiceBarModel { VoiceBarModel(state: state, talkMode: talkMode) }
    private var barHeight: CGFloat { min(max(scaledHeight, 68), 120) }

    var body: some View {
        HStack(spacing: 10) {
            muteButton
            orbButton
            Text(statusText)
                .font(.footnote.weight(.medium))
                .foregroundStyle(statusTint)
                .lineLimit(3)
                .minimumScaleFactor(0.75)
                .frame(maxWidth: .infinity, alignment: .leading)
                .accessibilityIdentifier("controls.statusLine")
            typeButton
            endButton
        }
        .padding(.horizontal, 12)
        .frame(height: barHeight)
        .frame(maxWidth: .infinity)
        .background(.bar, ignoresSafeAreaEdges: .bottom)
        .overlay(alignment: .top) { Divider() }
    }

    private var muteButton: some View {
        let hidden = model.muteAvailability == .hidden
        let enabled = model.muteAvailability == .enabled
        return MicButton(
            muted: muted,
            level: enabled && !muted ? level : 0,
            live: enabled && !muted,
            noSignal: noSignal,
            enabled: enabled,
            action: onMute
        )
        .opacity(hidden ? 0 : 1)
        .allowsHitTesting(!hidden && enabled)
        .accessibilityHidden(hidden)
        .accessibilityIdentifier("controls.mute")
    }

    /// Type instead of talking: a quiet room, or a test that needs the
    /// exact words.
    private var typeButton: some View {
        Button(action: onType) {
            Image(systemName: typing ? "keyboard.chevron.compact.down" : "keyboard")
                .font(.system(size: 17, weight: .medium))
                .foregroundStyle(typing ? Color.accentColor : (canType ? Color(.secondaryLabel) : Color(.tertiaryLabel)))
                .frame(width: 44, height: 44)
                .contentShape(Rectangle())
        }
        .disabled(!canType)
        .accessibilityLabel(typing ? "Hide the text field" : "Type instead")
        .accessibilityIdentifier("controls.type")
    }

    /// End: the rarest control, so a quiet grey ✕, not the loudest thing
    /// beside the orb.
    private var endButton: some View {
        let visible = model.endVisible
        return Button(action: onEnd) {
            Image(systemName: "xmark")
                .font(.system(size: 15, weight: .semibold))
                .foregroundStyle(Color(.secondaryLabel))
                .frame(width: 44, height: 44)
                .background(Circle().fill(Color(.tertiarySystemFill)))
        }
        .opacity(visible ? 1 : 0)
        .allowsHitTesting(visible)
        .accessibilityHidden(!visible)
        .accessibilityLabel("End session")
        .accessibilityIdentifier("controls.end")
    }

    @ViewBuilder
    private var orbButton: some View {
        if model.orbAction == .holdToTalk {
            HoldToTalkOrb(state: state, level: level, holding: holding, onPress: onPress, onRelease: onRelease)
                .accessibilityIdentifier("controls.orb")
        } else {
            Button(action: tapOrb) {
                OrbGlyph(state: state, level: level, size: 56, muted: muted)
            }
            .disabled(model.orbAction == .none)
            .accessibilityLabel(orbAccessibilityLabel)
            .accessibilityIdentifier("controls.orb")
        }
    }

    private func tapOrb() {
        switch model.orbAction {
        case .connect: onConnect()
        case .stop: onStop()
        case .resume: onResume()
        case .holdToTalk, .none: break
        }
    }

    private var orbAccessibilityLabel: String {
        state == .disconnected ? "Connect" : state.accessibilityLabel(voiceName: voiceName)
    }
}

/// The mic button. Live: the microphone inside a ring that follows the
/// input level, so it reads as "this is on and hearing you". Muted: a red,
/// filled, slashed mic. No signal: an amber ring and a warning badge.
struct MicButton: View {
    let muted: Bool
    let level: Float
    let live: Bool
    var noSignal = false
    let enabled: Bool
    let action: () -> Void

    @Environment(\.accessibilityReduceMotion) private var reduceMotion

    private var ringFraction: CGFloat {
        guard live, !noSignal else { return 0 }
        return CGFloat(min(1, sqrt(max(0, level)) * 1.8))
    }

    private var tint: Color {
        if muted { return Color(.systemRed) }
        if noSignal { return Theme.blocked }
        return enabled ? Color(.label) : Color(.tertiaryLabel)
    }

    var body: some View {
        Button(action: action) {
            ZStack {
                Circle()
                    .fill(muted ? Color(.systemRed).opacity(0.18) : Color(.secondarySystemBackground))
                if muted {
                    Circle().strokeBorder(Color(.systemRed).opacity(0.6), lineWidth: 1.5)
                } else if live {
                    // The level ring: a faint track, and an arc that grows
                    // with the voice.
                    Circle()
                        .strokeBorder(Theme.done.opacity(0.25), lineWidth: 3)
                    Circle()
                        .trim(from: 0, to: max(0.04, ringFraction))
                        .stroke(noSignal ? Theme.blocked : Theme.done, style: StrokeStyle(lineWidth: 3, lineCap: .round))
                        .rotationEffect(.degrees(-90))
                        .padding(1.5)
                        .animation(reduceMotion ? nil : .easeOut(duration: 0.12), value: ringFraction)
                }
                Image(systemName: muted ? "mic.slash.fill" : "mic.fill")
                    .font(.system(size: 20, weight: .medium))
                    .foregroundStyle(tint)
                if noSignal, !muted {
                    Image(systemName: "exclamationmark.circle.fill")
                        .font(.system(size: 15, weight: .bold))
                        .foregroundStyle(Theme.blocked)
                        .background(Circle().fill(Color(.systemBackground)))
                        .offset(x: 18, y: -18)
                }
            }
            .frame(width: 52, height: 52)
        }
        .disabled(!enabled)
        .accessibilityLabel(VoiceBarModel.muteLabel(muted: muted))
        .accessibilityValue(muted ? "Muted" : (noSignal ? "No microphone signal" : "Listening"))
        .accessibilityHint(SessionViewModel.muteHint)
    }
}

/// The orb as a walkie-talkie button: press and hold to talk, release to
/// send. VoiceOver gets a toggle instead of a hold.
struct HoldToTalkOrb: View {
    let state: SessionState
    let level: Float
    let holding: Bool
    let onPress: () -> Void
    let onRelease: () -> Void

    @GestureState private var pressed = false
    @Environment(\.accessibilityReduceMotion) private var reduceMotion

    var body: some View {
        OrbGlyph(state: holding ? .listening : state, level: holding ? level : 0, size: 56)
            .overlay(
                Circle()
                    .strokeBorder(Theme.done, lineWidth: holding ? 3 : 0)
                    .scaleEffect(holding ? 1.12 : 1)
            )
            .scaleEffect(holding ? 1.08 : 1)
            .animation(reduceMotion ? nil : .spring(duration: 0.2), value: holding)
            .contentShape(Circle())
            .gesture(
                DragGesture(minimumDistance: 0)
                    .updating($pressed) { _, pressed, _ in pressed = true }
            )
            .onChange(of: pressed) { _, isPressed in
                isPressed ? onPress() : onRelease()
            }
            .accessibilityElement()
            .accessibilityAddTraits(.isButton)
            .accessibilityLabel(holding ? "Talking. Double-tap to send" : "Hold to talk")
            .accessibilityAction {
                holding ? onRelease() : onPress()
            }
    }
}

/// The orb's pure graphic — colour, icon and motion following the session
/// state, with the microphone level as a ring while listening. Sized for
/// the 64 pt bar button; the old full-size display is gone, its space
/// given back to the conversation and the approval card.
struct OrbGlyph: View {
    let state: SessionState
    let level: Float
    var size: CGFloat = 64
    /// The microphone is muted. Shown as a small red slashed-mic badge
    /// while the orb is speaking or thinking (which outrank "Muted"), never
    /// as a separate orb state.
    var muted = false

    @State private var pulse = false
    @Environment(\.accessibilityReduceMotion) private var reduceMotion

    private var tint: Color { Theme.color(for: state) }
    private var showsStopBadge: Bool { state == .thinking || state == .stalled || state == .speaking }
    private var showsSpinner: Bool { state == .thinking || state == .stalled }

    /// How loud the voice is, 0...1, while listening.
    private var loudness: CGFloat {
        guard state == .listening else { return 0 }
        return CGFloat(min(1, sqrt(max(0, level)) * 1.6))
    }

    /// The halo grows with the voice; under Reduce Motion it stays put and
    /// only gets denser instead.
    private var ringScale: CGFloat { reduceMotion ? 1 : 1 + loudness * 0.22 }
    private var haloOpacity: Double { 0.12 + (reduceMotion ? Double(loudness) * 0.18 : 0) }

    var body: some View {
        ZStack {
            Circle()
                .fill(tint.opacity(haloOpacity))
                .frame(width: size, height: size)
                .scaleEffect(ringScale)
                .animation(reduceMotion ? nil : .easeOut(duration: 0.12), value: ringScale)

            Circle()
                .strokeBorder(tint.opacity(0.35), lineWidth: max(1.5, size / 64))
                .frame(width: size * 0.762, height: size * 0.762)
                .scaleEffect(breathing ? (pulse ? 1.06 : 0.97) : 1)

            Circle()
                .fill(tint.opacity(0.22))
                .frame(width: size * 0.655, height: size * 0.655)

            glyph

            if showsStopBadge {
                stopBadge
            }

            if SessionState.showsMuteBadge(state, muted: muted) {
                muteBadge
            }

            if state == .noMicrophone {
                // The mic button's own "no signal" badge, on the orb: the
                // microphone was refused or would not start.
                Image(systemName: "exclamationmark.circle.fill")
                    .font(.system(size: size * 0.24, weight: .bold))
                    .foregroundStyle(Theme.blocked)
                    .background(Circle().fill(Color(.systemBackground)))
                    .offset(x: size * 0.32, y: -size * 0.32)
                    .accessibilityHidden(true)
            }
        }
        .frame(width: size, height: size)
        .onAppear { startPulse() }
        .onChange(of: state) { _, _ in startPulse() }
    }

    @ViewBuilder
    private var glyph: some View {
        if showsSpinner {
            ProgressView()
                .tint(tint)
        } else {
            Image(systemName: Theme.symbol(for: state))
                .font(.system(size: size * 0.226, weight: .medium))
                .foregroundStyle(tint)
                .symbolEffect(.pulse, isActive: breathing && !reduceMotion)
                .contentTransition(.symbolEffect(.replace))
        }
    }

    private var stopBadge: some View {
        Image(systemName: "stop.fill")
            .font(.system(size: size * 0.14, weight: .bold))
            .foregroundStyle(.white)
            .padding(size * 0.09)
            .background(Circle().fill(tint))
            .offset(x: size * 0.32, y: size * 0.32)
    }

    private var muteBadge: some View {
        Image(systemName: "mic.slash.fill")
            .font(.system(size: size * 0.14, weight: .bold))
            .foregroundStyle(.white)
            .padding(size * 0.08)
            .background(Circle().fill(Color(.systemRed)))
            .offset(x: -size * 0.32, y: size * 0.32)
            .accessibilityIdentifier("orb.muteBadge")
            .accessibilityLabel("Microphone muted")
            .transition(.opacity)
    }

    private var breathing: Bool {
        switch state {
        case .listening, .speaking, .thinking, .connecting, .reconnecting: return true
        default: return false
        }
    }

    private func startPulse() {
        guard breathing, !reduceMotion else {
            pulse = false
            return
        }
        pulse = false
        withAnimation(.easeInOut(duration: 1.3).repeatForever(autoreverses: true)) {
            pulse = true
        }
    }
}
