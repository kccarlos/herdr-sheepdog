import SwiftUI

/// The app's windows on iPad (several scenes). There is one session view
/// model and so one voice session for the whole app; what differs per
/// window is who shows its controls and its sheets:
///
/// - **The audio owner** is the oldest main window still open. It shows
///   the full voice capsule, reports what is on screen (`visibility`) and
///   drives the session's foreground and background. Every other window
///   shows the mini capsule.
/// - **The presenter** shows the session's sheets (Settings, New agent,
///   Spaces, a card, the pairing flow): the window whose menu asked for
///   one, else the owner. Without it every window would open the same
///   sheet at once.
///
/// An agent window opened with Open in New Window never registers: it is
/// always a mini-capsule window.
@MainActor
@Observable
final class SceneCoordinator {
    static let shared = SceneCoordinator()

    /// Main windows, oldest first.
    var scenes: [UUID] = []
    /// The window that last asked for a session sheet.
    var requester: UUID?
    /// The offline demo's view model while it is open: an agent window
    /// opened from it shows the demo's agents.
    var demoModel: SessionViewModel?

    var owner: UUID? { scenes.first }
    var presenter: UUID? { requester ?? owner }

    func register(_ id: UUID) {
        guard !scenes.contains(id) else { return }
        scenes.append(id)
    }

    func unregister(_ id: UUID) {
        scenes.removeAll { $0 == id }
        if requester == id { requester = nil }
    }

    /// A window's own menu is about to open a session sheet: it shows there.
    func request(from id: UUID?) {
        guard let id, scenes.contains(id) else { return }
        requester = id
    }

    /// Before any window registered (its first frame), the window is
    /// taken for the owner, so the capsule does not flash mini.
    func isOwner(_ id: UUID?) -> Bool {
        guard let id else { return true }
        return owner == nil || owner == id
    }

    func isPresenter(_ id: UUID?) -> Bool {
        guard let id else { return true }
        return presenter == nil || presenter == id
    }
}

/// What one window keeps for itself: its dialogs, the composer and, on
/// iPad, what its detail shows (kept across size-class changes).
@MainActor
@Observable
final class SceneUI {
    var confirmNewConversation = false
    /// The capsule turned into the composer.
    var typing = false

    // iPad (IPadRootView)
    var detail: IPadDetail = .conversation
    var columns: NavigationSplitViewVisibility = .all
    /// A shell pushed over the detail.
    var detailPath = NavigationPath()
    /// Bumped to focus the Conversation's composer (the capsule's keyboard).
    var focusComposer = 0
    /// Of two panes, the one keys and ⌘ shortcuts act on.
    var focusedPane = 0
    /// The first of two panes' share of the room.
    var splitFraction: CGFloat = 0.5

    /// A sidebar row: focus the agent if it is on screen, else show it in
    /// the focused pane.
    func show(_ id: String) {
        let panes = detail.panes
        if let index = panes.firstIndex(of: id) {
            focusedPane = index
        } else if panes.count == 2 {
            replace(pane: focusedPane, with: id)
        } else {
            detail = .agent(id)
            focusedPane = 0
        }
    }

    /// Beside the current pane (a new agent, a drop, the split menu). Of
    /// two, the one not focused makes way. Never the same agent twice.
    func beside(_ id: String) {
        let panes = detail.panes
        if let index = panes.firstIndex(of: id) {
            focusedPane = index
            return
        }
        switch panes.count {
        case 1:
            detail = .pair(panes[0], id)
            focusedPane = 1
        case 2:
            let other = focusedPane == 0 ? 1 : 0
            replace(pane: other, with: id)
            focusedPane = other
        default:
            detail = .agent(id)
            focusedPane = 0
        }
    }

    func replace(pane index: Int, with id: String) {
        var panes = detail.panes
        guard panes.indices.contains(index), !panes.contains(id) else {
            if let at = panes.firstIndex(of: id) { focusedPane = at }
            return
        }
        panes[index] = id
        detail = panes.count == 2 ? .pair(panes[0], panes[1]) : .agent(panes[0])
    }

    /// A pane's ✕, or its agent ended: the other one stays; with none
    /// left, the Conversation.
    func close(pane index: Int) {
        var panes = detail.panes
        guard panes.indices.contains(index) else { return }
        panes.remove(at: index)
        detail = panes.first.map { .agent($0) } ?? .conversation
        focusedPane = 0
    }
}

/// Where an agent's or a shell's window opens.
enum WindowRequest: Hashable {
    case agent(String)
    /// A new agent: on iPad it opens beside the focused pane.
    case newAgent(String)
    case shell(cwd: String?)
}

/// Opens a window the way this layout does: a push on iPhone, the detail
/// (or a pane beside it) on iPad.
struct OpenWindowAction {
    let run: (WindowRequest) -> Void
    func callAsFunction(_ request: WindowRequest) { run(request) }
}

private struct OpenWindowKey: EnvironmentKey {
    static let defaultValue = OpenWindowAction { _ in }
}

extension EnvironmentValues {
    var openAgentWindow: OpenWindowAction {
        get { self[OpenWindowKey.self] }
        set { self[OpenWindowKey.self] = newValue }
    }
}

private struct SceneIDKey: EnvironmentKey {
    static let defaultValue: UUID? = nil
}

private struct AppPhaseKey: EnvironmentKey {
    static let defaultValue: ScenePhase = .active
}

extension EnvironmentValues {
    /// This window's id in `SceneCoordinator`; nil in an agent window.
    var sceneID: UUID? {
        get { self[SceneIDKey.self] }
        set { self[SceneIDKey.self] = newValue }
    }

    /// The app's phase across all its windows (the `App`'s `scenePhase`):
    /// the session goes to the background only when every window has.
    var appPhase: ScenePhase {
        get { self[AppPhaseKey.self] }
        set { self[AppPhaseKey.self] = newValue }
    }
}

/// A main window's root: registers it while it is open.
struct MainSceneRoot<Content: View>: View {
    @ViewBuilder let content: () -> Content
    @State private var id = UUID()
    private let scenes = SceneCoordinator.shared

    var body: some View {
        content()
            .environment(\.sceneID, id)
            .onAppear { scenes.register(id) }
            .onDisappear { scenes.unregister(id) }
            .debugAnimationsOff()
    }
}

/// `-sheepdogNoAnimations` in a debug build; always false in a release.
/// The endless animations (the orb breathing, a spinner) check it too, as
/// they do Reduce Motion: one left running keeps the main thread busy and a
/// UI test's queries time out.
var debugNoAnimations: Bool {
    #if DEBUG
    DebugLaunch.noAnimations
    #else
    false
    #endif
}

extension View {
    /// `-sheepdogNoAnimations` (debug builds, UI tests): no SwiftUI
    /// animation runs in this window, so a test's tap never lands on a
    /// sheet, menu or cover that is still moving. UIKit's own (the
    /// presentations) are off from launch (`AppDelegate`).
    @ViewBuilder func debugAnimationsOff() -> some View {
        #if DEBUG
        if DebugLaunch.noAnimations {
            transaction { transaction in
                transaction.disablesAnimations = true
                transaction.animation = nil
            }
        } else {
            self
        }
        #else
        self
        #endif
    }
}
