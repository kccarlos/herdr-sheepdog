import SheepdogKit
import SwiftUI

struct FactorySettingsView: View {
    let initialHead: FactoryProjectHead, computerID: String, onGone: () -> Void
    @EnvironmentObject private var model: SessionViewModel
    @State private var loaded: Result<[FactorySettingRow], Error>?
    @State private var asks: [FactoryAsk] = []
    @State private var drafts: [String: String] = [:]
    @State private var rowErrors: [String: String] = [:]
    @State private var notices: [String: String] = [:]
    @State private var busyRow: String?
    @State private var showAll = false
    @State private var editingModels: FactorySettingRow?

    private var computer: ComputerModel? { model.fleet.model(computerID) }
    private var head: FactoryProjectHead { computer?.factoryHeads.first { $0.id == initialHead.id } ?? initialHead }
    private var reloadKey: String { "\(computer?.factoryChanges[head.id] ?? 0):\(computer?.questions.map(\.id).joined(separator: ",") ?? "")" }

    var body: some View {
        ScrollView {
            let name = computer?.name ?? "your computer"
            VStack(alignment: .leading, spacing: 12) {
                Text("For this project only. A change is read back to you and saved when you confirm.").font(.footnote).foregroundStyle(Theme.muted)
                if case let .failure(error) = loaded {
                    VStack(spacing: 10) {
                        Text("Can't load the settings. Trying again.").font(.headline)
                        Text("\(FactorySettingRow.errorMessage(error, computerName: name, linkUsable: computer?.link.isUsable == true)) The rest of the app still works.").foregroundStyle(Theme.muted)
                        Button("Try again") { Task { await load() } }.buttonStyle(PillButtonStyle(fullWidth: false)).accessibilityIdentifier("factorySettings.retry")
                    }.multilineTextAlignment(.center).frame(maxWidth: .infinity).padding(.top, 16)
                } else if case let .success(rows) = loaded {
                    if rows.isEmpty { Text("No settings available.").foregroundStyle(Theme.muted).padding(.top, 12) }
                    else {
                        let rest = rows.filter { !$0.top }
                        ForEach(rows.filter(\.top)) { rowView($0) }
                        if !rest.isEmpty {
                            Button { showAll.toggle() } label: {
                                HStack(spacing: 8) {
                                    Image(systemName: showAll ? "chevron.down" : "chevron.right").font(.caption.weight(.semibold)).foregroundStyle(Theme.chevron)
                                    Text("All settings (\(rest.count))").foregroundStyle(Theme.ink); Spacer()
                                }.frame(maxWidth: .infinity, minHeight: 24).factoryCard().contentShape(Rectangle())
                            }.buttonStyle(.plain).accessibilityValue(showAll ? "expanded" : "collapsed").accessibilityIdentifier("factorySettings.allFold")
                            if showAll { ForEach(rest) { rowView($0) } }
                        }
                    }
                } else { ProgressView("Loading the settings…").frame(maxWidth: .infinity).padding(.top, 20) }
            }.frame(maxWidth: 660, alignment: .leading).padding(16)
        }
        .background(Theme.surface).navigationTitle("Settings").navigationBarTitleDisplayMode(.inline).toolbar(.visible, for: .navigationBar)
        .task(id: reloadKey) { await load() }
        .sheet(item: $editingModels) { row in
            FactoryModelsEditor(row: row, computerName: computer?.name ?? "your computer", linkUsable: computer?.link.isUsable == true) { try await propose(row: row, value: $0) }
        }
    }

