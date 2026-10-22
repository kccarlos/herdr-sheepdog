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
final class SceneCoordinator: ObservableObject {
    static let shared = SceneCoordinator()

    /// Main windows, oldest first.
    @Published private(set) var scenes: [UUID] = []
    /// The window that last asked for a session sheet.
    @Published private(set) var requester: UUID?

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
final class SceneUI: ObservableObject {
    @Published var confirmNewConversation = false
    @Published var showsConnection = false
    /// The capsule turned into the composer.
    @Published var typing = false

    // iPad (IPadRootView)
    @Published var detail: IPadDetail = .conversation
    @Published var columns: NavigationSplitViewVisibility = .all
    /// A shell pushed over the detail.
    @Published var detailPath = NavigationPath()
    /// Bumped to focus the Conversation's composer (the capsule's keyboard).
    @Published var focusComposer = 0
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
    @ObservedObject private var scenes = SceneCoordinator.shared

    var body: some View {
        content()
            .environment(\.sceneID, id)
            .onAppear { scenes.register(id) }
            .onDisappear { scenes.unregister(id) }
    }
}
