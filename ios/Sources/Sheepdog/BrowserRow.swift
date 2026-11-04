import SheepdogKit
import SwiftUI

/// One row of the browser's lists (a file, a changed file, the space page's
/// Files and Changes): an icon, the text, what goes with it (the counts), and
/// a chevron. The icon sits in a column that grows with the text size, and at
/// the accessibility sizes the row stacks (icon above, text, then the
/// counts) so nothing overlaps and no word breaks to make room.
struct BrowserRow<Icon: View, Content: View, Accessory: View>: View {
    private let chevron: Bool
    private let icon: Icon
    private let content: Content
    private let accessory: Accessory

    @Environment(\.dynamicTypeSize) private var dynamicTypeSize
    @ScaledMetric(relativeTo: .title3) private var iconWidth: CGFloat = 26

    init(
        chevron: Bool = false, @ViewBuilder icon: () -> Icon, @ViewBuilder content: () -> Content,
        @ViewBuilder accessory: () -> Accessory
    ) {
        self.chevron = chevron
        self.icon = icon()
        self.content = content()
        self.accessory = accessory()
    }

    var body: some View {
        if dynamicTypeSize.isAccessibilitySize {
            VStack(alignment: .leading, spacing: 6) {
                HStack(spacing: 8) {
                    icon.frame(width: iconWidth, alignment: .leading)
                    Spacer(minLength: 0)
                    chevronView
                }
                content.frame(maxWidth: .infinity, alignment: .leading)
                accessory
            }
        } else {
            HStack(spacing: 12) {
                icon.frame(width: iconWidth)
                content.frame(maxWidth: .infinity, alignment: .leading)
                accessory
                chevronView
            }
        }
    }

    @ViewBuilder private var chevronView: some View {
        if chevron {
            Image(systemName: "chevron.right")
                .font(.system(size: 13, weight: .semibold))
                .foregroundStyle(Theme.chevron)
                .accessibilityHidden(true)
        }
    }
}

extension BrowserRow where Accessory == EmptyView {
    init(chevron: Bool = false, @ViewBuilder icon: () -> Icon, @ViewBuilder content: () -> Content) {
        self.init(chevron: chevron, icon: icon, content: content, accessory: { EmptyView() })
    }
}

/// "+42 −7" in green and red: a side that is nothing is left out, and the
/// numbers never break across lines.
struct LineCounts: View {
    let added: Int
    let deleted: Int

    var body: some View {
        let parts = LineCountText.parts(added: added, deleted: deleted)
        if !parts.isEmpty {
            HStack(spacing: 6) {
                ForEach(parts, id: \.text) { part in
                    Text(part.text).foregroundStyle(part.isAddition ? DiffColors.added : DiffColors.removed)
                }
            }
            .font(.footnote.monospacedDigit().weight(.medium))
            .lineLimit(1)
            .fixedSize()
            .accessibilityElement(children: .ignore)
            .accessibilityLabel(LineCountText.spoken(added: added, deleted: deleted))
        }
    }
}

/// A soft edge for text that scrolls under a bar: the top few points fade
/// out of the page's colour, so nothing is cut hard at the line.
private struct SoftTopEdge: ViewModifier {
    func body(content: Content) -> some View {
        content.overlay(alignment: .top) {
            LinearGradient(colors: [Theme.surface, Theme.surface.opacity(0)], startPoint: .top, endPoint: .bottom)
                .frame(height: 14)
                .allowsHitTesting(false)
                .accessibilityHidden(true)
        }
    }
}

extension View {
    func softTopEdge() -> some View { modifier(SoftTopEdge()) }
}
