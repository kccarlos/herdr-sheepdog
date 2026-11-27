import SheepdogKit
import SwiftUI

struct FactoryModelsEditor: View {
    private struct Choice: Hashable, Identifiable {
        let id: String
    }

    let row: FactorySettingRow
    let computerName: String
    let linkUsable: Bool
    let onPropose: (String) async throws -> FactoryAsk

    @Environment(\.dismiss) private var dismiss
    @State private var candidates: [FactoryModelCandidate]
    @State private var sending = false
    @State private var error: (text: String, fix: String?)?

    init(
        row: FactorySettingRow,
        computerName: String,
        linkUsable: Bool,
        onPropose: @escaping (String) async throws -> FactoryAsk
    ) {
        self.row = row
        self.computerName = computerName
        self.linkUsable = linkUsable
        self.onPropose = onPropose
        let parsed = FactoryModelCandidate.parse(row.value)
        let fallback = [FactoryModelCandidate(cli: row.clis.first ?? "claude", model: "")]
        _candidates = State(initialValue: parsed.isEmpty ? fallback : parsed)
    }

    var body: some View {
        let hints = cliHints
        let intro = "The CLI, model and effort each role uses. In order: the engine uses the first one it can start."
            + (row.name == "models.retro" ? " A retro reviewer looks at a task whose reviews keep failing." : "")
        let title = row.label.prefix(1).uppercased() + row.label.dropFirst()
        NavigationStack {
            ScrollView {
                VStack(alignment: .leading, spacing: 10) {
                    Text(intro).font(.caption).foregroundStyle(Theme.muted)
                    if candidates.isEmpty {
                        Text("not set").font(.subheadline).foregroundStyle(Theme.muted).factoryCard()
                    }
                    ForEach(Array(candidates.indices), id: \.self) { candidateCard($0) }
                    ForEach(hints, id: \.self) {
                        Text($0).font(.caption).foregroundStyle(Theme.muted)
                    }
                    OptionFlow(spacing: 8) {
                        Button("Add a model") {
                            candidates.append(.init(cli: row.clis.first ?? "claude", model: ""))
                            error = nil
                        }
                        .buttonStyle(PillButtonStyle(fullWidth: false))
                        .disabled(sending)
                        Button("Back to the default") {
                            candidates = FactoryModelCandidate.parse(row.defaultValue)
                            error = nil
                        }
                        .buttonStyle(PillButtonStyle(fullWidth: false))
                        .disabled(sending)
                        .accessibilityLabel("Fill in the default \(row.label)")
                    }
                    if let error {
                        Text(error.text).factoryAlert()
                        if let fix = error.fix {
                            FactoryFixView(fix: fix, computerName: computerName)
                        }
                    }
                    Button(sending ? "Asking…" : "Change") { submit() }
                        .buttonStyle(PillButtonStyle(prominent: true))
                        .disabled(sending)
                        .accessibilityLabel(sending ? "Asking… \(row.label)" : "Change \(row.label)")
                    Text("Nothing changes yet: the new list is read back to you, and saved when you answer Save.")
                        .font(.caption).foregroundStyle(Theme.muted)
                }
                .padding(16)
            }
            .background(Theme.surface)
            .navigationTitle(title)
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .cancellationAction) {
                    Button("Cancel") { dismiss() }
                }
            }
        }
    }

    private var cliHints: [String] {
        let seen = candidates.reduce(into: [String]()) { result, candidate in
            if !candidate.cli.isEmpty, !result.contains(candidate.cli) {
                result.append(candidate.cli)
            }
        }
        return seen.map { cli in
            if let offered = row.models[cli], !offered.isEmpty {
                let joined = offered.joined(separator: " · ")
                return "Model names \(cli) offers on this account: \(joined). The field also takes a typed name."
            }
            return "sheepdog cannot list the models \(cli) offers. This name is checked when an agent starts."
        }
    }

    private func candidateCard(_ index: Int) -> some View {
        let ordinal = FactoryModelCandidate.ordinal(index)
        let candidate = index < candidates.count ? candidates[index] : .init(cli: "", model: "")
        let tag = "\(candidate.cli) \(candidate.model.trimmedOrNil ?? "model")"
        let modelBinding = Binding(
            get: { index < candidates.count ? candidates[index].model : "" },
            set: {
                guard index < candidates.count else { return }
                candidates[index].model = $0
                error = nil
            }
        )
        return VStack(alignment: .leading, spacing: 8) {
            OptionFlow(spacing: 8) {
                Text(ordinal).font(.caption.weight(.semibold)).foregroundStyle(Theme.muted)
                if index > 0 {
                    Button("Move up") {
                        guard index < candidates.count else { return }
                        candidates.swapAt(index, index - 1)
                        error = nil
                    }
                    .buttonStyle(.bordered).controlSize(.large).disabled(sending)
                    .accessibilityLabel("Move \(tag) up")
                }
                if candidates.count > 1 {
                    Button("Remove") {
                        guard index < candidates.count else { return }
                        candidates.remove(at: index)
                        error = nil
                    }
                    .buttonStyle(.bordered).controlSize(.large).tint(Theme.danger).disabled(sending)
                    .accessibilityLabel("Remove \(tag)")
                }
            }
            SegmentedChoice(
                options: row.clis.map(Choice.init),
                selected: candidate.cli.isEmpty ? nil : Choice(id: candidate.cli),
                title: \.id,
                identifier: "factoryModels.cli.\(index)",
                accessibilityName: "CLI, \(ordinal)",
                fills: true
            ) {
                guard index < candidates.count else { return }
                candidates[index].cli = $0.id
                error = nil
            }
            .disabled(sending)
            Text("model").font(.caption).foregroundStyle(Theme.muted).accessibilityHidden(true)
            TextField("model", text: modelBinding)
                .font(.subheadline.monospaced())
                .autocorrectionDisabled().textInputAutocapitalization(.never)
                .padding(.horizontal, 10)
                .frame(minHeight: 44)
                .background(Theme.surface, in: .rect(cornerRadius: 10))
                .overlay(RoundedRectangle(cornerRadius: 10).strokeBorder(Theme.line))
                .accessibilityLabel("model, \(ordinal)")
                .accessibilityIdentifier("factoryModels.model.\(index)")
            SegmentedChoice(
                options: FactoryModelCandidate.efforts.map(Choice.init),
                selected: Choice(id: candidate.effort),
                title: \.id,
                identifier: "factoryModels.effort.\(index)",
                accessibilityName: "effort, \(ordinal)",
                fills: true
            ) {
                guard index < candidates.count else { return }
                candidates[index].effort = $0.id
                error = nil
            }
            .disabled(sending)
        }
        .factoryCard()
    }

    private func submit() {
        if candidates.contains(where: { $0.model.trimmedOrNil == nil }) {
            error = ("Type a model name.", nil)
            AccessibilityNotification.Announcement("Type a model name.").post()
            return
        }
        sending = true
        error = nil
        Task {
            do {
                _ = try await onPropose(FactoryModelCandidate.format(candidates))
                dismiss()
            } catch {
                let msg = FactorySettingRow.errorMessage(
                    error, computerName: computerName, linkUsable: linkUsable
                )
                sending = false
                self.error = (msg, (error as? FactoryRefusal)?.fix)
                AccessibilityNotification.Announcement(msg).post()
            }
        }
    }
}
