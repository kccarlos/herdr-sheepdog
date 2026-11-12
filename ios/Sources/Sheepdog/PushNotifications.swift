import SheepdogKit
import SwiftUI
import UIKit
import UserNotifications

/// The app delegate, for the two things SwiftUI's `App` does not give: the
/// APNs device token, and a notification-center delegate set before
/// launch finishes (otherwise a cold start from a notification action
/// loses the response).
final class AppDelegate: NSObject, UIApplicationDelegate {
    func application(_ application: UIApplication, didFinishLaunchingWithOptions launchOptions: [UIApplication.LaunchOptionsKey: Any]? = nil) -> Bool {
        MainActor.assumeIsolated { PushCoordinator.shared.launched() }
        Self.useRoundedNavigationTitles()
        #if DEBUG
        if DebugLaunch.noAnimations { UIView.setAnimationsEnabled(false) }
        #endif
        return true
    }

    /// Brand type: navigation titles (large and inline) in SF Rounded; body
    /// text everywhere stays the default SF. `.fontDesign` on a view does
    /// not reach the bar's title, so it goes through the bar's attributes.
    static func useRoundedNavigationTitles() {
        func rounded(_ style: UIFont.TextStyle, _ weight: UIFont.Weight) -> UIFont {
            let size = UIFont.preferredFont(forTextStyle: style).pointSize
            let base = UIFont.systemFont(ofSize: size, weight: weight)
            guard let design = base.fontDescriptor.withDesign(.rounded) else { return base }
            return UIFont(descriptor: design, size: size)
        }
        let appearance = UINavigationBarAppearance()
        appearance.configureWithDefaultBackground()
        appearance.largeTitleTextAttributes = [.font: rounded(.largeTitle, .bold)]
        appearance.titleTextAttributes = [.font: rounded(.headline, .semibold)]
        let bar = UINavigationBar.appearance()
        bar.standardAppearance = appearance
        bar.compactAppearance = appearance
        bar.scrollEdgeAppearance = appearance
    }

    func application(_ application: UIApplication, didRegisterForRemoteNotificationsWithDeviceToken deviceToken: Data) {
        MainActor.assumeIsolated { PushCoordinator.shared.didRegister(deviceToken: deviceToken) }
    }

    func application(_ application: UIApplication, didFailToRegisterForRemoteNotificationsWithError error: Error) {
        MainActor.assumeIsolated { PushCoordinator.shared.didFailToRegister(error) }
    }

    /// Swiped away from the app switcher: take the Live Activity and the
    /// Now Playing entry with it (the next launch ends any it missed).
    func applicationWillTerminate(_ application: UIApplication) {
        MainActor.assumeIsolated { PushCoordinator.shared.model?.away.tearDown() }
    }
}

/// Phone notifications: asks for permission after the first successful
/// connection (not on first launch), registers with APNs, hands the device
/// token to the daemon (`POST /api/push/register`) and keeps it there
/// across token and address changes, answers Approve / Decline from the
/// notification through `POST /api/pending/answer`, and opens the agent's
/// Agents card on a tap. While the session is live in the foreground the
/// banner is suppressed — the app already shows the card.
@MainActor
@Observable
final class PushCoordinator: NSObject, UNUserNotificationCenterDelegate {
    static let shared = PushCoordinator()

    /// The screen, once SwiftUI has built it (nil during a background launch
    /// for a notification action).
    @ObservationIgnored weak var model: SessionViewModel? {
        didSet {
            applyPendingFocus()
            model?.onQuestionsChanged = { [weak self] id, closed in self?.reconcileQuestions(computerID: id, closedID: closed) }
            if model !== oldValue { for c in model?.fleet.models ?? [] where c.lastState != nil { reconcileQuestions(computerID: c.id) } }
        }
    }

