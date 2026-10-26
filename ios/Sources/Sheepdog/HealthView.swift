import SheepdogKit
import SwiftUI

/// Health: what the Mac's `/debug` page used to be needed for. Pushed from
/// the connection chip's popover and from Settings > Diagnostics.
/// The daemon (version, uptime, address), the voice and the orchestrator with
/// their pickers, chattiness, the audio path (echo cancellation, playback)
/// and the recent problems in plain words, each with its raw JSON behind a
/// tap.
struct HealthView: View {
    @EnvironmentObject private var model: SessionViewModel
    let meter: VoiceMeter
    /// "Change in Settings": where Models lives from here. Nil hides the
    /// row (Settings already has the Models screen one tap away).
    var changeModels: (() -> Void)?
    @Environment(\.dynamicTypeSize) private var dynamicTypeSize

    var body: some View {
        List {
            daemonSection
            modelsSection
            chattinessSection
            audioSection
            problemsSection
        }
        .listStyle(.insetGrouped)
        .navigationTitle("Health")
        .navigationBarTitleDisplayMode(.inline)
        .refreshable { await model.loadHealth() }
        .task { await model.loadHealth() }
        .accessibilityIdentifier("health.list")
    }

    // MARK: - Daemon

    private var daemonSection: some View {
        Section("Daemon") {
            LabeledContent("Connection", value: model.linkPresentation.chip)
                .accessibilityIdentifier("health.connection")
            LabeledContent("Live updates", value: liveUpdatesWord)
                .accessibilityIdentifier("health.liveUpdates")
            if let note = model.linkPresentation.health, !note.hasPrefix("Live updates") {
                // Degraded: still Connected on the chip, said only here
                // (the row above already says live updates are back soon).
                Text(note)
                    .font(.footnote)
                    .foregroundStyle(Theme.muted)
                    .accessibilityIdentifier("health.linkNote")
            }
            LabeledContent("Address", value: model.endpointDisplay)
            LabeledContent("Version", value: model.daemonVersion ?? "—")
                .accessibilityIdentifier("health.version")
            LabeledContent("Protocol", value: model.protocolDisplay)
                .accessibilityIdentifier("health.protocol")
            TimelineView(.periodic(from: .now, by: 30)) { context in
                LabeledContent("Up for", value: model.daemonStartedAt.map { Agent.duration(context.date.timeIntervalSince($0)) } ?? "—")
                    .accessibilityIdentifier("health.uptime")
            }
        }
    }

    /// The `/events` stream: "Connected", "Connecting…",
    /// "Reconnecting…", "Off".
    private var liveUpdatesWord: String {
        switch model.liveUpdates {
        case .open: return "Connected"
        case .connecting: return "Connecting…"
        case .reconnecting: return "Reconnecting…"
        case .idle: return "Off"
        }
    }

    // MARK: - Models (read-only; changed in Settings ▸ Models)

    @ViewBuilder
    private var modelsSection: some View {
        Section {
            LabeledContent("Status", value: model.state.title)
            LabeledContent("Voice", value: model.voiceProviderDisplay ?? "—")
                .accessibilityIdentifier("health.voiceModel")
            LabeledContent("Orchestrator", value: model.brainDisplay ?? "—")
                .accessibilityIdentifier("health.brainModel")
            // Every stats line carries the field once the daemon can switch
            // voice (0 with Gemini): only worth a row when something billed.
            if let billed = model.healthModel.health.billedSeconds, billed > 0 || model.settingsModel.voiceProviders?.provider == "openai" {
                LabeledContent("GPT-Live billed", value: Self.billed(billed))
                    .accessibilityIdentifier("health.billed")
            }
            if let changeModels {
                Button {
                    changeModels()
                } label: {
                    HStack {
                        Text("Change in Settings")
                        Spacer()
                        Image(systemName: "chevron.right")
                            .font(.footnote.weight(.semibold))
                            .foregroundStyle(.tertiary)
                    }
                    .frame(minHeight: 44)
                    .contentShape(Rectangle())
                }
                .accessibilityIdentifier("health.changeModels")
            }
        } header: {
            Text("Voice and orchestrator")
        } footer: {
            if model.healthModel.health.billedSeconds != nil {
                Text("GPT-Live bills every second a session is open, this daemon run so far.")
            }
        }
    }

    static func billed(_ seconds: Double) -> String {
        let s = Int(seconds.rounded())
        let cost = seconds / 60 * 0.05
        return String(format: "%d min %02d s · ≈ $%.2f", s / 60, s % 60, cost)
    }

