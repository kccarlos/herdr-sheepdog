import SheepdogKit
import SwiftUI

struct FactorySettingsView: View {
    let initialHead: FactoryProjectHead
    let computerID: String
    let onGone: () -> Void
    @EnvironmentObject private var model: SessionViewModel
    @Environment(\.dynamicTypeSize) private var dynamicTypeSize
    @AccessibilityFocusState private var focusedRow: String?
    @State private var loaded: Result<[FactorySettingRow], Error>?
    @State private var asks: [FactoryAsk] = []
    @State private var drafts: [String: String] = [:]
    @State private var feedback: [String: (text: String, isError: Bool, done: Bool, fix: String?)] = [:]
    @State private var busyRows: Set<String> = []
    @State private var showAll = false

    private var computer: ComputerModel? { model.fleet.model(computerID) }
    private var head: FactoryProjectHead {
        computer?.factoryHeads.first { $0.id == initialHead.id } ?? initialHead
    }
    private var reloadKey: String {
        let rev = computer?.factoryChanges[head.id] ?? 0
        let ids = computer?.questions.map(\.id).joined(separator: ",") ?? ""
        return "\(rev):\(ids)"
    }

    var body: some View {
        ScrollView {
            let name = computer?.name ?? "your computer"
            VStack(alignment: .leading, spacing: 12) {
                Text("For this project only. A change is read back to you and saved when you confirm.")
                    .font(.footnote).foregroundStyle(Theme.muted)
                switch loaded {
                case let .failure(error)?:
                    let raw = FactorySettingRow.errorMessage(
                        error, computerName: name, linkUsable: computer?.link.isUsable == true
                    )
                    let message = raw.last.map { ".!?".contains($0) } == true ? raw : "\(raw)."
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

    private func openAsk(for name: String) -> FactoryAsk? {
        asks.first { $0.status == "open" && $0.project == head.id && $0.set?.name == name }
    }

    private func rowView(_ row: FactorySettingRow) -> some View {
        let ask = openAsk(for: row.name)
        let current = drafts[row.name] ?? ask?.set?.value ?? row.value
        let sending = busyRows.contains(row.name)
        let dirty = ask == nil && drafts[row.name] != nil && drafts[row.name] != row.value
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
                rowControl(row, current: current, locked: ask != nil || sending)
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
            if !row.isModels, ask == nil, dirty || row.isChanged {
                rowActions(row, current: current, dirty: dirty, sending: sending)
            }
            rowFooter(row, ask: ask)
        }
        .factoryCard(border: ask != nil ? Theme.blocked : Theme.line)
        .accessibilityElement(children: .contain)
        .accessibilityIdentifier("factorySettings.row.\(row.name)")
    }

    private func rowActions(
        _ row: FactorySettingRow, current: String, dirty: Bool, sending: Bool
    ) -> some View {
        OptionFlow(spacing: 8) {
            if dirty {
                Button(sending ? "Asking…" : "Change") {
                    Task { await propose(row: row, value: current) }
                }
                .buttonStyle(PillButtonStyle(prominent: true, fullWidth: false))
                .disabled(sending)
                .accessibilityLabel(sending ? "Asking…" : "Change \(row.label)")
            }
            if row.isChanged {
                Button(sending ? "Asking…" : "Back to the default") {
                    Task { await propose(row: row, value: row.defaultValue) }
                }
                .buttonStyle(PillButtonStyle(fullWidth: false))
                .disabled(sending)
                .accessibilityLabel(
                    sending ? "Asking…" : "Set \(row.label) back to the default, \(row.displayDefault)"
                )
            }
        }
    }

    @ViewBuilder
    private func rowControl(_ row: FactorySettingRow, current: String, locked: Bool) -> some View {
        let binding = Binding(
            get: { drafts[row.name] ?? current },
            set: {
                drafts[row.name] = $0
                feedback[row.name] = nil
            }
        )
        let ax = dynamicTypeSize.isAccessibilitySize
        if row.isNumber {
            let num = Int(current) ?? row.min
            HStack(spacing: 0) {
                Button("−") { binding.wrappedValue = String(max(row.min, min(row.max, num - 1))) }
                    .frame(minWidth: 44, minHeight: 44).contentShape(Rectangle())
                    .accessibilityLabel("One fewer \(row.label)")
                    .accessibilityIdentifier("factorySettings.decrement.\(row.name)")
                TextField(row.label, text: binding)
                    .keyboardType(.numbersAndPunctuation)
                    .font(.subheadline.weight(.semibold)).monospacedDigit()
                    .padding(.horizontal, 6)
                    .frame(minWidth: 52, maxWidth: ax ? .infinity : 76, minHeight: 44)
                    .accessibilityLabel(row.label)
                    .accessibilityHint("default \(row.displayDefault), allowed \(row.min) to \(row.max)")
                    .accessibilityFocused($focusedRow, equals: row.name)
                Button("+") { binding.wrappedValue = String(max(row.min, min(row.max, num + 1))) }
                    .frame(minWidth: 44, minHeight: 44).contentShape(Rectangle())
                    .accessibilityLabel("One more \(row.label)")
                    .accessibilityIdentifier("factorySettings.increment.\(row.name)")
            }
            .foregroundStyle(Theme.ink).buttonStyle(.plain).disabled(locked)
            .background(Theme.fill, in: .rect(cornerRadius: 10))
        } else if row.isChoice {
            let choices = row.allowed.contains(current) ? row.allowed : [current] + row.allowed
            Picker(row.label, selection: binding) {
                ForEach(choices, id: \.self) { Text($0.isEmpty ? "not set" : $0).tag($0) }
            }
            .labelsHidden().frame(minHeight: 44).disabled(locked)
            .accessibilityLabel(row.label).accessibilityFocused($focusedRow, equals: row.name)
        } else if !row.isModels {
            TextField("not set", text: binding)
                .textFieldStyle(.roundedBorder)
                .autocorrectionDisabled().textInputAutocapitalization(.never)
                .frame(maxWidth: ax ? .infinity : 160, minHeight: 44).disabled(locked)
                .accessibilityLabel(row.label).accessibilityFocused($focusedRow, equals: row.name)
        }
    }

    @ViewBuilder
    private func rowFooter(_ row: FactorySettingRow, ask: FactoryAsk?) -> some View {
        let name = computer?.name ?? "your computer"
        if let ask {
            let pending = pendingAction(for: ask)
            let phase = answerPhase(for: pending.id, computerName: name)
            let hint = "Waiting for your answer. The same question is under asks for you,"
                + " on your phone and by voice."
            ApprovalCardView(
                card: .init(pending: pending, phase: phase),
                embedded: true,
                idPrefix: "setting.\(ask.id)",
                coordinatorHint: hint
            ) {
                answer(ask: ask, pending: pending, row: row, optionIndex: $0)
            }
            .padding(.top, 8).background(Theme.accentSoft, in: .rect(cornerRadius: 10))
        } else if let fb = feedback[row.name] {
            if fb.isError {
                Text(fb.text).factoryAlert()
                if let fix = fb.fix {
                    FactoryFixView(fix: fix, computerName: name)
                }
            } else {
                Text(fb.text).font(.footnote.weight(.semibold))
                    .foregroundStyle(fb.done ? Theme.done : Theme.muted)
            }
        } else if let stored = row.storedProblemMessage {
            Text(stored).factoryAlert()
        }
    }

    private func pendingAction(for ask: FactoryAsk) -> PendingAction {
        if let existing = computer?.questions.first(where: { $0.id == "fask-\(ask.id)" }) {
            return existing
        }
        let options = ask.options.enumerated().map {
            PendingOption(index: $0.offset + 1, label: $0.element, isRecommended: $0.element == ask.recommend)
        }
        return PendingAction(
            id: "fask-\(ask.id)", paneID: "factory:\(head.id)", kind: "answer_prompt",
            question: ask.question, options: options, agent: head.name, summary: ask.question,
            factory: FactoryAskMeta(project: head.id, task: ask.task, kind: ask.kind)
        )
    }

    private func answerPhase(for id: String, computerName: String) -> ApprovalFlow.Phase {
        let raw = computer?.phase(id) ?? .open(error: nil)
        if case let .open(err?) = raw {
            return .open(error: FactorySettingRow.answerError(err, computerName: computerName))
        }
        return raw
    }

    private func load() async {
        if case .failure = loaded { loaded = nil }
        guard let computer else { return }
        do {
            let (rows, openAsks) = try await computer.request(as: DaemonClient.self) { [id = head.id] client in
                (try await client.factorySettings(projectID: id), try? await client.factoryAsks())
            }
            guard !Task.isCancelled else { return }
            loaded = .success(rows)
            if let openAsks { asks = openAsks }
        } catch {
            guard !Task.isCancelled else { return }
            if FactoryProjectBoard.isProjectGone(error) {
                onGone()
            } else if (try? loaded?.get()) == nil {
                loaded = .failure(error)
            }
        }
    }

    private func propose(row: FactorySettingRow, value: String) async {
        guard let computer else { return }
        busyRows.insert(row.name)
        feedback[row.name] = nil
        defer { busyRows.remove(row.name) }
        do {
            let key = "setting:\(head.id):\(row.name):\(value)"
            let ask = try await computer.actions.run(
                key, .init(timeout: 30, keyed: true), as: DaemonClient.self
            ) { [id = head.id] in
                try await $0.proposeFactorySetting(projectID: id, name: row.name, value: value)
            }
            asks.removeAll { $0.project == head.id && $0.set?.name == row.name }
            asks.append(ask)
            drafts[row.name] = nil
            AccessibilityNotification.Announcement(ask.question).post()
            Task { await computer.refresh() }
        } catch {
            if FactoryProjectBoard.isProjectGone(error) {
                onGone()
                return
            }
            let msg = FactorySettingRow.errorMessage(
                error, computerName: computer.name, linkUsable: computer.link.isUsable
            )
            feedback[row.name] = (msg, true, false, (error as? FactoryRefusal)?.fix)
            AccessibilityNotification.Announcement(msg).post()
        }
    }

    private func answer(ask: FactoryAsk, pending: PendingAction, row: FactorySettingRow, optionIndex: Int) {
        guard let computer, let task = computer.answer(pending, option: optionIndex) else { return }
        Task {
            await task.value
            let text: String
            let done: Bool
            let expire: Bool
            switch computer.phase(pending.id) {
            case .answered:
                text = optionIndex == 1
                    ? "Saved. The engine uses it from now on."
                    : row.keptNotice(was: ask.set?.was)
                done = true
                expire = optionIndex == 1
            case let .gone(msg):
                let finished = "Not saved: the project finished before you answered,"
                    + " so this change was cancelled. You can make it again."
                text = head.state == .finished ? finished : msg
                done = false
                expire = false
            case let .open(err?):
                let msg = FactorySettingRow.answerError(err, computerName: computer.name)
                AccessibilityNotification.Announcement(msg).post()
                return
            case .open(nil), .sending:
                return
            }
            asks.removeAll { $0.id == ask.id }
            drafts[row.name] = nil
            feedback[row.name] = (text, false, done, nil)
            focusedRow = row.name
            AccessibilityNotification.Announcement(text).post()
            if expire {
                Task {
                    try? await Task.sleep(for: .seconds(5))
                    if feedback[row.name]?.text == text { feedback[row.name] = nil }
                }
            }
            await load()
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