    private(set) var authorization: UNAuthorizationStatus = .notDetermined
    /// The device token (hex), once APNs gave one.
    private(set) var deviceToken: String?
    /// Per-computer registration state and errors.
    private var registrationState = PushRegistrationState()
    private var apnsError: String?
    var registrationError: String? {
        if let apnsError { return apnsError }
        let endpoint = makeSettings().endpoint
        return registrationState.error(computerID: store.currentID, endpointDisplay: endpoint?.display ?? "")
    }
    /// `GET /api/push/status` of the current daemon.
    private(set) var daemonStatus: PushStatus?
    private(set) var daemonStatusError: String?

    private let defaults: UserDefaults
    private let store: ComputerStore
    private let makeSettings: () -> SettingsStore
    private let makeKeyStore: (String?) -> PushContentKeyStore
    private let namesSuite: String
    private let makeClient: @Sendable (DaemonEndpoint) -> DaemonClient
    private let notifyOpenHandler: ((PushPayload, String) async -> Void)?
    private let removeDeliveredHandler: (([String]) -> Void)?
    @ObservationIgnored var deliveredProvider: (() async -> [(id: String, payload: PushPayload)])?
    private var center: UNUserNotificationCenter { UNUserNotificationCenter.current() }
    @ObservationIgnored private var pendingFocus: AgentFocus?
    @ObservationIgnored private var targetEpochs: [String: Int] = [:]
    @ObservationIgnored private var statusEpoch = 0

    private func epochKey(for id: String?) -> String { id ?? "_bare" }

    private func bumpTargetEpoch(for id: String?) {
        let key = epochKey(for: id)
        targetEpochs[key, default: 0] &+= 1
    }

    private func bumpAllTargetEpochs() {
        for key in Array(targetEpochs.keys) { targetEpochs[key, default: 0] &+= 1 }
        targetEpochs["_bare", default: 0] &+= 1
        for id in store.ids { targetEpochs[id, default: 0] &+= 1 }
    }

    private func isTargetStillValid(id: String?, endpoint: DaemonEndpoint) -> Bool {
        if let id {
            return store.computer(id) != nil
        }
        return store.count == 0 && makeSettings().endpoint?.display == endpoint.display
    }

    private enum Key {
        static let token = "push.deviceToken"
        static let registeredWith = "push.registeredWith"
        static let registeredTargets = "push.registeredTargets"
        static let asked = "push.askedPermission"
    }

    /// Debug builds talk to APNs' sandbox, TestFlight and App Store builds
    /// to production — from the Info.plist key project.yml sets per
    /// configuration, the same split as the `aps-environment` entitlement.
    static let environment = PushEnvironment(infoValue: Bundle.main.object(forInfoDictionaryKey: "SheepdogPushEnvironment") as? String)

    override private convenience init() {
        let rootStore = PushContentKeyStore()
        self.init(
            defaults: .standard,
            store: .shared,
            makeSettings: { SettingsStore() },
            makeKeyStore: { id in id.map { PushContentKeyStore.forComputer($0) } ?? rootStore },
            namesSuite: PushContentKeyStore.appGroup,
            makeClient: { DaemonClient(endpoint: $0) }
        )
    }

    init(
        defaults: UserDefaults,
        store: ComputerStore,
        makeSettings: @escaping () -> SettingsStore,
        makeKeyStore: @escaping (String?) -> PushContentKeyStore,
        namesSuite: String,
        makeClient: @escaping @Sendable (DaemonEndpoint) -> DaemonClient,
        notifyOpenHandler: ((PushPayload, String) async -> Void)? = nil,
        removeDeliveredHandler: (([String]) -> Void)? = nil,
        initialDeviceToken: String? = nil
    ) {
        self.defaults = defaults
        self.store = store
        self.makeSettings = makeSettings
        self.makeKeyStore = makeKeyStore
        self.namesSuite = namesSuite
        self.makeClient = makeClient
        self.notifyOpenHandler = notifyOpenHandler
        self.removeDeliveredHandler = removeDeliveredHandler
        super.init()
        if let initialDeviceToken {
            defaults.set(initialDeviceToken, forKey: Key.token)
        }
        deviceToken = defaults.string(forKey: Key.token)
        var saved = Set(defaults.stringArray(forKey: Key.registeredTargets) ?? [])
        if saved.isEmpty, let legacy = defaults.string(forKey: Key.registeredWith), !legacy.isEmpty {
            let migrated = PushRegistrationState.targetKey(computerID: store.currentID, endpointDisplay: legacy)
            saved.insert(migrated)
        }
        registrationState = PushRegistrationState(registeredTargets: saved)
    }

