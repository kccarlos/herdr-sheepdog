import SheepdogKit
import SwiftUI

// The screens behind Settings' menu. Each one applies a change at once.

/// Settings ▸ Connection: the paired computer (address, when it was
/// paired, its key's pin), Test connection, Scan a new code and Remove. On
/// the first run: pair by scanning, or try the demo.
struct DaemonSettingsView: View {
    @EnvironmentObject private var model: SessionViewModel
    let firstRun: Bool
    /// Closes Settings (after a first pairing).
    let done: () -> Void

    @State private var probing = false
    @State private var probe: SessionViewModel.Probe?
    @State private var pairing: PairingSheet?
    @State private var confirmRemove = false
    @State private var removing = false
    @Environment(\.dynamicTypeSize) private var dynamicTypeSize

    var body: some View {
        Form {
            if let computer = model.settings.computer {
                pairedSections(computer)
            } else if !model.settings.address.isEmpty {
                // A loopback fixture (the tests): no pairing, no key.
                Section {
                    LabeledContent("Address", value: model.settings.address)
                        .accessibilityIdentifier("settings.address")
                    testButton
                    probeResult
                } footer: {
                    Text("This computer, over loopback: no pairing needed.")
                }
                scanSection(title: "Scan a pairing code")
            } else {
                if dynamicTypeSize.isAccessibilitySize {
                    demoSection
                    introSection
                } else {
                    introSection
                }
                scanSection(title: "Scan the pairing code")
                if !dynamicTypeSize.isAccessibilitySize { demoSection }
            }
        }
        .navigationTitle(firstRun ? "Welcome" : "Connection")
        .navigationBarTitleDisplayMode(.inline)
        .sheet(item: $pairing) { sheet in
            PairingFlowView(stage: sheet.stage) {
                if firstRun { done() }
            }
            .environmentObject(model)
        }
        .confirmationDialog("Remove \(model.settings.computer?.name ?? "this computer")?", isPresented: $confirmRemove, titleVisibility: .visible) {
            Button("Remove", role: .destructive) {
                removing = true
                Task {
                    await model.removeComputer()
                    removing = false
                    probe = nil
                }
            }
            .accessibilityIdentifier("settings.remove.confirm")
            Button("Cancel", role: .cancel) {}
        } message: {
            Text("The computer forgets this phone, and this phone forgets its key. To connect again, scan a new code.")
        }
        .onAppear {
            if let notice = model.daemonNotice {
                probe = notice
                model.daemonNotice = nil
            }
            if let problem = model.daemonNoticeProblem {
                model.daemonNoticeProblem = nil
                pairing = PairingSheet(stage: .problem(problem))
            }
        }
        .onChange(of: model.daemonNoticeProblem) { _, problem in
            guard let problem else { return }
            model.daemonNoticeProblem = nil
            pairing = PairingSheet(stage: .problem(problem))
        }
    }

    @ViewBuilder
    private func pairedSections(_ computer: PairedComputer) -> some View {
        Section {
            VStack(alignment: .leading, spacing: 4) {
                Text(computer.name.isEmpty ? "Your computer" : computer.name)
                    .font(.title3.weight(.semibold))
                    .foregroundStyle(Theme.ink)
                Text(model.settings.address)
                    .font(.subheadline.monospaced())
                    .foregroundStyle(Theme.muted)
                    .textSelection(.enabled)
                    .accessibilityIdentifier("settings.address")
                Text("Paired \(computer.pairedAt.formatted(date: .abbreviated, time: .omitted)) · cert \(computer.shortPin)")
                    .font(.caption)
                    .foregroundStyle(Theme.muted)
                    .accessibilityLabel("Paired \(computer.pairedAt.formatted(date: .long, time: .omitted)). Certificate \(computer.shortPin)")
                    .accessibilityIdentifier("settings.pairedLine")
            }
            .padding(.vertical, 4)
            .accessibilityElement(children: .combine)
            NavigationLink {
                AddressesView(snapshot: { model.addressSnapshot }, add: { await model.addAddress($0) },
                              use: { await model.useAddress($0) }, remove: { model.removeAddress($0) })
            } label: {
                LabeledContent("Addresses", value: "\(computer.addresses.count)")
            }
            .accessibilityIdentifier("settings.addresses")
            testButton
            probeResult
        } footer: {
            Text("Encrypted, and checked against this computer's key on every connection.")
        }
        Section {
            Button {
                pairing = PairingSheet(stage: .start)
            } label: {
                Label("Scan a new code", systemImage: "qrcode.viewfinder")
            }
            .tint(Theme.primaryText)
            .accessibilityIdentifier("settings.scan")
            Button(role: .destructive) {
                confirmRemove = true
            } label: {
                HStack {
                    Label("Remove", systemImage: "minus.circle")
                    Spacer()
                    if removing { ProgressView() }
                }
            }
            .disabled(removing)
            .accessibilityIdentifier("settings.remove")
        }
    }

    private var testButton: some View {
        Button {
            probing = true
            probe = nil
            Task {
                probe = await model.testConnection()
                probing = false
            }
        } label: {
            HStack {
                Text("Test connection")
                Spacer()
                if probing { ProgressView() }
            }
        }
        .tint(Theme.primaryText)
        .accessibilityIdentifier("settings.test")
        .disabled(probing)
    }

    @ViewBuilder
    private var probeResult: some View {
        if let probe {
            VStack(alignment: .leading, spacing: 5) {
                Label(probe.title, systemImage: probe.ok ? "checkmark.circle.fill" : "exclamationmark.triangle.fill")
                    .font(.subheadline.weight(.medium))
                    .foregroundStyle(probe.ok ? Theme.done : Theme.danger)
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
    }

    private func scanSection(title: String) -> some View {
        Section {
            Button {
                pairing = PairingSheet(stage: .start)
            } label: {
                Label(title, systemImage: "qrcode.viewfinder")
            }
            .tint(Theme.primaryText)
            .accessibilityIdentifier("settings.scan")
        } footer: {
            Text("Open the setup page on your computer (`sheepdog setup`), or run `sheepdog pair`. Each code works once, for 10 minutes.")
        }
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
            Text("No computer yet? The demo runs scripted agents on this phone: no computer, no account, no network.")
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
    let meter: VoiceMeter

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
                Text("The app keeps the last \(ConversationHistory.cacheCapacity) messages for each daemon so it opens on your conversation straight away, even offline. No keys are stored with them.")
            }
        }
        .navigationTitle("About")
        .navigationBarTitleDisplayMode(.inline)
    }
}
