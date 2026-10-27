import SheepdogKit
import SwiftUI

/// The card label's state for the session: `CardLabelState` plus the
/// five-second timer. Its own `@Observable`, so a label coming or going
/// re-renders the label and nothing else.
@MainActor
@Observable
final class CardLabelModel {
    private(set) var state = CardLabelState()
    @ObservationIgnored private var timer: Task<Void, Never>?
    @ObservationIgnored private let duration: Duration

    init(duration: Duration = .seconds(CardLabelState.duration)) {
        self.duration = duration
    }

    var card: ScreenCard? { state.current }

    /// A card arrived as a row while the app is active.
    func show(_ card: ScreenCard, appActive: Bool) {
        guard let generation = state.show(card, appActive: appActive) else { return }
        AccessibilityNotification.Announcement(CardLabelState.announcement(for: card)).post()
        timer?.cancel()
        timer = Task { [weak self, duration] in
            try? await Task.sleep(for: duration)
            guard !Task.isCancelled else { return }
            self?.state.expire(generation: generation)
        }
    }

    func hide() {
        guard state.current != nil else { return }
        timer?.cancel()
        state.hide()
    }
}

/// "Card: <title>" in a small capsule at the top edge, over whatever
/// screen is in front. A button without focus: it never takes the keyboard
/// from a terminal. Quiet by design (brand: nothing blinks): a material
/// surface, ink words, the card's icon; Reduce Motion fades only.
struct CardLabelOverlay: View {
    let model: CardLabelModel
    let open: (ScreenCard) -> Void
    @Environment(\.accessibilityReduceMotion) private var reduceMotion

    var body: some View {
        ZStack {
            if let card = model.card {
                Button {
                    open(card)
                } label: {
                    HStack(spacing: 6) {
                        Image(systemName: card.symbol)
                            .font(.caption.weight(.semibold))
                            .foregroundStyle(Theme.ink)
                        Text("Card: \(card.displayTitle)")
                            .font(.footnote.weight(.medium))
                            .foregroundStyle(Theme.ink)
                            .lineLimit(1)
                            .truncationMode(.tail)
                    }
                    .padding(.horizontal, 12)
                    .padding(.vertical, 7)
                    .background(.regularMaterial, in: Capsule())
                    .overlay(Capsule().strokeBorder(Theme.ink.opacity(0.12), lineWidth: 0.5))
                    .shadow(color: .black.opacity(0.12), radius: 6, y: 2)
                }
                .buttonStyle(.plain)
                .frame(maxWidth: 320)
                .padding(.top, 4)
                .padding(.horizontal, 16)
                .id(card.cardID)
                .transition(.opacity)
                .accessibilityElement(children: .ignore)
                .accessibilityLabel("Card ready: \(card.displayTitle)")
                .accessibilityHint("Opens the card")
                .accessibilityAddTraits(.isButton)
                .accessibilityIdentifier("cardLabel")
            }
        }
        .frame(maxWidth: .infinity, maxHeight: .infinity, alignment: .top)
        .allowsHitTesting(model.card != nil)
        .animation(reduceMotion || debugNoAnimations ? nil : .easeOut(duration: 0.2), value: model.card?.cardID)
    }
}