    private func persistRegistrationTargets() {
        if registrationState.registeredTargets.isEmpty {
            defaults.removeObject(forKey: Key.registeredTargets)
            defaults.removeObject(forKey: Key.registeredWith)
        } else {
            defaults.set(Array(registrationState.registeredTargets).sorted(), forKey: Key.registeredTargets)
        }
    }

    // MARK: - Launch and permission

    func launched() {
        center.delegate = self
        let approve = UNNotificationAction(identifier: PushActions.approve, title: "Approve", options: [.authenticationRequired])
        // Also behind the passcode: the daemon's token lives in the keychain
        // as "when unlocked", so a locked phone could not send the answer.
        let decline = UNNotificationAction(identifier: PushActions.decline, title: "Decline", options: [.authenticationRequired, .destructive])
        let approval = UNNotificationCategory(identifier: PushActions.approvalCategory, actions: [approve, decline], intentIdentifiers: [], options: [])
        let agent = UNNotificationCategory(identifier: PushActions.agentCategory, actions: [], intentIdentifiers: [], options: [])
        center.setNotificationCategories([approval, agent])
        Task { await refreshAuthorization() }
    }

    /// The session opened: the moment the app has proved it can reach the
    /// daemon. Asks once per install, and only when this daemon can send
    /// notifications at all (`/api/push/status` enabled): a daemon without
    /// an APNs key from the team that signed this app can never deliver
    /// one, so asking would promise something that cannot happen.
    func sessionConnected() {
        Task {
            await refreshAuthorization()
            switch authorization {
            case .authorized, .provisional, .ephemeral:
                if deviceToken != nil {
                    let current = makeSettings().endpoint
                    if let current, !registrationState.isRegistered(computerID: store.currentID, endpointDisplay: current.display) {
                        await register()
                    }
                }
                return
            case .notDetermined:
                break
            default:
                return
            }
            guard !defaults.bool(forKey: Key.asked) else { return }
            await loadDaemonStatus()
            guard PushPolicy.shouldAskPermission(daemon: daemonStatus) else { return }
            defaults.set(true, forKey: Key.asked)
            guard await model?.ask(.notifications) ?? true else { return }
            let granted = (try? await center.requestAuthorization(options: [.alert, .sound, .badge])) ?? false
            await refreshAuthorization()
            if granted { UIApplication.shared.registerForRemoteNotifications() }
        }
    }

    /// Retries push registration when a computer's link transitions `.online`
    /// after being unreachable on launch.
    func computerCameOnline(id: String?, endpoint: DaemonEndpoint?) {
        guard deviceToken != nil, let endpoint else { return }
        switch authorization {
        case .authorized, .provisional, .ephemeral:
            guard !registrationState.isRegistered(computerID: id, endpointDisplay: endpoint.display) else { return }
            Task { await registerTarget(id: id, endpoint: endpoint) }
        default:
            break
        }
    }

    /// On launch and on every return to the foreground: register again
    /// when allowed (Apple's advice: the token can change), and take the
    /// device off the daemon when notifications were turned off.
    func refreshAuthorization() async {
        let settings = await center.notificationSettings()
        authorization = settings.authorizationStatus
        switch settings.authorizationStatus {
        case .authorized, .provisional, .ephemeral:
            UIApplication.shared.registerForRemoteNotifications()
        case .denied:
            if let token = deviceToken {
                await unregisterEverywhere(token: token)
                forgetToken()
            }
        case .notDetermined:
            break
        @unknown default:
            break
        }
    }

