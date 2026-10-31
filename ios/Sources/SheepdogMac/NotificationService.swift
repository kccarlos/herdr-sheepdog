import Foundation
import SheepdogKit
import UserNotifications

/// What the daemon status monitor hands the Mac's notification service.
@MainActor
protocol NotificationServing: AnyObject {
    func receive(_ event: DaemonEvent, voice: VoiceMenuState)
}

/// One local notification, classified from an event by the same words the
/// phone uses for its conversation and local audio notices.
struct NotificationNotice: Equatable {
    var eventID: UInt64
    var paneID: String?
    var title: String
    var body: String

    init?(event: DaemonEvent) {
        switch event.type {
        case "pending.created":
            guard let pending = PendingAction(event: event), pending.isAnswerPrompt else { return nil }
            eventID = event.seq
            paneID = pending.paneID.nilIfEmpty
            title = pending.agent?.nilIfEmpty ?? "Sheepdog"
            body = pending.displaySummary
        case "herdr.transition":
            guard event.string("from").flatMap(Agent.State.init(rawValue:)) == .working,
                  let to = event.string("to").flatMap(Agent.State.init(rawValue:)),
                  to == .done || to == .idle
            else { return nil }
            let agent = event.string("name")?.nilIfEmpty
                ?? event.string("alias")?.nilIfEmpty
                ?? event.string("agent")?.nilIfEmpty
                ?? "Sheepdog"
            eventID = event.seq
            paneID = event.paneID
            title = agent
            body = "\(agent) finished."
        case "voice.closed":
            let reason = event.string("reason")?.nilIfEmpty ?? "no reason given"
            guard reason != "shutdown", reason != "replaced", reason != "voice_off" else { return nil }
            eventID = event.seq
            paneID = nil
            title = "Sheepdog"
            body = LocalNotice.voiceClosed(reason: reason).text
        case "voice.connect_error", "voice.session_error", "daemon.voice_error", "daemon.voice_provider_error":
            let reason = event.string("message")?.nilIfEmpty
                ?? event.string("error")?.nilIfEmpty
                ?? event.string("reason")?.nilIfEmpty
                ?? event.type
            eventID = event.seq
            paneID = nil
            title = "Sheepdog"
            body = LocalNotice.voiceClosed(reason: reason).text
        default:
            return nil
        }
    }
}

/// The one rule that stands in for "the herdr-app panel is visible" until
/// the panel reports its visibility: while this Mac holds the voice and its
/// existing speech detector hears the user, the panel is close enough that
/// a notification would interrupt what the user is already seeing.
enum NotificationPolicy {
    static func suppresses(voice: VoiceMenuState) -> Bool {
        voice.heldHere && voice.userTalking
    }
}

/// The small part of `UNUserNotificationCenter` this service uses, so the
/// permission and delivery path runs against a fake in tests.
@MainActor
protocol NotificationCenterClient: AnyObject {
    func authorizationStatus() async -> UNAuthorizationStatus
    func requestAuthorization() async -> Bool
    func add(_ request: UNNotificationRequest) async throws
}

@MainActor
final class UserNotificationCenterClient: NotificationCenterClient {
    private let center: UNUserNotificationCenter

    init(center: UNUserNotificationCenter = .current()) {
        self.center = center
    }

    func authorizationStatus() async -> UNAuthorizationStatus {
        await center.notificationSettings().authorizationStatus
    }

    func requestAuthorization() async -> Bool {
        (try? await center.requestAuthorization(options: [.alert, .sound])) ?? false
    }

    func add(_ request: UNNotificationRequest) async throws {
        try await center.add(request)
    }
}

/// Local notifications from the daemon's `/events` stream. Permission is
/// asked only when the first event has something to say. A click opens the
/// herdr-app panel through the same `PanelOpening` the menu uses.
@MainActor
final class NotificationService: NSObject, NotificationServing, UNUserNotificationCenterDelegate {
    private let center: NotificationCenterClient
    private let panel: PanelOpening
    private let log: AppLogging
    private var voice = VoiceMenuState()
    private var allowed: Bool?
    private var permissionRequest: Task<Bool, Never>?

    init(
        panel: PanelOpening,
        log: AppLogging,
        center: NotificationCenterClient? = nil,
        systemCenter: UNUserNotificationCenter? = nil
    ) {
        self.panel = panel
        self.log = log
        self.center = center ?? UserNotificationCenterClient()
        super.init()
        (systemCenter ?? .current()).delegate = self
    }

    func receive(_ event: DaemonEvent, voice: VoiceMenuState) {
        self.voice = voice
        guard !NotificationPolicy.suppresses(voice: voice),
              let notice = NotificationNotice(event: event)
        else { return }
        Task { [weak self] in
            await self?.deliver(notice)
        }
    }

    private func deliver(_ notice: NotificationNotice) async {
        guard await mayDeliver() else { return }
        let content = UNMutableNotificationContent()
        content.title = notice.title
        content.body = notice.body
        content.sound = .default
        if let paneID = notice.paneID {
            content.threadIdentifier = paneID
            content.userInfo = ["sheepdog": ["pane_id": paneID]]
        }
        let request = UNNotificationRequest(identifier: "event-\(notice.eventID)", content: content, trigger: nil)
        do {
            try await center.add(request)
        } catch {
            log.log("could not show a notification: \(error.localizedDescription)")
        }
    }

    private func mayDeliver() async -> Bool {
        if let allowed { return allowed }
        if let permissionRequest { return await permissionRequest.value }
        let request = Task { [center] in
            switch await center.authorizationStatus() {
            case .authorized, .provisional, .ephemeral:
                return true
            case .denied:
                return false
            case .notDetermined:
                return await center.requestAuthorization()
            @unknown default:
                return false
            }
        }
        permissionRequest = request
        let answer = await request.value
        permissionRequest = nil
        allowed = answer
        return answer
    }

    nonisolated func userNotificationCenter(
        _ center: UNUserNotificationCenter,
        willPresent notification: UNNotification
    ) async -> UNNotificationPresentationOptions {
        let suppress = await MainActor.run {
            NotificationPolicy.suppresses(voice: voice)
        }
        return suppress ? [] : [.banner, .sound]
    }

    nonisolated func userNotificationCenter(
        _ center: UNUserNotificationCenter,
        didReceive response: UNNotificationResponse
    ) async {
        guard response.actionIdentifier == UNNotificationDefaultActionIdentifier else { return }
        await MainActor.run { openPanelFromNotification() }
    }

    func openPanelFromNotification() {
        do {
            try panel.openPanel()
        } catch {
            log.log("could not open the panel from a notification: \(error.localizedDescription)")
        }
    }
}

private extension String {
    var nilIfEmpty: String? {
        let trimmed = trimmingCharacters(in: .whitespacesAndNewlines)
        return trimmed.isEmpty ? nil : trimmed
    }
}
