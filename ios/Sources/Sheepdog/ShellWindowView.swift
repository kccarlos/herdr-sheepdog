import SheepdogKit
import SwiftUI
import UIKit

/// Pushed from an agent's ⋯ menu ("Open shell here") or the Agents tab's
/// Shell button: a plain shell in that folder (nil: the first project
/// folder). The daemon keeps one shell per folder and reuses it.
struct ShellRoute: Hashable {
    let cwd: String?
}

/// The phone shell's window (docs/dev/design/phone-terminal.md section 5,
/// "Shell"): the agent window's terminal and key bar, titled "Shell ·
/// folder", with no composer. It opens straight in typing mode. Leaving
/// only detaches; Close shell (⋯, confirmed) ends it on the computer.
struct ShellWindowView: View {
    let route: ShellRoute
    @EnvironmentObject private var model: SessionViewModel

    /// The content observes only its controller: a session change
    /// re-renders nothing in the shell (it shows nothing of the session).
    var body: some View {
        ShellWindowContent(route: route, model: model).equatable()
    }
}

private struct ShellWindowContent: View, Equatable {
    let route: ShellRoute
    /// For actions only.
    let model: SessionViewModel

    static func == (a: Self, b: Self) -> Bool { a.route == b.route && a.model === b.model }

    @StateObject private var window: AgentWindowController
    @Environment(\.colorScheme) private var colorScheme
    @Environment(\.scenePhase) private var scenePhase
    @Environment(\.dismiss) private var dismiss
    @Environment(\.accessibilityReduceMotion) private var reduceMotion
    @State private var showsMoreKeys = false
    @State private var confirmClose = false
    @State private var closeError: String?
    /// Typing mode goes on once, when the shell first goes live.
    @State private var typedOnce = false

    init(route: ShellRoute, model: SessionViewModel) {
        self.route = route
        self.model = model
        _window = StateObject(wrappedValue: AgentWindowController(paneID: "shell:" + (route.cwd ?? ""), shell: true, shellCwd: route.cwd))
    }

    private var title: String { PhoneShell.title(cwd: route.cwd) }
    private var theme: TerminalTheme { colorScheme == .dark ? .dark : .light }
    private var background: Color { Color(theme.background) }
    private var noticeFill: Color { Color(TerminalRGB(hex: colorScheme == .dark ? "#1F2126" : "#F1EEE7")) }
    private var state: AgentWindowState { window.state }
    private var endedEmpty: Bool { state.phase == .ended && !window.hasScreen }

    var body: some View {
        VStack(spacing: 0) {
            if let notice = state.notice, !endedEmpty {
                NoticeBar(notice: notice, fill: noticeFill) { act(on: notice) }
                    .transition(.move(edge: .top).combined(with: .opacity))
            }
            terminal
            if !endedEmpty { controls }
        }
        .background(background.ignoresSafeArea())
        .animation(Theme.ease, value: state.notice)
        .navigationTitle(title)
        .navigationBarTitleDisplayMode(.inline)
        .toolbarBackground(background, for: .navigationBar)
        .toolbarBackground(.visible, for: .navigationBar)
        .toolbar {
            ToolbarItem(placement: .principal) {
                Label(title, systemImage: "apple.terminal")
                    .labelStyle(.titleAndIcon)
                    .font(.headline)
                    .lineLimit(1)
                    .truncationMode(.middle)
                    .dynamicTypeSize(...DynamicTypeSize.accessibility1)
                    .accessibilityElement(children: .ignore)
                    .accessibilityLabel(title)
                    .accessibilityAddTraits(.isHeader)
                    .accessibilityIdentifier("shell.header")
            }
            ToolbarItem(placement: .topBarTrailing) { menu }
        }
        .onAppear {
            window.surface.apply(theme: theme)
            window.attach(model: model)
        }
        .onDisappear { window.detach() }
        .onChange(of: colorScheme) { _, _ in window.surface.apply(theme: theme) }
        .onChange(of: scenePhase) { _, phase in
            switch phase {
            case .background: window.enteredBackground()
            case .active: window.becameActive()
            default: break
            }
        }
        .onChange(of: window.state.takesRawInput) { _, live in
            if live, !typedOnce {
                typedOnce = true
                window.setTyping(true)
            } else if !live {
                window.setTyping(false)
            }
        }
        .sheet(isPresented: $showsMoreKeys) {
            MoreKeysSheet(window: window) { window.sendText($0) }
        }
        .alert("Close this shell?", isPresented: $confirmClose) {
            Button("Close shell", role: .destructive) { close() }
                .accessibilityIdentifier("shell.close.confirm")
            Button("Cancel", role: .cancel) {}
        } message: {
            Text("It ends the shell on your computer, and anything still running in it.")
        }
    }