    private func rowView(_ row: FactorySettingRow) -> some View {
        let ask = asks.first { $0.status == "open" && $0.set?.name == row.name && ($0.project.isEmpty || $0.project == head.id) }
        let current = drafts[row.name] ?? ask?.set?.value ?? row.value, sending = busyRow == row.name
        let dirty = ask == nil && drafts[row.name] != nil && drafts[row.name] != row.value
        return VStack(alignment: .leading, spacing: 8) {
            HStack(spacing: 8) {
                VStack(alignment: .leading, spacing: 2) {
                    Text(row.label).font(.body)
                    Text(row.isModels ? row.displayValue : row.metaLine).font(row.isModels ? .caption.monospaced() : .caption).foregroundStyle(Theme.muted)
                }
                Spacer(minLength: 4)
                rowControl(row, current: current, locked: ask != nil || sending, hasAsk: ask != nil)
            }
            if row.isModels { Text(row.metaLine).font(.caption).foregroundStyle(Theme.muted) }
            if row.problem != nil || row.isChanged || (row.note?.isEmpty == false) {
                OptionFlow(spacing: 6) {
                    if row.problem != nil { Text("not allowed").factoryChip(Theme.danger, Theme.danger.opacity(0.12)) }
                    else if row.isChanged { Text("changed").factoryChip(Theme.working, Theme.workingSoft) }
                    if row.note?.isEmpty == false { Text("not used").factoryChip(Theme.mutedOnFill, Theme.fill) }
                }
            }
            if let note = row.note?.trimmedOrNil { Text(note).font(.caption).foregroundStyle(Theme.muted) }
            if !row.isModels, ask == nil, dirty || row.isChanged {
                OptionFlow(spacing: 8) {
                    if dirty { Button(sending ? "Asking…" : "Change") { Task { _ = try? await propose(row: row, value: current) } }.buttonStyle(PillButtonStyle(prominent: true, fullWidth: false)).disabled(sending).accessibilityLabel("Change \(row.label)") }
                    if row.isChanged { Button(sending ? "Asking…" : "Back to the default") { Task { _ = try? await propose(row: row, value: row.defaultValue) } }.buttonStyle(PillButtonStyle(fullWidth: false)).disabled(sending).accessibilityLabel("Set \(row.label) back to the default, \(row.displayDefault)") }
                }
            }
            rowFooter(row, ask: ask)
        }.factoryCard(border: ask != nil ? Theme.blocked : Theme.line).accessibilityElement(children: .contain).accessibilityIdentifier("factorySettings.row.\(row.name)")
    }

    @ViewBuilder
    private func rowControl(_ row: FactorySettingRow, current: String, locked: Bool, hasAsk: Bool) -> some View {
        let binding = Binding(get: { drafts[row.name] ?? current }, set: { setDraft(row.name, $0) })
        if row.isModels, !hasAsk {
            Button("Edit") { editingModels = row }.foregroundStyle(Theme.primaryText).frame(minHeight: 40).accessibilityLabel("Edit \(row.label)")
        } else if row.isNumber {
            let num = Int(current) ?? row.min
            HStack(spacing: 0) {
                Button("−") { setDraft(row.name, String(max(0, num - 1))) }.frame(width: 40, height: 36).accessibilityLabel("One fewer \(row.label)")
                Text(current.isEmpty ? "not set" : current).font(.subheadline.weight(.semibold)).monospacedDigit().frame(minWidth: 28)
                Button("+") { setDraft(row.name, String(num + 1)) }.frame(width: 40, height: 36).accessibilityLabel("One more \(row.label)").accessibilityIdentifier("factorySettings.increment.\(row.name)")
            }.foregroundStyle(Theme.ink).buttonStyle(.plain).disabled(locked).background(Theme.fill, in: .rect(cornerRadius: 10))
        } else if row.isChoice {
            Picker(row.label, selection: binding) { ForEach(row.allowed.contains(current) ? row.allowed : [current] + row.allowed, id: \.self) { Text($0.isEmpty ? "not set" : $0).tag($0) } }.labelsHidden().disabled(locked)
        } else if !row.isModels {
            TextField("not set", text: binding).textFieldStyle(.roundedBorder).autocorrectionDisabled().textInputAutocapitalization(.never).frame(maxWidth: 160).disabled(locked)
        }
    }

