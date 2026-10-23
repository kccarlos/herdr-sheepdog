import SwiftUI

/// The agent window's ⋯ menu, built from plain values only.
///
/// The window re-renders on every screen push (`AgentWindowController`) and
/// on every change of the session; a menu built there had its items rebuilt
/// each time, and an open menu flickered. As an `Equatable` view (closures
/// left out of `==`), it re-renders only when what it shows changes.
struct AgentWindowMenu: View, Equatable {
    enum Item {
        case takeOver, copy, changes, resetTextSize
        case shell(cwd: String)
        case action(AgentWindowAction)
        case switchTo(String)
    }

    struct Other: Equatable {
        var paneID: String
        var name: String
        var blocked: Bool
    }

    /// The iPhone's navigation bar, or an iPad pane's header.
    enum Style: Equatable { case toolbar, pane }

    var style: Style
    var observing: Bool
    /// Open shell here: the agent's folder, when the shell is offered.
    var shellCwd: String?
    var canResetTextSize: Bool
    var actionsAvailable: Bool
    var working: Bool
    var others: [Other]
    /// iPad's Open in New Window (fixed for a pane's life).
    var extra: AnyView?
    var perform: (Item) -> Void

    static func == (a: Self, b: Self) -> Bool {
        a.style == b.style && a.observing == b.observing && a.shellCwd == b.shellCwd
            && a.canResetTextSize == b.canResetTextSize && a.actionsAvailable == b.actionsAvailable
            && a.working == b.working && a.others == b.others && (a.extra == nil) == (b.extra == nil)
    }

    var body: some View {
        #if DEBUG
        let _ = RenderProbe.hit("menu.agent")
        #endif
        switch style {
        case .toolbar:
            Menu { items } label: {
                Image(systemName: "ellipsis.circle")
                    .accessibilityLabel("More")
            }
            .accessibilityIdentifier("agentScreen.menu")
        case .pane:
            Menu { items } label: {
                Image(systemName: "ellipsis")
                    .font(.system(size: 17, weight: .semibold))
                    .foregroundStyle(Theme.ink)
                    .frame(width: 44, height: 44)
                    .contentShape(Circle())
                    .hoverEffect()
            }
            .accessibilityLabel("Agent actions")
            .accessibilityIdentifier("agentScreen.menu")
        }
    }

    @ViewBuilder
    private var items: some View {
        if let extra { extra }
        if observing {
            Button { perform(.takeOver) } label: { Label("Take over", systemImage: "hand.raised") }
                .accessibilityIdentifier("agentScreen.menu.takeover")
        }
        Button { perform(.copy) } label: { Label("Copy screen", systemImage: "doc.on.doc") }
            .accessibilityIdentifier("agentScreen.menu.copy")
        Button { perform(.changes) } label: { Label("Changes", systemImage: "plusminus") }
            .accessibilityIdentifier("agentScreen.menu.changes")
        if let shellCwd {
            Button { perform(.shell(cwd: shellCwd)) } label: { Label("Open shell here", systemImage: "apple.terminal") }
                .accessibilityIdentifier("agentScreen.menu.shell")
        }
        Button { perform(.resetTextSize) } label: { Label("Reset text size", systemImage: "textformat.size") }
            .disabled(!canResetTextSize)
            .accessibilityIdentifier("agentScreen.menu.textSize")
        if actionsAvailable {
            AgentActionItems(working: working) { perform(.action($0)) }
        }
        if !others.isEmpty {
            Menu {
                ForEach(others, id: \.paneID) { other in
                    Button { perform(.switchTo(other.paneID)) } label: {
                        Label(other.name, systemImage: other.blocked ? "hand.raised.fill" : "terminal")
                    }
                }
            } label: {
                Label("Switch agent", systemImage: "rectangle.stack")
            }
        }
    }
}

/// The shell window's ⋯ menu, from plain values for the same reason as
/// `AgentWindowMenu`.
struct ShellWindowMenu: View, Equatable {
    enum Item { case copy, resetTextSize, close }

    var canResetTextSize: Bool
    var canClose: Bool
    var perform: (Item) -> Void

    static func == (a: Self, b: Self) -> Bool {
        a.canResetTextSize == b.canResetTextSize && a.canClose == b.canClose
    }

    var body: some View {
        #if DEBUG
        let _ = RenderProbe.hit("menu.shell")
        #endif
        Menu {
            Button { perform(.copy) } label: { Label("Copy screen", systemImage: "doc.on.doc") }
                .accessibilityIdentifier("shell.menu.copy")
            Button { perform(.resetTextSize) } label: { Label("Reset text size", systemImage: "textformat.size") }
                .disabled(!canResetTextSize)
            Divider()
            Button(role: .destructive) { perform(.close) } label: { Label("Close shell", systemImage: "xmark.circle") }
                .disabled(!canClose)
                .accessibilityIdentifier("shell.menu.close")
        } label: {
            Image(systemName: "ellipsis.circle")
                .accessibilityLabel("More")
        }
        .accessibilityIdentifier("shell.menu")
    }
}
