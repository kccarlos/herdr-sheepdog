import SheepdogKit
import SwiftUI

struct FactorySettingsView: View {
    let initialHead: FactoryProjectHead
    let computerID: String
    let onGone: () -> Void
    @EnvironmentObject private var model: SessionViewModel
    @Environment(\.dynamicTypeSize) private var dynamicTypeSize
    @State private var loaded: Result<[FactorySettingRow], Error>?
    @State private var showAll = false

    private var computer: ComputerModel? { model.fleet.model(computerID) }
    private var head: FactoryProjectHead {
        computer?.factoryHeads.first { $0.id == initialHead.id } ?? initialHead
    }
    private var reloadKey: Int { computer?.factoryChanges[head.id] ?? 0 }

    var body: some View {
        ScrollView {
            let name = computer?.name ?? "your computer"
            VStack(alignment: .leading, spacing: 12) {
                Text("For this project only.")
                    .font(.footnote).foregroundStyle(Theme.muted)
                switch loaded {
                case let .failure(error)?:
                    let message = FactorySettingRow.errorMessage(
                        error, computerName: name, linkUsable: computer?.link.isUsable == true
                    )
                    VStack(spacing: 10) {
                        Text("Can't load the settings. Trying again.").font(.headline)
                        Text("\(message) The rest of the app still works.").foregroundStyle(Theme.muted)
                        Button("Try again") { Task { await load() } }
                            .buttonStyle(PillButtonStyle(fullWidth: false))
                            .accessibilityIdentifier("factorySettings.retry")
                    }
                    .multilineTextAlignment(.center).frame(maxWidth: .infinity).padding(.top, 16)
                case let .success(rows)? where rows.isEmpty:
                    Text("No settings available.").foregroundStyle(Theme.muted).padding(.top, 12)
                case let .success(rows)?:
                    let rest = rows.filter { !$0.top }
                    ForEach(rows.filter(\.top)) { rowView($0) }
                    if !rest.isEmpty {
                        allSettingsFold(count: rest.count)
                        if showAll { ForEach(rest) { rowView($0) } }
                    }
                case nil:
                    ProgressView("Loading the settings…").frame(maxWidth: .infinity).padding(.top, 20)
                }
            }
            .frame(maxWidth: 660, alignment: .leading).padding(16)
        }
        .background(Theme.surface)
        .navigationTitle("Settings")
        .navigationBarTitleDisplayMode(.inline)
        .toolbar(.visible, for: .navigationBar)
        .task(id: reloadKey) { await load() }
    }

    private func allSettingsFold(count: Int) -> some View {
        Button { showAll.toggle() } label: {
            HStack(spacing: 8) {
                Image(systemName: showAll ? "chevron.down" : "chevron.right")
                    .font(.caption.weight(.semibold)).foregroundStyle(Theme.chevron)
                    .accessibilityHidden(true)
                Text("All settings (\(count))").foregroundStyle(Theme.ink)
                Spacer()
            }
            .frame(maxWidth: .infinity, minHeight: 24).factoryCard().contentShape(Rectangle())
        }
        .buttonStyle(.plain)
        .accessibilityValue(showAll ? "expanded" : "collapsed")
        .accessibilityIdentifier("factorySettings.allFold")
    }

    private func rowView(_ row: FactorySettingRow) -> some View {
        let ax = dynamicTypeSize.isAccessibilitySize
        let layout = ax
            ? AnyLayout(VStackLayout(alignment: .leading, spacing: 8))
            : AnyLayout(HStackLayout(spacing: 8))
        return VStack(alignment: .leading, spacing: 8) {
            layout {
                VStack(alignment: .leading, spacing: 2) {
                    Text(row.label).font(.body)
                    Text(row.isModels ? row.displayValue : row.metaLine)
                        .font(row.isModels ? .caption.monospaced() : .caption)
                        .foregroundStyle(Theme.muted)
                }
                if !ax { Spacer(minLength: 4) }
                if !row.isModels {
                    Text(row.displayValue)
                        .font(.subheadline.weight(.semibold)).monospacedDigit()
                        .foregroundStyle(Theme.ink)
                }
            }
            if row.isModels { Text(row.metaLine).font(.caption).foregroundStyle(Theme.muted) }
            if row.problem != nil || row.isChanged || row.note?.isEmpty == false {
                OptionFlow(spacing: 6) {
                    if row.problem != nil {
                        Text("not allowed").factoryChip(Theme.danger, Theme.danger.opacity(0.12))
                    } else if row.isChanged {
                        Text("changed").factoryChip(Theme.working, Theme.workingSoft)
                    }
                    if row.note?.isEmpty == false {
                        Text("not used").factoryChip(Theme.mutedOnFill, Theme.fill)
                    }
                }
            }
            if let note = row.note?.trimmedOrNil { Text(note).font(.caption).foregroundStyle(Theme.muted) }
            if let stored = row.storedProblemMessage {
                Text(stored).factoryAlert()
            }
        }
        .factoryCard()
        .accessibilityElement(children: .contain)
        .accessibilityIdentifier("factorySettings.row.\(row.name)")
    }

    private func load() async {
        if case .failure = loaded { loaded = nil }
        guard let computer else { return }
        do {
            let rows = try await computer.request(as: DaemonClient.self) { [id = head.id] client in
                try await client.factorySettings(projectID: id)
            }
            guard !Task.isCancelled else { return }
            loaded = .success(rows)
        } catch {
            guard !Task.isCancelled else { return }
            if FactoryProjectBoard.isProjectGone(error) {
                onGone()
            } else if (try? loaded?.get()) == nil {
                loaded = .failure(error)
            }
        }
    }
}

private extension View {
    func factoryCard(border: Color = Theme.line) -> some View {
        padding(12)
            .frame(maxWidth: .infinity, alignment: .leading)
            .background(Theme.card, in: .rect(cornerRadius: Theme.Radius.card))
            .overlay(RoundedRectangle(cornerRadius: Theme.Radius.card).strokeBorder(border))
    }

    func factoryChip(_ fg: Color, _ bg: Color) -> some View {
        font(.caption2.weight(.semibold))
            .foregroundStyle(fg)
            .padding(.horizontal, 7).padding(.vertical, 2)
            .background(bg, in: Capsule())
    }

    func factoryAlert() -> some View {
        font(.footnote.weight(.semibold))
            .foregroundStyle(Theme.danger)
            .padding(10)
            .frame(maxWidth: .infinity, alignment: .leading)
            .background(Theme.danger.opacity(0.1), in: .rect(cornerRadius: 10))
    }
}
