import SheepdogKit
import SwiftUI

// Cards (docs/ws-contract.md "Cards"): what the orchestrator's `show` tool
// put on the screen. A new one opens once by itself as a sheet; the
// conversation keeps it as a row that opens it again. Display only: an
// approval card never answers, the pinned approval card does.

// MARK: - Environment

private struct OpenCardKey: EnvironmentKey {
    static let defaultValue: (ScreenCard) -> Void = { _ in }
}

private struct ScreenCoverKey: EnvironmentKey {
    static let defaultValue: (String, Bool) -> Void = { _, _ in }
}

extension EnvironmentValues {
    /// Opens a card as a sheet (a tap on its row).
    var openCard: (ScreenCard) -> Void {
        get { self[OpenCardKey.self] }
        set { self[OpenCardKey.self] = newValue }
    }

    /// Reports a sheet over the voice screen, by name: while one is up, the
    /// daemon is told the voice screen is not in front, and shows no card.
    var reportScreenCover: (String, Bool) -> Void {
        get { self[ScreenCoverKey.self] }
        set { self[ScreenCoverKey.self] = newValue }
    }
}

extension View {
    /// Marks this view (a sheet's content) as covering the voice screen
    /// while it is on screen.
    func coversVoiceScreen(_ name: String) -> some View {
        modifier(CoversVoiceScreen(name: name))
    }
}

private struct CoversVoiceScreen: ViewModifier {
    let name: String
    @Environment(\.reportScreenCover) private var report

    func body(content: Content) -> some View {
        content
            .onAppear { report(name, true) }
            .onDisappear { report(name, false) }
    }
}

// MARK: - Words

extension ScreenCard {
    /// What kind of card it is, in a word or two.
    var kindWord: String {
        switch kind {
        case .card, .unknown: return "Card"
        case .agent: return "Agent window"
        case .excerpt: return "Screen"
        case .approval: return "Question"
        }
    }
}

// MARK: - The conversation row

/// A card in the conversation: its icon, title and agent. A tap opens it
/// again. A card the daemon no longer stores shows its words only.
struct CardRow: View {
    let card: ScreenCard?
    /// The item's text: the title, or the daemon's "no longer stored".
    let text: String
    let at: Date

    @Environment(\.openCard) private var openCard

    private var title: String { card?.displayTitle ?? text }

    private var subtitle: String {
        var parts = [card?.kindWord ?? "Card"]
        if let agent = card?.agentName { parts.append(agent) }
        parts.append(ConversationTimeline.defaultTimeFormatter.string(from: at))
        return parts.joined(separator: " · ")
    }

    var body: some View {
        if let card {
            Button { openCard(card) } label: { content(symbol: card.symbol, chevron: true) }
                .buttonStyle(CardRowButtonStyle())
                .accessibilityElement(children: .ignore)
                .accessibilityLabel("\(card.kindWord): \(title)" + (card.agentName.map { ", \($0)" } ?? ""))
                .accessibilityHint("Opens the card")
                .accessibilityAddTraits(.isButton)
                .accessibilityIdentifier("bubble.card")
        } else {
            content(symbol: "rectangle.on.rectangle", chevron: false)
                .accessibilityElement(children: .combine)
                .accessibilityIdentifier("bubble.cardMissing")
        }
    }

    private func content(symbol: String, chevron: Bool) -> some View {
        HStack(spacing: 12) {
            Image(systemName: symbol)
                .font(.subheadline.weight(.semibold))
                .foregroundStyle(.secondary)
                .frame(width: 36, height: 36)
                .background(Color(.tertiarySystemFill), in: RoundedRectangle(cornerRadius: 8, style: .continuous))
                .accessibilityHidden(true)
            VStack(alignment: .leading, spacing: 2) {
                Text(title)
                    .font(chevron ? .subheadline.weight(.semibold) : .footnote)
                    .foregroundStyle(chevron ? .primary : .secondary)
                    .multilineTextAlignment(.leading)
                    .fixedSize(horizontal: false, vertical: true)
                if chevron {
                    Text(subtitle)
                        .font(.caption)
                        .foregroundStyle(.secondary)
                        .fixedSize(horizontal: false, vertical: true)
                }
            }
            Spacer(minLength: 0)
            if chevron {
                Image(systemName: "chevron.right")
                    .font(.caption.weight(.semibold))
                    .foregroundStyle(.tertiary)
                    .accessibilityHidden(true)
            }
        }
        .padding(.horizontal, 12)
        .padding(.vertical, 10)
        .frame(maxWidth: .infinity, alignment: .leading)
        .background(
            RoundedRectangle(cornerRadius: 14, style: .continuous)
                .fill(Color(.tertiarySystemBackground))
        )
        .overlay(
            RoundedRectangle(cornerRadius: 14, style: .continuous)
                .strokeBorder(Color(.separator), lineWidth: 0.5)
        )
        .contentShape(RoundedRectangle(cornerRadius: 14, style: .continuous))
    }
}

