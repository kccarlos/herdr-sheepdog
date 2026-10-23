import SheepdogKit
import SwiftUI

/// The key bar under the terminal: esc ⇥ ↑ ↓ ⏎, then ⌨︎ (typing mode)
/// and ··· (more keys). A shell window adds ⌃C before ⏎: there it is the
/// everyday key; an agent window keeps it in More keys. Every key goes out
/// at once, with no confirm. In typing mode the ⌃C slot (or, in an agent
/// window, a slot before ⏎) is ⌃, which latches for the next key or typed
/// character.
struct KeyBar: View {
    @ObservedObject var window: AgentWindowController
    /// A shell window: ⌃C on the bar.
    var shell = false
    let onMore: () -> Void

    var body: some View {
        HStack(spacing: 6) {
            ForEach([TermKey.esc, .tab, .up, .down]) { key($0) }
            if window.typing {
                KeyCap(label: "⌃", accessibility: "Control, for the next key", enabled: true, latched: window.modifiers.ctrl) {
                    window.modifiers.ctrl.toggle()
                    Haptics.tap()
                }
                .accessibilityValue(window.modifiers.ctrl ? "on" : "off")
                .accessibilityIdentifier("agentScreen.key.ctrl")
            } else if shell {
                key(.ctrl("c"))
            }
            key(.enter)
            KeyCap(systemImage: window.typing ? "keyboard.chevron.compact.down" : "keyboard",
                   accessibility: window.typing ? "Stop typing in the terminal" : "Type in the terminal",
                   enabled: window.state.takesRawInput, latched: window.typing) {
                window.setTyping(!window.typing)
            }
            .accessibilityIdentifier("agentScreen.key.typing")
            KeyCap(systemImage: "ellipsis", accessibility: "More keys", enabled: window.canOperate || window.state.takesRawInput, action: onMore)
                .accessibilityIdentifier("agentScreen.key.more")
        }
        .accessibilityElement(children: .contain)
        .accessibilityIdentifier("agentScreen.keyBar")
    }

    private func key(_ key: TermKey, tint: Color? = nil) -> some View {
        KeyCap(label: key.label, accessibility: key.accessibilityLabel, enabled: window.canSend(key), tint: tint) {
            window.send(termKey: key)
        }
        .accessibilityIdentifier("agentScreen.key.\(key.allowListed?.rawValue ?? key.label)")
    }
}

/// One key: a monospaced glyph or a symbol on a quiet fill; orange when
/// latched.
struct KeyCap: View {
    var label: String?
    var systemImage: String?
    let accessibility: String
    let enabled: Bool
    var latched = false
    var tint: Color?
    let action: () -> Void

    init(label: String, accessibility: String, enabled: Bool, latched: Bool = false, tint: Color? = nil, action: @escaping () -> Void) {
        self.label = label
        self.tint = tint
        self.accessibility = accessibility
        self.enabled = enabled
        self.latched = latched
        self.action = action
    }

    init(systemImage: String, accessibility: String, enabled: Bool, latched: Bool = false, action: @escaping () -> Void) {
        self.systemImage = systemImage
        self.accessibility = accessibility
        self.enabled = enabled
        self.latched = latched
        self.action = action
    }

    var body: some View {
        Button(action: action) {
            Group {
                if let systemImage {
                    Image(systemName: systemImage).font(.subheadline.weight(.medium))
                } else {
                    Text(label ?? "").font(.system(.subheadline, design: .monospaced).weight(.medium))
                }
            }
            .lineLimit(1)
            .minimumScaleFactor(0.7)
            .foregroundStyle(latched ? Color.white : (tint ?? Color(.label)))
            .frame(maxWidth: .infinity, minHeight: 36)
            .background(latched ? Color.accentColor : Color(.tertiarySystemFill), in: RoundedRectangle(cornerRadius: 8, style: .continuous))
        }
        .buttonStyle(.plain)
        .disabled(!enabled)
        .opacity(enabled ? 1 : 0.4)
        .accessibilityLabel(accessibility)
    }
}

