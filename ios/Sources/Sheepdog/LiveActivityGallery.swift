#if DEBUG
import SwiftUI

/// `-sheepdogLiveActivityGallery`: the Live Activity's views drawn inside
/// the app, for screenshots (a headless simulator neither locks nor
/// draws the Dynamic Island). The banner is the widget's own view; the
/// island is a stand-in shape around the widget's own pieces.
struct LiveActivityGallery: View {
    private let states: [VoiceSessionAttributes.ContentState] = [
        .init(phase: "listening", muted: false, waiting: 2, title: "Listening", waitingText: "2 agents waiting for an answer",
              accessibilityLabel: "", compactAccessibilityLabel: ""),
        .init(phase: "speaking", muted: false, waiting: 0, title: "Speaking", waitingText: nil,
              accessibilityLabel: "", compactAccessibilityLabel: ""),
        .init(phase: "muted", muted: true, waiting: 1, title: "Muted", waitingText: "1 agent waiting for an answer",
              accessibilityLabel: "", compactAccessibilityLabel: ""),
    ]

    var body: some View {
        ScrollView {
            VStack(alignment: .leading, spacing: 14) {
                Text("Lock screen").font(.caption.weight(.semibold)).foregroundStyle(.secondary)
                ForEach(states, id: \.self) { state in
                    VoiceSessionLockScreenView(state: state, voiceName: "Sheepdog", isStale: false)
                        .background(Color(uiColor: .systemBackground), in: RoundedRectangle(cornerRadius: 22, style: .continuous))
                        .overlay(RoundedRectangle(cornerRadius: 22, style: .continuous).strokeBorder(Color(.separator), lineWidth: 0.5))
                }
                VoiceSessionLockScreenView(state: states[0], voiceName: "Sheepdog", isStale: true)
                    .background(Color(uiColor: .systemBackground), in: RoundedRectangle(cornerRadius: 22, style: .continuous))
                    .overlay(RoundedRectangle(cornerRadius: 22, style: .continuous).strokeBorder(Color(.separator), lineWidth: 0.5))
                Text("Dynamic Island (stand-in shape)").font(.caption.weight(.semibold)).foregroundStyle(.secondary)
                ForEach(states, id: \.self) { state in compact(state) }
                expanded(states[0])
            }
            .padding(16)
        }
        .background(Color(uiColor: .secondarySystemBackground))
    }

    private func compact(_ state: VoiceSessionAttributes.ContentState) -> some View {
        let look = PhaseLook(state)
        return HStack {
            Image(systemName: look.symbol).foregroundStyle(look.tint)
            Spacer()
            if state.waiting > 0 {
                Text("\(state.waiting)").font(.caption.weight(.bold).monospacedDigit()).foregroundStyle(PhaseLook.waitingInk)
            } else {
                Image(systemName: state.muted ? "mic.slash.fill" : "mic.fill").foregroundStyle(state.muted ? Color.red : Color.secondary)
            }
        }
        .padding(.horizontal, 16)
        .frame(width: 250, height: 37)
        .background(Capsule().fill(.black))
        .environment(\.colorScheme, .dark)
        .frame(maxWidth: .infinity)
    }

    private func expanded(_ state: VoiceSessionAttributes.ContentState) -> some View {
        let look = PhaseLook(state)
        return VStack(spacing: 10) {
            HStack(spacing: 12) {
                PhaseBadge(look: look, size: 44)
                VStack(alignment: .leading, spacing: 2) {
                    Text(state.title).font(.headline)
                    Text(state.waitingText ?? "Sheepdog").font(.caption)
                        .foregroundStyle(state.waiting > 0 ? PhaseLook.waitingInk : .secondary)
                }
                Spacer()
                if state.waiting > 0 { WaitingBadge(count: state.waiting) }
            }
            SessionButtons(muted: state.muted)
        }
        .padding(18)
        .background(RoundedRectangle(cornerRadius: 40, style: .continuous).fill(.black))
        .environment(\.colorScheme, .dark)
    }
}
#endif
