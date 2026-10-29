import Foundation

/// Where the floating voice capsule sits on iPad (docs/dev/design/app-redesign.md,
/// "The voice capsule"): one per window, 366 pt wide, centred in the
/// content area, never over the sidebar. Pure geometry; the view animates
/// between frames.
public enum CapsuleFrame {
    /// The capsule's width where there is room.
    public static let width: CGFloat = 366
    /// Under this much content the capsule takes the width less the gutters.
    public static let minimumContent: CGFloat = 390
    /// The gap kept on each side when the content is narrow.
    public static let gutter: CGFloat = 12
    /// The sidebar's width when it shows.
    public static let sidebarWidth: CGFloat = 390

    /// The capsule's width in `content` points of room.
    public static func width(content: CGFloat) -> CGFloat {
        content < minimumContent ? max(content - 2 * gutter, 0) : width
    }

    /// The content area's width: the window less the sidebar when it shows
    /// (beside the detail, or over it in portrait: either way the capsule
    /// stays clear of it).
    public static func content(window: CGFloat, sidebarShown: Bool) -> CGFloat {
        max(window - (sidebarShown ? sidebarWidth : 0), 0)
    }

    /// The capsule's horizontal centre, in the window's coordinates.
    public static func centerX(window: CGFloat, sidebarShown: Bool) -> CGFloat {
        let content = content(window: window, sidebarShown: sidebarShown)
        return (window - content) + content / 2
    }
}
