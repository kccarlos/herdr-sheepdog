import Foundation
import SheepdogKit

/// Pin, unread and space mute from the phone. The logic is in SheepdogKit
/// (`AttentionOverlay`, `SeenTracker`, `MuteChoice`, the `DaemonClient`
/// routes); this forwards the taps, rolls a failed write back and says so
/// in plain words.
extension SessionViewModel {
    /// Pin or unpin an agent. The row moves at once; a failed write puts it
    /// back and says why.
    func setPinned(_ agent: Agent, _ pinned: Bool) {
        guard agent.hasAttention else { return }
        let pane = agent.paneID
        agentsModel.change { $0.setPinned(pane, pinned, at: Date()) }
        Task {
            do {
                _ = try await action({ ActionRunner.Key.agent($0, pane, "pin:\(pinned)") }, .agent) { try await $0.setPinned(paneID: pane, pinned) }
                Haptics.answered()
            } catch {
                failed(.pinned, pane: pane, "Couldn't \(pinned ? "pin" : "unpin") \(agent.alias).", error)
            }
        }
    }

    /// Mark as unread: only for a read, finished agent.
    func markUnread(_ agent: Agent) {
        guard agent.hasAttention, agent.group == .done, !agent.isUnread else { return }
        let pane = agent.paneID
        agentsModel.change { $0.markUnread(pane, at: Date()) }
        agentsModel.forgetSeen(pane)
        Task {
            do {
                _ = try await action({ ActionRunner.Key.agent($0, pane, "unread") }, .agent) { try await $0.markUnread(paneID: pane) }
                Haptics.answered()
            } catch {
                failed(.unread, pane: pane, "Couldn't mark \(agent.alias) as unread.", error)
            }
        }
    }

    /// These agents are on screen (their window is open or their card is
    /// expanded) and the app is in front: mark what they finished as seen,
    /// once per turn. Quiet: a failure is retried at the next look and
    /// never shown.
    func markVisible(_ paneIDs: Set<String>, appActive: Bool) {
        let visible = agentsModel.agents.filter { paneIDs.contains($0.paneID) }
        for write in agentsModel.takeSeenWrites(visible: visible, appActive: appActive) {
            agentsModel.change { $0.markSeen(write.paneID, turn: write.turn, at: Date()) }
            Task {
                do {
                    _ = try await action({ ActionRunner.Key.agent($0, write.paneID, "seen") }, .quiet) { try await $0.markSeen(paneID: write.paneID, turn: write.turn) }
                } catch {
                    agentsModel.change { $0.rollback(write.paneID, .seen) }
                    agentsModel.seenFailed(write)
                    SheepdogLog.session.error("seen: \(String(describing: error), privacy: .public)")
                }
            }
        }
    }

    /// Mute or unmute a space. The time is worked out here, on this phone's
    /// clock and in its time zone, and sent as an instant.
    func setSpaceMute(_ space: SpacesView.Space, _ choice: MuteChoice?) async throws {
        let panes = space.agents.map(\.paneID)
        let until = choice?.until(from: Date())
        agentsModel.change { $0.setMuted(panes, choice != nil, at: Date()) }
        do {
            _ = try await action({ ActionRunner.Key.space($0, "mute:\(space.id)") }, .space) {
                try await $0.setSpaceMute(spaceID: space.id, muted: choice != nil, until: until)
            }
            Haptics.answered()
        } catch {
            agentsModel.change { overlay in panes.forEach { overlay.rollback($0, .muted) } }
            throw error
        }
    }

    private func failed(_ field: AttentionOverlay.Field, pane: String, _ words: String, _ error: Error) {
        agentsModel.change { $0.rollback(pane, field) }
        Haptics.answerFailed()
        if let daemonError = error as? DaemonError, daemonError.meansFeatureAbsent {
            notices.showFeedback("\(words) Update sheepdog on your computer.")
        } else {
            notices.showFeedback("\(words) Try again.")
        }
        SheepdogLog.session.error("attention: \(String(describing: error), privacy: .public)")
    }
}
