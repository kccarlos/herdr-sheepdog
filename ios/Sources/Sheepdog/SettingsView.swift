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

                voiceSection

                microphoneSection

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
                        Text(model.metrics.jitterSummary)
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

                diagnosticsSection
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
            .task { await model.loadVoiceProviders() }
            .onReceive(NotificationCenter.default.publisher(for: UIApplication.didBecomeActiveNotification)) { _ in
                // Back from Control Center's Mic Mode panel.
                model.refreshMicrophoneMode()
            }
        }
    }

    // MARK: - Voice provider

    @ViewBuilder
    private var voiceSection: some View {
        if let list = model.voiceProviders, !list.providers.isEmpty {
            Section {
                ForEach(list.providers) { option in
                    Button {
                        model.selectVoiceProvider(option.id)
                    } label: {
                        HStack(alignment: .firstTextBaseline) {
                            VStack(alignment: .leading, spacing: 2) {
                                Text(option.displayName)
                                    .foregroundStyle(option.isAvailable ? Color(.label) : Color(.tertiaryLabel))
                                if let detail = providerDetail(option) {
                                    Text(detail)
                                        .font(.caption)
                                        .foregroundStyle(option.isAvailable ? Color(.secondaryLabel) : Color(.tertiaryLabel))
                                }
                            }
                            Spacer()
                            if option.id == list.provider {
                                Image(systemName: "checkmark")
                                    .font(.body.weight(.semibold))
                                    .foregroundStyle(Color.accentColor)
                            }
                        }
                    }
                    .disabled(!option.isAvailable || model.switchingProvider)
                    .accessibilityIdentifier("settings.voiceProvider.\(option.id)")
                    .accessibilityAddTraits(option.id == list.provider ? .isSelected : [])
                }
                if model.switchingProvider {
                    HStack {
                        ProgressView()
                        Text("Switching the live session…")
                            .font(.caption)
                            .foregroundStyle(.secondary)
                    }
                }
                if let error = model.providerError {
                    Text(error)
                        .font(.caption)
                        .foregroundStyle(Color(.systemRed))
                }
            } header: {
                Text("Voice")
            } footer: {
                Text(voiceFooter(list))
            }
        }
    }

    private func providerDetail(_ option: VoiceProviderOption) -> String? {
        var parts: [String] = []
        if let model = option.model { parts.append(model) }
        if !option.isAvailable {
            parts.append(option.reason.map { "Unavailable — \($0)" } ?? "Unavailable")
        } else if option.billedPerMinute {
            parts.append("Billed per minute")
        }
        return parts.isEmpty ? nil : parts.joined(separator: " · ")
    }

    private func voiceFooter(_ list: VoiceProviders) -> String {
        var text = "Which model \(model.voiceName) talks with. The daemon switches the live session; the conversation and the flock carry over."
        if list.providers.contains(where: { $0.billedPerMinute }) {
            text += " GPT-Live is billed per minute of open session, whether anyone is talking or not — End the session when you are done."
        }
        return text
    }

    // MARK: - Microphone

    private var microphoneSection: some View {
        Section {
            Button {
                model.showMicrophoneModes()
            } label: {
                LabeledContent("Microphone mode") {
                    HStack(spacing: 4) {
                        Text(model.microphoneMode.label)
                        Image(systemName: "chevron.right")
                            .font(.caption.weight(.semibold))
                            .foregroundStyle(.tertiary)
                    }
                }
            }
            .foregroundStyle(Color(.label))
            .accessibilityIdentifier("settings.microphoneMode")

            Toggle("Hold to talk", isOn: Binding(
                get: { model.talkMode == .holdToTalk },
                set: { model.setTalkMode($0 ? .holdToTalk : .openMic) }
            ))
            .accessibilityIdentifier("settings.holdToTalk")
        } header: {
            Text("Microphone")
        } footer: {
            VStack(alignment: .leading, spacing: 6) {
                Text(model.microphoneMode.advice + (model.preferredMicrophoneMode != model.microphoneMode ? " (Chosen: \(model.preferredMicrophoneMode.label) — it applies while the microphone is running.)" : ""))
                Text(model.talkMode == .holdToTalk
                    ? "Hold to talk is on: hold the orb while you speak, let go to send. Nothing is sent otherwise."
                    : "Off: just talk. \(model.voiceName) listens all the time and ignores the room; the mic button only mutes.")
            }
        }
    }

    // MARK: - Diagnostics

    private var diagnosticsSection: some View {
        let d = model.gateDiagnostics
        return Section {
            LabeledContent("Input level", value: CaptureGate.Diagnostics.format(dBFS: d.levelDBFS))
                .accessibilityIdentifier("settings.diag.level")
            if d.detector == .voiceActivity {
                LabeledContent("Noise floor", value: CaptureGate.Diagnostics.format(dBFS: d.floorDBFS))
                    .accessibilityIdentifier("settings.diag.floor")
                if let margin = d.marginDB {
                    LabeledContent("Opens at", value: String(format: "floor + %.0f dB", margin))
                }
            }
            LabeledContent("Gate", value: gateWord(d))
                .accessibilityIdentifier("settings.diag.gate")
            LabeledContent("Opens", value: "\(d.opens) · \(d.spuriousOpens) spurious")
                .accessibilityIdentifier("settings.diag.opens")
            LabeledContent("Microphone signal", value: micSignalWord)
                .accessibilityIdentifier("settings.diag.signal")
            Toggle("Use the old peak gate", isOn: Binding(
                get: { model.detector == .peak },
                set: { model.setDetector($0 ? .peak : .voiceActivity) }
            ))
            .accessibilityIdentifier("settings.diag.peakGate")
        } header: {
            Text("Voice detection")
        } footer: {
            Text("Sheepdog sends audio only while the gate is open: when your voice is clearly above the room's noise floor (speech band, 300–3 400 Hz). A spurious open is one too short to be speech — a door, a cough. The old peak gate (the browser's) is here to compare; it lets almost any room noise through.")
        }
    }

    private func gateWord(_ d: CaptureGate.Diagnostics) -> String {
        guard model.state.capturing || model.state == .muted else { return "—" }
        if model.isMuted { return "Muted" }
        if let manual = d.manual { return manual ? "Open (holding)" : "Closed (hold to talk)" }
        return d.isOpen ? "Open — sending" : "Closed — room noise"
    }

    private var micSignalWord: String {
        if !model.state.capturing { return "—" }
        return model.noMicSignal ? "None — digital silence" : "OK"
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
