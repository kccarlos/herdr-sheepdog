import SheepdogKit
import SwiftUI

/// Settings ▸ Connection ▸ Addresses: the paired computer's addresses, which
/// one the app uses now and why, and the preferred one
/// (docs/dev/design/ios-state-architecture.md 3.3). The app tries the
/// preferred address first and uses the first that answers with the
/// computer's key (its pin), so a phone that leaves home moves to another
/// address by itself. "Add address" is for a router's port forward, whose
/// address the computer cannot put in its pairing code.
struct AddressesView: View {
    /// The rows, live from the current computer's link.
    let rows: [AddressRow]
    /// Checks a typed address and adds it; nil on success, else the reason.
    let add: (String) async -> String?
    let prefer: (String) -> Void
    let remove: (String) -> Void
    var initialError: String?

    @State private var busy = false
    @State private var error: String?
    @State private var adding = false
    @State private var typed = ""

    var body: some View {
        Form {
            Section {
                ForEach(rows) { row in
                    rowView(row)
                }
            } footer: {
                Text("The app tries the preferred address first, then the others, and uses the first that answers with this computer's key. Tap an address to prefer it.")
            }
            Section {
                Button {
                    typed = ""
                    adding = true
                } label: {
                    HStack {
                        Label("Add address", systemImage: "plus.circle")
                        Spacer()
                        if busy { ProgressView().accessibilityLabel("Checking the address") }
                    }
                }
                .tint(Theme.primaryText)
                .disabled(busy)
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
            Button("Add") { addTyped() }
                .accessibilityIdentifier("addresses.confirm")
            Button("Cancel", role: .cancel) {}
        } message: {
            Text("The app checks it against this computer's key before keeping it.")
        }
        .onAppear {
            if error == nil { error = initialError }
        }
    }

    private func rowView(_ row: AddressRow) -> some View {
        Button {
            guard !row.isPreferred else { return }
            prefer(row.address)
        } label: {
            // "Preferred" sits on the right while the address fits next to
            // it, and under the address at the large text sizes.
            ViewThatFits(in: .horizontal) {
                HStack(alignment: .center, spacing: 12) {
                    details(row, scales: false)
                    Spacer(minLength: 8)
                    if row.isPreferred { preferredLabel }
                }
                VStack(alignment: .leading, spacing: 3) {
                    details(row, scales: true)
                    if row.isPreferred { preferredLabel }
                }
            }
            .frame(maxWidth: .infinity, minHeight: 44, alignment: .leading)
            .contentShape(Rectangle())
        }
        .disabled(busy)
        .accessibilityElement(children: .ignore)
        .accessibilityLabel(Self.spoken(row))
        .accessibilityHint(row.isPreferred ? "" : "Prefers this address: the app tries it first")
        .accessibilityAddTraits(.isButton)
        .accessibilityIdentifier("addresses.row")
        .swipeActions {
            if Self.removable(row) {
                Button(role: .destructive) {
                    remove(row.address)
                } label: {
                    Label("Remove", systemImage: "trash")
                }
            }
        }
    }

    /// The address and its status. `scales`: the address may shrink to
    /// fit one line (an address does not wrap well).
    private func details(_ row: AddressRow, scales: Bool) -> some View {
        VStack(alignment: .leading, spacing: 3) {
            Text(row.address)
                .font(.body.monospaced())
                .foregroundStyle(Theme.ink)
                .lineLimit(1)
                .minimumScaleFactor(scales ? 0.5 : 1)
            if let text = row.statusText {
                Label {
                    Text(text).fixedSize(horizontal: false, vertical: true)
                } icon: {
                    Image(systemName: Self.icon(row.status))
                }
                .font(.footnote)
                .foregroundStyle(Self.tint(row.status))
            }
        }
    }

    private var preferredLabel: some View {
        Text("Preferred")
            .font(.footnote.weight(.medium))
            .foregroundStyle(Theme.muted)
    }

    /// Neither the preferred address nor the one in use is removed.
    private static func removable(_ row: AddressRow) -> Bool { !row.isPreferred && !row.isInUse }

    private static func spoken(_ row: AddressRow) -> String {
        [row.address, row.isPreferred ? "preferred" : nil, row.statusText].compactMap { $0 }.joined(separator: ", ")
    }

    private static func icon(_ status: AddressRow.Status?) -> String {
        switch status {
        case .inUse: return "checkmark.circle.fill"
        case .checking: return "arrow.triangle.2.circlepath"
        case .anotherKey: return "exclamationmark.shield.fill"
        case nil: return "circle"
        }
    }

    private static func tint(_ status: AddressRow.Status?) -> Color {
        switch status {
        case .inUse: return Theme.primaryText
        case .anotherKey: return Theme.danger
        case .checking, nil: return Theme.muted
        }
    }

    private func addTyped() {
        busy = true
        error = nil
        let text = typed
        Task {
            let problem = await add(text)
            busy = false
            error = problem
        }
    }
}

/// The Addresses screen on the current computer's live rows.
struct AddressesScreen: View {
    @EnvironmentObject private var model: SessionViewModel

    var body: some View {
        AddressesView(rows: model.addressRows, add: { await model.addAddress($0) },
                      prefer: { model.preferAddress($0) }, remove: { model.removeAddress($0) })
            .onAppear { model.presentAddresses() }
    }
}

#if DEBUG
/// The Addresses screen with made-up addresses (`-sheepdogAddressesScreen`):
/// on Wi-Fi the LAN address is in use although the Tailscale address is
/// preferred, and a third answered with another key. Prefer moves the
/// preferred label and the race's winner, like the real link.
struct AddressesFixture: View {
    var error: Bool
    @State private var addresses = ["100.101.102.103:7777", "192.168.1.20:7777", "100.101.1.1:7777"]
    @State private var preferred = "100.101.102.103:7777"
    @State private var inUse = "192.168.1.20:7777"

    var body: some View {
        AddressesView(rows: rows, add: { _ in nil },
                      prefer: { address in
                          preferred = address
                          // The fixture's network: the mismatching address never wins.
                          if address != "100.101.1.1:7777" { inUse = address }
                      },
                      remove: { address in addresses.removeAll { $0 == address } },
                      initialError: error ? "Something answered at 100.101.1.2:7777, but not with this computer's key, so the app did not use it." : nil)
    }

    private var rows: [AddressRow] {
        var ordered = addresses.filter { $0 != preferred }
        ordered.insert(preferred, at: 0)
        return ordered.map { address in
            let status: AddressRow.Status? = address == inUse ? .inUse(.wifi) : address == "100.101.1.1:7777" ? .anotherKey : nil
            return AddressRow(address: address, isPreferred: address == preferred, status: status)
        }
    }
}
#endif
