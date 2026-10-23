import SheepdogKit
import SwiftUI

/// The agent window's prompt field (docs/dev/design/phone-terminal.md
/// section 5): one compact line until it is used, then up to six. The
/// trailing button is Send while there is a draft, and Stop while the
/// draft is blank and the agent is working. `leading` is the slot for the
/// attach button (phase 4).
struct TerminalComposer<Leading: View>: View {
    @Binding var text: String
    let placeholder: String
    let enabled: Bool
    let sending: Bool
    /// The agent is working: a blank draft offers Stop.
    let canStop: Bool
    let focused: FocusState<Bool>.Binding
    let onSend: () -> Void
    let onStop: () -> Void
    @ViewBuilder var leading: () -> Leading

    private var draft: String { text.trimmingCharacters(in: .whitespacesAndNewlines) }
    private var showsStop: Bool { draft.isEmpty && canStop && enabled }
    private var canSend: Bool { enabled && AgentScreenModel.canSend(text) }

    var body: some View {
        HStack(alignment: .bottom, spacing: 8) {
            HStack(alignment: .bottom, spacing: 6) {
                leading()
                TextField(placeholder, text: $text, axis: .vertical)
                    .lineLimit(focused.wrappedValue ? 1 ... 6 : 1 ... 1)
                    .textInputAutocapitalization(.sentences)
                    .focused(focused)
                    .disabled(!enabled && !sending)
                    .accessibilityIdentifier("agentScreen.field")
            }
            .padding(.horizontal, 12)
            .padding(.vertical, 8)
            .background(Color(.secondarySystemBackground), in: RoundedRectangle(cornerRadius: 18, style: .continuous))
            button
        }
        .animation(Theme.ease, value: focused.wrappedValue)
    }

    @ViewBuilder private var button: some View {
        if sending {
            ProgressView().frame(width: 32, height: 32)
        } else if showsStop {
            Button(action: onStop) {
                Image(systemName: "stop.circle.fill")
                    .font(.system(size: 30))
                    .foregroundStyle(Color(.label))
            }
            .accessibilityLabel("Stop the current turn")
            .accessibilityIdentifier("agentScreen.stop")
        } else {
            Button(action: onSend) {
                Image(systemName: "arrow.up.circle.fill")
                    .font(.system(size: 30))
                    .foregroundStyle(canSend ? Color.accentColor : Color(.tertiaryLabel))
            }
            .disabled(!canSend)
            .accessibilityLabel("Send prompt")
            .accessibilityIdentifier("agentScreen.send")
        }
    }
}

extension TerminalComposer where Leading == EmptyView {
    init(text: Binding<String>, placeholder: String, enabled: Bool, sending: Bool, canStop: Bool,
         focused: FocusState<Bool>.Binding, onSend: @escaping () -> Void, onStop: @escaping () -> Void) {
        self.init(text: text, placeholder: placeholder, enabled: enabled, sending: sending, canStop: canStop,
                  focused: focused, onSend: onSend, onStop: onStop, leading: { EmptyView() })
    }
}

// MARK: - The controller's keys, typing and Stop

extension AgentWindowController {
    /// A key from the bar or the more-keys sheet, with the latched
    /// modifiers: raw bytes on a live channel, else the daemon's
    /// allow-listed key (the snapshot window), else nothing.
    func send(termKey key: TermKey) {
        if state.takesRawInput {
            var mods = modifiers
            let bytes = mods.apply(to: key.bytes)
            modifiers = mods
            sendRaw(bytes)
            Haptics.tap()
            return
        }
        modifiers = KeyModifiers()
        if let allowed = key.allowListed { send(key: allowed) }
    }

    /// Whether `key` can be sent now (the snapshot window has only the
    /// daemon's allow-list).
    func canSend(_ key: TermKey) -> Bool {
        state.takesRawInput || (canOperate && key.allowListed != nil)
    }

    /// Text typed on the system keyboard in typing mode (IME and
    /// dictation commit through SwiftTerm), with the latched modifiers.
    func typed(_ bytes: [UInt8]) {
        guard !modifiers.isEmpty else { sendRaw(bytes); return }
        var mods = modifiers
        let out = mods.apply(to: bytes)
        modifiers = mods
        sendRaw(out)
    }

    /// Typing mode: the system keyboard types straight into the pane.
    func setTyping(_ on: Bool) {
        let on = on && state.takesRawInput
        guard on != typing else { return }
        typing = on
        surface.setTyping(on)
        if !on { modifiers = KeyModifiers() }
    }

    /// Text into the pane as typed (a snippet in typing mode), no Enter.
    func sendText(_ text: String) {
        typed(Array(text.utf8))
    }

    /// Stop: the daemon's checked interrupt when it has agent actions,
    /// else Ctrl-C.
    func stop(actions: Bool) {
        guard let model else { return }
        guard actions else {
            send(termKey: .ctrl("c"))
            return
        }
        Task {
            sending = true
            inputError = nil
            do {
                let reply = try await model.daemon { try await $0.interruptAgent(paneID: self.paneID) }
                sending = false
                lastReply = reply.spoken?.trimmedOrNil
                Haptics.stop()
            } catch {
                sending = false
                inputError = (error as? DaemonError)?.message ?? error.localizedDescription
                Haptics.answerFailed()
            }
        }
    }
}
