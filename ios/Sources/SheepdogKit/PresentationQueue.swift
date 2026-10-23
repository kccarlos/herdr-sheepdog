import Foundation

/// Which sheet or cover is up, and what waits for it to close: the pure
/// part of the app's `PresentationCoordinator`
/// (docs/dev/design/ios-state-architecture.md, 3.6).
///
/// - One item at a time. Asking for another while one is up, or while one
///   is still sliding away, queues it; the next one is shown when the
///   presentation reports its dismissal (`didDismiss`), never after a
///   guessed delay, so a slow device or Reduce Motion cannot drop it.
/// - Identity comes from the item (`id`), never minted when it is built:
///   asking twice for the same id is a no-op that only refreshes its
///   payload, so a re-render can never close and reopen a sheet.
/// - Work that must wait until the screen is clear (push an agent's window
///   after a card closes) is queued with `dismiss(then:)` and handed back
///   by `didDismiss`.
///
/// The caller runs the returned follow-ups; this type does no I/O.
public struct PresentationQueue<Item: Identifiable & Equatable, Followup> where Item.ID: Hashable {
    /// What the presentation shows now; nil while nothing is up or the
    /// last item is closing.
    public private(set) var current: Item?
    /// Waiting their turn, oldest first.
    public private(set) var pending: [Item] = []
    /// The item taken down whose dismissal has not been reported yet.
    public private(set) var closing: Item?
    /// The current item as the presentation reported it on screen
    /// (`appeared`). One that never appeared (its window was not the
    /// presenter, the splash held it back) has no dismissal to wait for.
    private var shown: Item.ID?
    private var after: [Followup] = []

    public init() {}

    /// Nothing is up, closing or waiting.
    public var isIdle: Bool { current == nil && closing == nil && pending.isEmpty }

    /// Asks for `item`. Shown at once when the screen is clear, else queued.
    /// The same id again (up, or queued) only refreshes its payload.
    public mutating func present(_ item: Item) {
        if current?.id == item.id {
            current = item
            return
        }
        if let i = pending.firstIndex(where: { $0.id == item.id }) {
            pending[i] = item
            return
        }
        if current == nil, closing == nil {
            current = item
        } else {
            pending.append(item)
        }
    }

    /// Shows `item` next, closing what is up first (a pairing link while
    /// Settings is open). With nothing up it is `present`.
    @discardableResult
    public mutating func presentInstead(_ item: Item) -> [Followup] {
        guard let current, current.id != item.id else {
            present(item)
            return []
        }
        pending.removeAll { $0.id == item.id }
        pending.insert(item, at: 0)
        return dismiss(current.id)
    }

    /// Shows `item` in place of what is up, without waiting for a
    /// dismissal (a newer card replacing the card on screen: the
    /// presentation swaps them itself). With nothing up it is `present`.
    public mutating func replace(_ item: Item) {
        guard current != nil, closing == nil else {
            present(item)
            return
        }
        current = item
        shown = nil
    }

    /// The presentation put `id` on screen.
    public mutating func appeared(_ id: Item.ID) {
        if current?.id == id { shown = id }
    }

    /// Takes `id` down: the current item, or a queued one. Without an id,
    /// whatever is up. Returns the follow-ups to run now, when nothing was
    /// on screen to wait for.
    @discardableResult
    public mutating func dismiss(_ id: Item.ID? = nil) -> [Followup] {
        guard let current else {
            if let id { pending.removeAll { $0.id == id } }
            return []
        }
        if let id, id != current.id {
            pending.removeAll { $0.id == id }
            return []
        }
        self.current = nil
        closing = current
        if shown == current.id { return [] }
        // Never on screen: nothing will report a dismissal.
        return didDismiss()
    }

    /// Takes down whatever is up (if anything) and runs `followup` once the
    /// screen is clear. Returns the follow-ups to run now.
    @discardableResult
    public mutating func dismiss(then followup: Followup) -> [Followup] {
        after.append(followup)
        if current != nil { return dismiss() }
        if closing != nil { return [] }
        return takeAfter()
    }

    /// The presentation finished closing. Shows the next queued item and
    /// returns the follow-ups that waited for a clear screen. A dismissal
    /// nobody asked for (a swap by `replace`, or a window that stopped
    /// presenting while its item stays current) changes nothing.
    @discardableResult
    public mutating func didDismiss() -> [Followup] {
        guard closing != nil else { return [] }
        closing = nil
        shown = nil
        let ready = takeAfter()
        if !pending.isEmpty { current = pending.removeFirst() }
        return ready
    }

    private mutating func takeAfter() -> [Followup] {
        defer { after = [] }
        return after
    }
}