    // MARK: - Token

    func didRegister(deviceToken data: Data) {
        let hex = PushToken.hex(data)
        let previous = deviceToken
        deviceToken = hex
        defaults.set(hex, forKey: Key.token)
        apnsError = nil
        if previous != hex { bumpAllTargetEpochs() }
        Task {
            // A new token replaces the old one on every computer.
            if let previous, previous != hex { await unregisterEverywhere(token: previous) }
            await register()
        }
    }

    func didFailToRegister(_ error: Error) {
        apnsError = "APNs refused: \(error.localizedDescription)"
    }

    /// The computers or the address changed: register again with every
    /// computer (each keeps its own registration and content key). With
    /// nothing paired, the keys go.
    func endpointChanged(from old: DaemonEndpoint?) {
        let current = makeSettings().endpoint
        let sameCurrent = old.map { current.map(ConversationCache.key(for:)) == ConversationCache.key(for: $0) } ?? (current == nil)
        if !sameCurrent || current == nil {
            daemonStatus = nil
            daemonStatusError = nil
            statusEpoch &+= 1
        }
        guard deviceToken != nil else {
            if current == nil {
                bumpAllTargetEpochs()
                removeAllKeys()
                registrationState.reset()
                persistRegistrationTargets()
            }
            writeNames()
            return
        }
        Task { await register() }
    }

    /// Where the token goes: every paired computer by its id, or the bare
    /// loopback address of the tests (nil id).
    private func targets() -> [(id: String?, endpoint: DaemonEndpoint)] {
        let paired = store.ids.compactMap { id in store.endpoint(for: id).map { (id: Optional(id), endpoint: $0) } }
        if !paired.isEmpty { return paired }
        return makeSettings().endpoint.map { [(id: nil, endpoint: $0)] } ?? []
    }

    /// A request to one computer through its runtime, on the address its
    /// link uses now (not the stored preferred one). Before the models
    /// exist (a launch, a notification action that woke the app) it goes
    /// straight to the stored endpoint.
    private func onComputer<T>(_ id: String?, endpoint: DaemonEndpoint, _ body: @escaping @Sendable (DaemonClient) async throws -> T) async throws -> T {
        if let fleet = model?.fleet, let computer = id.flatMap({ fleet.model($0) }) ?? (id == nil ? fleet.current : nil) {
            return try await computer.request(as: DaemonClient.self, body)
        }
        let client = makeClient(endpoint)
        defer { client.invalidate() }
        return try await body(client)
    }

    private func keys(for id: String?) -> PushContentKeyStore {
        makeKeyStore(id)
    }

    /// Registers one computer or bare endpoint and updates `registrationState`.
    private func registerTarget(id: String?, endpoint: DaemonEndpoint) async {
        guard let token = deviceToken else { return }
        let key = epochKey(for: id)
        let epoch = targetEpochs[key, default: 0]
        do {
            let registration = PushRegistration(token: token, environment: Self.environment, device: UIDevice.current.name)
            let answer = try await onComputer(id, endpoint: endpoint) { try await $0.registerPushDevice(registration) }
            guard deviceToken == token,
                  targetEpochs[key, default: 0] == epoch,
                  isTargetStillValid(id: id, endpoint: endpoint)
            else { return }
            if let contentKey = answer?.contentKey {
                keys(for: id).store(base64: contentKey)
            } else {
                keys(for: id).remove()
            }
            if answer != nil {
                registrationState.recordSuccess(computerID: id, endpointDisplay: endpoint.display)
            } else {
                registrationState.recordNoService(computerID: id, endpointDisplay: endpoint.display)
            }
            persistRegistrationTargets()
        } catch let error as DaemonError {
            guard deviceToken == token,
                  targetEpochs[key, default: 0] == epoch,
                  isTargetStillValid(id: id, endpoint: endpoint)
            else { return }
            registrationState.recordError(error.message, computerID: id, endpointDisplay: endpoint.display)
        } catch {
            guard deviceToken == token,
                  targetEpochs[key, default: 0] == epoch,
                  isTargetStillValid(id: id, endpoint: endpoint)
            else { return }
            registrationState.recordError(error.localizedDescription, computerID: id, endpointDisplay: endpoint.display)
        }
    }

