import SheepdogKit
import SwiftUI

/// The screens Settings can show. The root is a shallow menu; every screen
/// applies a change at once (there is no Save or Cancel), the way iOS's own
/// Settings does.
enum SettingsPage: Hashable {
    case daemon, voice, models, languages, notifications, diagnostics, about
}

/// Settings: a short list of sections, each one screen deep.
struct SettingsView: View {
    @EnvironmentObject private var model: SessionViewModel
    /// Passed explicitly (not read off `model.meter` inside the body):
    /// an `@EnvironmentObject`'s own `@Published` changes are what
    /// re-renders a view, and a nested object's changes do not propagate
    /// through it, so Diagnostics' live numbers need their own subscription.
    @ObservedObject var meter: VoiceMeter
    @Environment(\.dismiss) private var dismiss

    /// The daemon's address and token as typed. They are written once the
    /// field is left (return, back, Done): a reconnect per keystroke would
    /// be worse than none.
    @State private var address = ""
    @State private var token = ""
    @State private var path: [SettingsPage] = []
    /// No daemon saved yet: Settings opens on the Daemon screen and cannot
    /// be closed until an address is in.
    @State private var firstRun = false

    var body: some View {
        NavigationStack(path: $path) {
            List {
                if model.isDemo {
                    Section {
                        Button {
                            dismiss()
                            model.leaveDemo()
                        } label: {
                            Label("Leave the demo", systemImage: "xmark.circle")
                        }
                        .accessibilityIdentifier("settings.leaveDemo")
                    } footer: {
                        Text("You are in the demo: scripted agents on this phone, no daemon and no network. Leaving returns to your own setup.")
                    }
                } else {
                    Section {
                        NavigationLink(value: SettingsPage.daemon) {
                            MenuRow(title: "Daemon", symbol: "desktopcomputer", value: model.isConfigured ? model.endpointDisplay : "Not set")
                        }
                        .accessibilityIdentifier("settings.daemon")
                        Button {
                            model.openDemo()
                        } label: {
                            Label("Try the demo", systemImage: "play.circle")
                        }
                        .accessibilityIdentifier("settings.tryDemo")
                    } footer: {
                        Text("The demo runs scripted agents on this phone, with no daemon and no network.")
                    }
                }

                Section {
                    row("Voice", "waveform", .voice, id: "settings.voice")
                    // Models, Languages and Notifications are the daemon's:
                    // the demo has none to show, so it does not list them.
                    if !model.isDemo {
                        NavigationLink(value: SettingsPage.models) {
                            MenuRow(title: "Models", symbol: "cpu", value: model.modelsSummary)
                        }
                        .accessibilityIdentifier("settings.models")
                        NavigationLink(value: SettingsPage.languages) {
                            MenuRow(title: "Languages", symbol: "globe", value: model.daemonSettings?.languagesSummary ?? "—")
                        }
                        .accessibilityIdentifier("settings.languages")
                        row("Notifications", "bell", .notifications, id: "settings.notifications")
                    }
                }

                Section {
                    row("Diagnostics", "stethoscope", .diagnostics, id: "settings.diagnostics")
                    row("About", "info.circle", .about, id: "settings.about")
                }
            }
            .navigationTitle("Settings")
            .navigationBarTitleDisplayMode(.inline)
            .navigationDestination(for: SettingsPage.self) { page in
                switch page {
                case .daemon:
                    DaemonSettingsView(address: $address, token: $token, firstRun: firstRun, commit: commitDaemon, done: done)
                case .voice: VoiceSettingsView()
                case .models: ModelsView()
                case .languages: LanguagesView()
                case .notifications: NotificationsView()
                case .diagnostics: DiagnosticsView(meter: meter)
                case .about: AboutView()
                }
            }
            .toolbar {
                ToolbarItem(placement: .confirmationAction) {
                    Button("Done", action: done)
                        .tint(Theme.primaryText)
                        .accessibilityIdentifier("settings.done")
                        .disabled(firstRun && !model.isConfigured && DaemonEndpoint(text: address) == nil)
                }
            }
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
        .interactiveDismissDisabled(firstRun && !model.isConfigured)
        .onDisappear { commitDaemon() }
    }

    private func row(_ title: String, _ symbol: String, _ page: SettingsPage, id: String) -> some View {
        NavigationLink(value: page) {
            Label(title, systemImage: symbol)
        }
        .accessibilityIdentifier(id)
    }

    private func load() {
        guard address.isEmpty, token.isEmpty, path.isEmpty else { return }
        address = model.settings.address
        token = model.settings.token
        firstRun = !model.settings.hasBeenConfigured
        // Health's "Change in Settings" opens straight on Models; the first
        // run opens on the Daemon screen.
        if let route = model.settingsRoute {
            switch route {
            case .models: path = [.models]
            case .languages: path = [.languages]
            case .daemon: path = [.daemon]
            }
            model.settingsRoute = nil
        } else if firstRun {
            path = [.daemon]
        }
    }

    /// Writes the address and token when they are valid and different, and
    /// repoints the app at them.
    private func commitDaemon() {
        let trimmedAddress = address.trimmingCharacters(in: .whitespacesAndNewlines)
        let trimmedToken = token.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !trimmedAddress.isEmpty, DaemonEndpoint(text: trimmedAddress) != nil else { return }
        let changed = model.settings.address != trimmedAddress
            || model.settings.token != trimmedToken
            || !model.settings.hasBeenConfigured
        guard changed else { return }
        model.settings.address = trimmedAddress
        model.settings.token = trimmedToken
        model.settings.hasBeenConfigured = true
        model.endpointChanged()
    }

    private func done() {
        commitDaemon()
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
/// voice plays on the current route — the thing to check when the voice
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

/// A menu row: icon and title, with a one-line summary beside it, or under
/// it at the accessibility sizes where side by side leaves no room.
private struct MenuRow: View {
    let title: String
    let symbol: String
    let value: String
    @Environment(\.dynamicTypeSize) private var dynamicTypeSize

    var body: some View {
        if dynamicTypeSize.isAccessibilitySize {
            VStack(alignment: .leading, spacing: 2) {
                Label(title, systemImage: symbol)
                Text(value)
                    .font(.footnote)
                    .foregroundStyle(.secondary)
                    .lineLimit(2)
            }
        } else {
            LabeledContent {
                Text(value).lineLimit(1)
            } label: {
                Label(title, systemImage: symbol)
            }
        }
    }
}
