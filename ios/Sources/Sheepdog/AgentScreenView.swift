import SheepdogKit
import SwiftUI

/// Pushed from a Flock card or the strip's agent sheet: what an agent's
/// navigation route carries. Only the pane id, so the window always shows
/// the agent's current row.
struct AgentWindowRoute: Hashable {
    let paneID: String
}

/// An agent's live terminal window, and the controls to operate it: a
/// prompt field and a key bar. Polls `GET /api/agents/{pane}/screen` every
/// 1.5 s while on screen, passing the last hash back so an idle window
/// costs a few bytes; follows new output only while scrolled to the
/// bottom. Wide TUI lines scroll sideways instead of wrapping.
struct AgentScreenView: View {
    /// The row the window was opened from (name and state until the first
    /// frame arrives).
    let agent: Agent

    @EnvironmentObject private var model: SessionViewModel
    @State private var screen: AgentScreenModel
    @State private var atBottom = true
    @State private var prompt = ""
    @State private var confirmKey: AgentKey?
    @FocusState private var promptFocused: Bool

    init(agent: Agent) {
        self.agent = agent
        _screen = State(initialValue: AgentScreenModel(paneID: agent.paneID))
    }

    private var name: String { screen.screen?.displayName ?? agent.displayName }
    private var state: Agent.State { screen.screen?.state ?? agent.state }
    private var stateSince: Date? { screen.screen?.stateSince ?? agent.stateSince }

    var body: some View {
        VStack(spacing: 0) {
            header
            terminal
            controls
        }
        .navigationTitle(name)
        .navigationBarTitleDisplayMode(.inline)
        .task(id: agent.paneID) { await poll() }
        .confirmationDialog(
            confirmKey.map { $0.confirmationTitle(agent: name) } ?? "",
            isPresented: Binding(get: { confirmKey != nil }, set: { if !$0 { confirmKey = nil } }),
            titleVisibility: .visible,
            presenting: confirmKey
        ) { key in
            Button("Send \(key.label)", role: .destructive) { send(keys: [key]) }
                .accessibilityIdentifier("agentScreen.confirm")
            Button("Cancel", role: .cancel) {}
        }
    }

    // MARK: - Header

    private var header: some View {
        HStack(spacing: 8) {
            Circle().fill(Theme.color(for: state)).frame(width: 10, height: 10)
            TimelineView(.periodic(from: .now, by: 30)) { context in
                let word = Theme.word(for: state)
                let since = stateSince.map { Agent.duration(context.date.timeIntervalSince($0)) }
                Text(since.map { "\(word) · \($0)" } ?? word)
                    .font(.caption.weight(.semibold))
                    .foregroundStyle(Theme.textColor(for: state))
                    .accessibilityIdentifier("agentScreen.state")
            }
            if let kind = (screen.screen?.kind ?? agent.kind)?.trimmedOrNil {
                Text(kind)
                    .font(.caption2.weight(.medium))
                    .foregroundStyle(.secondary)
                    .padding(.horizontal, 6)
                    .padding(.vertical, 2)
                    .background(Color(.tertiarySystemFill), in: Capsule())
            }
            Spacer(minLength: 4)
            if !atBottom {
                Button {
                    atBottom = true
                } label: {
                    Label("Latest", systemImage: "arrow.down.to.line")
                        .font(.caption.weight(.semibold))
                }
                .accessibilityIdentifier("agentScreen.latest")
            }
        }
        .dynamicTypeSize(...DynamicTypeSize.accessibility1)
        .padding(.horizontal, 16)
        .padding(.vertical, 8)
    }

    // MARK: - Terminal

    private var terminal: some View {
        GeometryReader { viewport in
            ScrollViewReader { proxy in
                ScrollView(.vertical) {
                    VStack(alignment: .leading, spacing: 0) {
                        ScrollView(.horizontal, showsIndicators: true) {
                            Text(screen.text.isEmpty ? placeholder : screen.text)
                                .font(.system(.footnote, design: .monospaced))
                                .foregroundStyle(screen.text.isEmpty ? .secondary : .primary)
                                .fixedSize(horizontal: true, vertical: true)
                                .textSelection(.enabled)
                                .padding(12)
                                .accessibilityIdentifier("agentScreen.text")
                        }
                        Color.clear
                            .frame(height: 1)
                            .id(Self.bottomID)
                            .background(GeometryReader { g in
                                Color.clear.preference(key: BottomOffsetKey.self, value: g.frame(in: .named(Self.space)).maxY)
                            })
                    }
                    .frame(maxWidth: .infinity, alignment: .leading)
                }
                .coordinateSpace(name: Self.space)
                .background(Color(.secondarySystemBackground))
                .refreshable { await refresh() }
                .onPreferenceChange(BottomOffsetKey.self) { maxY in
                    atBottom = AgentScreenModel.isAtBottom(anchorMaxY: maxY, viewportHeight: viewport.size.height)
                }
                .onChange(of: screen.revision) { _, _ in
                    // After the new text has been laid out.
                    if atBottom { DispatchQueue.main.async { proxy.scrollTo(Self.bottomID, anchor: .bottom) } }
                }
                .onChange(of: viewport.size.height) { _, _ in
                    // The keyboard (or a rotation) resized the window: one
                    // that was following the bottom keeps following it.
                    if atBottom { DispatchQueue.main.async { proxy.scrollTo(Self.bottomID, anchor: .bottom) } }
                }
                .onChange(of: atBottom) { _, now in
                    // The "Latest" button sets it; scrolling down by hand
                    // arrives here already at the bottom.
                    if now { withAnimation { proxy.scrollTo(Self.bottomID, anchor: .bottom) } }
                }
            }
        }
    }

