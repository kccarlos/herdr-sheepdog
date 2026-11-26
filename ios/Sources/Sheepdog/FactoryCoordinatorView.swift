import SheepdogKit
import SwiftUI

struct FactoryCoordinatorView: View {
    let initialHead: FactoryProjectHead
    let computerID: String
    let onAsks: () -> Void
    @EnvironmentObject private var model: SessionViewModel
    @Environment(\.dismiss) private var dismiss
    @Environment(\.colorScheme) private var scheme

    var body: some View {
        let computer = model.fleet.model(computerID)
        let head = computer?.factoryHeads.first { $0.id == initialHead.id } ?? missingHead
        let asks = computer?.questions.filter { $0.factory?.project == head.id } ?? []
        let coordinator = FactoryCoordinatorWindow(head: head, openAsks: asks)
        let background = Color((scheme == .dark ? TerminalTheme.dark : .light).background)
        Group {
            if let pane = coordinator.coordinatorPane {
                AgentScreenView(agent: Agent(paneID: pane, alias: head.name, state: .working),
                                computer: computerID, coordinator: coordinator, onCoordinatorAsks: {
                    onAsks()
                    dismiss()
                })
            } else if let off = coordinator.offState {
                GeometryReader { geometry in
                    ScrollView {
                        offContent(off, backLabel: coordinator.backButtonLabel)
                            .frame(minHeight: geometry.size.height)
                    }
                }
            }
        }
        .background(background.ignoresSafeArea())
        .navigationTitle(coordinator.title)
        .navigationBarTitleDisplayMode(.inline)
        .toolbarBackground(background, for: .navigationBar)
        .toolbarBackground(.visible, for: .navigationBar)
        .toolbar {
            ToolbarItem(placement: .principal) {
                HStack(spacing: 6) {
                    Circle().fill(coordinator.isLive ? Theme.working : Theme.muted)
                        .frame(width: 8, height: 8)
                    Text(coordinator.title).font(.headline)
                        .multilineTextAlignment(.center).lineLimit(2)
                }
                .accessibilityElement(children: .ignore)
                .accessibilityLabel(coordinator.headerAccessibilityLabel)
                .accessibilityAddTraits(.isHeader)
                .accessibilityIdentifier("agentScreen.header")
            }
        }
    }

    private var missingHead: FactoryProjectHead {
        var head = initialHead
        head.coordinatorPane = nil
        return head
    }

    private func offContent(_ off: FactoryCoordinatorWindow.OffState, backLabel: String) -> some View {
        VStack(spacing: 12) {
            Image(systemName: "terminal").font(.largeTitle).foregroundStyle(Theme.muted).accessibilityHidden(true)
            Text(off.heading).font(.title3.weight(.semibold)).accessibilityAddTraits(.isHeader)
            if let cause = off.alertCause {
                VStack(alignment: .leading, spacing: 4) {
                    Text(cause).fontWeight(.semibold)
                    if let fix = off.alertFix { Text(fix) }
                }
                .font(.subheadline).multilineTextAlignment(.leading)
                .padding(12).frame(maxWidth: .infinity, alignment: .leading)
                .background(Theme.card, in: .rect(cornerRadius: 12))
                .overlay { RoundedRectangle(cornerRadius: 12).strokeBorder(Theme.muted, style: StrokeStyle(dash: [3, 3])) }
            }
            Text(off.body).foregroundStyle(Theme.muted)
            Button(backLabel) { dismiss() }.buttonStyle(PillButtonStyle(fullWidth: false))
        }
        .multilineTextAlignment(.center).frame(maxWidth: 480).padding(24)
        .frame(maxWidth: .infinity)
    }
}
