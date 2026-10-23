import SheepdogKit
import SwiftUI

/// Settings ▸ Connection ▸ Addresses: the paired computer's addresses, the
/// one in use checked. "Add address" is for a router's port forward, whose
/// address the computer cannot put in its pairing code. Every address is
/// checked against the computer's key (its pin) before the app keeps or
/// uses it.
struct AddressesView: View {
    /// The addresses and the one in use, read again after every change.
    let snapshot: () -> (addresses: [String], current: String)
    /// Checks a typed address and adds it; nil on success, else the reason.
    let add: (String) async -> String?
    /// Checks an address and uses it; nil on success, else the reason.
    let use: (String) async -> String?
    let remove: (String) -> Void
    var initialError: String?

    @State private var addresses: [String] = []
    @State private var current = ""
    @State private var busy: String?
    @State private var error: String?
    @State private var adding = false
    @State private var typed = ""

    private static let addingKey = "+"

    var body: some View {
        Form {
            Section {
                ForEach(addresses, id: \.self) { address in
                    row(address)
                }
            } footer: {
                Text("The app talks to the checked address. Tap another to check it and switch.")
            }
            Section {
                Button {
                    typed = ""
                    adding = true
                } label: {
                    HStack {
                        Label("Add address", systemImage: "plus.circle")
                        Spacer()
                        if busy == Self.addingKey { ProgressView().accessibilityLabel("Checking the address") }
                    }
                }
                .tint(Theme.primaryText)
                .disabled(busy != nil)
                .accessibilityIdentifier("addresses.add")
            } footer: {
                Text("For a router's port forward: the router's address and port, like 100.101.1.1:7777. Subnet routing needs nothing.")
            }
            if let error {
                Section {
                    Label {
                        Text(error).fixedSize(horizontal: false, vertical: true)
                    } icon: {
                        Image(systemName: "exclamationmark.triangle.fill")
                    }
                    .font(.subheadline)
                    .foregroundStyle(Theme.danger)
                    .accessibilityIdentifier("addresses.error")
                }
            }
        }
        .navigationTitle("Addresses")
        .navigationBarTitleDisplayMode(.inline)
        .alert("Add address", isPresented: $adding) {
            TextField("100.101.1.1:7777", text: $typed)
                .keyboardType(.numbersAndPunctuation)
                .textInputAutocapitalization(.never)
                .autocorrectionDisabled()
                .accessibilityIdentifier("addresses.field")
            Button("Add") { run(Self.addingKey) { await add(typed) } }
                .accessibilityIdentifier("addresses.confirm")
            Button("Cancel", role: .cancel) {}
        } message: {
            Text("The app checks it against this computer's key before keeping it.")
        }
        .onAppear {
            refresh()
            if error == nil { error = initialError }
        }
    }

    private func row(_ address: String) -> some View {
        let inUse = address == current
        return Button {
            guard !inUse else { return }
            run(address) { await use(address) }
        } label: {
            HStack {
                Text(address)
                    .font(.body.monospaced())
                    .foregroundStyle(Theme.ink)
                    .lineLimit(1)
                    .minimumScaleFactor(0.7)
                Spacer()
                if busy == address {
                    ProgressView()
                } else if inUse {
                    Image(systemName: "checkmark")
                        .font(.body.weight(.semibold))
                        .foregroundStyle(Theme.primaryText)
                }
            }
            .frame(minHeight: 44)
            .contentShape(Rectangle())
        }
        .disabled(busy != nil)
        .accessibilityLabel(inUse ? "\(address), in use" : address)
        .accessibilityHint(inUse ? "" : "Checks this address and switches to it")
        .accessibilityIdentifier("addresses.row")
        .swipeActions {
            if !inUse {
                Button(role: .destructive) {
                    remove(address)
                    refresh()
                } label: {
                    Label("Remove", systemImage: "trash")
                }
            }
        }
    }

    private func run(_ key: String, _ action: @escaping () async -> String?) {
        busy = key
        error = nil
        Task {
            let problem = await action()
            busy = nil
            error = problem
            refresh()
        }
    }

    private func refresh() {
        let s = snapshot()
        addresses = s.addresses
        current = s.current
    }
}
