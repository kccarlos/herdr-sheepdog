import SheepdogKit
import SwiftUI

/// Where the daemon lives, what the token is, and one button that proves
/// both. The token goes to the keychain; nothing else is a secret.
struct SettingsView: View {
    @EnvironmentObject private var model: SessionViewModel
    @Environment(\.dismiss) private var dismiss

    @State private var address = ""
    @State private var token = ""
    @State private var keepListening = true
    @State private var probing = false
    @State private var probe: SessionViewModel.Probe?
    @State private var confirmClear = false
    @FocusState private var addressFocused: Bool

    private var firstRun: Bool { !model.settings.hasBeenConfigured }

    var body: some View {
        NavigationStack {
            Form {
                if firstRun {
                    Section {
                        VStack(alignment: .leading, spacing: 6) {
                            Text("Point the app at your daemon")
                                .font(.headline)
                            Text("Run `sheepdog serve` on your Mac with `web.bind` set to your LAN or Tailscale address and `web.token` set to a secret. The daemon's banner prints a `lan url` line — the host and port from that line go below.")
                                .font(.footnote)
                                .foregroundStyle(.secondary)
                        }
                        .padding(.vertical, 2)
                    }
                }

                Section {
                    LabeledContent("Address") {
                        TextField("192.168.8.141:7777", text: $address)
                            .textInputAutocapitalization(.never)
                            .autocorrectionDisabled()
                            .keyboardType(.URL)
                            .multilineTextAlignment(.trailing)
                            .focused($addressFocused)
                            .accessibilityIdentifier("settings.address")
                    }
                    LabeledContent("Token") {
                        SecureField("web.token, if it has one", text: $token)
                            // Not a website password: this stops iOS offering
                            // to save it to the user's passwords every time
                            // Settings is closed.
                            .textContentType(.oneTimeCode)
                            .textInputAutocapitalization(.never)
                            .autocorrectionDisabled()
                            .multilineTextAlignment(.trailing)
                            .accessibilityIdentifier("settings.token")
                    }
                } header: {
                    Text("Daemon")
                } footer: {
                    Text("Plain HTTP, no TLS — keep this to a network you trust (your LAN, or Tailscale). The token is kept in the keychain.")
                }

                Section {
                    Button {
                        runProbe()
                    } label: {
                        HStack {
                            Text("Test connection")
                            Spacer()
                            if probing { ProgressView() }
                        }
                    }
                    .accessibilityIdentifier("settings.test")
                    .disabled(address.trimmingCharacters(in: .whitespaces).isEmpty || probing)

                    if let probe {
                        VStack(alignment: .leading, spacing: 5) {
                            Label(probe.title, systemImage: probe.ok ? "checkmark.circle.fill" : "exclamationmark.triangle.fill")
                                .font(.subheadline.weight(.medium))
                                .foregroundStyle(probe.ok ? Theme.done : Color(.systemRed))
                            ForEach(probe.lines, id: \.self) { line in
                                Text(line)
                                    .font(.caption)
                                    .foregroundStyle(.secondary)
                            }
                        }
                        .padding(.vertical, 2)
                        .accessibilityIdentifier("settings.probe.result")
                    }
                }

                Section {
                    Toggle("Keep listening with the screen locked", isOn: $keepListening)
                } footer: {
                    Text("Uses the audio background mode, so a session survives the lock screen. Turn it off to have iOS stop the microphone when you put the phone down.")
                }

                Section {
                    Button("Clear conversation on this phone", role: .destructive) {
                        confirmClear = true
                    }
                    .accessibilityIdentifier("settings.clearConversation")
                    .confirmationDialog("Clear the conversation on this phone?", isPresented: $confirmClear, titleVisibility: .visible) {
                        Button("Clear conversation", role: .destructive) { model.clearConversation() }
                    } message: {
                        Text("The daemon keeps its own record, so the latest conversation comes back the next time the app reaches it.")
                    }
                } header: {
                    Text("Conversation")
                } footer: {
                    Text("The app keeps the last \(ConversationHistory.cacheCapacity) messages for each daemon so it opens on your conversation straight away, even offline. No token is stored with them.")
                }

                Section("About") {
                    LabeledContent("App", value: Bundle.appVersion)
                    LabeledContent("Daemon", value: model.daemonVersion ?? "not connected")
                    LabeledContent("Voice name", value: model.voiceName)
                    VStack(alignment: .leading, spacing: 3) {
                        Text("Audio")
                            .font(.subheadline)
                        Text("16 kHz out / 24 kHz in, PCM16 mono")
                            .font(.caption)
                            .foregroundStyle(.secondary)
                        Text(model.metrics.summary)
                            .font(.caption.monospaced())
                            .foregroundStyle(.secondary)
                        if model.metrics.hardwareSampleRate > 0 {
                            Text("Microphone hardware: \(Int(model.metrics.hardwareSampleRate)) Hz")
                                .font(.caption.monospaced())
                                .foregroundStyle(.secondary)
                            Text("Output: \(model.metrics.outputRoute.isEmpty ? "—" : model.metrics.outputRoute) · played \(model.metrics.playedChunks)/\(model.metrics.scheduledChunks) chunks")
                                .font(.caption.monospaced())
                                .foregroundStyle(.secondary)
                        }
                    }
                    .padding(.vertical, 2)
                }
            }
            .navigationTitle("Settings")
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .cancellationAction) {
                    Button("Cancel") { dismiss() }
                        .disabled(firstRun && !model.isConfigured)
                }
                ToolbarItem(placement: .confirmationAction) {
                    Button("Save") { save() }
                        .accessibilityIdentifier("settings.save")
                        .disabled(DaemonEndpoint(text: address) == nil)
                }
            }
            .onAppear(perform: load)
        }
    }

    private func load() {
        address = model.settings.address
        token = model.settings.token
        keepListening = model.settings.keepListeningWhenLocked
        if address.isEmpty { addressFocused = true }
    }

    private func runProbe() {
        probing = true
        probe = nil
        Task {
            let result = await model.probe(address: address, token: token)
            probe = result
            probing = false
        }
    }

    private func save() {
        model.settings.address = address.trimmingCharacters(in: .whitespacesAndNewlines)
        model.settings.token = token.trimmingCharacters(in: .whitespacesAndNewlines)
        model.settings.keepListeningWhenLocked = keepListening
        model.settings.hasBeenConfigured = true
        model.endpointChanged()
        dismiss()
    }
}

extension Bundle {
    static var appVersion: String {
        let short = main.object(forInfoDictionaryKey: "CFBundleShortVersionString") as? String ?? "?"
        let build = main.object(forInfoDictionaryKey: "CFBundleVersion") as? String ?? "?"
        return "\(short) (\(build))"
    }
}
