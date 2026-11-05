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

/// `POST /api/push/register`'s answer: the phone's end-to-end content key
/// (base64 of 32 bytes; the same key on every registration with the same
/// daemon), which the Notification Service Extension decrypts relay
/// notifications with. A daemon from before the relay answers no key.
public struct PushRegistrationAnswer: Codable, Equatable, Sendable {
    public var contentKey: String?
    /// `aes-256-gcm`.
    public var encryption: String?

    public init(contentKey: String? = nil, encryption: String? = nil) {
        self.contentKey = contentKey
        self.encryption = encryption
    }

    enum CodingKeys: String, CodingKey {
        case contentKey = "content_key"
        case encryption
    }
}

/// How the daemon delivers notifications (`GET /api/push/status` `mode`).
public enum PushMode: String, Codable, Equatable, Sendable {
    /// Through the shared push relay, end-to-end encrypted (the default).
    case relay
    /// Straight to APNs with the user's own push key (a self-built app).
    case apns
    case off
}

/// `GET /api/push/status`.
public struct PushStatus: Codable, Equatable, Sendable {
    public var enabled: Bool
    /// Why sending is off, when it is.
    public var reason: String?
    public var devices: Int
    /// Nil from a daemon that predates the relay (it could only send
    /// directly).
    public var mode: PushMode?

    public init(enabled: Bool, reason: String? = nil, devices: Int = 0, mode: PushMode? = nil) {
        self.enabled = enabled
        self.reason = reason
        self.devices = devices
        self.mode = mode
    }

