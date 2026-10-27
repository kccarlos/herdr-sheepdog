import Foundation
import SheepdogKit

/// The current computer's agents and open questions as the screens show
/// them, plus the view state that goes with them: the cards the user opened,
/// the sheets over the voice screen and whether the daemon offers the phone
/// shell. Split out of `SessionViewModel` (phase 8), so a poll that changes
/// one agent re-renders the views that read agents and nothing else.
///
/// `agents`, `pendingAction` and `questions` are read from the current
/// `ComputerModel`, not copied: every state the app applies lands there
/// first (the model's own refreshes and the ones the session fetched itself,
/// through `accept`), so the views, the Live Activity and the answer phases
/// read one copy. The agents are sorted here, and the DEBUG launch flags add
/// their extra agent and question.
@MainActor
@Observable
final class AgentsModel {
    /// Where the current computer is.
    @ObservationIgnored weak var fleet: ComputerFleet?

    private var current: ComputerModel? { fleet?.current }

    /// The agents, blocked first, then working, idle and done; what is not
    /// running last (`Flock.sorted`).
    var agents: [Agent] {
        guard let current else { return [] }
        var all = current.agents
        #if DEBUG
        if DebugLaunch.demoMarkdown { all.append(DebugLaunch.markdownAgent) }
        if DebugLaunch.demoSecondQuestion { all.append(DebugLaunch.secondQuestionAgent) }
        #endif
        return Flock.sorted(all)
    }

    /// The voice's own open question (`/api/state` `pending_action`); it goes
    /// through the approval card.
    var pendingAction: PendingAction? { current?.pendingAction }

    /// Every open question (`/api/state` `questions`), oldest first: each
    /// blocked agent gets its own answer buttons. The voice's question,
    /// `pendingAction`, is among them.
    var questions: [PendingAction] {
        guard let current else { return [] }
        #if DEBUG
        if DebugLaunch.demoSecondQuestion { return current.questions + [DebugLaunch.secondQuestion] }
        #endif
        return current.questions
    }

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
}
