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
        didSet { applyPendingFocus() }
    }

    private(set) var authorization: UNAuthorizationStatus = .notDetermined
    /// The device token (hex), once APNs gave one.
    private(set) var deviceToken: String?
    /// Per-computer registration state and errors.
    private var registrationState = PushRegistrationState()
    private var apnsError: String?
    var registrationError: String? {
        if let apnsError { return apnsError }
        let endpoint = SettingsStore().endpoint
        return registrationState.error(computerID: ComputerStore.shared.currentID, endpointDisplay: endpoint?.display ?? "")
    }
    /// `GET /api/push/status` of the current daemon.
    private(set) var daemonStatus: PushStatus?
    private(set) var daemonStatusError: String?

    private let defaults = UserDefaults.standard
    /// The daemon's end-to-end content key, in the App Group keychain the
    /// Notification Service Extension reads it from.
    private let contentKeys = PushContentKeyStore()
    private let center = UNUserNotificationCenter.current()
    @ObservationIgnored private var pendingFocus: AgentFocus?

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

    override private init() {
        super.init()
        deviceToken = defaults.string(forKey: Key.token)
        var saved = Set(defaults.stringArray(forKey: Key.registeredTargets) ?? [])
        if saved.isEmpty, let legacy = defaults.string(forKey: Key.registeredWith), !legacy.isEmpty {
            let migrated = PushRegistrationState.targetKey(computerID: ComputerStore.shared.currentID, endpointDisplay: legacy)
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
                    let current = SettingsStore().endpoint
                    if let current, !registrationState.isRegistered(computerID: ComputerStore.shared.currentID, endpointDisplay: current.display) {
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
        guard deviceToken != nil else {
            if SettingsStore().endpoint == nil {
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
        let store = ComputerStore.shared
        let paired = store.ids.compactMap { id in store.endpoint(for: id).map { (id: Optional(id), endpoint: $0) } }
        if !paired.isEmpty { return paired }
        return SettingsStore().endpoint.map { [(id: nil, endpoint: $0)] } ?? []
    }

    /// A request to one computer through its runtime, on the address its
    /// link uses now (not the stored preferred one). Before the models
    /// exist (a launch, a notification action that woke the app) it goes
    /// straight to the stored endpoint.
    private func onComputer<T>(_ id: String?, endpoint: DaemonEndpoint, _ body: @escaping @Sendable (DaemonClient) async throws -> T) async throws -> T {
        if let fleet = model?.fleet, let computer = id.flatMap({ fleet.model($0) }) ?? (id == nil ? fleet.current : nil) {
            return try await computer.request(as: DaemonClient.self, body)
        }
        let client = DaemonClient(endpoint: endpoint)
        defer { client.invalidate() }
        return try await body(client)
    }

    private func keys(for id: String?) -> PushContentKeyStore {
        id.map { PushContentKeyStore.forComputer($0) } ?? contentKeys
    }

    /// Registers one computer or bare endpoint and updates `registrationState`.
    private func registerTarget(id: String?, endpoint: DaemonEndpoint) async {
        guard let token = deviceToken else { return }
        do {
            let registration = PushRegistration(token: token, environment: Self.environment, device: UIDevice.current.name)
            let answer = try await onComputer(id, endpoint: endpoint) { try await $0.registerPushDevice(registration) }
            if let key = answer?.contentKey {
                keys(for: id).store(base64: key)
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
            registrationState.recordError(error.message, computerID: id, endpointDisplay: endpoint.display)
        } catch {
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
        if targets.contains(where: { $0.id != nil }) { contentKeys.remove() }
        await loadDaemonStatus()
    }

    /// Takes the token off one computer (Remove), with that computer's key.
    func unregister(computer id: String) async {
        let store = ComputerStore.shared
        keys(for: id).remove()
        registrationState.unregister(computerID: id)
        persistRegistrationTargets()
        guard let token = deviceToken, let endpoint = store.endpoint(for: id) else { return }
        _ = try? await onComputer(id, endpoint: endpoint) { try await $0.unregisterPush(token: token) }
    }

    /// Takes the token off every computer, with their keys.
    private func unregisterEverywhere(token: String) async {
        for target in targets() {
            keys(for: target.id).remove()
            _ = try? await onComputer(target.id, endpoint: target.endpoint) { try await $0.unregisterPush(token: token) }
        }
        registrationState.reset()
        persistRegistrationTargets()
    }

    private func removeAllKeys() {
        contentKeys.remove()
        for id in ComputerStore.shared.ids { PushContentKeyStore.forComputer(id).remove() }
    }

    /// The names the extension adds to alerts, only with several computers.
    func writeNames() {
        let computers = ComputerStore.shared.computers
        PushComputerNames.write(Dictionary(computers.map { ($0.daemonID, $0.displayName) }, uniquingKeysWith: { a, _ in a }))
    }

    private func forgetToken() {
        removeAllKeys()
        registrationState.reset()
        persistRegistrationTargets()
        deviceToken = nil
        defaults.removeObject(forKey: Key.token)
    }

    func loadDaemonStatus() async {
        guard let endpoint = SettingsStore().endpoint else { return }
        do {
            daemonStatus = try await onComputer(ComputerStore.shared.currentID, endpoint: endpoint) { try await $0.pushStatus() }
            daemonStatusError = daemonStatus == nil ? "This daemon predates notifications." : nil
        } catch let error as DaemonError {
            daemonStatusError = error.message
        } catch {
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
        if let endpoint = SettingsStore().endpoint,
           registrationState.isRegistered(computerID: ComputerStore.shared.currentID, endpointDisplay: endpoint.display) {
            return "Registered"
        }
        return "Not registered yet"
    }

    // MARK: - UNUserNotificationCenterDelegate

    nonisolated func userNotificationCenter(_ center: UNUserNotificationCenter, willPresent notification: UNNotification) async -> UNNotificationPresentationOptions {
        let userInfo = notification.request.content.userInfo
        let payload = PushPayload(userInfo: userInfo)
        return await MainActor.run {
            // Suppress only when the target computer's live stream / screen
            // or voice already shows this update in the foreground.
            (model?.shouldSuppressForegroundPush(payload) ?? false) ? [] : [.banner, .list, .sound]
        }
    }

    nonisolated func userNotificationCenter(_ center: UNUserNotificationCenter, didReceive response: UNNotificationResponse) async {
        let userInfo = response.notification.request.content.userInfo
        let action = response.actionIdentifier
        let identifier = response.notification.request.identifier
        guard let payload = PushPayload(userInfo: userInfo) else { return }
        await handle(action: action, payload: payload, notificationID: identifier)
    }

    private func handle(action: String, payload: PushPayload, notificationID: String) async {
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
        // Answered on the computer that asked.
        let store = ComputerStore.shared
        let targetID = payload.computer ?? store.currentID
        let candidates: [DaemonEndpoint] = {
            if let targetID {
                let ordered = store.candidateEndpoints(for: targetID, interface: model?.fleet.path.interface)
                if !ordered.isEmpty { return ordered }
            }
            return SettingsStore().endpoint.map { [$0] } ?? []
        }()
        guard !candidates.isEmpty else {
            await notifyOpenInstead(payload, reason: "Sheepdog has no daemon address yet.")
            return
        }
        let task = UIApplication.shared.beginBackgroundTask(withName: "answer approval")
        defer { UIApplication.shared.endBackgroundTask(task) }
        let pendingID = answer.pendingID
        let option = answer.option.index
        do {
            if let computer = payload.computer.flatMap({ model?.fleet.model($0) }) ?? model?.fleet.current.flatMap({ payload.computer == nil ? $0 : nil }) {
                // The running app: through the question's phase on its
                // computer, under the question's own key, so its cards show
                // it on its way and a tap on one joins this answer instead
                // of sending a second.
                try await computer.answerFromNotification(question: pendingID, option: answer.option)
            } else {
                let winner = try await PushActions.answerPending(id: pendingID, option: option, endpoints: candidates)
                if let targetID, candidates.count > 1 {
                    store.setAddress(winner.display, for: targetID)
                }
            }
            center.removeDeliveredNotifications(withIdentifiers: [notificationID])
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
