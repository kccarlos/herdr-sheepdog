import SheepdogKit
import SwiftUI

/// Settings ▸ Computers ▸ one computer (several computers only): its name
/// on this phone, then for the current one its Connection, Models and
/// Languages; for another one Talk to, and Remove.
struct ComputerSettingsView: View {
    @EnvironmentObject private var model: SessionViewModel
    let id: String
    let done: () -> Void

    @State private var name = ""
    @State private var confirmRemove = false
    @State private var removing = false
    @Environment(\.dismiss) private var dismiss

    private var computer: PairedComputer? { model.settings.computers.computer(id) }
    private var snapshot: ComputerSnapshot? { model.computerSnapshots.first { $0.id == id } }
    private var isCurrent: Bool { id == model.currentComputerID }

    var body: some View {
        Form {
            if let computer {
                Section {
                    TextField(computer.name.isEmpty ? "Name" : computer.name, text: $name)
                        .textInputAutocapitalization(.words)
                        .submitLabel(.done)
                        .onSubmit(saveName)
                        .accessibilityLabel("Name on this phone")
                        .accessibilityIdentifier("computer.name")
                } header: {
                    Text("Name")
                } footer: {
                    Text(computer.name.isEmpty || computer.localName == nil
                         ? "Only this phone uses this name."
                         : "Only this phone uses this name. The computer calls itself \(computer.name).")
                }

                Section {
                    VStack(alignment: .leading, spacing: 4) {
                        Text(isCurrent ? "Talking here" : (snapshot?.summary() ?? ""))
                            .font(.body.weight(.semibold))
                            .foregroundStyle(Theme.ink)
                        Text("Paired \(computer.pairedAt.formatted(date: .abbreviated, time: .omitted))" + (computer.shortPin.isEmpty ? "" : " · cert \(computer.shortPin)"))
                            .font(.caption)
                            .foregroundStyle(Theme.muted)
                    }
                    .padding(.vertical, 2)
                    .accessibilityElement(children: .combine)
                    if isCurrent {
                        NavigationLink(value: SettingsPage.daemon) {
                            Label("Connection", systemImage: "network")
                        }
                        .accessibilityIdentifier("computer.connection")
                        NavigationLink(value: SettingsPage.models) {
                            LabeledContent { Text(model.modelsSummary) } label: { Label("Models", systemImage: "cpu") }
                        }
                        NavigationLink(value: SettingsPage.languages) {
                            LabeledContent { Text(model.daemonSettings?.languagesSummary ?? "—") } label: { Label("Languages", systemImage: "globe") }
                        }
                    } else {
                        Button {
                            model.talk(to: id)
                            done()
                        } label: {
                            Label("Talk to \(computer.displayName)", systemImage: "waveform")
                        }
                        .tint(Theme.primaryText)
                        .disabled({ if case .offline = snapshot?.reachability { return true }; return false }())
                        .accessibilityIdentifier("computer.talkTo")
                    }
                } header: {
                    Text("On this computer")
                }

                Section {
                    Button(role: .destructive) {
                        confirmRemove = true
                    } label: {
                        HStack {
                            Label("Remove", systemImage: "trash")
                            if removing { Spacer(); ProgressView() }
                        }
                    }
                    .disabled(removing)
                    .accessibilityIdentifier("computer.remove")
                } footer: {
                    Text(isCurrent
                         ? "The voice moves to the next computer. The computer forgets this phone."
                         : "The computer forgets this phone, and this phone forgets its key.")
                }
            } else {
                ContentUnavailableView("This computer was removed", systemImage: "desktopcomputer",
                                       description: Text("Go back to see the computers this phone knows."))
            }
        }
        .navigationTitle(computer?.displayName ?? "Computer")
        .navigationBarTitleDisplayMode(.inline)
        .onAppear { name = computer?.localName ?? "" }
        .onDisappear(perform: saveName)
        .confirmationDialog("Remove \(computer?.displayName ?? "this computer")?", isPresented: $confirmRemove, titleVisibility: .visible) {
            Button("Remove", role: .destructive) {
                removing = true
                Task {
                    await model.removeComputer(id)
                    removing = false
                    dismiss()
                }
            }
            .accessibilityIdentifier("computer.remove.confirm")
            Button("Cancel", role: .cancel) {}
        } message: {
            Text("To connect again, scan a new code from that computer.")
        }
    }

    private func saveName() {
        guard let computer else { return }
        let trimmed = name.trimmingCharacters(in: .whitespacesAndNewlines)
        guard trimmed != (computer.localName ?? "") else { return }
        model.renameComputer(id, to: trimmed)
    }
}
