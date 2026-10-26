import SheepdogKit
import SwiftUI

/// The agent window's own actions in the ⋯ menu (docs/dev/design/
/// phone-terminal.md section 5): Rename, Stop current turn and Close
/// agent. Stop and Close ask first; Close names the agent. They need a
/// daemon with `agent-actions`.
enum AgentWindowAction: Identifiable {
    case rename, stop, close
    var id: Self { self }
}

/// The menu's items.
struct AgentActionItems: View {
    let working: Bool
    let perform: (AgentWindowAction) -> Void

    var body: some View {
        Button { perform(.rename) } label: { Label("Rename", systemImage: "pencil") }
            .accessibilityIdentifier("agentScreen.menu.rename")
        Divider()
        Button { perform(.stop) } label: { Label("Stop current turn", systemImage: "stop.circle") }
            .disabled(!working)
            .accessibilityIdentifier("agentScreen.menu.stop")
        Button(role: .destructive) { perform(.close) } label: { Label("Close agent", systemImage: "xmark.circle") }
            .accessibilityIdentifier("agentScreen.menu.close")
    }
}

/// The dialogs behind the items, and the calls.
struct AgentActionDialogs: ViewModifier {
    @Binding var pending: AgentWindowAction?
    let name: String
    let paneID: String
    @ObservedObject var window: AgentWindowController
    let onClosed: () -> Void
    @EnvironmentObject private var model: SessionViewModel
    @State private var newName = ""

    func body(content: Content) -> some View {
        content
            .alert("Rename \(name)", isPresented: binding(.rename)) {
                TextField("Name", text: $newName)
                    .accessibilityIdentifier("agentScreen.rename.field")
                Button("Rename") { run(.rename) }
                    .disabled(newName.trimmingCharacters(in: .whitespaces).isEmpty)
                Button("Cancel", role: .cancel) {}
            } message: {
                Text("The name you and the voice use for it.")
            }
            .confirmationDialog("Stop \(name)'s current turn? It sends the interrupt key its CLI uses.",
                                isPresented: binding(.stop), titleVisibility: .visible) {
                Button("Stop turn", role: .destructive) { run(.stop) }
                    .accessibilityIdentifier("agentScreen.stop.confirm")
                Button("Cancel", role: .cancel) {}
            }
            .alert("Close \(name)?", isPresented: binding(.close)) {
                Button("Close \(name)", role: .destructive) { run(.close) }
                    .accessibilityIdentifier("agentScreen.close.confirm")
                Button("Cancel", role: .cancel) {}
            } message: {
                Text("Its session ends and its pane closes on your computer. This can't be undone.")
            }
            .onChange(of: pending) { _, action in
                if action == .rename { newName = name }
            }
    }

    private func binding(_ action: AgentWindowAction) -> Binding<Bool> {
        Binding(get: { pending == action }, set: { if !$0, pending == action { pending = nil } })
    }

    private func run(_ action: AgentWindowAction) {
        let name = newName
        let pane = paneID
        Task {
            window.sending = true
            window.inputError = nil
            do {
                let reply: AgentActionReply
                switch action {
                case .rename: reply = try await model.action({ ActionRunner.Key.agent($0, pane, "rename:" + name) }, .agent) { try await $0.renameAgent(paneID: pane, name: name) }
                case .stop: reply = try await model.action({ ActionRunner.Key.agent($0, pane, "stop") }, .agent) { try await $0.interruptAgent(paneID: pane) }
                case .close: reply = try await model.action({ ActionRunner.Key.agent($0, pane, "close") }, .agent) { try await $0.closeAgent(paneID: pane) }
                }
                window.sending = false
                window.lastReply = reply.spoken?.trimmedOrNil
                Haptics.answered()
                if action == .close { onClosed() }
            } catch {
                window.sending = false
                window.inputError = model.requestFailure(error).message
                Haptics.answerFailed()
            }
        }
    }
}
