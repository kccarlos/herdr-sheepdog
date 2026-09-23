import SheepdogKit
import SwiftUI

/// The bottom bar: Mute, the orb, and End (✕), each in a fixed 56–64 pt
/// slot so a hidden or disabled control never shifts the orb. Replaces the
/// old display-only orb and the Connect/Mute/Stop pill row — stopping now
/// lives on the orb itself (`VoiceBarModel.orbAction`), so there is no
/// separate Stop button any more.
struct VoiceBarView: View {
    let state: SessionState
    let level: Float
    let muted: Bool
    let voiceName: String
    let onConnect: () -> Void
    let onStop: () -> Void
    let onMute: () -> Void
    let onEnd: () -> Void

    private var model: VoiceBarModel { VoiceBarModel(state: state) }

    var body: some View {
        HStack(spacing: 20) {
            muteButton
            orbButton
            endButton
        }
        .padding(.horizontal, 24)
        .frame(height: 72)
        .background(.bar)
    }

    private var muteButton: some View {
        let hidden = model.muteAvailability == .hidden
        return CircleButton(
            systemImage: muted ? "mic.slash.fill" : "mic.fill",
            title: muted ? "Unmute" : "Mute",
            hint: SessionViewModel.muteHint,
            tint: muted ? Color(.systemRed) : Color(.label),
            filled: muted,
            enabled: model.muteAvailability == .enabled,
            action: onMute
        )
        .opacity(hidden ? 0 : 1)
        .allowsHitTesting(!hidden && model.muteAvailability == .enabled)
        .accessibilityHidden(hidden)
        .accessibilityIdentifier("controls.mute")
    }

    private var endButton: some View {
        let visible = model.endVisible
        return Button(action: onEnd) {
            Image(systemName: "xmark")
                .font(.system(size: 20, weight: .semibold))
                .frame(width: 56, height: 56)
                .background(Circle().fill(Color(.systemRed).opacity(0.15)))
                .foregroundStyle(Color(.systemRed))
        }
        .opacity(visible ? 1 : 0)
        .allowsHitTesting(visible)
        .accessibilityHidden(!visible)
        .accessibilityLabel("End session")
        .accessibilityIdentifier("controls.end")
    }

    private var orbButton: some View {
        Button(action: tapOrb) {
            OrbGlyph(state: state, level: level, size: 64)
        }
        .disabled(model.orbAction == .none)
        .accessibilityLabel(orbAccessibilityLabel)
        .accessibilityIdentifier("controls.orb")
    }

    private func tapOrb() {
        switch model.orbAction {
        case .connect: onConnect()
        case .stop: onStop()
        case .none: break
        }
    }

    private var orbAccessibilityLabel: String {
        state == .disconnected ? "Connect" : state.accessibilityLabel(voiceName: voiceName)
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

    @State private var pulse = false
    @Environment(\.accessibilityReduceMotion) private var reduceMotion

    private var tint: Color { Theme.color(for: state) }
    private var showsStopBadge: Bool { state == .thinking || state == .stalled || state == .speaking }
    private var showsSpinner: Bool { state == .thinking || state == .stalled }

    private var ringScale: CGFloat {
        guard state == .listening else { return 1 }
        return 1 + CGFloat(min(1, sqrt(max(0, level)) * 1.6)) * 0.22
    }

    var body: some View {
        ZStack {
            Circle()
                .fill(tint.opacity(0.12))
                .frame(width: size, height: size)
                .scaleEffect(ringScale)
                .animation(.easeOut(duration: 0.12), value: ringScale)

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

struct CircleButton: View {
    let systemImage: String
    let title: String
    var hint: String? = nil
    let tint: Color
    let filled: Bool
    let enabled: Bool
    let action: () -> Void

    var body: some View {
        Button(action: action) {
            Image(systemName: systemImage)
                .font(.system(size: 20, weight: .medium))
                .frame(width: 56, height: 56)
                .background(
                    Circle().fill(filled ? tint.opacity(0.18) : Color(.secondarySystemBackground))
                )
                .overlay(Circle().strokeBorder(filled ? tint.opacity(0.6) : .clear, lineWidth: 1.5))
                .foregroundStyle(enabled ? tint : Color(.tertiaryLabel))
        }
        .disabled(!enabled)
        .accessibilityLabel(title)
        .accessibilityHint(hint ?? "")
    }
}