    @ViewBuilder
    private func rowFooter(_ row: FactorySettingRow, ask: FactoryAsk?) -> some View {
        if let ask {
            let pending = computer?.questions.first { $0.id == "fask-\(ask.id)" } ?? PendingAction(id: "fask-\(ask.id)", paneID: "factory:\(head.id)", kind: "answer_prompt", question: ask.question, options: ask.options.enumerated().map { PendingOption(index: $0.offset + 1, label: $0.element, isRecommended: $0.element == ask.recommend) }, agent: head.name, summary: ask.question, factory: FactoryAskMeta(project: head.id, task: ask.task, kind: ask.kind))
            ApprovalCardView(card: .init(pending: pending, phase: computer?.phase(pending.id) ?? .open(error: nil)), embedded: true, idPrefix: "setting.\(ask.id)", coordinatorHint: "Waiting for your answer. The same question is under asks for you, on your phone and by voice.") { idx in
                answer(ask: ask, pending: pending, row: row, optionIndex: idx, optionLabel: ask.options.indices.contains(idx - 1) ? ask.options[idx - 1] : "")
            }.padding(.top, 8).background(Theme.accentSoft, in: .rect(cornerRadius: 10))
        } else if let err = rowErrors[row.name] ?? row.storedProblemMessage { Text(err).factoryAlert() }
        else if let notice = notices[row.name] { Text(notice).font(.footnote.weight(.semibold)).foregroundStyle(Theme.done) }
    }

    private func setDraft(_ name: String, _ value: String) { drafts[name] = value; rowErrors[name] = nil; notices[name] = nil }

    private func load() async {
        if case .failure = loaded { loaded = nil }
        guard let computer else { return }
        do {
            let (rows, openAsks) = try await computer.request(as: DaemonClient.self) { [id = head.id] in (try await $0.factorySettings(projectID: id), (try? await $0.factoryAsks()) ?? []) }
            if !Task.isCancelled { loaded = .success(rows); asks = openAsks }
        } catch { if !Task.isCancelled { if FactoryProjectBoard.isProjectGone(error) { onGone() } else if (try? loaded?.get()) == nil { loaded = .failure(error) } } }
    }

    @discardableResult
    private func propose(row: FactorySettingRow, value: String) async throws -> FactoryAsk {
        guard let computer else { throw DaemonError.unreachable(endpoint: "", detail: "Not connected.") }
        busyRow = row.name; rowErrors[row.name] = nil; notices[row.name] = nil; defer { busyRow = nil }
        do {
            let ask = try await computer.actions.run("setting:\(head.id):\(row.name):\(value)", .init(timeout: 30, keyed: true), as: DaemonClient.self) { [id = head.id] in try await $0.proposeFactorySetting(projectID: id, name: row.name, value: value) }
            asks.removeAll { $0.set?.name == row.name }; asks.append(ask); drafts[row.name] = nil
            AccessibilityNotification.Announcement(ask.question).post(); Task { await computer.refresh() }
            return ask
        } catch {
            if FactoryProjectBoard.isProjectGone(error) { onGone(); throw error }
            let msg = FactorySettingRow.errorMessage(error, computerName: computer.name, linkUsable: computer.link.isUsable)
            if !row.isModels { rowErrors[row.name] = msg }
            AccessibilityNotification.Announcement(msg).post(); throw error
        }
    }

    private func answer(ask: FactoryAsk, pending: PendingAction, row: FactorySettingRow, optionIndex: Int, optionLabel: String) {
        guard let computer else { return }
        let task = computer.answer(pending, option: optionIndex)
        Task {
            await task?.value
            if case .open(let err?) = computer.phase(pending.id) { rowErrors[row.name] = err; return }
            asks.removeAll { $0.id == ask.id }; drafts[row.name] = nil
            let msg = optionLabel == "Save" ? (row.isModels ? "Saved. The next agent started for this role uses it. Running agents keep their model." : "Saved. The engine uses it from now on.") : "Kept at \(ask.set?.was?.trimmedOrNil ?? "not set"). Nothing changed."
            notices[row.name] = msg; AccessibilityNotification.Announcement(msg).post(); await load()
        }
    }
}