    public init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        enabled = try c.decodeIfPresent(Bool.self, forKey: .enabled) ?? false
        reason = try c.decodeIfPresent(String.self, forKey: .reason)
        devices = try c.decodeIfPresent(Int.self, forKey: .devices) ?? 0
        mode = (try? c.decodeIfPresent(String.self, forKey: .mode)).flatMap { $0.flatMap(PushMode.init(rawValue:)) }
    }

    /// Sending works: enabled, and not in mode `off`.
    public var canSend: Bool { enabled && mode != .off }

    /// One line for Settings: "On · shared relay · 1 device" / "Off — push.apns not configured".
    public var summary: String {
        if canSend {
            let via: String
            switch mode {
            case .relay: via = "shared relay · "
            case .apns: via = "your push key · "
            default: via = ""
            }
            return "On · \(via)\(devices) \(devices == 1 ? "device" : "devices")"
        }
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
    /// The daemon id of the computer that sent it (`sheepdog.computer`).
    public var computer: String?
    public var options: [PendingOption]
    /// `APPROVAL` or `AGENT`.
    public var category: String?
    public var title: String?
    public var body: String?

    public init(kind: String, paneID: String, agent: String? = nil, pendingID: String? = nil, eventID: String? = nil,
                computer: String? = nil, options: [PendingOption] = [], category: String? = nil, title: String? = nil, body: String? = nil) {
        self.kind = kind
        self.paneID = paneID
        self.agent = agent
        self.pendingID = pendingID
        self.eventID = eventID
        self.computer = computer
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
        computer = string("computer")
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

    /// `userInfo` for the replacement follow-up notification posted when a
    /// background action fails or cannot answer, preserving `computer` so
    /// tapping it still opens the right computer's card.
    public func openInsteadUserInfo() -> [String: Any] {
        var sd: [String: Any] = [
            "kind": kind,
            "pane_id": paneID,
            "agent": agent ?? "",
        ]
        if let computer = computer?.trimmed, !computer.isEmpty {
            sd["computer"] = computer
        }
        return ["sheepdog": sd]
    }
}

/// Which agent card a notification tap asks the app to reveal: the pane id,
/// plus the computer id when the payload named one (`sheepdog.computer`).
public struct AgentFocus: Equatable, Sendable {
    public var computerID: String?
    public var paneID: String

    public init(computerID: String? = nil, paneID: String) {
        let trimmedComputer = computerID?.trimmed
        self.computerID = (trimmedComputer?.isEmpty == false) ? trimmedComputer : nil
        self.paneID = paneID.trimmed
    }

    /// True when this focus targets a secondary computer rather than `currentComputerID`.
    public func isOtherComputer(currentComputerID: String?) -> Bool {
        guard let computerID else { return false }
        return computerID != currentComputerID
    }

    /// The card expansion and scroll anchor key used in multi-computer Agents views.
    public func cardKey(currentComputerID: String?) -> String {
        if let computerID, isOtherComputer(currentComputerID: currentComputerID) {
            return "\(computerID)|\(paneID)"
        }
        return paneID
    }

    /// True when the focused agent card is present in the rendered agent list
    /// for its target computer, so `ScrollViewReader` can scroll to it before
    /// clearing `focusedAgent`.
    public func isTargetPresent(
        currentComputerID: String?,
        sectioned: Bool,
        currentAgents: [Agent],
        otherComputerAgents: (String) -> [Agent]?
    ) -> Bool {
        if sectioned, isOtherComputer(currentComputerID: currentComputerID), let cid = computerID {
            guard let remote = otherComputerAgents(cid) else { return false }
            return Flock(remote).running.contains { $0.paneID == paneID }
        }
        return currentAgents.contains { $0.paneID == paneID }
    }
}

/// Which computer an Approve / Decline notification action may answer on.
/// Never guesses another computer or falls back to the current endpoint when
/// the computer that asked was removed or cannot be identified.
public enum PushActionTarget: Equatable, Sendable {
    case paired(computerID: String, endpoints: [DaemonEndpoint])
    case bare(endpoint: DaemonEndpoint)
    case refused(reason: String)

    public static func resolve(
        payload: PushPayload,
        store: ComputerStore,
        bareEndpoint: DaemonEndpoint?,
        interface: NetworkInterface? = nil
    ) -> PushActionTarget {
        if let rawID = payload.computer?.trimmed, !rawID.isEmpty {
            guard store.computer(rawID) != nil else {
                return .refused(reason: "That computer is no longer paired. Your answer was not sent.")
            }
            let ordered = store.candidateEndpoints(for: rawID, interface: interface)
            guard !ordered.isEmpty else {
                return .refused(reason: "Sheepdog has no address for that computer. Open Sheepdog to check its connection.")
            }
            return .paired(computerID: rawID, endpoints: ordered)
        }
        if store.count > 1 {
            return .refused(reason: "Open Sheepdog to answer — more than one computer is paired.")
        }
        if store.count == 1, let onlyID = store.ids.first {
            let ordered = store.candidateEndpoints(for: onlyID, interface: interface)
            guard !ordered.isEmpty else {
                return .refused(reason: "Sheepdog has no daemon address yet.")
            }
            return .paired(computerID: onlyID, endpoints: ordered)
        }
        if let bareEndpoint {
            return .bare(endpoint: bareEndpoint)
        }
        return .refused(reason: "Sheepdog has no daemon address yet.")
    }
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

    /// Answers a pending prompt from a cold background notification wake:
    /// when a computer has multiple stored addresses (e.g. LAN + Tailscale),
    /// stagger-races `GET /api/hello` across them so a blackholed LAN IP
    /// does not stall for a full request timeout, then sends
    /// `POST /api/pending/answer` once on the winning endpoint.
    @discardableResult
    public static func answerPending(
        id: String,
        option: Int,
        endpoints: [DaemonEndpoint],
        stagger: TimeInterval = AddressRace.stagger,
        makeClient: @escaping @Sendable (DaemonEndpoint) -> DaemonClient = { DaemonClient(endpoint: $0) }
    ) async throws -> DaemonEndpoint {
        guard let first = endpoints.first else { throw DaemonError.pairing(.notPaired) }
        if endpoints.count == 1 {
            let client = makeClient(first)
            defer { client.invalidate() }
            _ = try await client.answerPending(id: id, option: option)
            return first
        }
        enum ProbeOutcome: Sendable {
            case ok(Int)
            case failed(Int, Error)
        }
        var winnerIndex: Int?
        var preferredError: Error?
        var fallbackError: Error?
        await withTaskGroup(of: ProbeOutcome.self) { group in
            for (index, endpoint) in endpoints.enumerated() {
                group.addTask {
                    if index > 0, stagger > 0 {
                        try? await Task.sleep(nanoseconds: UInt64(stagger * Double(index) * 1_000_000_000))
                        if Task.isCancelled { return .failed(index, CancellationError()) }
                    }
                    let client = makeClient(endpoint)
                    defer { client.invalidate() }
                    do {
                        _ = try await client.hello()
                        return .ok(index)
                    } catch {
                        return .failed(index, error)
                    }
                }
            }
            for await outcome in group {
                switch outcome {
                case let .ok(index):
                    winnerIndex = index
                    group.cancelAll()
                case let .failed(index, error):
                    if !(error is CancellationError) {
                        if index == 0 {
                            preferredError = error
                        } else if fallbackError == nil {
                            fallbackError = error
                        }
                    }
                }
                if winnerIndex != nil { break }
            }
        }
        guard let winnerIndex else {
            throw preferredError ?? fallbackError ?? DaemonError.pairing(.notPaired)
        }
        let chosen = endpoints[winnerIndex]
        let client = makeClient(chosen)
        defer { client.invalidate() }
        _ = try await client.answerPending(id: id, option: option)
        return chosen
    }
}

extension DaemonClient {
    /// `POST /api/push/register`. Returns false when the daemon has no
    /// push service (503) or predates it (404); throws for a refused token
    /// and any other refusal.
    @discardableResult
    public func registerPush(_ registration: PushRegistration) async throws -> Bool {
        try await registerPushDevice(registration) != nil
    }

    /// `POST /api/push/register` with its answer (the content key); nil
    /// when the daemon has no push service (503) or predates it (404).
    public func registerPushDevice(_ registration: PushRegistration) async throws -> PushRegistrationAnswer? {
        let request = try pushRegisterRequest(registration)
        let (data, response) = try await perform(request)
        guard let http = response as? HTTPURLResponse else { throw DaemonError.malformed("no HTTP response") }
        switch http.statusCode {
        case 200 ..< 300:
            return (try? JSONDecoder().decode(PushRegistrationAnswer.self, from: data)) ?? PushRegistrationAnswer()
        case 404, 503: return nil
        case 401: throw DaemonError.unauthorized(endpoint: endpoint.display)
        default:
            let object = (try? JSONSerialization.jsonObject(with: data)) as? [String: Any]
            throw DaemonError.rejected(status: http.statusCode, message: (object?["error"] as? String) ?? "")
        }
    }

    /// `DELETE /api/push/register {"token": …}`.
    @discardableResult
    public func unregisterPush(token: String) async throws -> Bool {
        let body = try JSONSerialization.data(withJSONObject: ["token": token])
        let request = try request(path: "/api/push/register", method: "DELETE", body: body)
        return try await sendPush(request)
    }

    /// `DELETE /api/devices/self`: this computer forgets this phone (Remove).
    /// Best effort; the reply's status is not checked.
    public func unpairDevice() async throws {
        let request = try request(path: "/api/devices/self", method: "DELETE")
        _ = try await perform(request)
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

    /// `POST /api/push/activity`: the voice session's Live Activity push
    /// token, so the daemon can keep the lock-screen banner current while
    /// the app is away. False when the daemon has no push service.
    @discardableResult
    public func registerLiveActivity(token: String, environment: PushEnvironment) async throws -> Bool {
        let encoder = JSONEncoder()
        encoder.outputFormatting = .sortedKeys
        let body = try encoder.encode(["token": token, "environment": environment.rawValue])
        return try await sendPush(try request(path: "/api/push/activity", method: "POST", body: body))
    }

    /// `DELETE /api/push/activity`: the activity ended.
    @discardableResult
    public func endLiveActivity() async throws -> Bool {
        try await sendPush(try request(path: "/api/push/activity", method: "DELETE"))
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

/// What the app may promise about notifications. The App Store build gets
/// closed-app notifications through the shared push relay (the daemon's
/// default, end-to-end encrypted); a self-built app can instead use the
/// user's own push key straight to APNs. The app asks for permission only
/// when the daemon says it can send, and says plainly what works without
/// push.
public enum PushPolicy {
    /// Ask iOS for notification permission only when the daemon can send
    /// (mode `relay` or `apns`).
    public static func shouldAskPermission(daemon: PushStatus?) -> Bool {
        daemon?.canSend == true
    }

    /// The footer under Settings ▸ Notifications.
    public static func explanation(daemon: PushStatus?) -> String {
        let always = "While the app is open and connected, approvals appear as cards and the voice tells you; during a voice session the Live Activity shows it on the Lock Screen and in the Dynamic Island."
        let options = "Two ways to get notifications with the app closed: the shared relay (the daemon's default, no account needed) carries them end-to-end encrypted, so it sees this phone's push token, when a notification is sent, its size and whether it is an approval, never what it says; or build the app yourself and give your daemon your own push key, so it talks to Apple directly."
        guard let daemon, daemon.canSend else {
            return always + " Your daemon is not sending notifications now. " + options
        }
        let lead: String
        switch daemon.mode {
        case .relay:
            lead = "Your daemon sends a notification when an agent needs you, even with the app closed, through the shared relay, end-to-end encrypted: the relay sees this phone's push token, when a notification is sent, its size and whether it is an approval, never what it says. Approvals can be answered from the notification."
        case .apns:
            lead = "Your daemon sends a notification when an agent needs you, even with the app closed, straight to Apple with your own push key. Approvals can be answered from the notification."
        default:
            lead = "Your daemon sends a notification when an agent needs you, even with the app closed; approvals can be answered from it."
        }
        return lead + " " + always
    }

    /// Whether a notification arriving while the app is in the foreground
    /// should suppress its system banner/sound because the screen or voice
    /// already shows it live.
    public static func shouldSuppressForegroundBanner(
        payload: PushPayload?,
        currentComputerID: String?,
        currentLinkUsable: Bool,
        currentEventsOpen: Bool,
        voiceConnected: Bool,
        viewingAgents: Bool,
        otherComputerEventsOpen: Bool,
        viewingVoiceScreen: Bool = true,
        demoActive: Bool = false
    ) -> Bool {
        guard let payload, !demoActive else { return false }
        let approvalSurfaceVisible = viewingVoiceScreen || viewingAgents
        let isOther = payload.computer != nil && currentComputerID != nil && payload.computer != currentComputerID
        if isOther {
            guard otherComputerEventsOpen else { return false }
            return (payload.isAnswerable && approvalSurfaceVisible) || viewingAgents
        }
        guard currentLinkUsable && (currentEventsOpen || voiceConnected) else { return false }
        return (payload.isAnswerable && approvalSurfaceVisible) || voiceConnected || viewingAgents
    }
}

/// Tracks which computers (or bare fixture endpoints) have accepted this
/// phone's APNs device token, keyed by `computer:<daemonID>` for paired
/// computers so failing over between addresses or switching computers never
/// invalidates registration status.
public struct PushRegistrationState: Equatable, Sendable {
    public private(set) var registeredTargets: Set<String> = []
    public private(set) var errors: [String: String] = [:]

    public init(registeredTargets: Set<String> = [], errors: [String: String] = [:]) {
        self.registeredTargets = registeredTargets
        self.errors = errors
    }

    public static func targetKey(computerID: String?, endpointDisplay: String) -> String {
        if let id = computerID?.trimmed, !id.isEmpty {
            return "computer:\(id)"
        }
        return "endpoint:\(endpointDisplay)"
    }

    public mutating func recordSuccess(computerID: String?, endpointDisplay: String) {
        let key = Self.targetKey(computerID: computerID, endpointDisplay: endpointDisplay)
        registeredTargets.insert(key)
        errors.removeValue(forKey: key)
    }

    public mutating func recordNoService(computerID: String?, endpointDisplay: String) {
        let key = Self.targetKey(computerID: computerID, endpointDisplay: endpointDisplay)
        registeredTargets.remove(key)
        errors[key] = "This daemon has no notification service."
    }

    public mutating func recordError(_ message: String, computerID: String?, endpointDisplay: String) {
        let key = Self.targetKey(computerID: computerID, endpointDisplay: endpointDisplay)
        errors[key] = message
    }

    public mutating func unregister(computerID: String) {
        let key = Self.targetKey(computerID: computerID, endpointDisplay: "")
        registeredTargets.remove(key)
        errors.removeValue(forKey: key)
    }

    public mutating func reset() {
        registeredTargets.removeAll()
        errors.removeAll()
    }

    public func isRegistered(computerID: String?, endpointDisplay: String) -> Bool {
        registeredTargets.contains(Self.targetKey(computerID: computerID, endpointDisplay: endpointDisplay))
    }

    public func error(computerID: String?, endpointDisplay: String) -> String? {
        errors[Self.targetKey(computerID: computerID, endpointDisplay: endpointDisplay)]
    }
}
