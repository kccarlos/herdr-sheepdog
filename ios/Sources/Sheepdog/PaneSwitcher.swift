import SwiftUI

/// The three panes of the main screen.
enum MainPane: String, CaseIterable, Identifiable {
    case conversation = "Conversation"
    case flock = "Agents"
    var id: String { rawValue }
}

/// Conversation | Agents, the top bar's left half. A segmented look, but built from buttons
/// so the Agents pane can carry an amber badge for the agents waiting on you
/// (a segmented `Picker` draws its segments itself, in one colour); at the
/// accessibility text sizes, where the segments no longer fit, a menu. Health
/// is not a pane: it lives behind the connection chip and in Settings.
struct PaneSwitcher: View {
    @Binding var pane: MainPane
    /// Agents waiting on the user (blocked): the amber badge.
    let waiting: Int

    @Environment(\.dynamicTypeSize) private var dynamicTypeSize

    var body: some View {
        Group {
            if dynamicTypeSize.isAccessibilitySize {
                menu
            } else {
                segments
            }
        }
        .accessibilityElement(children: .contain)
        .accessibilityIdentifier("main.pane")
    }

    private var segments: some View {
        HStack(spacing: 2) {
            ForEach(MainPane.allCases) { option in
                Button {
                    pane = option
                } label: {
                    label(for: option)
                        .font(.subheadline.weight(pane == option ? .semibold : .medium))
                        .foregroundStyle(Theme.ink)
                        .padding(.horizontal, 13)
                        .frame(minHeight: 38)
                        .background {
                            if pane == option {
                                RoundedRectangle(cornerRadius: 9, style: .continuous)
                                    .fill(Theme.selected)
                                    .shadow(color: .black.opacity(0.12), radius: 1, y: 1)
                            }
                        }
                        .contentShape(Rectangle())
                }
                .buttonStyle(.plain)
                .accessibilityLabel(accessibilityLabel(for: option))
                .accessibilityAddTraits(pane == option ? .isSelected : [])
                .accessibilityIdentifier("main.pane.\(option)")
            }
        }
        .padding(3)
        .background(RoundedRectangle(cornerRadius: 12, style: .continuous).fill(Theme.track))
    }

    private var menu: some View {
        Menu {
            ForEach(MainPane.allCases) { option in
                Button {
                    pane = option
                } label: {
                    Text(accessibilityLabel(for: option))
                }
                .accessibilityIdentifier("main.pane.\(option)")
            }
        } label: {
            HStack(spacing: 8) {
                label(for: pane)
                Image(systemName: "chevron.up.chevron.down")
                    .font(.footnote.weight(.semibold))
            }
            .font(.headline)
            .foregroundStyle(Theme.ink)
            .padding(.horizontal, 14)
            .frame(minHeight: 44)
            .background(RoundedRectangle(cornerRadius: 12, style: .continuous).fill(Theme.track))
        }
        .accessibilityLabel("Show: \(accessibilityLabel(for: pane))")
    }

    @ViewBuilder
    private func label(for option: MainPane) -> some View {
        HStack(spacing: 5) {
            Text(option.rawValue)
                .lineLimit(1)
                .minimumScaleFactor(0.8)
            switch option {
            case .flock where waiting > 0:
                Badge(count: waiting, fill: .accentColor, text: Theme.onAccent)
                    .contentTransition(.numericText())
            default:
                EmptyView()
            }
        }
    }

    private func accessibilityLabel(for option: MainPane) -> String {
        switch option {
        case .conversation: return "Conversation"
        case .flock: return waiting == 0 ? "Agents" : "Agents, \(waiting) waiting on you"
        }
    }
}

/// A small count in a capsule.
struct Badge: View {
    let count: Int
    let fill: Color
    let text: Color

    var body: some View {
        Text("\(count)")
            .font(.caption.weight(.bold).monospacedDigit())
            .foregroundStyle(text)
            .padding(.horizontal, 6)
            .padding(.vertical, 1)
            .background(Capsule().fill(fill))
    }
}
