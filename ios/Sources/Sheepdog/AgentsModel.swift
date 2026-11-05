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
        return Flock.sorted(overlay.applying(to: all, now: Date()))
    }

    /// What the user just changed (a pin, a look, a mute) shown before the
    /// daemon's state says so; see `AttentionOverlay`.
    private(set) var overlay = AttentionOverlay()
    /// Which turns were already marked seen; not shown, so not observed.
    @ObservationIgnored private var seenTracker = SeenTracker()

    /// Changes the overlay and settles it five seconds on, in case the
    /// daemon's state never shows the change.
    func change(_ edit: (inout AttentionOverlay) -> Void) {
        edit(&overlay)
        Task { [weak self] in
            try? await Task.sleep(nanoseconds: UInt64((AttentionOverlay.lifetime + 0.2) * 1_000_000_000))
            self?.settleAttention()
        }
    }

    /// A fresh state: drop the overlay entries it now shows.
    func settleAttention() {
        guard !overlay.isEmpty, let current else { return }
        overlay.settle(with: current.agents, now: Date())
    }

    func takeSeenWrites(visible: [Agent], appActive: Bool) -> [SeenTracker.Write] {
        seenTracker.writes(visible: visible, appActive: appActive)
    }

    func seenFailed(_ write: SeenTracker.Write) { seenTracker.failed(write) }
    func forgetSeen(_ paneID: String) { seenTracker.forget(paneID) }

    /// Clears per-computer optimistic attention state, seen turn marks, and
    /// expanded card keys when switching to a different computer.
    func resetForComputerSwitch() {
        overlay = AttentionOverlay()
        seenTracker = SeenTracker()
        expandedAgents.removeAll()
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
    /// Computer sections the user closed. Kept for the session; a section
    /// starts expanded and pairing order never changes.
    private(set) var collapsedComputers: Set<String> = []
    /// Sheets over the voice screen that the session cannot see itself (the
    /// connection chip's, the feedback flag's), by name.
    private(set) var screenCovers: Set<String> = []
    /// `term-ready.shell` from this daemon: whether it offers the phone shell
    /// to this phone on this network. nil until `/term` answered, and again
    /// after `/api/hello` reports a different daemon version.
    private(set) var shellOffer = PhoneShellOffer()
    var shellOffered: Bool? { shellOffer.offered }

    /// The agents as the screen groups them: running (blocked, working, idle
    /// and done) and the rest.
    var flock: Flock { Flock(agents) }

    func isComputerExpanded(_ id: String) -> Bool {
        !collapsedComputers.contains(id)
    }

    func expandComputer(_ id: String) {
        collapsedComputers.remove(id)
    }

    func toggleComputer(_ id: String) {
        if collapsedComputers.contains(id) {
            collapsedComputers.remove(id)
        } else {
            collapsedComputers.insert(id)
        }
    }

    func setShellOffered(_ next: Bool?) {
        if let next {
            shellOffer.ready(shell: next)
        } else {
            shellOffer.reset()
        }
    }

    /// A fresh `/api/hello`: an upgraded daemon is asked about the shell
    /// again instead of inheriting the old binary's answer.
    func setHello(_ hello: Hello) {
        shellOffer.hello(version: hello.version)
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