    /// `POST /api/push/register` to every paired computer. Idempotent on
    /// the daemon, so it is sent on every launch with a token. Each
    /// answer's content key replaces that computer's stored one; a
    /// computer without a push service leaves no key behind.
    func register() async {
        writeNames()
        let targets = targets()
        guard !targets.isEmpty else {
            bumpAllTargetEpochs()
            removeAllKeys()
            registrationState.reset()
            persistRegistrationTargets()
            return
        }
        guard deviceToken != nil else { return }
        for target in targets {
            await registerTarget(id: target.id, endpoint: target.endpoint)
        }
        // The one key from before several computers.
        if targets.contains(where: { $0.id != nil }) { keys(for: nil).remove() }
        await loadDaemonStatus()
    }

    /// Takes the token off one computer (Remove), with that computer's key.
    func unregister(computer id: String) async {
        bumpTargetEpoch(for: id)
        if store.currentID == id || store.currentID == nil {
            daemonStatus = nil
            daemonStatusError = nil
            statusEpoch &+= 1
        }
        keys(for: id).remove()
        registrationState.unregister(computerID: id)
        persistRegistrationTargets()
        guard let token = deviceToken, let endpoint = store.endpoint(for: id) else { return }
        _ = try? await onComputer(id, endpoint: endpoint) { try await $0.unregisterPush(token: token) }
    }

    /// Takes the token off every computer, with their keys.
    private func unregisterEverywhere(token: String) async {
        bumpAllTargetEpochs()
        for target in targets() {
            keys(for: target.id).remove()
            _ = try? await onComputer(target.id, endpoint: target.endpoint) { try await $0.unregisterPush(token: token) }
        }
        registrationState.reset()
        persistRegistrationTargets()
    }

    private func removeAllKeys() {
        keys(for: nil).remove()
        for id in store.ids { keys(for: id).remove() }
    }

    /// The names the extension adds to alerts, only with several computers.
    func writeNames() {
        let computers = store.computers
        PushComputerNames.write(Dictionary(computers.map { ($0.daemonID, $0.displayName) }, uniquingKeysWith: { a, _ in a }), suite: namesSuite)
    }

    private func forgetToken() {
        bumpAllTargetEpochs()
        daemonStatus = nil
        daemonStatusError = nil
        statusEpoch &+= 1
        removeAllKeys()
        registrationState.reset()
        persistRegistrationTargets()
        deviceToken = nil
        defaults.removeObject(forKey: Key.token)
    }

    func loadDaemonStatus() async {
        guard let endpoint = makeSettings().endpoint else {
            daemonStatus = nil
            daemonStatusError = nil
            return
        }
        let targetID = store.currentID
        let targetKey = ConversationCache.key(for: endpoint)
        statusEpoch &+= 1
        let epoch = statusEpoch
        do {
            let status = try await onComputer(targetID, endpoint: endpoint) { try await $0.pushStatus() }
            guard statusEpoch == epoch,
                  store.currentID == targetID,
                  makeSettings().endpoint.map(ConversationCache.key(for:)) == targetKey
            else { return }
            daemonStatus = status
            daemonStatusError = status == nil ? "This daemon predates notifications." : nil
        } catch let error as DaemonError {
            guard statusEpoch == epoch,
                  store.currentID == targetID,
                  makeSettings().endpoint.map(ConversationCache.key(for:)) == targetKey
            else { return }
            daemonStatusError = error.message
        } catch {
            guard statusEpoch == epoch,
                  store.currentID == targetID,
                  makeSettings().endpoint.map(ConversationCache.key(for:)) == targetKey
            else { return }
            daemonStatusError = error.localizedDescription
        }
    }