    /// The ⋯ menu, from values only: it stays still while the screen
    /// streams (`ShellWindowMenu`).
    private var menu: some View {
        ShellWindowMenu(
            canResetTextSize: window.fontSize != SwiftTermSurface.defaultFontSize,
            canClose: window.shellPane != nil
        ) { item in
            switch item {
            case .copy:
                UIPasteboard.general.string = window.surface.snapshotText()
                Haptics.tap()
            case .resetTextSize: window.resetFontSize()
            case .close: confirmClose = true
            }
        }
        .equatable()
    }

    private var terminal: some View {
        ZStack {
            TerminalHost(surface: window.surface)
                .padding(.horizontal, 6)
                .padding(.top, 4)
                .opacity(state.showsSkeleton || endedEmpty ? 0 : 1)
            if state.showsSkeleton {
                TerminalSkeleton(name: title, reduceMotion: reduceMotion, message: "Opening a shell…")
            } else if endedEmpty {
                ContentUnavailableView {
                    Label(state.notice?.text ?? "This shell closed.", systemImage: state.notice == .shellOff ? "lock" : "apple.terminal")
                } description: {
                    Text(PhoneShell.endedDescription(state.notice))
                } actions: {
                    Button("Back") { dismiss() }
                        .accessibilityIdentifier("shell.ended.back")
                }
                .background(background)
                .accessibilityIdentifier("shell.ended")
            }
        }
        .frame(maxWidth: .infinity, maxHeight: .infinity)
        .background(background)
    }

    private var controls: some View {
        VStack(alignment: .leading, spacing: 8) {
            KeyBar(window: window, shell: true, onMore: { showsMoreKeys = true })
            Group {
                if let closeError {
                    Text(closeError)
                        .foregroundStyle(Color(.systemRed))
                        .accessibilityIdentifier("shell.error")
                } else if window.typing {
                    Label("Typing goes straight to the shell.", systemImage: "keyboard")
                        .foregroundStyle(.secondary)
                        .accessibilityIdentifier("shell.typingHint")
                } else {
                    Label("Tap the keyboard key to type.", systemImage: "keyboard")
                        .foregroundStyle(.secondary)
                }
            }
            .font(.caption)
            .fixedSize(horizontal: false, vertical: true)
        }
        .dynamicTypeSize(...DynamicTypeSize.accessibility2)
        .padding(.horizontal, 12)
        .padding(.top, 8)
        .padding(.bottom, 6)
        .overlay(alignment: .top) { Divider() }
    }

    private func act(on notice: AgentWindowState.Notice) {
        switch notice {
        case .herdrDown, .tooMany: window.retry()
        case .shellOff, .shellFolder, .shellClosed, .gone, .notTracked: dismiss()
        case .otherDevice, .snapshotOnly, .reconnecting: break
        }
    }

    private func close() {
        guard let pane = window.shellPane else { return }
        Task {
            do {
                _ = try await model.action({ ActionRunner.Key.agent($0, pane, "closeShell") }, .agent) { try await $0.closeShell(paneID: pane) }
                Haptics.answered()
                dismiss()
            } catch {
                closeError = model.requestFailure(error).message
                Haptics.answerFailed()
            }
        }
    }
}
