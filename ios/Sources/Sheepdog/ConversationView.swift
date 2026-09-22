import SheepdogKit
import SwiftUI

/// The conversation: the shepherd on the right, the dog on the left, and
/// the daemon's pushes as full-width cards.
struct ConversationView: View {
    let bubbles: [ConversationReducer.Bubble]
    let voiceName: String

    @State private var atBottom = true
    @State private var lastSeenID: Int?

    var body: some View {
        ScrollViewReader { proxy in
            ScrollView {
                LazyVStack(spacing: 10) {
                    if bubbles.isEmpty {
                        EmptyConversation(voiceName: voiceName)
                            .padding(.top, 28)
                    }
                    ForEach(bubbles) { bubble in
                        BubbleView(bubble: bubble, voiceName: voiceName)
                            .id(bubble.id)
                    }
                    Color.clear
                        .frame(height: 1)
                        .id(Self.bottomAnchor)
                        .onAppear { atBottom = true }
                        .onDisappear { atBottom = false }
                }
                .padding(.horizontal, 16)
                .padding(.vertical, 12)
            }
            .overlay(alignment: .bottom) {
                if !atBottom, let newest = bubbles.last?.id, newest != lastSeenID {
                    Button {
                        withAnimation { proxy.scrollTo(Self.bottomAnchor, anchor: .bottom) }
                    } label: {
                        Label("Jump to latest", systemImage: "arrow.down")
                            .font(.footnote.weight(.semibold))
                            .padding(.horizontal, 14)
                            .padding(.vertical, 8)
                            .background(.regularMaterial, in: Capsule())
                    }
                    .padding(.bottom, 10)
                    .transition(.move(edge: .bottom).combined(with: .opacity))
                }
            }
            .onChange(of: bubbles.last?.id) { _, newest in
                guard let newest else { return }
                if atBottom {
                    lastSeenID = newest
                    withAnimation(.easeOut(duration: 0.18)) {
                        proxy.scrollTo(Self.bottomAnchor, anchor: .bottom)
                    }
                }
            }
            .onChange(of: atBottom) { _, isBottom in
                if isBottom { lastSeenID = bubbles.last?.id }
            }
        }
    }

    private static let bottomAnchor = "conversation.bottom"
}

struct BubbleView: View {
    let bubble: ConversationReducer.Bubble
    let voiceName: String

    var body: some View {
        switch bubble.speaker {
        case .shepherd:
            row(alignment: .trailing) {
                text
                    .padding(.horizontal, 14)
                    .padding(.vertical, 10)
                    .background(Color.accentColor, in: RoundedRectangle(cornerRadius: 18, style: .continuous))
                    .foregroundStyle(.white)
            }
        case .sheepdog:
            row(alignment: .leading) {
                text
                    .padding(.horizontal, 14)
                    .padding(.vertical, 10)
                    .background(Color(.secondarySystemBackground), in: RoundedRectangle(cornerRadius: 18, style: .continuous))
            }
        case .system:
            systemCard
        }
    }

    private var text: some View {
        Text(bubble.text + (bubble.isFinal ? "" : " …"))
            .font(.body)
            .multilineTextAlignment(.leading)
            .fixedSize(horizontal: false, vertical: true)
    }

    private func row<Content: View>(alignment: HorizontalAlignment, @ViewBuilder content: () -> Content) -> some View {
        HStack {
            if alignment == .trailing { Spacer(minLength: 44) }
            content()
                .frame(maxWidth: 300, alignment: alignment == .trailing ? .trailing : .leading)
            if alignment == .leading { Spacer(minLength: 44) }
        }
        // One element per bubble, read as a sentence: "You said, what's
        // going on." Keeping the words in the label (not the value) is what
        // VoiceOver announces when it moves through the conversation.
        .accessibilityElement(children: .combine)
        .accessibilityLabel("\(bubble.speaker == .shepherd ? "You" : voiceName) said, \(bubble.text)")
        .accessibilityIdentifier(bubble.speaker == .shepherd ? "bubble.shepherd" : "bubble.sheepdog")
    }

    @ViewBuilder
    private var systemCard: some View {
        let needsUser: Bool = {
            if case let .push(needs) = bubble.kind { return needs }
            return false
        }()
        let isPush: Bool = {
            if case .push = bubble.kind { return true }
            return false
        }()
        let tint: Color = needsUser ? Theme.blocked : Theme.idle

        HStack(alignment: .top, spacing: 10) {
            Image(systemName: needsUser ? "hand.raised.fill" : (isPush ? "bell.fill" : "info.circle"))
                .font(.footnote)
                .foregroundStyle(tint)
                .padding(.top, 2)
            Text(bubble.text)
                .font(needsUser ? .callout.weight(.medium) : .footnote)
                .foregroundStyle(needsUser ? .primary : .secondary)
                .fixedSize(horizontal: false, vertical: true)
            Spacer(minLength: 0)
        }
        .padding(.horizontal, 14)
        .padding(.vertical, 11)
        .frame(maxWidth: .infinity, alignment: .leading)
        .background(
            RoundedRectangle(cornerRadius: 14, style: .continuous)
                .fill(needsUser ? tint.opacity(0.12) : Color(.tertiarySystemBackground))
        )
        .overlay(
            RoundedRectangle(cornerRadius: 14, style: .continuous)
                .strokeBorder(needsUser ? tint.opacity(0.5) : .clear, lineWidth: 1.5)
        )
        .accessibilityElement(children: .combine)
        .accessibilityLabel("\(needsUser ? "Needs your decision" : (isPush ? "\(voiceName) says" : "Notice")), \(bubble.text)")
        .accessibilityIdentifier(isPush ? "bubble.push" : "bubble.notice")
    }
}

struct EmptyConversation: View {
    let voiceName: String

    var body: some View {
        VStack(spacing: 8) {
            Image(systemName: "bubble.left.and.bubble.right")
                .font(.title2)
                .foregroundStyle(.tertiary)
            Text("Nothing said yet")
                .font(.subheadline.weight(.medium))
                .foregroundStyle(.secondary)
            Text("Connect and talk to \(voiceName). What you say and what it answers shows up here.")
                .font(.footnote)
                .foregroundStyle(.tertiary)
                .multilineTextAlignment(.center)
                .padding(.horizontal, 28)
        }
        .frame(maxWidth: .infinity)
    }
}
