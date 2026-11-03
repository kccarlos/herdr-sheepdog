import Foundation

/// How tall the floating voice capsule may get. It grows with the text
/// size, but past a cap it would cover the content it floats over, so the
/// name and status scroll inside it instead. At the accessibility sizes the
/// view also stops the capsule's own text at the largest regular size, and
/// the cap is lower: the controls stay one compact row at the largest size.
public enum CapsuleSizing {
    /// The capsule's height at the default text size.
    public static let baseHeight: CGFloat = 60
    /// The tallest it gets at a regular size.
    public static let regularCap: CGFloat = 150
    /// The tallest it gets at an accessibility size.
    public static let accessibilityCap: CGFloat = 96

    public static func cap(accessibility: Bool) -> CGFloat {
        accessibility ? accessibilityCap : regularCap
    }

    /// The least the row measures: the scaled base height, never under
    /// `baseHeight` nor over the cap. At an accessibility size the text no
    /// longer grows, so neither does the minimum.
    public static func minimumHeight(scaled: CGFloat, accessibility: Bool) -> CGFloat {
        accessibility ? baseHeight : min(max(scaled, baseHeight), regularCap)
    }

    /// The capsule's height for a row that measured `row` points.
    public static func height(scaled: CGFloat, row: CGFloat, accessibility: Bool) -> CGFloat {
        min(max(row, minimumHeight(scaled: scaled, accessibility: accessibility)), cap(accessibility: accessibility))
    }

    /// The row is taller than the capsule can be: it scrolls inside.
    public static func scrolls(row: CGFloat, accessibility: Bool) -> Bool {
        row > cap(accessibility: accessibility)
    }
}