    /// One line for Settings: is this phone registered with this daemon.
    var phoneLine: String {
        switch authorization {
        case .denied: return "Off in iOS Settings"
        case .notDetermined: return daemonStatus?.canSend == true ? "Asked after the first connection" : "Not needed yet"
        default: break
        }
        if let error = registrationError { return error }
        guard deviceToken != nil else { return "Waiting for Apple" }
        if let endpoint = makeSettings().endpoint,
           registrationState.isRegistered(computerID: store.currentID, endpointDisplay: endpoint.display) {
            return "Registered"
        }
        return "Not registered yet"
    }

    // MARK: - UNUserNotificationCenterDelegate

    // The completion-handler forms, called back on the main thread: UIKit
    // asserts when they come from another thread, and the async forms'
    // thunk called them from wherever the method finished (a tap from the
    // lock screen crashed on it).
    nonisolated func userNotificationCenter(_ center: UNUserNotificationCenter, willPresent notification: UNNotification,
                                            withCompletionHandler completionHandler: @escaping (UNNotificationPresentationOptions) -> Void) {
        let payload = PushPayload(userInfo: notification.request.content.userInfo)
        Task { @MainActor in
            // Suppress only when the target computer's live stream / screen
            // or voice already shows this update in the foreground.
            completionHandler((model?.shouldSuppressForegroundPush(payload) ?? false) ? [] : [.banner, .list, .sound])
        }
    }

    nonisolated func userNotificationCenter(_ center: UNUserNotificationCenter, didReceive response: UNNotificationResponse,
                                            withCompletionHandler completionHandler: @escaping () -> Void) {
        let payload = PushPayload(userInfo: response.notification.request.content.userInfo)
        let action = response.actionIdentifier
        let identifier = response.notification.request.identifier
        Task { @MainActor in
            if let payload { await handle(action: action, payload: payload, notificationID: identifier) }
            completionHandler()
        }
    }

    func handle(action: String, payload: PushPayload, notificationID: String) async {
        guard let answer = PushActions.answer(actionIdentifier: action, payload: payload) else {
            if action == PushActions.approve || action == PushActions.decline {
                // Nothing that action can mean for this prompt: let the
                // user answer it on the card.
                await notifyOpenInstead(payload, reason: "Open Sheepdog to answer this one.")
            } else if action != UNNotificationDismissActionIdentifier {
                focus(AgentFocus(computerID: payload.computer, paneID: payload.paneID))
            }
            return
        }
        // Answered strictly on the computer that asked; never guessed or
        // routed to whatever computer happens to be current now.
        let target = PushActionTarget.resolve(
            payload: payload,
            store: store,
            bareEndpoint: store.count == 0 ? makeSettings().endpoint : nil,
            interface: model?.fleet.path.interface
        )
        let targetID: String?
        let candidates: [DaemonEndpoint]
        switch target {
        case let .refused(reason):
            await notifyOpenInstead(payload, reason: reason)
            return
        case let .paired(id, endpoints):
            targetID = id
            candidates = endpoints
        case let .bare(endpoint):
            targetID = nil
            candidates = [endpoint]
        }
        let task = UIApplication.shared.beginBackgroundTask(withName: "answer approval")
        defer { UIApplication.shared.endBackgroundTask(task) }
        let pendingID = answer.pendingID
        let option = answer.option.index
        do {
            if let computer = targetID.flatMap({ model?.fleet.model($0) }) ?? (targetID == nil && store.count == 0 ? model?.fleet.current : nil) {
                // The running app: through the question's phase on its
                // computer, under the question's own key, so its cards show
                // it on its way and a tap on one joins this answer instead
                // of sending a second.
                try await computer.answerFromNotification(question: pendingID, option: answer.option)
            } else {
                let winner = try await PushActions.answerPending(id: pendingID, option: option, endpoints: candidates, makeClient: makeClient)
                if let targetID, candidates.count > 1 {
                    store.recordRaceWinner(winner.display, for: targetID, interface: model?.fleet.path.interface)
                }
            }
            if let removeDeliveredHandler {
                removeDeliveredHandler([notificationID])
            } else {
                center.removeDeliveredNotifications(withIdentifiers: [notificationID])
            }
            Task { await model?.refreshState(quiet: true) }
        } catch DaemonError.pendingGone {
            await notifyOpenInstead(payload, reason: ApprovalFlow.goneMessage + " Open Sheepdog to see where things are.")
        } catch let error as DaemonError {
            await notifyOpenInstead(payload, reason: "Couldn't answer: \(error.message)")
        } catch {
            await notifyOpenInstead(payload, reason: "Couldn't answer: \(error.localizedDescription)")
        }
    }

