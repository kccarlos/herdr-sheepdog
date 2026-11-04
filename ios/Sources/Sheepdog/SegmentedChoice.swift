import SwiftUI

/// A segmented choice in the app's own look (`PaneSwitcher`'s): a track with
/// the chosen segment raised. Built from buttons so a segment can carry a
/// count, and so none has to be chosen (an image that is neither Fit nor
/// 100%). At the accessibility text sizes, where segments no longer fit, it
/// is a menu.
struct SegmentedChoice<Value: Hashable & Identifiable>: View {
    let options: [Value]
    /// Nil when none of them describes the state.
    let selected: Value?
    var title: (Value) -> String
    /// A quiet word after the title ("6" on Changes).
    var detail: (Value) -> String? = { _ in nil }
    /// What VoiceOver says, when it is more than the title.
    var spoken: (Value) -> String? = { _ in nil }
    /// The prefix of each segment's identifier: `<prefix>.<id>`.
    let identifier: String
    var accessibilityName: String
    /// Segments share the width (the Files | Changes switch) or take what
    /// they need.
    var fills = false
    let onSelect: (Value) -> Void

    @Environment(\.dynamicTypeSize) private var dynamicTypeSize

    var body: some View {
        Group {
            if dynamicTypeSize.isAccessibilitySize { menu } else { segments }
        }
        .accessibilityElement(children: .contain)
        .accessibilityLabel(accessibilityName)
        .accessibilityIdentifier(identifier)
    }

    private var segments: some View {
        HStack(spacing: 2) {
            ForEach(options) { option in
                let isSelected = option == selected
                Button { onSelect(option) } label: {
                    label(option)
                        .font(.subheadline.weight(isSelected ? .semibold : .medium))
                        .foregroundStyle(Theme.ink)
                        .padding(.horizontal, 12)
                        .frame(maxWidth: fills ? .infinity : nil, minHeight: 36)
                        .background {
                            if isSelected {
                                RoundedRectangle(cornerRadius: 9, style: .continuous)
                                    .fill(Theme.selected)
                            }
                        }
                        .contentShape(Rectangle())
                }
                .buttonStyle(.plain)
                .accessibilityLabel(spoken(option) ?? title(option))
                .accessibilityAddTraits(isSelected ? .isSelected : [])
                .accessibilityIdentifier("\(identifier).\(option.id)")
            }
        }
        .padding(3)
        .background(RoundedRectangle(cornerRadius: 12, style: .continuous).fill(Theme.track))
    }

    private var menu: some View {
        Menu {
            ForEach(options) { option in
                Button {
                    onSelect(option)
                } label: {
                    if option == selected {
                        Label(spoken(option) ?? title(option), systemImage: "checkmark")
                    } else {
                        Text(spoken(option) ?? title(option))
                    }
                }
                .accessibilityIdentifier("\(identifier).\(option.id)")
            }
        } label: {
            HStack(spacing: 8) {
                Text(menuTitle)
                    .fixedSize(horizontal: false, vertical: true)
                    .frame(maxWidth: fills ? .infinity : nil, alignment: .leading)
                Image(systemName: "chevron.up.chevron.down").font(.footnote.weight(.semibold))
            }
            // A calmer size than the screen's text: the chosen word and its
            // count, in a bar, not a block.
            .font(.subheadline.weight(.semibold))
            .foregroundStyle(Theme.ink)
            .padding(.horizontal, 14)
            .frame(maxWidth: fills ? .infinity : nil, minHeight: 44)
            .background(RoundedRectangle(cornerRadius: 12, style: .continuous).fill(Theme.track))
        }
    }

    /// "Changes · 5": the chosen choice with its count, as the segments show it.
    private var menuTitle: String {
        guard let selected else { return accessibilityName }
        return [title(selected), detail(selected)].compactMap { $0 }.joined(separator: " · ")
    }

    private func label(_ option: Value) -> some View {
        HStack(spacing: 6) {
            Text(title(option)).lineLimit(1).minimumScaleFactor(0.8)
            if let detail = detail(option) {
                Text(detail)
                    .foregroundStyle(Theme.muted)
                    .monospacedDigit()
                    .lineLimit(1)
            }
        }
    }
}
