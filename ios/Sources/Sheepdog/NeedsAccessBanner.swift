import SheepdogKit
import SwiftUI

/// A calm inline note on New agent and Spaces: macOS has not let the
/// daemon into a folder (`needs_access` of `GET /api/spaces`). Not an error
/// screen: the spaces that did load stay below it. One sentence per folder
/// and a Retry that fetches again once the user has allowed it.
struct NeedsAccessBanner: View {
    let items: [SpacesView.NeedsAccess]
    let retry: () -> Void
    @ScaledMetric(relativeTo: .subheadline) private var disc: CGFloat = 28

    private var tint: Color { Theme.blockedText }

    var body: some View {
        HStack(alignment: .top, spacing: 10) {
            Image(systemName: "lock")
                .font(.footnote.weight(.bold))
                .foregroundStyle(tint)
                .frame(width: min(disc, 36), height: min(disc, 36))
                .background(Circle().fill(tint.opacity(0.14)))
                .accessibilityHidden(true)
            VStack(alignment: .leading, spacing: 6) {
                ForEach(items, id: \.self) { item in
                    Text(item.message)
                        .font(.subheadline)
                        .foregroundStyle(Theme.ink)
                        .fixedSize(horizontal: false, vertical: true)
                }
                Button(action: retry) {
                    Text("Retry")
                        .font(.subheadline.weight(.semibold))
                        .foregroundStyle(Theme.ink)
                        .padding(.horizontal, 16)
                        .frame(minHeight: 44)
                        .background(Capsule().strokeBorder(Theme.line, lineWidth: 1))
                        .contentShape(Capsule())
                }
                .buttonStyle(.plain)
                .accessibilityHint("Checks again whether your computer allows access")
                .accessibilityIdentifier("needsAccess.retry")
            }
            .padding(.top, 3)
            Spacer(minLength: 0)
        }
        .padding(12)
        .background(RoundedRectangle(cornerRadius: Theme.Radius.card, style: .continuous).fill(Theme.card))
        .overlay(RoundedRectangle(cornerRadius: Theme.Radius.card, style: .continuous).strokeBorder(tint.opacity(0.45), lineWidth: 1))
        .accessibilityElement(children: .contain)
        .accessibilityLabel("Folder access needed")
        .accessibilityIdentifier("needsAccess.banner")
    }
}

extension View {
    /// The banner above an empty state, when the daemon reported folders
    /// it cannot read.
    @ViewBuilder func needsAccessAbove(_ items: [SpacesView.NeedsAccess], retry: @escaping () -> Void) -> some View {
        if items.isEmpty {
            self
        } else {
            VStack(spacing: 0) {
                NeedsAccessBanner(items: items, retry: retry)
                    .padding(.horizontal, 16)
                    .padding(.top, 8)
                self
            }
        }
    }
}