    // MARK: - Chattiness

    @ViewBuilder
    private var chattinessSection: some View {
        if let settings = model.settingsModel.daemonSettings {
            Section {
                let selection = Binding(
                    get: { settings.chattiness },
                    set: { model.settingsModel.setChattiness($0) }
                )
                Group {
                    if dynamicTypeSize.isAccessibilitySize {
                        Picker("Chattiness", selection: selection) {
                            ForEach(settings.levels, id: \.self) { level in
                                Text(level.capitalized).tag(level)
                            }
                        }
                        .pickerStyle(.menu)
                    } else {
                        Picker("Chattiness", selection: selection) {
                            ForEach(settings.levels, id: \.self) { level in
                                Text(level.capitalized).tag(level)
                            }
                        }
                        .pickerStyle(.segmented)
                    }
                }
                .accessibilityIdentifier("health.chattiness")
                if let error = model.settingsModel.settingsError {
                    Text(error).font(.caption).foregroundStyle(Color(.systemRed))
                }
            } header: {
                Text("Pushes")
            } footer: {
                Text("Quiet: only what needs you. Normal: also finishes of agents you dealt with recently. Chatty: everything the orchestrator wants to say.")
            }
        }
    }

    // MARK: - Audio

    private var audioSection: some View {
        Section("Audio on this phone") {
            EchoCancellationRow(status: meter.metrics.voiceProcessing, running: meter.metrics.hardwareSampleRate > 0)
            VStack(alignment: .leading, spacing: 3) {
                Text("Playback")
                Text(meter.metrics.stretchSummary)
                    .font(.caption.monospaced())
                    .foregroundStyle(.secondary)
                Text(meter.metrics.jitterSummary)
                    .font(.caption.monospaced())
                    .foregroundStyle(.secondary)
            }
        }
    }

    // MARK: - Problems

    private var problemsSection: some View {
        Section {
            let problems = DaemonHealth.grouped(model.healthModel.health.problems)
            if problems.isEmpty {
                Label(model.healthModel.loading ? "Looking…" : "Nothing went wrong this run.", systemImage: "checkmark.circle")
                    .foregroundStyle(.secondary)
            }
            ForEach(problems) { group in
                ProblemRow(problem: group.latest, count: group.count)
            }
        } header: {
            Text("Recent problems")
        } footer: {
            Text("Fallbacks, rate limits, connect errors, held and dropped pushes, echoes, unheard audio, cut-off replies and missing worker transcripts, from the daemon's current run. Repeats are grouped (×N, newest time shown). Tap one for its raw event and codes.")
        }
    }
}

/// One problem: time, a coloured icon, one line, and the raw event.
struct ProblemRow: View {
    let problem: DaemonHealth.Problem
    /// How many times it happened (the newest one is shown).
    var count = 1
    @State private var expanded = false

    private var tint: Color {
        switch problem.severity {
        case .error: return Color(.systemRed)
        case .warning: return Theme.blocked
        case .info: return Theme.working
        }
    }

    private var symbol: String {
        switch problem.severity {
        case .error: return "xmark.octagon.fill"
        case .warning: return "exclamationmark.triangle.fill"
        case .info: return "info.circle.fill"
        }
    }

    var body: some View {
        DisclosureGroup(isExpanded: $expanded) {
            VStack(alignment: .leading, spacing: 4) {
                Text(problem.type)
                    .font(.caption2.monospaced())
                    .foregroundStyle(.secondary)
                RawJSONView(json: problem.raw)
            }
        } label: {
            HStack(alignment: .top, spacing: 10) {
                Image(systemName: symbol)
                    .foregroundStyle(tint)
                    .font(.footnote)
                    .padding(.top, 2)
                VStack(alignment: .leading, spacing: 2) {
                    HStack(alignment: .firstTextBaseline) {
                        Text(problem.title + (count > 1 ? "  ×\(count)" : ""))
                            .font(.subheadline.weight(.medium))
                            .fixedSize(horizontal: false, vertical: true)
                        Spacer(minLength: 4)
                        Text(problem.ts.formatted(date: .omitted, time: .standard))
                            .font(.caption2.monospacedDigit())
                            .foregroundStyle(.secondary)
                    }
                    if let detail = problem.detail {
                        Text(detail)
                            .font(.caption)
                            .foregroundStyle(.secondary)
                            .lineLimit(2)
                    }
                }
            }
        }
        .accessibilityIdentifier("health.problem")
    }
}
