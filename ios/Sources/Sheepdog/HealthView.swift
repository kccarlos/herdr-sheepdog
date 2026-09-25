import SheepdogKit
import SwiftUI

/// The Health pane: what the Mac's `/debug` page used to be needed for.
/// The daemon (version, uptime, address), the voice and the brain with
/// their pickers, chattiness, the audio path (echo cancellation, playback)
/// and the recent problems in plain words, each with its raw JSON behind a
/// tap.
struct HealthView: View {
    @EnvironmentObject private var model: SessionViewModel
    @ObservedObject var meter: VoiceMeter

    var body: some View {
        List {
            daemonSection
            voiceSection
            brainSection
            chattinessSection
            audioSection
            problemsSection
        }
        .listStyle(.insetGrouped)
        .refreshable { await model.loadHealth() }
        .task { await model.loadHealth() }
        .accessibilityIdentifier("health.list")
    }

    // MARK: - Daemon

    private var daemonSection: some View {
        Section("Daemon") {
            LabeledContent("Connection", value: model.connectionChip.word)
            LabeledContent("Address", value: model.endpointDisplay)
            LabeledContent("Version", value: model.daemonVersion ?? "—")
                .accessibilityIdentifier("health.version")
            TimelineView(.periodic(from: .now, by: 30)) { context in
                LabeledContent("Up for", value: model.daemonStartedAt.map { Agent.duration(context.date.timeIntervalSince($0)) } ?? "—")
                    .accessibilityIdentifier("health.uptime")
            }
        }
    }

    // MARK: - Voice

    @ViewBuilder
    private var voiceSection: some View {
        Section {
            LabeledContent("Status", value: model.state.title)
            if let list = model.voiceProviders, !list.providers.isEmpty {
                Picker("Voice", selection: Binding(
                    get: { list.provider ?? "" },
                    set: { model.selectVoiceProvider($0) }
                )) {
                    ForEach(list.providers) { option in
                        Text(option.isAvailable ? option.displayName : "\(option.displayName) (unavailable)")
                            .tag(option.id)
                    }
                }
                .disabled(model.switchingProvider)
                .accessibilityIdentifier("health.voicePicker")
            }
            if let voice = model.voiceProviderDisplay {
                LabeledContent("Model", value: voice)
            }
            // Every stats line carries the field once the daemon can switch
            // voice (0 with Gemini): only worth a row when something billed.
            if let billed = model.health.billedSeconds, billed > 0 || model.voiceProviders?.provider == "openai" {
                LabeledContent("GPT-Live billed", value: Self.billed(billed))
                    .accessibilityIdentifier("health.billed")
            }
            if let error = model.providerError {
                Text(error).font(.caption).foregroundStyle(Color(.systemRed))
            }
        } header: {
            Text("Voice")
        } footer: {
            if model.health.billedSeconds != nil {
                Text("GPT-Live bills every second a session is open, this daemon run so far.")
            }
        }
    }

    static func billed(_ seconds: Double) -> String {
        let s = Int(seconds.rounded())
        let cost = seconds / 60 * 0.05
        return String(format: "%d min %02d s · ≈ $%.2f", s / 60, s % 60, cost)
    }

    // MARK: - Brain

    @ViewBuilder
    private var brainSection: some View {
        Section {
            if let models = model.brainModels {
                Picker("Provider", selection: Binding(
                    get: { models.provider },
                    set: { provider in
                        // A provider's catalog first, then a model from it.
                        Task {
                            await model.loadBrainModels(provider: provider)
                            guard let catalog = model.brainModels, catalog.modelsProvider == provider else { return }
                            if let first = catalog.defaultModelForSwitch {
                                model.selectBrain(provider: provider, model: first)
                            } else {
                                model.brainError = "No model list for \(provider)" + (catalog.error.map { ": \($0)" } ?? "") + " — the brain was left as it is."
                            }
                        }
                    }
                )) {
                    ForEach(models.providers) { provider in
                        Text(provider.available ? provider.displayName : "\(provider.displayName) (\(provider.reason ?? "unavailable"))")
                            .tag(provider.id)
                    }
                }
                .disabled(model.switchingBrain)
                .accessibilityIdentifier("health.brainProvider")
                Picker("Model", selection: Binding(
                    get: { models.model },
                    set: { model.selectBrain(provider: models.provider, model: $0) }
                )) {
                    ForEach(models.choices) { choice in
                        Text(choice.displayName ?? choice.name).tag(choice.name)
                    }
                }
                .disabled(model.switchingBrain)
                .accessibilityIdentifier("health.brainModel")
                if let error = models.error, !error.isEmpty {
                    Text("Model list not fresh: \(error)").font(.caption).foregroundStyle(.secondary)
                }
            } else if let brain = model.brain?.display {
                LabeledContent("Brain", value: brain)
            } else {
                Text("Loading…").foregroundStyle(.secondary)
            }
            if model.switchingBrain {
                HStack { ProgressView(); Text("Switching…").font(.caption).foregroundStyle(.secondary) }
            }
            if let error = model.brainError {
                Text(error).font(.caption).foregroundStyle(Color(.systemRed))
            }
        } header: {
            Text("Brain")
        } footer: {
            Text("Takes effect on the brain's next turn; the daemon keeps the choice across restarts.")
        }
    }

    // MARK: - Chattiness

    @ViewBuilder
    private var chattinessSection: some View {
        if let settings = model.daemonSettings {
            Section {
                Picker("Chattiness", selection: Binding(
                    get: { settings.chattiness },
                    set: { model.setChattiness($0) }
                )) {
                    ForEach(settings.levels, id: \.self) { level in
                        Text(level.capitalized).tag(level)
                    }
                }
                .pickerStyle(.segmented)
                .accessibilityIdentifier("health.chattiness")
                if let error = model.settingsError {
                    Text(error).font(.caption).foregroundStyle(Color(.systemRed))
                }
            } header: {
                Text("Pushes")
            } footer: {
                Text("Quiet: only what needs you. Normal: also finishes of agents you dealt with recently. Chatty: everything the brain wants to say.")
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
            let problems = Array(model.health.problems.reversed())
            if problems.isEmpty {
                Label(model.loadingHealth ? "Looking…" : "Nothing went wrong this run.", systemImage: "checkmark.circle")
                    .foregroundStyle(.secondary)
            }
            ForEach(problems) { problem in
                ProblemRow(problem: problem)
            }
        } header: {
            Text("Recent problems")
        } footer: {
            Text("Fallbacks, rate limits, connect errors, held and dropped pushes, echoes, unheard audio, cut-off replies and missing worker transcripts, from the daemon's current run. Tap one for its raw event.")
        }
    }
}

/// One problem: time, a coloured icon, one line, and the raw event.
struct ProblemRow: View {
    let problem: DaemonHealth.Problem
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
                        Text(problem.title)
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
