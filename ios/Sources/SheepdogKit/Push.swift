import Foundation

// Phone notifications (APNs). The daemon side is herdr-sheepdog's
// `docs/push.md`: the app registers its device token with
// `POST /api/push/register`, and the daemon sends an alert when an agent
// needs the user, with the approval's options in the payload so the
// notification's Approve / Decline can answer it through the same
// `POST /api/pending/answer` the approval card uses.

/// Which APNs environment issued the device token: `sandbox` for a
/// development-signed (Xcode debug) build, `production` for TestFlight and
/// the App Store. Decided at build time (the app's
/// `SheepdogPushEnvironment` Info.plist key, set per configuration in
/// project.yml), never guessed at run time.
public enum PushEnvironment: String, Codable, Equatable, Sendable {
    case production
    case sandbox

    /// From the Info.plist value; anything unexpected is `production`,
    /// the environment every distributed build uses.
    public init(infoValue: String?) {
        self = infoValue?.lowercased() == "sandbox" ? .sandbox : .production
    }
}

public enum PushToken {
    /// The device token as the daemon stores it: lowercase hex, two digits
    /// a byte (`Data.description` is not that, and changed across iOS
    /// versions).
    public static func hex(_ token: Data) -> String {
        token.map { String(format: "%02x", $0) }.joined()
    }
}

/// `POST /api/push/register`'s body.
public struct PushRegistration: Codable, Equatable, Sendable {
    public var token: String
    public var environment: PushEnvironment
    public var device: String?

    public init(token: String, environment: PushEnvironment, device: String? = nil) {
        self.token = token
        self.environment = environment
        self.device = device
    }
}

/// `GET /api/push/status`.
public struct PushStatus: Codable, Equatable, Sendable {
    public var enabled: Bool
    /// Why sending is off, when it is.
    public var reason: String?
    public var devices: Int

    public init(enabled: Bool, reason: String? = nil, devices: Int = 0) {
        self.enabled = enabled
        self.reason = reason
        self.devices = devices
    }

    public init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        enabled = try c.decodeIfPresent(Bool.self, forKey: .enabled) ?? false
        reason = try c.decodeIfPresent(String.self, forKey: .reason)
        devices = try c.decodeIfPresent(Int.self, forKey: .devices) ?? 0
    }

    /// One line for Settings: "On · 1 device" / "Off — push.apns not configured".
    public var summary: String {
        if enabled { return "On · \(devices) \(devices == 1 ? "device" : "devices")" }
        if let reason = reason?.trimmed, !reason.isEmpty { return "Off — \(reason)" }
        return "Off"
    }
}

/// What a notification from the daemon carries (`aps` and `sheepdog`).
public struct PushPayload: Equatable, Sendable {
    /// `approval`, `blocked`, `done`, `idle`, …
    public var kind: String
    public var paneID: String
    public var agent: String?
    /// The open answer-prompt action, for an approval.
    public var pendingID: String?
    public var eventID: String?
    public var options: [PendingOption]
    /// `APPROVAL` or `AGENT`.
    public var category: String?
    public var title: String?
    public var body: String?

    public init(kind: String, paneID: String, agent: String? = nil, pendingID: String? = nil, eventID: String? = nil,
                options: [PendingOption] = [], category: String? = nil, title: String? = nil, body: String? = nil) {
        self.kind = kind
        self.paneID = paneID
        self.agent = agent
        self.pendingID = pendingID
        self.eventID = eventID
        self.options = options
        self.category = category
        self.title = title
        self.body = body
    }

    /// From `UNNotificationContent.userInfo`. Nil without a `sheepdog`
    /// dictionary (not one of the daemon's).
    public init?(userInfo: [AnyHashable: Any]) {
        guard let sd = userInfo["sheepdog"] as? [String: Any] else { return nil }
        func string(_ key: String) -> String? {
            guard let s = (sd[key] as? String)?.trimmed, !s.isEmpty else { return nil }
            return s
        }
        kind = string("kind") ?? ""
        paneID = string("pane_id") ?? ""
        agent = string("agent")
        pendingID = string("pending_id")
        eventID = string("event_id")
        let raw = sd["options"] as? [[String: Any]] ?? []
        options = PendingOption.normalised(raw.compactMap { o -> PendingOption? in
            guard let label = o["label"] as? String else { return nil }
            let index = (o["index"] as? Int) ?? (o["index"] as? NSNumber)?.intValue ?? 0
            return PendingOption(index: index, label: label)
        }) ?? []
        let aps = userInfo["aps"] as? [String: Any]
        category = aps?["category"] as? String
        if let alert = aps?["alert"] as? [String: Any] {
            title = alert["title"] as? String
            body = alert["body"] as? String
        } else {
            body = aps?["alert"] as? String
        }
    }

    /// An approval this phone can answer from the notification.
    public var isAnswerable: Bool { pendingID != nil && !options.isEmpty }
}

