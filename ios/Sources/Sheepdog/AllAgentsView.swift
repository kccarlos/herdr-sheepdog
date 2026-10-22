import SheepdogKit
import SwiftUI

/// iPad ▸ All agents.
struct AllAgentsView: View {
    let leading: AnyView?
    @EnvironmentObject private var model: SessionViewModel

    private var paneState: FlockPaneState {
        FlockPaneState.resolve(chip: model.connectionChip.status, hasAgents: !model.flock.isEmpty)
    }

    var body: some View {
        VStack(spacing: 0) {
            DetailHeader(title: "All agents") {
                if let leading { leading }
            } trailing: {
                EmptyView()
            }
            if paneState != .content {
                FlockUnavailableView(state: paneState)
            } else {
                FlockView(
                    flock: model.flock,
                    question: { model.questionCard(for: $0) },
                    answer: { model.answer(question: $0, option: $1) },
                    expanded: $model.expandedAgents,
                    focusedPaneID: $model.focusedAgentPaneID,
                    onNewAgent: model.agentActionsAvailable ? { model.showsNewAgent = true } : nil,
                    voiceQuestionID: model.approval?.pending.id
                )
            }
        }
    }
}