/// A row that dims a little under the finger, like a list cell.
private struct CardRowButtonStyle: ButtonStyle {
    func makeBody(configuration: Configuration) -> some View {
        configuration.label
            .opacity(configuration.isPressed ? 0.6 : 1)
            .animation(.easeOut(duration: 0.15), value: configuration.isPressed)
    }
}

// MARK: - The sheet

/// A card on its own: what it is and when, its title, and its content.
struct CardSheet: View {
    let card: ScreenCard
    @EnvironmentObject private var model: SessionViewModel
    @Environment(\.dismiss) private var dismiss

    private var meta: String {
        var parts = [card.kindWord]
        if let agent = card.agentName { parts.append(agent) }
        if let at = card.at { parts.append(ConversationTimeline.defaultTimeFormatter.string(from: at)) }
        return parts.joined(separator: " · ")
    }

    var body: some View {
        ScrollView {
            VStack(alignment: .leading, spacing: 16) {
                Text(card.displayTitle)
                    .font(.title2.weight(.semibold))
                    .fontDesign(.rounded)
                    .fixedSize(horizontal: false, vertical: true)
                    .accessibilityAddTraits(.isHeader)
                    .accessibilityIdentifier("card.title")
                content
            }
            .padding(.horizontal, 20)
            .padding(.top, 4)
            .padding(.bottom, 24)
            .frame(maxWidth: .infinity, alignment: .leading)
        }
        .scrollBounceBehavior(.basedOnSize)
        .safeAreaInset(edge: .top, spacing: 8) { header }
        .background(Color(.systemBackground))
        .presentationDetents([.medium, .large])
        .presentationDragIndicator(.visible)
        .presentationContentInteraction(.scrolls)
    }

    private var header: some View {
        HStack(alignment: .center, spacing: 8) {
            Label(meta, systemImage: card.symbol)
                .font(.footnote.weight(.medium))
                .foregroundStyle(.secondary)
                .labelStyle(.titleAndIcon)
                .fixedSize(horizontal: false, vertical: true)
            Spacer(minLength: 8)
            Button("Done") { dismiss() }
                .font(.body.weight(.semibold))
                .tint(Theme.primaryText)
                .frame(minHeight: 44)
                .accessibilityIdentifier("card.done")
        }
        .padding(.horizontal, 20)
        .padding(.top, 14)
        .dynamicTypeSize(...DynamicTypeSize.accessibility2)
    }

    @ViewBuilder
    private var content: some View {
        switch card.kind {
        case .card:
            if let markdown = card.markdown?.trimmingCharacters(in: .whitespacesAndNewlines), !markdown.isEmpty {
                MarkdownBlocksView(markdown)
                    .textSelection(.enabled)
            } else {
                Text("This card is empty.")
                    .foregroundStyle(.secondary)
            }
        case .agent:
            Text("See \(card.agentName ?? "the agent")'s terminal, and send it keys or a prompt.")
                .foregroundStyle(.secondary)
                .fixedSize(horizontal: false, vertical: true)
            openWindowButton(prominent: true)
        case .excerpt:
            if let text = card.text, !text.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty {
                CodeBlockView(text: text)
            } else {
                Text("The screen was empty.")
                    .foregroundStyle(.secondary)
            }
            openWindowButton(prominent: false)
        case .approval:
            approval
        case .unknown:
            if let markdown = card.markdown?.trimmingCharacters(in: .whitespacesAndNewlines), !markdown.isEmpty {
                MarkdownBlocksView(markdown)
            } else if let text = card.text, !text.isEmpty {
                CodeBlockView(text: text)
            } else {
                Text("This version of the app can't show this kind of card. Update the app to see it.")
                    .foregroundStyle(.secondary)
                    .fixedSize(horizontal: false, vertical: true)
            }
            openWindowButton(prominent: false)
        }
    }

