import SheepdogKit
import SwiftUI

struct FactoryTaskView: View {
    let task: FactoryTask
    let projectName: String
    let projectState: FactoryProjectState

    var body: some View {
        let detail = FactoryTaskDetail(task: task, projectName: projectName, projectState: projectState)
        ScrollView {
            VStack(alignment: .leading, spacing: 12) {
                VStack(alignment: .leading, spacing: 4) {
                    Text(detail.task.ref).font(.footnote.monospaced()).foregroundStyle(Theme.muted)
                    Text(detail.task.title).font(.title2.weight(.bold)).fontDesign(.rounded).fixedSize(horizontal: false, vertical: true)
                    OptionFlow(spacing: 8) {
                        HStack(spacing: 5) {
                            Circle().fill(stageColor(detail.task.stage)).frame(width: 7, height: 7).accessibilityHidden(true)
                            Text(detail.stageLabel)
                        }.font(.footnote.weight(.semibold)).padding(.horizontal, 9).padding(.vertical, 3).background(Theme.fill, in: Capsule())
                        detail.afterLine.map { Text($0).font(.footnote).foregroundStyle(Theme.muted) }
                        detail.roundsLine.map { Text($0).font(.footnote).foregroundStyle(Theme.muted) }
                    }
                    if let run = detail.runLine {
                        Label(run, systemImage: "person").font(.footnote).foregroundStyle(Theme.muted)
                            .accessibilityElement(children: .combine).accessibilityLabel(detail.runAccessibilityLabel ?? run)
                            .accessibilityIdentifier("factoryTask.run")
                    }
                }
                card("Steps") {
                    if detail.isEmptySteps {
                        Text(detail.emptyStepsMessage).font(.subheadline).foregroundStyle(Theme.muted)
                    } else {
                        ForEach(detail.steps) { step in
                            HStack(alignment: .top, spacing: 10) {
                                Image(systemName: stepIcon(step.state)).foregroundStyle(stepColor(step.state)).accessibilityHidden(true)
                                VStack(alignment: .leading, spacing: 2) {
                                    HStack(alignment: .firstTextBaseline, spacing: 6) {
                                        Text(step.name).font(.subheadline.weight(step.state == "now" ? .bold : .medium))
                                        Text(step.stateWord).font(.caption).foregroundStyle(Theme.muted)
                                    }
                                    step.text.map { Text($0).font(.footnote).foregroundStyle(Theme.muted) }
                                }
                            }
                            .accessibilityElement(children: .combine).accessibilityAddTraits(.isStaticText)
                            .accessibilityLabel(step.accessibilityLabel).accessibilityIdentifier("factoryTask.step.\(step.id)")
                        }
                    }
                }
                if let last = detail.lastDecision {
                    card(last.heading) {
                        Text("\(last.actorLabel): \(last.decision)").font(.subheadline.weight(.semibold))
                        last.whyLine.map { Text($0).font(.subheadline) }
                        last.at.map { Text($0.formatted(date: .abbreviated, time: .shortened)).font(.footnote).foregroundStyle(Theme.muted) }
                    }.accessibilityElement(children: .contain).accessibilityIdentifier("factoryTask.lastDecision")
                }
                if let brief = detail.brief {
                    card("Brief") { Text(brief).font(.subheadline).textSelection(.enabled) }
                        .accessibilityElement(children: .contain).accessibilityIdentifier("factoryTask.brief")
                }
            }
            .frame(maxWidth: 660, alignment: .leading).padding(16)
            .accessibilityElement(children: .contain).accessibilityIdentifier("factoryTask.view")
        }
        .background(Theme.surface).navigationTitle(detail.task.ref).navigationBarTitleDisplayMode(.inline)
    }

    private func card<Content: View>(_ heading: String, @ViewBuilder content: () -> Content) -> some View {
        VStack(alignment: .leading, spacing: 8) {
            Text(heading).font(.footnote.weight(.semibold)).foregroundStyle(Theme.muted).accessibilityAddTraits(.isHeader)
            content()
        }
        .padding(14).frame(maxWidth: .infinity, alignment: .leading)
        .background(Theme.card, in: .rect(cornerRadius: Theme.Radius.card))
        .overlay(RoundedRectangle(cornerRadius: Theme.Radius.card).strokeBorder(Theme.line))
    }

    private func stageColor(_ stage: String) -> Color {
        stage == "working" || stage == "ci" ? Theme.working : stage == "done" ? Theme.done
            : ["owner_gate", "owner_answer", "blocked_or_failed"].contains(stage) ? Theme.blocked : Theme.idle
    }

    private func stepIcon(_ state: String) -> String {
        ["done": "checkmark.circle.fill", "now": "circle.fill", "skipped": "minus.circle", "ended": "xmark.circle"][state] ?? "circle"
    }