private struct FactoryModelsEditor: View {
    let row: FactorySettingRow, computerName: String, linkUsable: Bool, onPropose: (String) async throws -> FactoryAsk
    @Environment(\.dismiss) private var dismiss
    @State private var candidates: [FactoryModelCandidate]
    @State private var sending = false
    @State private var error: String?

    init(row: FactorySettingRow, computerName: String, linkUsable: Bool, onPropose: @escaping (String) async throws -> FactoryAsk) {
        self.row = row; self.computerName = computerName; self.linkUsable = linkUsable; self.onPropose = onPropose
        let parsed = FactoryModelCandidate.parse(row.value)
        _candidates = State(initialValue: parsed.isEmpty ? [.init(cli: row.clis.first ?? "claude", model: "")] : parsed)
    }

    var body: some View {
        let hints = candidates.reduce(into: [String]()) { if !$0.contains($1.cli), !$1.cli.isEmpty { $0.append($1.cli) } }.map { cli in
            row.models[cli].flatMap { $0.isEmpty ? nil : "Model names \(cli) offers on this account: \($0.joined(separator: " · ")). The field also takes a typed name." }
                ?? "sheepdog cannot list the models \(cli) offers. This name is checked when an agent starts."
        }
        NavigationStack {
            ScrollView {
                VStack(alignment: .leading, spacing: 10) {
                    Text("The CLI, model and effort each role uses. In order: the engine uses the first one it can start." + (row.name == "models.retro" ? " A retro reviewer looks at a task whose reviews keep failing." : "")).font(.caption).foregroundStyle(Theme.muted)
                    ForEach(Array(candidates.indices), id: \.self) { candidateCard($0) }
                    ForEach(hints, id: \.self) { Text($0).font(.caption).foregroundStyle(Theme.muted) }
                    OptionFlow(spacing: 8) {
                        Button("Add a model") { candidates.append(.init(cli: row.clis.first ?? "claude", model: "")); error = nil }.buttonStyle(PillButtonStyle(fullWidth: false)).disabled(sending)
                        Button("Back to the default") { candidates = FactoryModelCandidate.parse(row.defaultValue); error = nil }.buttonStyle(PillButtonStyle(fullWidth: false)).disabled(sending).accessibilityLabel("Fill in the default \(row.label)")
                    }
                    if let error { Text(error).factoryAlert() }
                    Button(sending ? "Asking…" : "Change") { submit() }.buttonStyle(PillButtonStyle(prominent: true)).disabled(sending).accessibilityLabel("Change \(row.label)")
                    Text("Nothing changes yet: the new list is read back to you, and saved when you answer Save.").font(.caption).foregroundStyle(Theme.muted)
                }.padding(16)
            }
            .background(Theme.surface).navigationTitle(row.label.prefix(1).uppercased() + row.label.dropFirst()).navigationBarTitleDisplayMode(.inline)
            .toolbar { ToolbarItem(placement: .cancellationAction) { Button("Cancel") { dismiss() } } }
        }
    }