    /// An action cannot bring the app forward from the background, so a
    /// stale or failed answer leaves a notification whose tap opens the
    /// agent's card instead.
    private func notifyOpenInstead(_ payload: PushPayload, reason: String) async {
        if let notifyOpenHandler {
            await notifyOpenHandler(payload, reason)
            return
        }
        let content = UNMutableNotificationContent()
        content.title = payload.agent ?? "Sheepdog"
        content.body = reason
        content.threadIdentifier = payload.paneID
        content.categoryIdentifier = PushActions.agentCategory
        content.userInfo = payload.openInsteadUserInfo()
        let request = UNNotificationRequest(identifier: "open-\(payload.pendingID ?? payload.paneID)", content: content, trigger: nil)
        try? await center.add(request)
    }

    /// A tap on a notification: the app on that agent's Agents card.
    func focus(_ target: AgentFocus) {
        pendingFocus = target.paneID.isEmpty ? nil : target
        applyPendingFocus()
    }

    func focus(paneID: String) {
        focus(AgentFocus(paneID: paneID))
    }

    private func applyPendingFocus() {
        guard let model, let target = pendingFocus else { return }
        pendingFocus = nil
        model.focusAgent(target)
    }

    func reconcileQuestions(computerID: String, closedID: String? = nil) {
        Task {
            let list = if let deliveredProvider { await deliveredProvider() } else {
                await center.deliveredNotifications().compactMap { n in PushPayload(userInfo: n.request.content.userInfo).map { (n.request.identifier, $0) } }
            }
            guard let c = model?.fleet.models.first(where: { $0.id == computerID }), closedID != nil || c.lastState != nil else { return }
            let open = Set(c.questions.map(\.id)).union(c.approval?.isSettled == false ? [c.approval!.pending.id] : [])
            let ids = PushPolicy.closedQuestionNotificationIDs(in: list, computerID: computerID, pairedComputerIDs: Set(store.ids), open: open, closedID: closedID)
            if !ids.isEmpty { if let h = removeDeliveredHandler { h(ids) } else { center.removeDeliveredNotifications(withIdentifiers: ids) } }
        }
    }
}

/// Settings ▸ Models: whether notifications reach this phone.
struct NotificationsSection: View {
    let push: PushCoordinator

    var body: some View {
        Section {
            LabeledContent("This phone", value: push.phoneLine)
                .accessibilityIdentifier("push.phone")
            LabeledContent("Daemon", value: push.daemonStatus?.summary ?? push.daemonStatusError ?? "—")
                .accessibilityIdentifier("push.daemon")
            if push.authorization == .denied {
                Button("Open iOS Settings") {
                    if let url = URL(string: UIApplication.openNotificationSettingsURLString) {
                        UIApplication.shared.open(url)
                    }
                }
            }
        } header: {
            Text("Notifications")
        } footer: {
            Text(PushPolicy.explanation(daemon: push.daemonStatus))
        }
        .task { await push.loadDaemonStatus() }
    }
}