    private static let bottomID = "agentScreen.bottom"
    private static let space = "agentScreen.scroll"

    private var placeholder: String {
        if screen.error != nil { return "No screen." }
        return "Reading \(name)'s window…"
    }

    // MARK: - Controls

    private var controls: some View {
        VStack(alignment: .leading, spacing: 8) {
            if let error = screen.error {
                Label(error, systemImage: "exclamationmark.triangle")
                    .font(.caption)
                    .foregroundStyle(Color(.systemRed))
                    .fixedSize(horizontal: false, vertical: true)
                    .accessibilityIdentifier("agentScreen.error")
            }
            keyBar
            HStack(spacing: 8) {
                TextField("Prompt \(name)…", text: $prompt, axis: .vertical)
                    .lineLimit(1 ... 4)
                    .textInputAutocapitalization(.sentences)
                    .submitLabel(.send)
                    .focused($promptFocused)
                    .onSubmit(sendPrompt)
                    .padding(.horizontal, 12)
                    .padding(.vertical, 8)
                    .background(Color(.secondarySystemBackground), in: RoundedRectangle(cornerRadius: 18, style: .continuous))
                    .disabled(!screen.canOperate && !screen.sending)
                    .accessibilityIdentifier("agentScreen.field")
                Button(action: sendPrompt) {
                    if screen.sending {
                        ProgressView().frame(width: 32, height: 32)
                    } else {
                        Image(systemName: "arrow.up.circle.fill")
                            .font(.system(size: 30))
                            .foregroundStyle(canSendPrompt ? Color.accentColor : Color(.tertiaryLabel))
                    }
                }
                .disabled(!canSendPrompt)
                .accessibilityLabel("Send prompt")
                .accessibilityIdentifier("agentScreen.send")
            }
            if let error = screen.inputError {
                Text(error)
                    .font(.caption)
                    .foregroundStyle(Color(.systemRed))
                    .fixedSize(horizontal: false, vertical: true)
                    .accessibilityIdentifier("agentScreen.inputError")
            } else if let reply = screen.lastReply {
                Text(reply)
                    .font(.caption)
                    .foregroundStyle(.secondary)
                    .fixedSize(horizontal: false, vertical: true)
                    .accessibilityIdentifier("agentScreen.reply")
            }
        }
        .dynamicTypeSize(...DynamicTypeSize.accessibility2)
        .padding(.horizontal, 12)
        .padding(.vertical, 8)
    }

    private var keyBar: some View {
        ScrollView(.horizontal, showsIndicators: false) {
            HStack(spacing: 6) {
                ForEach(AgentKey.allCases) { key in
                    Button {
                        if key.needsConfirmation {
                            confirmKey = key
                        } else {
                            send(keys: [key])
                        }
                    } label: {
                        Text(key.label)
                            .font(.system(.subheadline, design: .monospaced).weight(.semibold))
                            .frame(minWidth: 36, minHeight: 36)
                            .padding(.horizontal, 6)
                    }
                    .buttonStyle(.bordered)
                    .tint(key.needsConfirmation ? Color(.systemRed) : .accentColor)
                    .disabled(!screen.canOperate)
                    .accessibilityLabel(key.accessibilityLabel)
                    .accessibilityIdentifier("agentScreen.key.\(key.rawValue)")
                }
            }
        }
    }

    private var canSendPrompt: Bool { screen.canOperate && AgentScreenModel.canSend(prompt) }

    // MARK: - Actions

    private func poll() async {
        while !Task.isCancelled {
            await refresh()
            if screen.unsupported { return }
            try? await Task.sleep(nanoseconds: UInt64(AgentScreenModel.pollInterval * 1_000_000_000))
        }
    }

    private func refresh() async {
        do {
            if let frame = try await model.agentScreen(paneID: agent.paneID, hash: screen.hashForNextRead) {
                screen.apply(frame)
            } else {
                screen.markUnsupported()
            }
        } catch is CancellationError {
            return
        } catch {
            screen.fail(error)
        }
    }

    private func sendPrompt() {
        guard canSendPrompt else { return }
        let text = prompt.trimmingCharacters(in: .whitespacesAndNewlines)
        Task {
            screen.beginSend()
            do {
                let reply = try await model.agentInput(paneID: agent.paneID, text: text)
                screen.finishSend(reply: reply)
                prompt = ""
                Haptics.answered()
                atBottom = true
                await refresh()
            } catch {
                screen.finishSend(error: error)
                Haptics.answerFailed()
            }
        }
    }

    private func send(keys: [AgentKey]) {
        guard screen.canOperate else { return }
        Task {
            screen.beginSend()
            do {
                let reply = try await model.agentInput(paneID: agent.paneID, keys: keys)
                screen.finishSend(reply: reply)
                Haptics.answered()
                await refresh()
            } catch {
                screen.finishSend(error: error)
                Haptics.answerFailed()
            }
        }
    }
}

/// The bottom anchor's maxY in the scroll view's coordinate space.
private struct BottomOffsetKey: PreferenceKey {
    static var defaultValue: CGFloat = 0
    static func reduce(value: inout CGFloat, nextValue: () -> CGFloat) { value = nextValue() }
}