    private func candidateCard(_ i: Int) -> some View {
        let ord = FactoryModelCandidate.ordinal(i), c = i < candidates.count ? candidates[i] : .init(cli: "", model: ""), tag = "\(c.cli) \(c.model.trimmedOrNil ?? "model")"
        return VStack(alignment: .leading, spacing: 6) {
            HStack(spacing: 8) {
                Text(ord).font(.caption.weight(.semibold)).foregroundStyle(Theme.muted); Spacer()
                if i > 0 { miniBtn("Move up", Theme.ink, "Move \(tag) up") { if i < candidates.count { candidates.swapAt(i, i - 1); error = nil } } }
                if candidates.count > 1 { miniBtn("Remove", Theme.danger, "Remove \(tag)") { if i < candidates.count { candidates.remove(at: i); error = nil } } }
            }
            segmentBar(row.clis, selected: c.cli, label: "CLI, \(ord)", id: "factoryModels.cli.\(i)") { if i < candidates.count { candidates[i].cli = $0; error = nil } }
            Text("model").font(.caption).foregroundStyle(Theme.muted)
            TextField("model", text: Binding(get: { i < candidates.count ? candidates[i].model : "" }, set: { if i < candidates.count { candidates[i].model = $0; error = nil } }))
                .font(.subheadline.monospaced()).autocorrectionDisabled().textInputAutocapitalization(.never).padding(.horizontal, 10).frame(minHeight: 36)
                .background(Theme.surface, in: .rect(cornerRadius: 10)).overlay(RoundedRectangle(cornerRadius: 10).strokeBorder(Theme.line)).accessibilityLabel("model, \(ord)").accessibilityIdentifier("factoryModels.model.\(i)")
            segmentBar(FactoryModelCandidate.efforts, selected: c.effort, label: "effort, \(ord)", id: "factoryModels.effort.\(i)") { if i < candidates.count { candidates[i].effort = $0; error = nil } }
        }.factoryCard()
    }

    private func miniBtn(_ title: String, _ fg: Color, _ label: String, action: @escaping () -> Void) -> some View {
        Button(title, action: action).font(.caption).foregroundStyle(fg).padding(.horizontal, 10).frame(minHeight: 32).background(Theme.card, in: Capsule()).overlay(Capsule().strokeBorder(Theme.line)).buttonStyle(.plain).disabled(sending).accessibilityLabel(label)
    }

    private func segmentBar(_ options: [String], selected: String, label: String, id: String, onSelect: @escaping (String) -> Void) -> some View {
        HStack(spacing: 2) {
            ForEach(options, id: \.self) { opt in
                Button { onSelect(opt) } label: {
                    Text(opt).font(.footnote.weight(opt == selected ? .semibold : .regular)).foregroundStyle(Theme.ink).frame(maxWidth: .infinity, minHeight: 30).background(opt == selected ? Theme.selected : Color.clear, in: .rect(cornerRadius: 8))
                }.buttonStyle(.plain).disabled(sending).accessibilityAddTraits(opt == selected ? [.isSelected] : [])
            }
        }.padding(3).background(Theme.track, in: .rect(cornerRadius: 11)).accessibilityElement(children: .contain).accessibilityLabel(label).accessibilityIdentifier(id)
    }

    private func submit() {
        if candidates.contains(where: { $0.model.trimmedOrNil == nil }) { error = "Type a model name."; AccessibilityNotification.Announcement("Type a model name.").post(); return }
        sending = true; error = nil
        Task {
            do { _ = try await onPropose(FactoryModelCandidate.format(candidates)); dismiss() }
            catch { sending = false; let msg = FactorySettingRow.errorMessage(error, computerName: computerName, linkUsable: linkUsable); self.error = msg; AccessibilityNotification.Announcement(msg).post() }
        }
    }
}

private extension View {
    func factoryCard(border: Color = Theme.line) -> some View { padding(12).frame(maxWidth: .infinity, alignment: .leading).background(Theme.card, in: .rect(cornerRadius: Theme.Radius.card)).overlay(RoundedRectangle(cornerRadius: Theme.Radius.card).strokeBorder(border)) }
    func factoryChip(_ fg: Color, _ bg: Color) -> some View { font(.caption2.weight(.semibold)).foregroundStyle(fg).padding(.horizontal, 7).padding(.vertical, 2).background(bg, in: Capsule()) }
    func factoryAlert() -> some View { font(.footnote.weight(.semibold)).foregroundStyle(Theme.danger).padding(10).frame(maxWidth: .infinity, alignment: .leading).background(Theme.danger.opacity(0.1), in: .rect(cornerRadius: 10)) }
}