/// "··· more keys": modifiers, the rest of the keys, answers, F-keys and
/// the user's snippets. Keys go out at once and the sheet stays, like a
/// keyboard; a snippet goes into the composer (or the pane in typing
/// mode) and closes it.
struct MoreKeysSheet: View {
    @ObservedObject var window: AgentWindowController
    /// An agent window: ⌃C sits at the top, under Interrupt.
    var interrupt = false
    let onSnippet: (String) -> Void
    @Environment(\.dismiss) private var dismiss
    @State private var snippets = SnippetStore.load()
    @State private var newSnippet = ""

    private let columns = [GridItem(.adaptive(minimum: 58), spacing: 8)]

    var body: some View {
        NavigationStack {
            List {
                if interrupt {
                    Section {
                        KeyCap(label: "⌃C", accessibility: "Control C, interrupts the agent", enabled: window.canSend(.ctrl("c"))) {
                            window.send(termKey: .ctrl("c"))
                        }
                        .accessibilityIdentifier("moreKeys.ctrlC")
                    } header: {
                        Text("Interrupt")
                    } footer: {
                        Text("Sends Ctrl-C at once. Esc on the bar also stops Claude and Codex.")
                    }
                }
                if window.state.takesRawInput {
                    Section("Modifiers, for the next key") {
                        HStack(spacing: 8) {
                            KeyCap(label: "Ctrl", accessibility: "Control", enabled: true, latched: window.modifiers.ctrl) { window.modifiers.ctrl.toggle() }
                                .accessibilityIdentifier("moreKeys.ctrl")
                            KeyCap(label: "Alt", accessibility: "Alt", enabled: true, latched: window.modifiers.alt) { window.modifiers.alt.toggle() }
                                .accessibilityIdentifier("moreKeys.alt")
                        }
                    }
                } else {
                    Section {
                        Label("Only the keys the agent window allows work here. The live window sends every key.", systemImage: "info.circle")
                            .font(.footnote)
                            .foregroundStyle(.secondary)
                    }
                }
                grid("Keys", KeyEncoding.keys)
                grid("Arrows and paging", KeyEncoding.navigation)
                grid("Answers", KeyEncoding.answers)
                grid("Function keys", KeyEncoding.functions)
                snippetSection
            }
            .listStyle(.insetGrouped)
            .navigationTitle("More keys")
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .confirmationAction) {
                    Button("Done") { dismiss() }
                        .accessibilityIdentifier("moreKeys.done")
                }
            }
        }
        .presentationDetents([.medium, .large])
        .presentationDragIndicator(.visible)
    }

    private func grid(_ title: String, _ keys: [TermKey]) -> some View {
        Section(title) {
            LazyVGrid(columns: columns, spacing: 8) {
                ForEach(keys) { key in
                    KeyCap(label: key.label, accessibility: key.accessibilityLabel, enabled: window.canSend(key)) {
                        window.send(termKey: key)
                    }
                    .accessibilityIdentifier("moreKeys.\(key.label)")
                }
            }
            .padding(.vertical, 4)
        }
    }

    private var snippetSection: some View {
        Section {
            if snippets.items.isEmpty {
                Text("No snippets yet. Save text you send often, like “run the tests and fix what fails”.")
                    .font(.footnote)
                    .foregroundStyle(.secondary)
                    .accessibilityIdentifier("moreKeys.snippets.empty")
            }
            ForEach(snippets.items, id: \.self) { item in
                Button {
                    onSnippet(item)
                    dismiss()
                } label: {
                    Text(item).lineLimit(2).foregroundStyle(Color(.label))
                }
                .accessibilityHint(window.typing ? "Types it in the terminal" : "Puts it in the prompt field")
            }
            .onDelete { offsets in
                snippets.remove(atOffsets: offsets)
                snippets.save()
            }
            HStack {
                TextField("New snippet", text: $newSnippet, axis: .vertical)
                    .lineLimit(1 ... 3)
                    .accessibilityIdentifier("moreKeys.snippets.field")
                Button("Save") {
                    if snippets.add(newSnippet) {
                        snippets.save()
                        newSnippet = ""
                    }
                }
                .disabled(newSnippet.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty)
                .accessibilityIdentifier("moreKeys.snippets.save")
            }
        } header: {
            Text("Snippets")
        }
    }
}
