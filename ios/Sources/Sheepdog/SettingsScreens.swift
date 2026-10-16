import SheepdogKit
import SwiftUI

// The screens behind Settings' menu. Each one applies a change at once.

/// Settings > Daemon: where it lives, the token, and one button that proves
/// both. The token goes to the keychain; nothing else is a secret.
struct DaemonSettingsView: View {
    @EnvironmentObject private var model: SessionViewModel
    @Binding var address: String
    @Binding var token: String
    let firstRun: Bool
    /// Writes the fields (when valid and changed) and repoints the app.
    let commit: () -> Void
    /// Commits and closes Settings: offered on the first run, where this is
    /// the only screen that matters.
    let done: () -> Void

    @State private var probing = false
    @State private var probe: SessionViewModel.Probe?
    @State private var scanning = false
    @FocusState private var addressFocused: Bool
    @Environment(\.dynamicTypeSize) private var dynamicTypeSize

    var body: some View {
        Form {
            if firstRun {
                // At the accessibility text sizes the introduction fills
                // two screens: the demo comes first so it is not buried.
                if dynamicTypeSize.isAccessibilitySize {
                    demoSection
                    introSection
                } else {
                    introSection
                    demoSection
                }
            }

            Section {
                Button {
                    scanning = true
                } label: {
                    Label("Scan pairing code", systemImage: "qrcode.viewfinder")
                }
                .accessibilityIdentifier("settings.scan")
                .disabled(probing)
            } footer: {
                Text("`sheepdog setup` (step 4) or `sheepdog pair` on your computer shows the code. It fills in the address and the token.")
            }

            Section {
                LabeledContent("Address") {
                    TextField("e.g. 192.168.1.20:7777", text: $address)
                        .textInputAutocapitalization(.never)
                        .autocorrectionDisabled()
                        .keyboardType(.URL)
                        .multilineTextAlignment(.trailing)
                        .focused($addressFocused)
                        .submitLabel(.done)
                        .onSubmit(commit)
                        .accessibilityIdentifier("settings.address")
                }
                LabeledContent("Token") {
                    SecureField("From the pairing code", text: $token)
                        // Not a website password: this stops iOS offering
                        // to save it to the user's passwords every time
                        // Settings is closed.
                        .textContentType(.oneTimeCode)
                        .textInputAutocapitalization(.never)
                        .autocorrectionDisabled()
                        .multilineTextAlignment(.trailing)
                        .submitLabel(.done)
                        .onSubmit(commit)
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
                            Text(LocalizedStringKey(line))
                                .font(.caption)
                                .foregroundStyle(.secondary)
                                .fixedSize(horizontal: false, vertical: true)
                        }
                        if let help = probe.help {
                            Link("How to fix this", destination: help)
                                .tint(Theme.primaryText)
                                .font(.caption.weight(.semibold))
                                .accessibilityIdentifier("settings.probe.help")
                        }
                    }
                    .padding(.vertical, 2)
                    .accessibilityElement(children: .contain)
                    .accessibilityIdentifier("settings.probe.result")
                }
            } footer: {
                Text("Plain HTTP, no TLS — keep this to a network you trust (your LAN, or Tailscale). The token is kept in the keychain. Changes apply when you leave this screen.")
            }
        }
        .navigationTitle(firstRun ? "Welcome" : "Daemon")
        .navigationBarTitleDisplayMode(.inline)
        .toolbar {
            if firstRun {
                ToolbarItem(placement: .confirmationAction) {
                    Button("Done", action: done)
                        .tint(Theme.primaryText)
                        .accessibilityIdentifier("settings.done")
                        .disabled(DaemonEndpoint(text: address) == nil)
                }
            }
        }
        .scrollDismissesKeyboard(.interactively)
        .sheet(isPresented: $scanning) {
            PairingScannerSheet { code in
                scanning = false
                usePairing(code)
            }
        }
        .onAppear {
            // Why Settings was opened for the user: a refused token, a
            // pairing code nothing answered.
            if let notice = model.daemonNotice {
                probe = notice
                model.daemonNotice = nil
            }
            // No keyboard on the first run: it would cover the guidance
            // and the demo. Later visits are for typing.
            if address.isEmpty, !firstRun { addressFocused = true }
        }
        .onDisappear(perform: commit)
    }

    private var introSection: some View {
        Section {
            VStack(alignment: .leading, spacing: 10) {
                Text("Connect to your computer")
                    .font(.title3.weight(.semibold))
                    .accessibilityAddTraits(.isHeader)
                Text(LocalizedStringKey(SetupHelp.requirement))
                    .font(.subheadline)
                    .foregroundStyle(.secondary)
                    .fixedSize(horizontal: false, vertical: true)
                Link(destination: SetupHelp.appGuideURL) {
                    Label("How to set up sheepdog", systemImage: "arrow.up.right.square")
                        .font(.subheadline)
                }
                .tint(Theme.primaryText)
                .accessibilityIdentifier("settings.firstRun.guide")
            }
            .padding(.vertical, 4)
            .accessibilityElement(children: .contain)
            .accessibilityIdentifier("settings.firstRun.intro")
        }
    }

    private var demoSection: some View {
        Section {
            Button {
                model.openDemo()
            } label: {
                Label("Try the demo", systemImage: "play.circle.fill")
                    .labelStyle(.titleOnly)
                    .foregroundStyle(Color("OnAccent"))
                    .font(.body.weight(.semibold))
                    .multilineTextAlignment(.center)
                    .fixedSize(horizontal: false, vertical: true)
                    .frame(maxWidth: .infinity, minHeight: 34)
            }
            .buttonStyle(.borderedProminent)
            .listRowInsets(EdgeInsets())
            .listRowBackground(Color.clear)
            .accessibilityIdentifier("settings.firstRun.tryDemo")
        } footer: {
            Text("No daemon yet? The demo runs scripted agents on this phone: no computer, no account, no network.")
        }
    }

    /// Fills the fields from a scanned pairing code: the token, and the
    /// first of its addresses that answers (LAN before Tailscale), then
    /// tests the connection as the button does.
    private func usePairing(_ code: String) {
        guard let link = PairingLink(code) else {
            probe = SessionViewModel.Probe(ok: false, title: "That is not a sheepdog pairing code.", lines: ["Run `sheepdog pair` on your computer and scan the code it shows."])
            return
        }
        token = link.token
        probing = true
        probe = nil
        Task {
            let result = await model.resolvePairing(link)
            address = result.address ?? link.addresses[0]
            probing = false
            guard result.address != nil else {
                probe = result.probe
                return
            }
            commit()
            runProbe()
        }
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
}

