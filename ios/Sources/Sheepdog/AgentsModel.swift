import Foundation
import SheepdogKit

/// The current computer's agents and open questions as the screens show
/// them, plus the view state that goes with them: the cards the user opened,
/// the sheets over the voice screen and whether the daemon offers the phone
/// shell. Split out of `SessionViewModel` (phase 8), so a poll that changes
/// one agent re-renders the views that read agents and nothing else.
///
/// `agents`, `pendingAction` and `questions` are mirrors that the session
/// writes from each `/api/state` it applies, not values derived from the
/// current `ComputerModel`: the session also applies a state it fetched
/// straight (opening a session, a loud refresh, the demo's probe), which
/// the computer's model never sees; the agents are sorted here and carry the
/// DEBUG launch flags' extra agents; and the haptic cue for a question that
/// arrived without its own event compares against the last list applied.
/// Every setter assigns only when the value changed.
@MainActor
@Observable
final class AgentsModel {
    /// The agents, blocked first, then working, idle and done; what is not
    /// running last (`Flock.sorted`).
    private(set) var agents: [Agent] = []
    /// The voice's own open question (`/api/state` `pending_action`); it goes
    /// through the approval card.
    private(set) var pendingAction: PendingAction?
    /// Every open question (`/api/state` `questions`), oldest first: each
    /// blocked agent gets its own answer buttons. The voice's question,
    /// `pendingAction`, is among them.
    private(set) var questions: [PendingAction] = []
    /// Agents cards the user opened, by pane id; kept for the session.
    var expandedAgents: Set<String> = []
    /// Sheets over the voice screen that the session cannot see itself (the
    /// connection chip's, the feedback flag's), by name.
    private(set) var screenCovers: Set<String> = []
    /// `term-ready.shell` from this daemon: whether it offers the phone shell
    /// to this phone on this network. nil until `/term` answered.
    private(set) var shellOffered: Bool?

    /// The agents as the screen groups them: running (blocked, working, idle
    /// and done) and the rest.
    var flock: Flock { Flock(agents) }

    func setAgents(_ next: [Agent]) {
        if agents != next { agents = next }
    }

    func setPendingAction(_ next: PendingAction?) {
        if pendingAction != next { pendingAction = next }
    }

    func setQuestions(_ next: [PendingAction]) {
        if questions != next { questions = next }
    }

    func setShellOffered(_ next: Bool?) {
        if shellOffered != next { shellOffered = next }
    }

    func setScreenCover(_ name: String, _ covering: Bool) {
        if covering {
            if !screenCovers.contains(name) { screenCovers.insert(name) }
        } else if screenCovers.contains(name) {
            screenCovers.remove(name)
        }
    }

    /// The question the voice is asking, when it is this agent's.
    func pendingQuestion(for agent: Agent) -> String? {
        guard let pending = pendingAction, pending.paneID == agent.paneID else { return nil }
        return pending.question ?? pending.spokenSummary
    }

    /// The computer switched: nothing of the old one stays on screen.
    func clear() {
        setAgents([])
        setQuestions([])
    }
}
