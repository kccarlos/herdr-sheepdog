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

    @State private var loaded = false
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
                    if model.shellOffered == false {
                        Section {
                            MenuRow(title: "Shell", symbol: "apple.terminal", value: "Off on this computer")
                                .accessibilityIdentifier("settings.shell")
                        } footer: {
                            Text(PhoneShell.offExplanation)
                        }
                    }
                } else {
                    if let notice = model.update?.notice {
                        Section {
                            UpdateNoticeRow(notice: notice)
                        }
                    }
                    // The computer: with one computer, one row and what
                    // belongs to it. With several, this becomes the list
                    // of computers, each with its own page.
                    Section {
                        NavigationLink(value: SettingsPage.daemon) {
                            ComputerRow(
                                name: model.hello?.computer?.name ?? "Your computer",
                                status: computerStatus,
                                tint: computerTint
                            )
                        }
                        .accessibilityIdentifier("settings.daemon")
                        NavigationLink(value: SettingsPage.models) {
                            MenuRow(title: "Models", symbol: "cpu", value: model.modelsSummary)
                        }
                        .accessibilityIdentifier("settings.models")
                        NavigationLink(value: SettingsPage.languages) {
                            MenuRow(title: "Languages", symbol: "globe", value: model.daemonSettings?.languagesSummary ?? "—")
                        }
                        .accessibilityIdentifier("settings.languages")
                        if model.shellOffered == false {
                            MenuRow(title: "Shell", symbol: "apple.terminal", value: "Off on this computer")
                                .accessibilityIdentifier("settings.shell")
                        }
                    } header: {
                        Text("Computer")
                    } footer: {
                        if model.shellOffered == false {
                            Text(PhoneShell.offExplanation)
                        }
                    }
                }

                Section("This phone") {
                    row("Voice", "waveform", .voice, id: "settings.voice")
                    // Notifications come from the computer: the demo has
                    // none to show, so it does not list them.
                    if !model.isDemo {
                        row("Notifications", "bell", .notifications, id: "settings.notifications")
                    }
                }

                Section("Help") {
                    row("Diagnostics", "stethoscope", .diagnostics, id: "settings.diagnostics")
                    row("About", "info.circle", .about, id: "settings.about")
                }

                if !model.isDemo {
                    Section {
                        Button {
                            model.openDemo()
                        } label: {
                            Label("Try the demo", systemImage: "play.circle")
                        }
                        .accessibilityIdentifier("settings.tryDemo")
                    } footer: {
                        Text("Scripted agents on this phone. No computer, no network.")
                    }
                }
            }
            .navigationTitle("Settings")
            .navigationBarTitleDisplayMode(.inline)
            .navigationDestination(for: SettingsPage.self) { page in
                switch page {
                case .daemon:
                    DaemonSettingsView(firstRun: firstRun, done: done)
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
                        .disabled(firstRun && !model.isConfigured)
                }
            }
            .onAppear(perform: load)
            .task {
                // The update line reads /api/state: make it current.
                if !model.isDemo { await model.refreshState(quiet: true) }
                await model.loadVoiceProviders()
                await model.loadDaemonSettings()
            }
            .onReceive(NotificationCenter.default.publisher(for: UIApplication.didBecomeActiveNotification)) { _ in
                // Back from Control Center's Mic Mode panel.
                model.refreshMicrophoneMode()
            }
        }
        .interactiveDismissDisabled(firstRun && !model.isConfigured)
    }

    /// "Talking here · address" while the voice runs, "Connected ·
    /// address", "Offline · address", or "Not paired yet".
    private var computerStatus: String {
        guard model.isConfigured else { return "Not paired yet" }
        let address = model.endpointDisplay
        if model.wantsConnection, model.connectionChip.tint == .green { return "Talking here · \(address)" }
        return "\(model.connectionChip.word) · \(address)"
    }

    private var computerTint: Color {
        guard model.isConfigured else { return Theme.unknown }
        switch model.connectionChip.tint {
        case .green: return Theme.done
        case .amber: return Theme.blocked
        case .red: return Theme.danger
        case .grey: return Theme.unknown
        }
    }

    private func row(_ title: String, _ symbol: String, _ page: SettingsPage, id: String) -> some View {
        NavigationLink(value: page) {
            Label(title, systemImage: symbol)
        }
        .accessibilityIdentifier(id)
    }

    private func load() {
        guard !loaded, path.isEmpty else { return }
        loaded = true
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

    private func done() {
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

/// Settings' update line: what the daemon reports about a newer release
/// (`UpdateInfo.notice`), with the command to run on the computer in
/// monospace. Wraps at every text size; VoiceOver reads it as one sentence.
struct UpdateNoticeRow: View {
    let notice: UpdateNotice

    var body: some View {
        Label {
            VStack(alignment: .leading, spacing: 6) {
                Text(notice.text)
                    .foregroundStyle(Theme.ink)
                if let command = notice.command {
                    Text(command)
                        .font(.callout.monospaced())
                        .foregroundStyle(Theme.ink)
                        .textSelection(.enabled)
                        .padding(.horizontal, 10)
                        .padding(.vertical, 6)
                        .background(Theme.fill, in: RoundedRectangle(cornerRadius: Theme.Radius.small, style: .continuous))
                }
                if let after = notice.after {
                    Text(after)
                        .font(.footnote)
                        .foregroundStyle(Theme.muted)
                }
            }
            .fixedSize(horizontal: false, vertical: true)
        } icon: {
            Image(systemName: "arrow.down.circle")
                .foregroundStyle(Theme.working)
        }
        .accessibilityElement(children: .ignore)
        .accessibilityLabel(notice.spoken)
        .accessibilityIdentifier("settings.update")
    }
}

/// A menu row: icon and title, with a one-line summary beside it, or under
/// it at the accessibility sizes where side by side leaves no room.
/// The computer's row: a status dot, its name, and how the phone reaches
/// it ("Talking here · 192.168.1.20:7777", "Offline", "Not paired yet").
private struct ComputerRow: View {
    let name: String
    let status: String
    let tint: Color

    var body: some View {
        HStack(spacing: 12) {
            Image(systemName: "desktopcomputer")
                .foregroundStyle(Theme.ink)
                .frame(width: 28)
                .accessibilityHidden(true)
            VStack(alignment: .leading, spacing: 2) {
                Text(name)
                    .font(.body.weight(.semibold))
                    .foregroundStyle(Theme.ink)
                HStack(spacing: 6) {
                    Circle().fill(tint).frame(width: 7, height: 7)
                    Text(status)
                        .font(.footnote)
                        .foregroundStyle(Theme.muted)
                        .lineLimit(2)
                }
            }
        }
        .frame(minHeight: 44)
        .accessibilityElement(children: .combine)
    }
}

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
