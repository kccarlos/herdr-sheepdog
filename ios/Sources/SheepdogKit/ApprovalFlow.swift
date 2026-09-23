import Foundation

/// The approval card pinned above the controls: which question it shows and
/// where the shepherd's tap is.
///
/// ```
/// shown ──tap──▶ sending ──200──▶ answered (then dismissed)
///   │               ├──409──▶ gone (then dismissed; state refreshed)
///   │               └──error─▶ shown, with the error under the buttons
///   └── pending.confirmed / cancelled / expired / replaced, or a state
///       refresh without it ──▶ answered elsewhere, or dismissed
/// ```
///
/// Pure: the view model feeds it `/api/state`, `/events` and the answer's
/// result and renders `card`.
public struct ApprovalFlow: Equatable, Sendable {
    public enum Phase: Equatable, Sendable {
        /// Waiting for a tap. `error` is the last failed attempt, if any.
        case open(error: String?)
        /// The tap is on its way; the option is the one tapped.
        case sending(PendingOption)
        /// Answered: by this phone (`spoken` is the daemon's reply) or
        /// elsewhere (by voice, on the Mac).
        case answered(outcome: String, spoken: String?)
        /// The daemon said the question was no longer open (409).
        case gone(message: String)
    }

    public struct Card: Equatable, Sendable {
        public var pending: PendingAction
        public var phase: Phase

        /// Buttons are live only while nothing else is in flight.
        public var acceptsTaps: Bool {
            if case .open = phase { return true }
            return false
        }

        /// The card is finished and only waiting to be dismissed.
        public var isSettled: Bool {
            switch phase {
            case .answered, .gone: return true
            case .open, .sending: return false
            }
        }
    }

    public static let goneMessage = "That question was already answered or has gone."

    public private(set) var card: Card?

    public init() {}

    /// What changed, for the caller's haptics and timers.
    public enum Effect: Equatable, Sendable {
        case none
        /// A new question appeared.
        case appeared
        /// The card settled (answered or gone); dismiss it after a moment.
        case settled
        /// The card went away.
        case dismissed
    }

    /// A pending action from `/api/state` (nil: none) or a
    /// `pending.created` event.
    @discardableResult
    public mutating func present(_ pending: PendingAction?) -> Effect {
        guard let pending, pending.isAnswerPrompt else {
            // The state has no question: an open card was resolved
            // elsewhere. A card mid-send, or settled, finishes on its own.
            guard let card, case .open = card.phase else { return .none }
            self.card = nil
            return .dismissed
        }
        if var card, card.pending.id == pending.id {
            // Same question: refresh its words, keep where the tap is.
            card.pending = pending
            self.card = card
            return .none
        }
        if let card, case .sending = card.phase {
            // A new question while ours is in flight: ours finishes first.
            return .none
        }
        card = Card(pending: pending, phase: .open(error: nil))
        return .appeared
    }

    /// The shepherd tapped an option. Returns the option to send, or nil
    /// when the tap must be ignored (a double tap, a settled card).
    public mutating func tap(index: Int) -> PendingOption? {
        guard var card, card.acceptsTaps,
              let option = card.pending.options?.first(where: { $0.index == index })
        else { return nil }
        card.phase = .sending(option)
        self.card = card
        return option
    }

    /// `POST /api/pending/answer` said ok.
    @discardableResult
    public mutating func succeeded(pendingID: String, spoken: String?) -> Effect {
        guard var card, card.pending.id == pendingID, case let .sending(option) = card.phase else { return .none }
        let item = ConversationItem(id: 0, ts: Date(), kind: .approval, text: "", via: "tap", answer: option.label, resolution: .confirmed)
        card.phase = .answered(outcome: item.approvalOutcome ?? "Answered", spoken: spoken?.trimmed.isEmpty == false ? spoken : nil)
        self.card = card
        return .settled
    }

    /// 409: someone (the voice, the Mac) got there first, or it expired.
    @discardableResult
    public mutating func gone(pendingID: String) -> Effect {
        guard var card, card.pending.id == pendingID else { return .none }
        card.phase = .gone(message: ApprovalFlow.goneMessage)
        self.card = card
        return .settled
    }

    /// Any other failure: back to open, with the reason under the buttons.
    public mutating func failed(pendingID: String, message: String) {
        guard var card, card.pending.id == pendingID else { return }
        card.phase = .open(error: message)
        self.card = card
    }

    /// A `pending.confirmed` / `expired` / `cancelled` / `replaced` event
    /// (for `replaced`, the old id).
    @discardableResult
    public mutating func resolved(pendingID: String, as resolution: ConversationItem.Resolution, answer: String? = nil, via: String? = nil) -> Effect {
        guard var card, card.pending.id == pendingID else { return .none }
        switch card.phase {
        case .sending:
            // Our own answer's event can beat the HTTP reply; the reply
            // settles the card.
            if resolution == .confirmed { return .none }
            card.phase = .gone(message: ApprovalFlow.goneMessage)
            self.card = card
            return .settled
        case .answered, .gone:
            return .none
        case .open:
            if resolution == .confirmed {
                let item = ConversationItem(id: 0, ts: Date(), kind: .approval, text: "", via: via, answer: answer, resolution: .confirmed)
                card.phase = .answered(outcome: item.approvalOutcome ?? "Answered", spoken: nil)
                self.card = card
                return .settled
            }
            self.card = nil
            return .dismissed
        }
    }

    /// The settled card's moment is over.
    public mutating func dismiss(pendingID: String) {
        guard let card, card.pending.id == pendingID, card.isSettled else { return }
        self.card = nil
    }

    public mutating func reset() {
        card = nil
    }
}