    private func stepColor(_ state: String) -> Color {
        state == "done" ? Theme.done : state == "now" ? Theme.working : state == "ended" ? Theme.idle : Theme.muted
    }
}

struct FactoryGoalSheet: View {
    let head: FactoryProjectHead
    let computer: ComputerModel
    let onGone: () -> Void
    @Environment(\.dismiss) private var dismiss
    @State private var draft = ""
    @State private var errorMessage: String?
    @State private var checks: [FactoryStartCheck] = []
    @State private var submitting = false

    private var form: FactoryGoalForm { FactoryGoalForm(state: head.state, hasFolder: head.folder?.trimmedOrNil != nil, draft: draft) }

    var body: some View {
        ScrollView {
            VStack(alignment: .leading, spacing: 10) {
                Text(form.title).font(.title3.weight(.bold)).accessibilityAddTraits(.isHeader)
                Text(form.fieldLabel).font(.subheadline.weight(.semibold)).foregroundStyle(Theme.muted).accessibilityHidden(true)
                TextField("", text: Binding(get: { draft }, set: { draft = $0; errorMessage = nil; checks = [] }), prompt: Text(form.fieldLabel), axis: .vertical)
                    .font(.body).lineLimit(2...5).padding(10)
                    .background(Theme.card, in: .rect(cornerRadius: 12)).overlay(RoundedRectangle(cornerRadius: 12).strokeBorder(Theme.line))
                    .accessibilityLabel(form.fieldLabel).accessibilityIdentifier("factoryProject.goal.field")
                if let errorMessage {
                    Text(errorMessage).font(.footnote.weight(.semibold)).foregroundStyle(Theme.danger)
                        .fixedSize(horizontal: false, vertical: true).accessibilityIdentifier("factoryProject.goal.error")
                }
                ForEach(Array(checks.enumerated()), id: \.offset) { _, check in
                    checkRow(check)
                }
                Text(form.hint).font(.footnote).foregroundStyle(Theme.muted)
                Text(form.quotaHint).font(.footnote).foregroundStyle(Theme.muted)
            }.padding(16)
        }
        .safeAreaInset(edge: .bottom) {
            VStack(spacing: 8) {
                Button(submitting ? "Starting…" : form.submitLabel, action: submit)
                    .buttonStyle(PillButtonStyle(prominent: true)).disabled(!form.canSubmit || submitting)
                    .accessibilityIdentifier("factoryProject.goal.submit")
                Button(form.cancelLabel) { dismiss() }
                    .font(.body).foregroundStyle(Theme.primaryText).frame(minHeight: 44).accessibilityIdentifier("factoryProject.goal.cancel")
            }.padding(.horizontal, 16).padding(.vertical, 10).background(Theme.surface)
        }
        .background(Theme.surface).accessibilityElement(children: .contain).accessibilityIdentifier("factoryProject.goalSheet")
        .onChange(of: errorMessage) { if let errorMessage { AccessibilityNotification.Announcement(errorMessage).post() } }
    }

    private func checkRow(_ check: FactoryStartCheck) -> some View {
        VStack(alignment: .leading, spacing: 3) {
            HStack(spacing: 6) {
                Text(check.statusWord).fontWeight(.semibold).foregroundStyle(check.statusWord == "fail" ? Theme.danger : Theme.done)
                Text(check.name).fontWeight(.semibold)
            }
            if let detail = check.detail?.trimmedOrNil { Text(detail).foregroundStyle(Theme.muted) }
            if let fix = check.fix?.trimmedOrNil { Text("Fix: \(fix)").foregroundStyle(Theme.muted) }
        }
        .font(.footnote).padding(10).frame(maxWidth: .infinity, alignment: .leading)
        .background(Theme.card, in: .rect(cornerRadius: 10)).overlay(RoundedRectangle(cornerRadius: 10).strokeBorder(Theme.line))
    }

    private func submit() {
        guard let folder = head.folder?.trimmedOrNil, let goal = draft.trimmedOrNil, form.canSubmit, !submitting else { return }
        let state = head.state
        errorMessage = nil; checks = []; submitting = true
        Task {
            defer { submitting = false }
            do {
                try await computer.actions.run("goal:\(head.id):\(goal)", .init(timeout: 60, keyed: true), as: DaemonClient.self) {
                    try await $0.startFactory(folder: folder, goal: goal)
                }
                await computer.refresh(); dismiss()
            } catch where FactoryProjectBoard.isProjectGone(error) {
                dismiss(); onGone()
            } catch {
                checks = (error as? FactoryStartChecksError)?.results ?? []
                errorMessage = FactoryGoalForm.errorMessage(error, state: state, computerName: computer.name, linkUsable: computer.link.isUsable)
            }
        }
    }
}
