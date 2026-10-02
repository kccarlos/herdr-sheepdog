import SwiftUI

/// The three panes of the main screen.
enum MainPane: String, CaseIterable, Identifiable {
    case conversation = "Conversation"
    case flock = "Flock"
    case health = "Health"
    var id: String { rawValue }
}

/// Conversation / Flock / Health. A segmented look, but built from buttons
/// so the Flock pane can carry an amber badge for the agents waiting on you
/// (a segmented `Picker` draws its segments itself, in one colour); at the
/// accessibility text sizes, where three segments no longer fit, a menu.
struct PaneSwitcher: View {
    @Binding var pane: MainPane
    /// Agents waiting on the shepherd (blocked): the amber badge.
    let waiting: Int
    /// Recent errors on the daemon: the Health badge.
    let errors: Int

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
                        .font(.subheadline.weight(pane == option ? .semibold : .regular))
                        .foregroundStyle(Color(.label))
                        .frame(maxWidth: .infinity, minHeight: 44)
                        .background {
                            if pane == option {
                                Capsule()
                                    .fill(Color(.systemBackground))
                                    .shadow(color: .black.opacity(0.08), radius: 2, y: 1)
                            }
                        }
                        .contentShape(Capsule())
                }
                .buttonStyle(.plain)
                .accessibilityLabel(accessibilityLabel(for: option))
                .accessibilityAddTraits(pane == option ? .isSelected : [])
                .accessibilityIdentifier("main.pane.\(option.rawValue.lowercased())")
            }
        }
        .padding(3)
        .background(Capsule().fill(Color(.tertiarySystemFill)))
    }

    private var menu: some View {
        Menu {
            ForEach(MainPane.allCases) { option in
                Button {
                    pane = option
                } label: {
                    Text(accessibilityLabel(for: option))
                }
                .accessibilityIdentifier("main.pane.\(option.rawValue.lowercased())")
            }
        } label: {
            HStack(spacing: 8) {
                label(for: pane)
                Image(systemName: "chevron.up.chevron.down")
                    .font(.footnote.weight(.semibold))
            }
            .font(.headline)
            .foregroundStyle(Color(.label))
            .frame(maxWidth: .infinity, minHeight: 44)
            .background(Capsule().fill(Color(.tertiarySystemFill)))
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
                Badge(count: waiting, fill: Theme.blocked, text: Theme.onBlocked)
            case .health where errors > 0:
                Badge(count: errors, fill: Color(.systemRed), text: .white)
            default:
                EmptyView()
            }
        }
    }

    private func accessibilityLabel(for option: MainPane) -> String {
        switch option {
        case .conversation: return "Conversation"
        case .flock: return waiting == 0 ? "Flock" : "Flock, \(waiting) waiting on you"
        case .health: return errors == 0 ? "Health" : "Health, \(errors) \(errors == 1 ? "error" : "errors")"
        }
    }
}

/// A small count in a capsule.
private struct Badge: View {
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
