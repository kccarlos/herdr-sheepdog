import SheepdogKit
import SwiftUI

/// A computer's section header on the Agents page and All agents
/// (several computers only): its name, a line under it ("You're talking
/// here", "4 agents · 1 waiting") and, for another computer, Talk to.
struct ComputerSectionHeader: View {
    let id: String
    let name: String
    let detail: String?
    let talkTo: (() -> Void)?

    @Environment(\.dynamicTypeSize) private var dynamicTypeSize

    var body: some View {
        ViewThatFits(in: .horizontal) {
            HStack(alignment: .center, spacing: 12) {
                titles
                Spacer(minLength: 8)
                talkButton
            }
            VStack(alignment: .leading, spacing: 8) {
                titles
                talkButton
            }
        }
        .padding(.top, 4)
        .padding(.bottom, 2)
    }

    private var titles: some View {
        VStack(alignment: .leading, spacing: 2) {
            Text(name)
                .font(.title3.weight(.semibold))
                .fontDesign(.rounded)
                .foregroundStyle(Theme.ink)
                .fixedSize(horizontal: false, vertical: true)
            if let detail {
                Text(detail)
                    .font(.footnote)
                    .foregroundStyle(Theme.muted)
                    .fixedSize(horizontal: false, vertical: true)
            }
        }
        .accessibilityElement(children: .combine)
        .accessibilityAddTraits(.isHeader)
        .accessibilityIdentifier("computerLabel.section.\(id)")
    }

    @ViewBuilder private var talkButton: some View {
        if let talkTo {
            Button(action: talkTo) {
                Text("Talk to \(name)")
                    .font(.subheadline.weight(.semibold))
                    .foregroundStyle(Theme.ink)
                    .padding(.horizontal, 14)
                    .frame(minHeight: 36)
                    .overlay(Capsule().strokeBorder(Theme.line, lineWidth: 1))
                    .frame(minHeight: 44)
                    .contentShape(Capsule())
            }
            .buttonStyle(.plain)
            .hoverEffect()
            .accessibilityIdentifier("computerLabel.sectionTalkTo.\(id)")
        }
    }
}

/// A computer that does not answer: one dashed row with Retry. Its
/// last-known agents are not shown as if they were live.
struct OfflineComputerRow: View {
    let name: String
    /// "Last seen 9:12.", nil when it never answered.
    let lastSeen: String?
    let retry: () -> Void

    private var message: String {
        ["Can't reach \(name).", lastSeen, "Is it awake and on the same network?"].compactMap { $0 }.joined(separator: " ")
    }

    var body: some View {
        VStack(alignment: .leading, spacing: 10) {
            HStack(alignment: .firstTextBaseline, spacing: 8) {
                Image(systemName: "wifi.slash")
                    .foregroundStyle(Theme.muted)
                    .accessibilityHidden(true)
                Text(message)
                    .font(.subheadline)
                    .foregroundStyle(Theme.ink)
                    .fixedSize(horizontal: false, vertical: true)
            }
            Button(action: retry) {
                Text("Retry")
                    .font(.subheadline.weight(.semibold))
                    .foregroundStyle(Theme.ink)
                    .padding(.horizontal, 16)
                    .frame(minHeight: 36)
                    .overlay(Capsule().strokeBorder(Theme.ink, lineWidth: 1.5))
                    .frame(minHeight: 44)
                    .contentShape(Capsule())
            }
            .buttonStyle(.plain)
            .accessibilityLabel("Retry \(name)")
        }
        .padding(14)
        .frame(maxWidth: .infinity, alignment: .leading)
        .overlay(
            RoundedRectangle(cornerRadius: Theme.Radius.card, style: .continuous)
                .strokeBorder(Theme.line, style: StrokeStyle(lineWidth: 1.5, dash: [5, 4]))
        )
        .accessibilityElement(children: .contain)
        .accessibilityIdentifier("computerLabel.offline.\(name)")
    }
}
