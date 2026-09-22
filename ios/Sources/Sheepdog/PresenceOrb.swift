import SheepdogKit
import SwiftUI

/// The one thing on the screen that says what is happening, from across the
/// room: a soft disc whose colour, icon and motion follow the session state,
/// with the microphone level as a ring while it is listening.
struct PresenceOrb: View {
    let state: SessionState
    let level: Float
    let voiceName: String

    @State private var pulse = false
    @Environment(\.accessibilityReduceMotion) private var reduceMotion

    private var tint: Color { Theme.color(for: state) }

    private var ringScale: CGFloat {
        guard state == .listening else { return 1 }
        // A little compression so a quiet room still shows life.
        return 1 + CGFloat(min(1, sqrt(max(0, level)) * 1.6)) * 0.22
    }

    var body: some View {
        VStack(spacing: 14) {
            ZStack {
                Circle()
                    .fill(tint.opacity(0.12))
                    .frame(width: 168, height: 168)
                    .scaleEffect(ringScale)
                    .animation(.easeOut(duration: 0.12), value: ringScale)

                Circle()
                    .strokeBorder(tint.opacity(0.35), lineWidth: 2)
                    .frame(width: 128, height: 128)
                    .scaleEffect(breathing ? (pulse ? 1.06 : 0.97) : 1)

                Circle()
                    .fill(tint.opacity(0.22))
                    .frame(width: 110, height: 110)

                Image(systemName: Theme.symbol(for: state))
                    .font(.system(size: 38, weight: .medium))
                    .foregroundStyle(tint)
                    .symbolEffect(.pulse, isActive: breathing && !reduceMotion)
                    .contentTransition(.symbolEffect(.replace))
            }
            .frame(height: 176)

            VStack(spacing: 4) {
                Text(state.title)
                    .font(.headline)
                    .foregroundStyle(.primary)
                if let detail = detail {
                    Text(detail)
                        .font(.footnote)
                        .foregroundStyle(.secondary)
                        .multilineTextAlignment(.center)
                        .padding(.horizontal, 24)
                }
            }
        }
        .accessibilityElement(children: .ignore)
        .accessibilityLabel(state.accessibilityLabel(voiceName: voiceName))
        .accessibilityValue(state == .listening ? "input level \(Int(level * 100)) per cent" : "")
        .onAppear { startPulse() }
        .onChange(of: state) { _, _ in startPulse() }
    }

    private var breathing: Bool {
        switch state {
        case .listening, .speaking, .thinking, .connecting, .reconnecting: return true
        default: return false
        }
    }

    private var detail: String? {
        switch state {
        case .voiceOff: return "The daemon is running without a voice session."
        case .silent: return "Nothing is being sent until you speak."
        case .stalled: return "No reply yet — Stop cancels it."
        case .muted: return "Tap Unmute to be heard again."
        case .disconnected: return nil
        default: return nil
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