    /// The question, read-only. The pinned approval card answers it.
    @ViewBuilder
    private var approval: some View {
        let isOpen = card.pendingID != nil && model.approval?.pending.id == card.pendingID && model.approval?.isSettled == false
        if let question = card.question?.trimmingCharacters(in: .whitespacesAndNewlines), !question.isEmpty {
            Text(question.prefix(1).uppercased() + question.dropFirst())
                .font(.body)
                .fixedSize(horizontal: false, vertical: true)
        }
        if let command = card.command?.trimmingCharacters(in: .whitespacesAndNewlines), !command.isEmpty {
            CodeBlockView(text: command)
        }
        if let options = card.options, !options.isEmpty {
            VStack(alignment: .leading, spacing: 6) {
                Text("Choices")
                    .font(.footnote.weight(.semibold))
                    .foregroundStyle(.secondary)
                FlowOptions(options: options.map(\.label))
            }
        }
        if isOpen {
            Button {
                dismiss()
            } label: {
                Label("Go to the question", systemImage: "arrow.down.circle")
                    .font(.body.weight(.semibold))
                    .frame(maxWidth: .infinity, minHeight: 30)
            }
            .buttonStyle(.borderedProminent)
            .tint(.accentColor)
            .foregroundStyle(Color("OnAccent"))
            .accessibilityHint("Closes this card. Answer on the card below the conversation.")
            .accessibilityIdentifier("card.goToQuestion")
        } else {
            Label("This question is no longer open.", systemImage: "checkmark.circle")
                .font(.footnote)
                .foregroundStyle(.secondary)
                .fixedSize(horizontal: false, vertical: true)
        }
        openWindowButton(prominent: false)
    }

    @ViewBuilder
    private func openWindowButton(prominent: Bool) -> some View {
        if let pane = card.windowPaneID {
            let label = Label("Open \(card.agentName ?? "agent") window", systemImage: "macwindow")
                .font(.body.weight(.semibold))
                .frame(maxWidth: .infinity, minHeight: 30)
            Group {
                if prominent {
                    Button { model.openAgentWindow(paneID: pane) } label: { label }
                        .buttonStyle(.borderedProminent)
                        .tint(.accentColor)
                        .foregroundStyle(Color("OnAccent"))
                } else {
                    Button { model.openAgentWindow(paneID: pane) } label: { label }
                        .buttonStyle(.bordered)
                        .tint(Theme.primaryText)
                }
            }
            .accessibilityIdentifier("card.openWindow")
        }
    }
}

// MARK: - Blocks

/// Markdown as separate views, for a card: paragraphs, headings, lists,
/// code that scrolls sideways, and real tables. (`MarkdownText` is the
/// one-`Text` version for rows and bubbles.)
struct MarkdownBlocksView: View {
    private let blocks: [MarkdownRenderer.Block]

    init(_ source: String) {
        blocks = MarkdownRenderer.blocks(source)
    }

    var body: some View {
        VStack(alignment: .leading, spacing: 14) {
            ForEach(Array(blocks.enumerated()), id: \.offset) { _, block in
                view(for: block)
            }
        }
        .frame(maxWidth: .infinity, alignment: .leading)
    }

    @ViewBuilder
    private func view(for block: MarkdownRenderer.Block) -> some View {
        switch block {
        case let .paragraph(text):
            Text(MarkdownRenderer.inline(text))
                .fixedSize(horizontal: false, vertical: true)
        case let .heading(text):
            Text(MarkdownRenderer.inline(text))
                .font(.headline)
                .fixedSize(horizontal: false, vertical: true)
                .padding(.top, 4)
                .accessibilityAddTraits(.isHeader)
        case let .list(items):
            VStack(alignment: .leading, spacing: 8) {
                ForEach(Array(items.enumerated()), id: \.offset) { _, item in
                    HStack(alignment: .firstTextBaseline, spacing: 8) {
                        Text(item.marker)
                            .monospacedDigit()
                            .foregroundStyle(.secondary)
                        Text(MarkdownRenderer.inline(item.text))
                            .fixedSize(horizontal: false, vertical: true)
                    }
                    .padding(.leading, CGFloat(item.depth) * 18)
                    .accessibilityElement(children: .combine)
                }
            }
        case let .code(text):
            CodeBlockView(text: text)
        case let .table(table):
            MarkdownTableView(table: table)
        }
    }
}

/// Monospaced text that keeps its lines and scrolls sideways.
struct CodeBlockView: View {
    let text: String

