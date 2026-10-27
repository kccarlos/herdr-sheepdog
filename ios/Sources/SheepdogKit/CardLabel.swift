import Foundation

/// The small corner label that says "a card is waiting" when a card
/// arrives while the app is on screen but the conversation is not in
/// front (another screen, an agent's window, a sheet over it). The card
/// itself stays in the conversation (`CardInbox`); the label only points
/// at it for a few seconds. Pure state: the view shows `current`, and the
/// session schedules `expire(generation:)` after `duration`.
public struct CardLabelState: Equatable, Sendable {
    /// How long a label stays up.
    public static let duration: TimeInterval = 5

    public private(set) var current: ScreenCard?
    /// Bumped by every `show`, so an old timer cannot hide a newer label.
    public private(set) var generation = 0

    public init() {}

    /// A card arrived as a row only. Shows the label when the app is
    /// active; a newer card replaces it. Returns the generation to expire,
    /// or nil when nothing was shown.
    @discardableResult
    public mutating func show(_ card: ScreenCard, appActive: Bool) -> Int? {
        guard appActive else { return nil }
        current = card
        generation += 1
        return generation
    }

    /// The timer of `generation` fired: hides the label unless a newer
    /// one replaced it.
    public mutating func expire(generation: Int) {
        if generation == self.generation { current = nil }
    }

    /// Hide now (the card opened, the conversation came to the front, the
    /// app left the screen).
    public mutating func hide() {
        current = nil
    }

    /// What VoiceOver hears when the label appears.
    public static func announcement(for card: ScreenCard) -> String {
        "Card ready: \(card.displayTitle)"
    }
}
