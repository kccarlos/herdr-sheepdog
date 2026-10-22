import SheepdogKit
import SwiftUI

/// A space's wrap-up command: save a new one, edit a saved one, or run a
/// line once ("Run a command…"), with a switch to keep it for next time.
/// One line, run in a new tab of the space on the computer.
struct CommandSheet: View {
    enum Mode: Identifiable, Equatable {
        case add
        case edit(SpacesView.WrapUp)
        case runOnce

        var id: String {
            switch self {
            case .add: return "add"
            case let .edit(c): return "edit." + c.id
            case .runOnce: return "run"
            }
        }
    }

    let mode: Mode
    let spaceName: String
    /// name, command, and (run once) whether to save it too.
    let done: (String, String, Bool) async -> Void

    @Environment(\.dismiss) private var dismiss
    @State private var name = ""
    @State private var command = ""
    @State private var save = false
    @State private var working = false
    @FocusState private var focus: Field?

    private enum Field { case name, command }

    private var needsName: Bool { mode != .runOnce || save }

    private var cleanName: String { name.trimmingCharacters(in: .whitespaces) }
    private var cleanCommand: String { command.trimmingCharacters(in: .whitespaces) }

    private var valid: Bool {
        guard !cleanCommand.isEmpty, !cleanCommand.contains("\n"), cleanCommand.utf8.count <= 1000 else { return false }
        return !needsName || (!cleanName.isEmpty && cleanName.count <= 40)
    }

    private var title: String {
        switch mode {
        case .add: return "Save a command"
        case .edit: return "Edit command"
        case .runOnce: return "Run a command"
        }
    }

    private var actionTitle: String {
        switch mode {
        case .add, .edit: return "Save"
        case .runOnce: return "Run"
        }
    }

    var body: some View {
        NavigationStack {
            Form {
                Section {
                    TextField("Command, like npm test", text: $command, axis: .vertical)
                        .font(.body.monospaced())
                        .textInputAutocapitalization(.never)
                        .autocorrectionDisabled()
                        .focused($focus, equals: .command)
                        .accessibilityLabel("Command")
                        .accessibilityIdentifier("space.command.line")
                } footer: {
                    Text("One line. It runs in a new tab of \(spaceName) on your computer.")
                }
                if mode == .runOnce {
                    Section {
                        Toggle("Save it for next time", isOn: $save.animation(Theme.ease))
                            .accessibilityIdentifier("space.command.keep")
                    }
                }
                if needsName {
                    Section {
                        TextField("Name, like Run tests", text: $name)
                            .focused($focus, equals: .name)
                            .accessibilityLabel("Name")
                            .accessibilityIdentifier("space.command.name")
                    } footer: {
                        if cleanName.count > 40 {
                            Text("A name is at most 40 characters.").foregroundStyle(Theme.danger)
                        }
                    }
                }
            }
            .navigationTitle(title)
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .cancellationAction) { Button("Cancel") { dismiss() } }
                ToolbarItem(placement: .confirmationAction) {
                    Button {
                        working = true
                        Task {
                            await done(cleanName, cleanCommand, save)
                            dismiss()
                        }
                    } label: {
                        if working { ProgressView() } else { Text(actionTitle).fontWeight(.semibold) }
                    }
                    .disabled(!valid || working)
                    .accessibilityIdentifier("space.command.save")
                }
            }
            .onAppear {
                if case let .edit(c) = mode {
                    name = c.name
                    command = c.command
                }
                focus = .command
            }
        }
        .presentationDetents([.medium, .large])
    }
}