    var body: some View {
        ScrollView(.horizontal) {
            Text(text)
                .font(.footnote.monospaced())
                .fixedSize()
                .textSelection(.enabled)
                .padding(12)
        }
        .scrollIndicators(.hidden)
        .frame(maxWidth: .infinity, alignment: .leading)
        .background(Color(.secondarySystemBackground), in: RoundedRectangle(cornerRadius: 8, style: .continuous))
    }
}

/// A table that never truncates: a grid when it fits the width, else one
/// small card per row with each value under its column's name (a narrow
/// phone, a wide table, or the accessibility text sizes).
struct MarkdownTableView: View {
    let table: MarkdownRenderer.Table
    @Environment(\.dynamicTypeSize) private var dynamicTypeSize

    var body: some View {
        Group {
            if dynamicTypeSize.isAccessibilitySize {
                stacked
            } else {
                ViewThatFits(in: .horizontal) {
                    grid
                    stacked
                }
            }
        }
        .accessibilityIdentifier("card.table")
    }

    private func alignment(_ column: Int) -> HorizontalAlignment {
        guard column < table.alignments.count else { return .leading }
        switch table.alignments[column] {
        case .leading: return .leading
        case .center: return .center
        case .trailing: return .trailing
        }
    }

    private func frameAlignment(_ column: Int) -> Alignment {
        switch alignment(column) {
        case .center: return .center
        case .trailing: return .trailing
        default: return .leading
        }
    }

    private var grid: some View {
        Grid(alignment: .leading, horizontalSpacing: 0, verticalSpacing: 0) {
            GridRow {
                ForEach(Array(table.header.enumerated()), id: \.offset) { column, title in
                    Text(MarkdownRenderer.inline(title))
                        .font(.footnote.weight(.semibold))
                        .foregroundStyle(.secondary)
                        .fixedSize()
                        .padding(.horizontal, 12)
                        .padding(.vertical, 8)
                        .frame(maxWidth: .infinity, alignment: frameAlignment(column))
                        .background(Color(.secondarySystemBackground).padding(.horizontal, -1))
                        .gridColumnAlignment(alignment(column))
                }
            }
            .accessibilityAddTraits(.isHeader)
            ForEach(Array(table.rows.enumerated()), id: \.offset) { index, row in
                if index > 0 {
                    Divider().gridCellUnsizedAxes(.horizontal)
                }
                GridRow {
                    ForEach(Array(row.enumerated()), id: \.offset) { column, value in
                        Text(MarkdownRenderer.inline(value))
                            .font(.subheadline)
                            .fontWeight(column == 0 ? .medium : .regular)
                            .fixedSize()
                            .padding(.horizontal, 12)
                            .padding(.vertical, 9)
                            .frame(maxWidth: .infinity, alignment: frameAlignment(column))
                    }
                }
            }
        }
        .overlay(
            RoundedRectangle(cornerRadius: 14, style: .continuous)
                .strokeBorder(Color(.separator), lineWidth: 0.5)
        )
        .clipShape(RoundedRectangle(cornerRadius: 14, style: .continuous))
    }

    /// One card per row: the first column as its title, the others under
    /// their column's name.
    private var stacked: some View {
        VStack(alignment: .leading, spacing: 8) {
            ForEach(Array(table.rows.enumerated()), id: \.offset) { _, row in
                VStack(alignment: .leading, spacing: 6) {
                    ForEach(Array(row.enumerated()), id: \.offset) { column, value in
                        if column == 0 {
                            Text(MarkdownRenderer.inline(value.isEmpty ? "—" : value))
                                .font(.subheadline.weight(.semibold))
                                .fixedSize(horizontal: false, vertical: true)
                        } else if !value.isEmpty {
                            VStack(alignment: .leading, spacing: 1) {
                                Text(MarkdownRenderer.inline(column < table.header.count ? table.header[column] : ""))
                                    .font(.caption.weight(.medium))
                                    .foregroundStyle(.secondary)
                                Text(MarkdownRenderer.inline(value))
                                    .font(.subheadline)
                                    .fixedSize(horizontal: false, vertical: true)
                            }
                        }
                    }
                }
                .padding(12)
                .frame(maxWidth: .infinity, alignment: .leading)
                .background(Color(.secondarySystemBackground), in: RoundedRectangle(cornerRadius: 14, style: .continuous))
                .accessibilityElement(children: .combine)
            }
        }
    }
}
