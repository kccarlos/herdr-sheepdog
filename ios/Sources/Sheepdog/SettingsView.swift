import SheepdogKit
import SwiftUI

/// Where the daemon lives, what the token is, and one button that proves
/// both. The token goes to the keychain; nothing else is a secret.
struct SettingsView: View {
    @EnvironmentObject private var model: SessionViewModel
    /// Passed explicitly (not read off `model.meter` inside the body):
    /// an `@EnvironmentObject`'s own `@Published` changes are what
    /// re-renders a view, and a nested object's changes do not propagate
    /// through it, so About's live numbers need their own subscription.
    @ObservedObject var meter: VoiceMeter
    @Environment(\.dismiss) private var dismiss

    @State private var address = ""
    @State private var token = ""
    @State private var keepListening = true
    @State private var probing = false
    @State private var probe: SessionViewModel.Probe?
    @State private var confirmClear = false
    @FocusState private var addressFocused: Bool
    @State private var path: [SettingsRoute] = []

    private var firstRun: Bool { !model.settings.hasBeenConfigured }

    var body: some View {
        NavigationStack(path: $path) {
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
                        TextField("e.g. 192.168.1.20:7777", text: $address)
                            .textInputAutocapitalization(.never)
                            .autocorrectionDisabled()
                            .keyboardType(.URL)
                            .multilineTextAlignment(.trailing)
                            .focused($addressFocused)
                            .submitLabel(.next)
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
                    // Right under the fields, so the keyboard does not cover it.
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
                } header: {
                    Text("Daemon")
                } footer: {
                    Text("Plain HTTP, no TLS — keep this to a network you trust (your LAN, or Tailscale). The token is kept in the keychain.")
                }

                Section {
                    NavigationLink(value: SettingsRoute.models) {
                        LabeledContent("Models") {
                            Text(model.modelsSummary)
                                .lineLimit(1)
                        }
                    }
                    .accessibilityIdentifier("settings.models")
                } footer: {
                    Text("The voice \(model.voiceName) talks with and the brain behind it. Changes there apply at once.")
                }

                Section {
                    NavigationLink(value: SettingsRoute.languages) {
                        LabeledContent("Languages") {
                            Text(model.daemonSettings?.languagesSummary ?? "—")
                                .lineLimit(1)
                        }
                    }
                    .accessibilityIdentifier("settings.languages")
                } footer: {
                    Text("The languages you speak, most preferred first. \(model.voiceName) and the brain answer in the one you use, in your script.")
                }

                microphoneSection

                Section {
                    Toggle("Keep listening with the screen locked", isOn: $keepListening)
                } footer: {
                    Text("On: the session keeps running with the screen locked or in another app, so you can keep talking. Off: leaving the app pauses the microphone and \(model.voiceName)'s voice and tells the daemon you're muted (a per-minute voice session stops billing); both come back when you return.")
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
                    EchoCancellationRow(status: meter.metrics.voiceProcessing, running: meter.metrics.hardwareSampleRate > 0)
                    VStack(alignment: .leading, spacing: 3) {
                        Text("Audio")
                            .font(.subheadline)
                        Text("16 kHz out / 24 kHz in, PCM16 mono")
                            .font(.caption)
                            .foregroundStyle(.secondary)
                        Text(meter.metrics.summary)
                            .font(.caption.monospaced())
                            .foregroundStyle(.secondary)
                        Text(meter.metrics.jitterSummary)
                            .font(.caption.monospaced())
                            .foregroundStyle(.secondary)
                        Text(meter.metrics.stretchSummary)
                            .font(.caption.monospaced())
                            .foregroundStyle(.secondary)
                            .accessibilityIdentifier("about.stretch")
                        if meter.metrics.hardwareSampleRate > 0 {
                            Text("Microphone hardware: \(Int(meter.metrics.hardwareSampleRate)) Hz")
                                .font(.caption.monospaced())
                                .foregroundStyle(.secondary)
                            Text("Output: \(meter.metrics.outputRoute.isEmpty ? "—" : meter.metrics.outputRoute) · played \(meter.metrics.playedChunks)/\(meter.metrics.scheduledChunks) chunks")
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
            .navigationDestination(for: SettingsRoute.self) { route in
                switch route {
                case .models: ModelsView()
                case .languages: LanguagesView()
                }
            }
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
            .scrollDismissesKeyboard(.interactively)
            .onAppear(perform: load)
            .task {
                await model.loadVoiceProviders()
                await model.loadDaemonSettings()
            }
            .onReceive(NotificationCenter.default.publisher(for: UIApplication.didBecomeActiveNotification)) { _ in
                // Back from Control Center's Mic Mode panel.
                model.refreshMicrophoneMode()
            }
        }
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
        let d = meter.gateDiagnostics
        return Section {
            LabeledContent("Input level", value: CaptureGate.Diagnostics.format(dBFS: d.levelDBFS))
                .accessibilityIdentifier("settings.diag.level")
            LabeledContent("Noise floor", value: CaptureGate.Diagnostics.format(dBFS: d.floorDBFS))
                .accessibilityIdentifier("settings.diag.floor")
            if let margin = d.marginDB {
                LabeledContent("Opens at", value: String(format: "floor + %.0f dB", margin))
            }
            LabeledContent("Gate", value: gateWord(d))
                .accessibilityIdentifier("settings.diag.gate")
            LabeledContent("Opens", value: "\(d.opens) · \(d.spuriousOpens) spurious")
                .accessibilityIdentifier("settings.diag.opens")
            LabeledContent("Microphone signal", value: micSignalWord)
                .accessibilityIdentifier("settings.diag.signal")
        } header: {
            Text("Voice detection")
        } footer: {
            Text("Sheepdog sends audio only while the gate is open: when your voice is clearly above the room's noise floor (speech band, 300–3 400 Hz). A spurious open is one too short to be speech — a door, a cough.")
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
        // Health's "Change in Settings ›" opens straight on Models.
        if let route = model.settingsRoute {
            path = [route]
            model.settingsRoute = nil
        }
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

/// Settings ▸ About: whether the system echo canceller can hear what the
/// dog plays on the current route — the thing to check when the dog
/// answers itself from the speaker.
struct EchoCancellationRow: View {
    let status: VoiceProcessingStatus
    /// The engine has run this session (before that there is no route).
    let running: Bool

    var body: some View {
        VStack(alignment: .leading, spacing: 3) {
            HStack {
                Text("Echo cancellation")
                Spacer()
                if running {
                    Label(status.isActive ? "On" : "Off", systemImage: status.isActive ? "checkmark.circle.fill" : "exclamationmark.triangle.fill")
                        .labelStyle(.titleAndIcon)
                        .foregroundStyle(status.isActive ? Theme.done : Theme.blockedText)
                } else {
                    Text("—").foregroundStyle(.secondary)
                }
            }
            if running {
                Text(status.summary)
                    .font(.caption.monospaced())
                    .foregroundStyle(.secondary)
            }
        }
        .accessibilityElement(children: .combine)
        .accessibilityIdentifier("about.echo")
    }
}
