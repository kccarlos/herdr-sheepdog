import SheepdogKit
import SwiftUI

/// Settings ▸ Models: which model the voice talks with (the voice provider)
/// and which one thinks for it (the orchestrator). Pushed from Settings rather
/// than living in its form, because every choice here applies at once on
/// the daemon.
struct ModelsView: View {
    @EnvironmentObject private var model: SessionViewModel

    var body: some View {
        Form {
            voiceSection
            brainSection
        }
        .navigationTitle("Models")
        .navigationBarTitleDisplayMode(.inline)
        .task {
            async let providers: Void = model.loadVoiceProviders()
            async let brain: Void = model.loadBrainModels()
            _ = await (providers, brain)
        }
        .accessibilityIdentifier("models.list")
    }

    // MARK: - Voice

    @ViewBuilder
    private var voiceSection: some View {
        Section {
            if let list = model.voiceProviders, !list.providers.isEmpty {
                ForEach(list.providers) { option in
                    Button {
                        model.selectVoiceProvider(option.id)
                    } label: {
                        HStack(alignment: .firstTextBaseline) {
                            VStack(alignment: .leading, spacing: 2) {
                                Text(option.displayName)
                                    .foregroundStyle(option.isAvailable ? Color(.label) : Color(.tertiaryLabel))
                                if let detail = Self.providerDetail(option) {
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
                        .frame(minHeight: 44)
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
            } else {
                LabeledContent("Voice", value: model.voiceProviderDisplay ?? "—")
                    .accessibilityIdentifier("models.voice.current")
            }
            if let error = model.providerError {
                Text(error)
                    .font(.caption)
                    .foregroundStyle(Color(.systemRed))
            }
        } header: {
            Text("Voice")
        } footer: {
            Text(voiceFooter)
        }
    }

    static func providerDetail(_ option: VoiceProviderOption) -> String? {
        var parts: [String] = []
        if let model = option.model { parts.append(model) }
        if !option.isAvailable {
            parts.append(option.reason.map { "Unavailable — \($0)" } ?? "Unavailable")
        } else if option.billedPerMinute {
            parts.append("Billed per minute")
        }
        return parts.isEmpty ? nil : parts.joined(separator: " · ")
    }

    private var voiceFooter: String {
        guard let list = model.voiceProviders, !list.providers.isEmpty else {
            return "This daemon offers one voice; there is nothing to switch."
        }
        var text = "Which model \(model.voiceName) talks with. The daemon switches the live session at once; the conversation and your agents carry over."
        if list.providers.contains(where: { $0.billedPerMinute }) {
            text += " GPT-Live is billed per minute of open session, whether anyone is talking or not — End the session when you are done."
        }
        return text
    }

    // MARK: - Orchestrator

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
                                model.brainError = "No model list for \(provider)" + (catalog.error.map { ": \($0)" } ?? "") + " — the orchestrator was left as it is."
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
                .accessibilityIdentifier("models.brainProvider")
                Picker("Model", selection: Binding(
                    get: { models.model },
                    set: { model.selectBrain(provider: models.provider, model: $0) }
                )) {
                    ForEach(models.choices) { choice in
                        Text(choice.displayName ?? choice.name).tag(choice.name)
                    }
                }
                .disabled(model.switchingBrain)
                .accessibilityIdentifier("models.brainModel")
                if let error = models.error, !error.isEmpty {
                    Text("Model list not fresh: \(error)").font(.caption).foregroundStyle(.secondary)
                }
            } else if let brain = model.brain?.display {
                LabeledContent("Orchestrator", value: brain)
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
            Text("Orchestrator")
        } footer: {
            Text("The model that decides what \(model.voiceName) says and does. Takes effect on the orchestrator's next turn; the daemon keeps the choice across restarts.")
        }
    }
}
