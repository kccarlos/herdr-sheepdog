import SheepdogKit
import SwiftUI

// The orchestrator's suggested replies (docs/dev/design/
// conversation-timeline.md, section 14): one component for every place an
// orchestrator reply shows its options (Details under the voice's bubble,
// the purple panel of a reply the voice did not say). Which reply is live
// is `ConversationTimeline.suggestedReplies`; a tap is
// `SuggestedReplySender.send`, the typed-line path.

/// What the options need from the conversation, handed down by
/// `ConversationView`: which replies are live, the tap in flight, and the
/// action. Equatable by value and by the sender's identity, so a redraw of
/// the list does not invalidate every row.
struct SuggestedReplyContext: Equatable {
    var replies: [Int64: SuggestedReplies]
    var tap: SuggestedReplyTap?
    /// The typed line is on its way from the composer.
    var busy: Bool
    var computer: String
    let sender: SuggestedReplySender

    static func == (a: Self, b: Self) -> Bool {
        a.replies == b.replies && a.tap == b.tap && a.busy == b.busy && a.computer == b.computer && a.sender === b.sender
    }
}

private struct SuggestedReplyContextKey: EnvironmentKey {
    static let defaultValue: SuggestedReplyContext? = nil
}

extension EnvironmentValues {
    /// Nil where nothing can be sent (a preview, no computer): every
    /// option is plain text.
    var suggestedReplies: SuggestedReplyContext? {
        get { self[SuggestedReplyContextKey.self] }
        set { self[SuggestedReplyContextKey.self] = newValue }
    }
}

/// An orchestrator reply's options: buttons while the reply is the latest
/// one you have not answered, plain text after (the chosen one ticked), so
/// nobody taps a stale choice.
struct SuggestedReplyOptions: View {
    let replyID: Int64
    let options: [String]
    /// The pill's colour: `ThinkingSoft` under Details, `Card` on the
    /// purple panel.
    var fill = Theme.thinkingSoft
    let voiceName: String

    @Environment(\.suggestedReplies) private var context
    @Environment(\.accessibilityReduceMotion) private var reduceMotion

    private var replies: SuggestedReplies {
        context?.replies[replyID] ?? SuggestedReplies(replyID: replyID, options: options, status: .stale(chosen: nil))
    }

    private var states: [SuggestedReplies.OptionState] {
        options.map { replies.state(of: $0, tap: context?.tap, busy: context?.busy ?? false) }
    }

    var body: some View {
        let states = states
        let live = context != nil && replies.isLive
        VStack(alignment: .leading, spacing: 4) {
            ViewThatFits(in: .horizontal) {
                HStack(spacing: 6) { items(states, live: live) }
                VStack(alignment: .leading, spacing: 2) { items(states, live: live) }
            }
            if live, let message = replies.failure(context?.tap) {
                // The composer's error, where the tap was.
                Text(message)
                    .font(.caption)
                    .foregroundStyle(Theme.danger)
                    .fixedSize(horizontal: false, vertical: true)
                    .accessibilityIdentifier("reply.error")
            }
        }
        .animation(Theme.ease(reduceMotion: reduceMotion), value: states)
    }

    @ViewBuilder
    private func items(_ states: [SuggestedReplies.OptionState], live: Bool) -> some View {
        ForEach(Array(zip(options, states).enumerated()), id: \.offset) { _, pair in
            let (option, state) = pair
            switch state {
            case .available, .disabled, .sending:
                if live, let context {
                    SuggestedReplyButton(text: option, state: state, fill: fill, voiceName: voiceName) {
                        Task { await context.sender.send(option, of: replies, to: context.computer) }
                    }
                } else {
                    StaleOption(text: option)
                }
            case .sent:
                SentOption(text: option)
            case .stale:
                StaleOption(text: option)
            }
        }
    }
}

/// A live option: a purple pill, a real button with a 44 pt target.
private struct SuggestedReplyButton: View {
    let text: String
    let state: SuggestedReplies.OptionState
    let fill: Color
    let voiceName: String
    let action: () -> Void

    var body: some View {
        Button(action: action) {
            HStack(spacing: 6) {
                if state == .sending {
                    ProgressView()
                        .controlSize(.mini)
                        .tint(Theme.thinkingText)
                }
                Text(text)
                    .multilineTextAlignment(.leading)
                    .fixedSize(horizontal: false, vertical: true)
            }
            .font(.footnote.weight(.medium))
            .foregroundStyle(Theme.thinkingText)
            .padding(.horizontal, 12)
            .padding(.vertical, 6)
            .background(fill, in: RoundedRectangle(cornerRadius: 14, style: .continuous))
            .overlay(RoundedRectangle(cornerRadius: 14, style: .continuous).strokeBorder(Theme.thinkingRule, lineWidth: 1))
            .opacity(state == .disabled ? 0.45 : 1)
            .frame(minHeight: 44)
            .contentShape(Rectangle())
        }
        .buttonStyle(.plain)
        .disabled(state != .available)
        .accessibilityLabel("Reply: \(text)")
        .accessibilityHint("Sends this to \(voiceName)")
        .accessibilityValue(state == .sending ? "Sending" : "")
        .accessibilityIdentifier("reply.option")
    }
}

/// The option that was sent (or that your reply repeated): ticked, not a
/// button.
private struct SentOption: View {
    let text: String

    var body: some View {
        Label {
            Text(text).fixedSize(horizontal: false, vertical: true)
        } icon: {
            Image(systemName: "checkmark.circle.fill")
        }
        .font(.footnote.weight(.medium))
        .foregroundStyle(Theme.thinkingText)
        .padding(.vertical, 4)
        .accessibilityElement(children: .ignore)
        .accessibilityLabel("You replied: \(text)")
        .accessibilityIdentifier("reply.option.sent")
    }
}

/// An option that no longer answers anything: plain muted words, no pill,
/// so it does not look tappable.
private struct StaleOption: View {
    let text: String

    var body: some View {
        Text(text)
            .font(.footnote)
            .foregroundStyle(Theme.muted)
            .fixedSize(horizontal: false, vertical: true)
            .padding(.vertical, 4)
            .accessibilityLabel("Suggested: \(text)")
            .accessibilityIdentifier("reply.option.stale")
    }
}
