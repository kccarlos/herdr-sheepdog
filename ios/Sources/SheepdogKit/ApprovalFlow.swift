import Foundation

/// One computer's open questions as the app answers them: each question's
/// phase, and which one the voice's approval card pins. `ComputerModel`
/// owns one (`model.flow`), and every surface reads it: the voice's
/// approval card, the question cards, the floating questions, a
/// notification's Approve while the app runs.
///
/// Each question (by the daemon's id):
///
/// ```
/// open ──tap──▶ sending ──200──▶ answered
///   ▲              ├──409──▶ gone
///   └──error───────┘   (open again, with the error under the buttons)
/// ```
///
/// The voice's own question (`pending_action`) is pinned on the approval
/// card while it is open, sending or settled; a settled card stays a
/// moment (the model's linger) and is then dismissed. A `pending.*` event
/// for it settles it as answered elsewhere (by voice, on the Mac) or
/// dismisses it; a state refresh without it dismisses it only while open.
///
/// Pure: the model feeds it `/api/state`, the current computer's
/// `/events` and the answer's result.
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

        public var isSettled: Bool {
            switch self {
            case .answered, .gone: return true
            case .open, .sending: return false
            }
        }

        var isOpen: Bool {
            if case .open = self { return true }
            return false
        }
    }

    /// A question with its phase, as a card shows it.
    public struct Card: Equatable, Sendable {
        public var pending: PendingAction
        public var phase: Phase

        public init(pending: PendingAction, phase: Phase = .open(error: nil)) {
            self.pending = pending
            self.phase = phase
        }

        /// Buttons are live only while nothing else is in flight.
        public var acceptsTaps: Bool { phase.isOpen }

        /// The card is finished and only waiting to be dismissed.
        public var isSettled: Bool { phase.isSettled }
    }

    public static let goneMessage = "That question was already answered or has gone."

    /// Each question's phase; a question without one is open.
    public private(set) var phases: [String: Phase] = [:]
    /// The voice's own question, pinned on the approval card.
    public private(set) var pinned: PendingAction?

    public init() {}

    public func phase(_ id: String) -> Phase {
        phases[id] ?? .open(error: nil)
    }

    /// The approval card: the pinned question with its phase.
    public var card: Card? {
        pinned.map { Card(pending: $0, phase: phase($0.id)) }
    }

    /// What changed on the approval card.
    public enum Effect: Equatable, Sendable {
        case none
        /// A new question was pinned.
        case appeared
        /// The pinned question settled (answered or gone).
        case settled
        /// The card went away.
        case dismissed
    }

    // MARK: - The voice's own question

    /// The voice's question from `/api/state` (`pending_action`, nil: none)
    /// or a `pending.created` event.
    @discardableResult
    public mutating func present(_ pending: PendingAction?) -> Effect {
        guard let pending, pending.isAnswerPrompt else {
            // The state has no question: an open card was resolved
            // elsewhere. A card mid-send, or settled, finishes on its own.
            guard let pinned, phase(pinned.id).isOpen else { return .none }
            self.pinned = nil
            return .dismissed
        }
        if pinned?.id == pending.id {
            // Same question: refresh its words, keep where the tap is.
            pinned = pending
            return .none
        }
        if let pinned, case .sending = phase(pinned.id) {
            // A new question while ours is in flight: ours finishes first.
            return .none
        }
        pinned = pending
        return .appeared
    }

    /// A `pending.confirmed` / `expired` / `cancelled` / `replaced` event
    /// (for `replaced`, the old id) about the voice's question. Another
    /// question's phase follows its own answer and the next state.
    @discardableResult
    public mutating func resolved(pendingID: String, as resolution: ConversationItem.Resolution, answer: String? = nil, via: String? = nil) -> Effect {
        guard pinned?.id == pendingID else { return .none }
        switch phase(pendingID) {
        case .sending:
            // Our own answer's event can beat the HTTP reply; the reply
            // settles the card.
            if resolution == .confirmed { return .none }
            phases[pendingID] = .gone(message: ApprovalFlow.goneMessage)
            return .settled
        case .answered, .gone:
            return .none
        case .open:
            if resolution == .confirmed {
                let item = ConversationItem(id: 0, ts: Date(), kind: .approval, text: "", via: via, answer: answer, resolution: .confirmed)
                phases[pendingID] = .answered(outcome: item.approvalOutcome ?? "Answered", spoken: nil)
                return .settled
            }
            phases[pendingID] = nil
            pinned = nil
            return .dismissed
        }
    }

    /// The settled card's moment is over.
    public mutating func dismiss(pendingID: String) {
        guard let card, card.pending.id == pendingID, card.isSettled else { return }
        pinned = nil
    }

    // MARK: - Answers, from any surface

    /// A tap on `question`'s option. Returns the option to send, or nil
    /// when the tap must be ignored (a double tap, a settled question, an
    /// option it does not have).
    public mutating func tap(_ question: PendingAction, index: Int) -> PendingOption? {
        guard phase(question.id).isOpen,
              let option = question.options?.first(where: { $0.index == index })
        else { return nil }
        phases[question.id] = .sending(option)
        return option
    }

    /// `POST /api/pending/answer` said ok. The voice's card says it the way
    /// the conversation does ("You approved — Yes · by tap"); another
    /// question's card says "Answered: Yes".
    @discardableResult
    public mutating func succeeded(pendingID: String, spoken: String?) -> Effect {
        guard case let .sending(option) = phase(pendingID) else { return .none }
        let said = spoken?.trimmed.isEmpty == false ? spoken : nil
        if pinned?.id == pendingID {
            let item = ConversationItem(id: 0, ts: Date(), kind: .approval, text: "", via: "tap", answer: option.label, resolution: .confirmed)
            phases[pendingID] = .answered(outcome: item.approvalOutcome ?? "Answered", spoken: said)
            return .settled
        }
        phases[pendingID] = .answered(outcome: "Answered: \(option.label)", spoken: said)
        return .none
    }

    /// 409: someone (the voice, the Mac) got there first, or it expired.
    @discardableResult
    public mutating func gone(pendingID: String) -> Effect {
        guard case .sending = phase(pendingID) else { return .none }
        phases[pendingID] = .gone(message: ApprovalFlow.goneMessage)
        return pinned?.id == pendingID ? .settled : .none
    }

    /// Any other failure: back to open, with the reason under the buttons.
    public mutating func failed(pendingID: String, message: String) {
        guard case .sending = phase(pendingID) else { return }
        phases[pendingID] = .open(error: message)
    }

    // MARK: - State

    /// The open questions of a state: an open question that closed is
    /// forgotten; one in flight or settled finishes on its own.
    public mutating func retain(open ids: Set<String>) {
        let kept = phases.filter { ids.contains($0.key) || !$0.value.isOpen }
        if kept != phases { phases = kept }
    }

    /// New credentials: nothing of the old pairing stays.
    public mutating func reset() {
        phases = [:]
        pinned = nil
    }
}
