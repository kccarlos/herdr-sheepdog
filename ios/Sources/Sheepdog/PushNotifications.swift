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
        return true
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
final class PushCoordinator: NSObject, ObservableObject, UNUserNotificationCenterDelegate {
    static let shared = PushCoordinator()

    /// The screen, once SwiftUI has built it (nil during a background launch
    /// for a notification action).
    weak var model: SessionViewModel? {
        didSet { applyPendingFocus() }
    }

    @Published private(set) var authorization: UNAuthorizationStatus = .notDetermined
    /// The device token (hex), once APNs gave one.
    @Published private(set) var deviceToken: String?
    /// The daemon this token was last registered with (`host:port`).
    @Published private(set) var registeredWith: String?
    @Published private(set) var registrationError: String?
    /// `GET /api/push/status` of the current daemon.
    @Published private(set) var daemonStatus: PushStatus?
    @Published private(set) var daemonStatusError: String?

    private let defaults = UserDefaults.standard
    private let center = UNUserNotificationCenter.current()
    private var pendingFocus: String?

    private enum Key {
        static let token = "push.deviceToken"
        static let registeredWith = "push.registeredWith"
        static let asked = "push.askedPermission"
    }

    /// Debug builds talk to APNs' sandbox, TestFlight and App Store builds
    /// to production — from the Info.plist key project.yml sets per
    /// configuration, the same split as the `aps-environment` entitlement.
    static let environment = PushEnvironment(infoValue: Bundle.main.object(forInfoDictionaryKey: "SheepdogPushEnvironment") as? String)

    override private init() {
        super.init()
        deviceToken = defaults.string(forKey: Key.token)
        registeredWith = defaults.string(forKey: Key.registeredWith)
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
    /// daemon, and so the first moment notifications mean anything. Asks
    /// once per install.
    func sessionConnected() {
        Task {
            await refreshAuthorization()
            guard authorization == .notDetermined, !defaults.bool(forKey: Key.asked) else { return }
            defaults.set(true, forKey: Key.asked)
            let granted = (try? await center.requestAuthorization(options: [.alert, .sound, .badge])) ?? false
            await refreshAuthorization()
            if granted { UIApplication.shared.registerForRemoteNotifications() }
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
                await unregister(token: token, from: SettingsStore().endpoint)
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
        registrationError = nil
        Task {
            // A new token replaces the old one on the daemon.
            if let previous, previous != hex { await unregister(token: previous, from: SettingsStore().endpoint) }
            await register()
        }
    }

    func didFailToRegister(_ error: Error) {
        registrationError = "APNs refused: \(error.localizedDescription)"
    }

    /// Settings were saved: the token moves from the old daemon to the new.
    func endpointChanged(from old: DaemonEndpoint?) {
        guard let token = deviceToken else { return }
        Task {
            if let old, old.display != SettingsStore().endpoint?.display {
                await unregister(token: token, from: old)
            }
            await register()
        }
    }

    /// `POST /api/push/register` to the configured daemon. Idempotent on
    /// the daemon, so it is sent on every launch with a token.
    func register() async {
        guard let token = deviceToken, let endpoint = SettingsStore().endpoint else { return }
        do {
            let ok = try await DaemonClient(endpoint: endpoint).registerPush(PushRegistration(token: token, environment: Self.environment, device: UIDevice.current.name))
            if ok {
                registeredWith = endpoint.display
                defaults.set(endpoint.display, forKey: Key.registeredWith)
                registrationError = nil
            } else {
                registeredWith = nil
                defaults.removeObject(forKey: Key.registeredWith)
                registrationError = "This daemon has no notification service."
            }
        } catch let error as DaemonError {
            registrationError = error.message
        } catch {
            registrationError = error.localizedDescription
        }
        await loadDaemonStatus()
    }

    private func unregister(token: String, from endpoint: DaemonEndpoint?) async {
        guard let endpoint else { return }
        _ = try? await DaemonClient(endpoint: endpoint).unregisterPush(token: token)
        if registeredWith == endpoint.display {
            registeredWith = nil
            defaults.removeObject(forKey: Key.registeredWith)
        }
    }

    private func forgetToken() {
        deviceToken = nil
        defaults.removeObject(forKey: Key.token)
    }

    func loadDaemonStatus() async {
        guard let endpoint = SettingsStore().endpoint else { return }
        do {
            daemonStatus = try await DaemonClient(endpoint: endpoint).pushStatus()
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
        case .notDetermined: return "Asked after the first connection"
        default: break
        }
        if let error = registrationError { return error }
        guard deviceToken != nil else { return "Waiting for Apple" }
        if let registered = registeredWith, registered == SettingsStore().endpoint?.display { return "Registered" }
        return "Not registered yet"
    }

    // MARK: - UNUserNotificationCenterDelegate

    nonisolated func userNotificationCenter(_ center: UNUserNotificationCenter, willPresent notification: UNNotification) async -> UNNotificationPresentationOptions {
        await MainActor.run {
            // The app shows the card itself while the session is live.
            (model?.sessionIsLive ?? false) ? [] : [.banner, .list, .sound]
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
                focus(paneID: payload.paneID)
            }
            return
        }
        guard let endpoint = SettingsStore().endpoint else {
            await notifyOpenInstead(payload, reason: "Sheepdog has no daemon address yet.")
            return
        }
        let task = UIApplication.shared.beginBackgroundTask(withName: "answer approval")
        defer { UIApplication.shared.endBackgroundTask(task) }
        do {
            _ = try await DaemonClient(endpoint: endpoint).answerPending(id: answer.pendingID, option: answer.option.index)
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
        content.userInfo = ["sheepdog": ["kind": payload.kind, "pane_id": payload.paneID, "agent": payload.agent ?? ""]]
        let request = UNNotificationRequest(identifier: "open-\(payload.pendingID ?? payload.paneID)", content: content, trigger: nil)
        try? await center.add(request)
    }

    /// A tap on a notification: the app on that agent's Agents card.
    func focus(paneID: String) {
        pendingFocus = paneID.isEmpty ? nil : paneID
        applyPendingFocus()
    }

    private func applyPendingFocus() {
        guard let model, let id = pendingFocus else { return }
        pendingFocus = nil
        model.pane = .flock
        model.focusedAgentPaneID = id
    }
}

/// Settings ▸ Models: whether notifications reach this phone.
struct NotificationsSection: View {
    @ObservedObject var push: PushCoordinator

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
            Text("When an agent needs you, the daemon sends a notification even with the app closed; approvals can be answered from it. Not shown while the app is open and connected — the card is already there.")
        }
        .task { await push.loadDaemonStatus() }
    }
}