/// Settings > Voice: how you talk to the voice.
struct VoiceSettingsView: View {
    @EnvironmentObject private var model: SessionViewModel
    @State private var keepListening = true

    var body: some View {
        Form {
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

            Section {
                Toggle("Keep listening with the screen locked", isOn: $keepListening)
                    .accessibilityIdentifier("settings.keepListening")
            } footer: {
                Text("On: the session keeps running with the screen locked or in another app, so you can keep talking. Off: leaving the app pauses the microphone and \(model.voiceName)'s voice and tells the daemon you're muted (a per-minute voice session stops billing); both come back when you return.")
            }
        }
        .navigationTitle("Voice")
        .navigationBarTitleDisplayMode(.inline)
        .onAppear { keepListening = model.settings.keepListeningWhenLocked }
        .onChange(of: keepListening) { _, on in model.settings.keepListeningWhenLocked = on }
    }
}

/// Settings > Notifications: this phone, and what the daemon says about it.
struct NotificationsView: View {
    var body: some View {
        Form {
            NotificationsSection(push: PushCoordinator.shared)
        }
        .navigationTitle("Notifications")
        .navigationBarTitleDisplayMode(.inline)
        .accessibilityIdentifier("notifications.list")
    }
}

/// Settings > Diagnostics: Health, the audio path and voice detection. For
/// when something does not work.
struct DiagnosticsView: View {
    @EnvironmentObject private var model: SessionViewModel
    @ObservedObject var meter: VoiceMeter

    var body: some View {
        let d = meter.gateDiagnostics
        Form {
            Section {
                NavigationLink {
                    HealthView(meter: meter)
                } label: {
                    Text("Health")
                }
                .accessibilityIdentifier("settings.health")
            } footer: {
                Text("Daemon, models, audio path and recent problems.")
            }

            Section {
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
            } header: {
                Text("Audio")
            }

            Section {
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
        .navigationTitle("Diagnostics")
        .navigationBarTitleDisplayMode(.inline)
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
}

/// Settings > About: versions, and the one destructive button.
struct AboutView: View {
    @EnvironmentObject private var model: SessionViewModel
    @State private var confirmClear = false

    var body: some View {
        Form {
            Section {
                VStack(spacing: 8) {
                    Image("LogoColor")
                        .resizable()
                        .scaledToFit()
                        .frame(width: 96, height: 96)
                        .accessibilityHidden(true)
                    Text("sheepdog for herdr")
                        .font(.title3.weight(.semibold))
                        .fontDesign(.rounded)
                    Text(Bundle.appVersion)
                        .font(.footnote)
                        .foregroundStyle(.secondary)
                }
                .frame(maxWidth: .infinity)
                .listRowBackground(Color.clear)
                .accessibilityElement(children: .combine)
                .accessibilityIdentifier("about.header")
            }

            Section {
                LabeledContent("App", value: Bundle.appVersion)
                LabeledContent("Daemon", value: model.daemonVersion ?? "not connected")
                LabeledContent("Voice name", value: model.voiceName)
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
        }
        .navigationTitle("About")
        .navigationBarTitleDisplayMode(.inline)
    }
}