/// The notification categories and actions the app registers, and what
/// tapping an action answers. The actions are generic ("Approve",
/// "Decline") because a category's actions are fixed at registration; the
/// option each stands for is picked from the payload when it is tapped.
public enum PushActions {
    public static let approvalCategory = "APPROVAL"
    public static let agentCategory = "AGENT"
    public static let approve = "APPROVE"
    public static let decline = "DECLINE"

    /// Approve: the first option whose label starts with "Yes", else
    /// option 1 — never a "No…" option.
    public static func approveOption(_ options: [PendingOption]) -> PendingOption? {
        if let yes = options.first(where: { $0.label.trimmed.lowercased().hasPrefix("yes") }) { return yes }
        if let first = options.first(where: { $0.index == 1 }) ?? options.first, !first.isNegative { return first }
        return nil
    }

    /// Decline: the first "No…" option, or nil when the prompt has none.
    public static func declineOption(_ options: [PendingOption]) -> PendingOption? {
        options.first { $0.label.trimmed.lowercased().hasPrefix("no") && $0.isNegative }
    }

    /// What an action on a notification asks the daemon: the pending id
    /// and the option index for `POST /api/pending/answer`. Nil for the
    /// default tap, an unknown action or a payload with nothing to answer
    /// — the app opens instead.
    public static func answer(actionIdentifier: String, payload: PushPayload) -> (pendingID: String, option: PendingOption)? {
        guard let pendingID = payload.pendingID, !payload.options.isEmpty else { return nil }
        let option: PendingOption?
        switch actionIdentifier {
        case approve: option = approveOption(payload.options)
        case decline: option = declineOption(payload.options)
        default: option = nil
        }
        return option.map { (pendingID, $0) }
    }
}

extension DaemonClient {
    /// `POST /api/push/register`. Returns false when the daemon has no
    /// push service (503) or predates it (404); throws for a refused token
    /// and any other refusal.
    @discardableResult
    public func registerPush(_ registration: PushRegistration) async throws -> Bool {
        let request = try pushRegisterRequest(registration)
        return try await sendPush(request)
    }

    /// `DELETE /api/push/register {"token": …}`.
    @discardableResult
    public func unregisterPush(token: String) async throws -> Bool {
        let body = try JSONSerialization.data(withJSONObject: ["token": token])
        let request = try request(path: "/api/push/register", method: "DELETE", body: body)
        return try await sendPush(request)
    }

    /// `GET /api/push/status`; nil on a daemon without the route.
    public func pushStatus() async throws -> PushStatus? {
        let request = try request(path: "/api/push/status")
        let (data, response) = try await perform(request)
        guard let http = response as? HTTPURLResponse else { throw DaemonError.malformed("no HTTP response") }
        switch http.statusCode {
        case 200 ..< 300:
            guard let status = try? DaemonClient.decoder.decode(PushStatus.self, from: data) else {
                throw DaemonError.malformed("the push status did not decode")
            }
            return status
        case 404: return nil
        case 401: throw DaemonError.unauthorized(endpoint: endpoint.display)
        default: throw DaemonError.http(status: http.statusCode, endpoint: endpoint.display)
        }
    }

    public func pushRegisterRequest(_ registration: PushRegistration) throws -> URLRequest {
        let encoder = JSONEncoder()
        encoder.outputFormatting = .sortedKeys
        return try request(path: "/api/push/register", method: "POST", body: try encoder.encode(registration))
    }

    private func sendPush(_ request: URLRequest) async throws -> Bool {
        let (data, response) = try await perform(request)
        guard let http = response as? HTTPURLResponse else { throw DaemonError.malformed("no HTTP response") }
        switch http.statusCode {
        case 200 ..< 300: return true
        case 404, 503: return false
        case 401: throw DaemonError.unauthorized(endpoint: endpoint.display)
        default:
            let object = (try? JSONSerialization.jsonObject(with: data)) as? [String: Any]
            throw DaemonError.rejected(status: http.statusCode, message: (object?["error"] as? String) ?? "")
        }
    }
}

/// What the app may promise about notifications. Apple delivers a push
/// only when the daemon signs it with an APNs key from the Apple team that
/// signed this app, so a self-hosted daemon cannot reach the TestFlight or
/// App Store build. The app asks for permission only when the daemon says it
/// can send, and says plainly what works without push.
public enum PushPolicy {
    /// Ask iOS for notification permission only when the daemon can send.
    public static func shouldAskPermission(daemon: PushStatus?) -> Bool {
        daemon?.enabled == true
    }

    /// The footer under Settings ▸ Notifications.
    public static func explanation(daemon: PushStatus?) -> String {
        let always = "While the app is open and connected, approvals appear as cards and the voice tells you; during a voice session the Live Activity shows it on the Lock Screen and in the Dynamic Island."
        if daemon?.enabled == true {
            return "Your daemon sends a notification when an agent needs you, even with the app closed; approvals can be answered from it. " + always
        }
        return always + " Notifications with the app closed need your daemon to send them through Apple's push service with a push key from the Apple team that signed this app. A daemon you run yourself cannot send them to this build; build the app under your own Apple team and configure your own key (see the daemon's push docs) to get them."
    }
}
