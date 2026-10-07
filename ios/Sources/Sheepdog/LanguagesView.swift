import SheepdogKit
import SwiftUI

/// Settings ▸ Languages: the languages you speak, most preferred first.
/// The daemon tells both the voice and the orchestrator so they
/// answer in the language you use, in your script — 简体中文 and 繁體中文
/// are separate entries. The list comes from the daemon: languages both
/// voice providers support.
struct LanguagesView: View {
    @EnvironmentObject private var model: SessionViewModel

    var body: some View {
        List {
            if let settings = model.daemonSettings, settings.supportsLanguages {
                chosenSection(settings)
                otherSection(settings)
            } else {
                Section {
                    Text(model.settingsError ?? (model.daemonSettings == nil
                        ? "Connect to the daemon to choose your languages."
                        : "This daemon is too old for the language setting. Update sheepdog on your Mac."))
                        .foregroundStyle(.secondary)
                        .accessibilityIdentifier("languages.unavailable")
                }
            }
        }
        .navigationTitle("Languages")
        .navigationBarTitleDisplayMode(.inline)
        .toolbar {
            if model.daemonSettings?.chosenLanguages.isEmpty == false {
                EditButton()
            }
        }
        .task { await model.loadDaemonSettings() }
    }

    private func chosenSection(_ settings: DaemonSettings) -> some View {
        let chosen = settings.chosenLanguages
        return Section {
            if chosen.isEmpty {
                Text("None chosen: \(model.voiceName) follows whatever you speak.")
                    .foregroundStyle(.secondary)
            }
            ForEach(Array(chosen.enumerated()), id: \.element.code) { index, option in
                HStack {
                    Text("\(index + 1).")
                        .monospacedDigit()
                        .foregroundStyle(.secondary)
                    LanguageLabel(option: option)
                }
                .accessibilityIdentifier("languages.chosen.\(option.code)")
            }
            .onMove { source, destination in
                model.setLanguages(LanguagePreference.moving(fromOffsets: source, toOffset: destination, in: chosen.map(\.code)))
            }
            .onDelete { offsets in
                model.setLanguages(LanguagePreference.removing(atOffsets: offsets, from: chosen.map(\.code)))
            }
        } header: {
            Text("Your languages")
        } footer: {
            VStack(alignment: .leading, spacing: 6) {
                Text("Most preferred first. The voice and the orchestrator are both told, and answer in the language you use, in its script. Tap Edit to reorder or remove.")
                if let error = model.settingsError {
                    Text(error).foregroundStyle(Color(.systemRed))
                }
            }
        }
    }

    private func otherSection(_ settings: DaemonSettings) -> some View {
        let others = settings.otherLanguages
        let allBoth = (settings.languageOptions ?? []).allSatisfy { $0.providers.count >= 2 }
        return Section {
            ForEach(others) { option in
                Button {
                    model.setLanguages(LanguagePreference.adding(option.code, to: settings.chosenLanguages.map(\.code)))
                } label: {
                    HStack {
                        LanguageLabel(option: option, showProviders: !allBoth)
                        Spacer()
                        Image(systemName: "plus.circle")
                            .foregroundStyle(.tint)
                    }
                }
                .foregroundStyle(Color(.label))
                .accessibilityIdentifier("languages.add.\(option.code)")
            }
        } header: {
            Text("Add a language")
        } footer: {
            Text(allBoth
                ? "Every language here is supported by both voice providers, Gemini Live and GPT-Live."
                : "Each language shows the voice providers that support it.")
        }
    }
}

/// A language's own name, with the English name under it when they
/// differ, and optionally which voice providers support it.
private struct LanguageLabel: View {
    let option: LanguageOption
    var showProviders = false

    var body: some View {
        VStack(alignment: .leading, spacing: 2) {
            Text(option.native)
            if option.name != option.native {
                Text(option.name)
                    .font(.caption)
                    .foregroundStyle(.secondary)
            }
            if showProviders {
                Text(option.providersSummary)
                    .font(.caption2)
                    .foregroundStyle(.secondary)
            }
        }
        .accessibilityElement(children: .combine)
    }
}
