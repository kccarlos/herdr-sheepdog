import SheepdogKit
import SwiftUI

/// The small flag over the conversation: mark this moment for debugging
/// ("that paused"). A tap remembers *now* and opens a short tag sheet
/// whose first, biggest button is Paused, so one more tap files the
/// common case; the other tags and a note are optional. The voice keeps
/// running throughout. VoiceOver also gets "Mark a pause" as an action
/// that files Paused straight away.
struct FeedbackButton: View {
    let mark: (FeedbackTag, String?, Date) -> Void

    @State private var markedAt: Date?

    var body: some View {
        Button {
            Haptics.tap()
            markedAt = Date()
        } label: {
            Image(systemName: "flag")
                .font(.footnote.weight(.semibold))
                .foregroundStyle(.secondary)
                .frame(width: 34, height: 34)
                .background(.regularMaterial, in: Circle())
                .contentShape(Circle())
        }
        .buttonStyle(.plain)
        .accessibilityLabel("Mark this moment")
        .accessibilityHint("Saves a feedback mark for debugging, such as a pause")
        .accessibilityAction(named: "Mark a pause") {
            mark(.paused, nil, Date())
        }
        .accessibilityIdentifier("feedback.flag")
        .sheet(isPresented: Binding(get: { markedAt != nil }, set: { if !$0 { markedAt = nil } })) {
            if let at = markedAt {
                FeedbackSheet(at: at) { tag, note in
                    mark(tag, note, at)
                    markedAt = nil
                }
            }
        }
    }
}

/// The tags, Paused first and biggest, and an optional note.
struct FeedbackSheet: View {
    let at: Date
    let pick: (FeedbackTag, String?) -> Void

    @State private var note = ""
    @FocusState private var noteFocused: Bool

    private var others: [FeedbackTag] { FeedbackTag.allCases.filter { $0 != .paused } }

    var body: some View {
        VStack(alignment: .leading, spacing: 14) {
            VStack(alignment: .leading, spacing: 2) {
                Text("What happened?")
                    .font(.headline)
                Text("Marked at \(at.formatted(date: .omitted, time: .standard))")
                    .font(.footnote)
                    .foregroundStyle(.secondary)
            }
            .accessibilityElement(children: .combine)

            Button {
                pick(.paused, note)
            } label: {
                Label(FeedbackTag.paused.label, systemImage: FeedbackTag.paused.systemImage)
                    .font(.body.weight(.semibold))
                    .frame(maxWidth: .infinity, minHeight: 44)
            }
            .buttonStyle(.borderedProminent)
            .accessibilityIdentifier("feedback.tag.paused")

            LazyVGrid(columns: [GridItem(.flexible()), GridItem(.flexible())], spacing: 10) {
                ForEach(others) { tag in
                    Button {
                        pick(tag, note)
                    } label: {
                        Label(tag.label, systemImage: tag.systemImage)
                            .font(.subheadline)
                            .frame(maxWidth: .infinity, minHeight: 36)
                    }
                    .buttonStyle(.bordered)
                    .accessibilityIdentifier("feedback.tag.\(tag.rawValue)")
                }
            }

            TextField("Add a note (optional)", text: $note, axis: .vertical)
                .lineLimit(1 ... 3)
                .textFieldStyle(.roundedBorder)
                .focused($noteFocused)
                .accessibilityIdentifier("feedback.note")
        }
        .padding(20)
        .presentationDetents([.height(330), .large])
        .presentationDragIndicator(.visible)
    }
}

/// "Noted" for a moment after a mark.
struct FeedbackNoticeView: View {
    let text: String

    var body: some View {
        Label(text, systemImage: "checkmark.circle.fill")
            .font(.footnote.weight(.semibold))
            .padding(.horizontal, 14)
            .padding(.vertical, 8)
            .background(.regularMaterial, in: Capsule())
            .accessibilityElement(children: .ignore)
            .accessibilityLabel(text)
            .accessibilityIdentifier("feedback.notice")
    }
}
