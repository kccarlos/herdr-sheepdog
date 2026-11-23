import AppKit
import Foundation
import Security
import SheepdogKit

/// One-time short code returned by `POST /api/pair/browser-code`.
public struct BrowserPairingCode: Equatable, Sendable, Decodable {
    public var code: String; public var expires: Date; public var expiresIn: Int

    public init(code: String, expires: Date, expiresIn: Int) {
        self.code = code; self.expires = expires; self.expiresIn = expiresIn
    }

    enum CodingKeys: String, CodingKey { case code, expires, expiresIn = "expires_in" }

    public init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        let code = try c.decode(String.self, forKey: .code)
        guard !code.isEmpty, let expires = RFC3339Date.date(from: try c.decode(String.self, forKey: .expires)) else {
            throw DecodingError.dataCorruptedError(forKey: .expires, in: c, debugDescription: "invalid browser pairing code")
        }
        self.code = code; self.expires = expires
        self.expiresIn = try c.decodeIfPresent(Int.self, forKey: .expiresIn) ?? max(0, Int(expires.timeIntervalSinceNow.rounded()))
    }
}

/// Failures when asking the local daemon for a browser pairing code.
public enum BrowserPairingError: Error, Equatable, Sendable {
    case daemonDown, notEnrolled, rateLimited, serverError(status: Int, message: String)

    public var title: String {
        switch self {
        case .daemonDown: return "Sheepdog isn't running"
        case .notEnrolled: return "This Mac app isn't paired with Sheepdog"
        case .rateLimited: return "Too many pairing attempts"
        case .serverError: return "Sheepdog couldn't create a pairing code"
        }
    }

    public var message: String {
        switch self {
        case .daemonDown: return "Start the sheepdog daemon (sheepdog start), then choose Pair browser… again."
        case .notEnrolled: return "Sheepdog couldn't verify this Mac app's session. Restart sheepdog (sheepdog restart), check sheepdog logs, and choose Pair browser… again."
        case .rateLimited: return "Wait 5 minutes, then choose Pair browser… again."
        case let .serverError(status, detail):
            if status > 0 {
                let text = detail.isEmpty ? "HTTP \(status)." : "HTTP \(status): \(detail)."
                return "Sheepdog returned \(text) Check sheepdog logs and choose Pair browser… again."
            }
            let text = detail.isEmpty ? "Couldn't access this Mac's Keychain." : (detail.hasSuffix(".") ? detail : "\(detail).")
            return "\(text) Check Keychain Access permissions for Sheepdog, then choose Pair browser… again."
        }
    }

    public static func map(_ error: Error) -> BrowserPairingError {
        switch error {
        case let err as BrowserPairingError: return err
        case is URLError, DaemonError.unreachable, DaemonError.timedOut: return .daemonDown
        case DaemonError.unauthorized, is PairingProblem: return .notEnrolled
        case let DaemonError.rejected(s, m): return s == 429 ? .rateLimited : s == 401 ? .notEnrolled : .serverError(status: s, message: m)
        case let DaemonError.http(s, host): return s == 429 ? .rateLimited : s == 401 ? .notEnrolled : .serverError(status: s, message: "\(host) answered \(s).")
        case let err as DaemonError: return .serverError(status: 0, message: err.message)
        default: return .serverError(status: 0, message: error.localizedDescription)
        }
    }

    fileprivate static func run<T>(_ work: () async throws -> T) async throws -> T {
        do { return try await work() } catch { throw map(error) }
    }
}

/// What the "Pair browser…" alert displays for a fresh short code.
public struct BrowserPairingPresentation: Equatable, Sendable {
    public var title, code, expiryText, codeAccessibilityLabel: String

    public init(code: BrowserPairingCode, now: Date = Date()) {
        self.title = "Pair a browser"; self.code = code.code
        let remaining = code.expires.timeIntervalSince(now)
        let minutes = max(1, Int(ceil((remaining > 0 ? remaining : Double(code.expiresIn)) / 60)))
        let time = DateFormatter.localizedString(from: code.expires, dateStyle: .none, timeStyle: .short)
        self.expiryText = "Expires in \(minutes) \(minutes == 1 ? "minute" : "minutes") (at \(time)). Single-use."
        let spelled = code.code.split(separator: "-").map { $0.map(String.init).joined(separator: " ") }.joined(separator: ", dash, ")
        self.codeAccessibilityLabel = "Pairing code: \(spelled)"
    }
}

/// Provides a paired-device session ticket for the local daemon.
public protocol BrowserPairingSessionProviding {
    func sessionTicket(base: URL) async throws -> SessionTicket
    func invalidateTicket()
}

/// Injectable seam over `DeviceKey` so tests can supply an ephemeral in-memory `SecKey`.
public protocol DeviceKeyProviding: Sendable {
    var isHardware: Bool { get }
    func load(daemonID: String) -> SecKey?
    func loadOrCreate(daemonID: String) throws -> SecKey
    func publicKey(_ key: SecKey) -> String?
    func delete(daemonID: String)
}

public struct SystemDeviceKeyProvider: DeviceKeyProviding {
    public init() {}
    public var isHardware: Bool { DeviceKey.isHardware }
    public func load(daemonID: String) -> SecKey? { DeviceKey.load(daemonID: daemonID) }
    public func loadOrCreate(daemonID: String) throws -> SecKey { try DeviceKey.loadOrCreate(daemonID: daemonID) }
    public func publicKey(_ key: SecKey) -> String? { DeviceKey.publicKey(key) }
    public func delete(daemonID: String) { DeviceKey.delete(daemonID: daemonID) }
}

/// Shows a short browser-pairing code or a plain-fix error to the user.
@MainActor
public protocol BrowserPairingPresenting: AnyObject {
    func showCode(_ presentation: BrowserPairingPresentation)
    func showError(_ error: BrowserPairingError)
}

/// Requests a one-time browser-pairing short code over an authenticated device session.
public final class BrowserPairingClient {
    public let locator: DaemonLocating; public let sessionProvider: BrowserPairingSessionProviding; public let urlSession: URLSession

    public init(locator: DaemonLocating, sessionProvider: BrowserPairingSessionProviding, urlSession: URLSession? = nil) {
        self.locator = locator; self.sessionProvider = sessionProvider
        self.urlSession = urlSession ?? URLSession(configuration: DeviceCredentials.ephemeral(timeout: 8))
    }

    public func requestCode() async throws -> BrowserPairingCode {
        guard let base = locator.endpoint.url(path: ""), let url = locator.endpoint.url(path: "/api/pair/browser-code") else {
            throw BrowserPairingError.daemonDown
        }
        for attempt in 0 ... 1 {
            let ticket = try await BrowserPairingError.run { try await sessionProvider.sessionTicket(base: base) }
            var req = URLRequest(url: url)
            req.httpMethod = "POST"
            req.setValue(ticket.header, forHTTPHeaderField: "Authorization")
            let (data, response) = try await BrowserPairingError.run { try await urlSession.data(for: req) }
            let status = (response as? HTTPURLResponse)?.statusCode ?? 0
            if status == 200 {
                do { return try JSONDecoder().decode(BrowserPairingCode.self, from: data) }
                catch { throw BrowserPairingError.serverError(status: 200, message: "invalid pairing code response") }
            }
            if status == 401 {
                if attempt == 0 { sessionProvider.invalidateTicket(); continue }
                throw BrowserPairingError.notEnrolled
            }
            let obj = (try? JSONSerialization.jsonObject(with: data)) as? [String: Any] ?? [:]
            if status == 403 && (obj["reason"] as? String) == "device_session_required" { throw BrowserPairingError.notEnrolled }
            if status == 429 { throw BrowserPairingError.rateLimited }
            throw BrowserPairingError.serverError(status: status, message: obj["error"] as? String ?? "")
        }
        throw BrowserPairingError.notEnrolled
    }
}

/// Enrols `Sheepdog.app` on loopback (`kind: "macos"`) using `DeviceKeyProviding`, `SecretStore`, and `DeviceAuth`.
public final class MacDeviceSessionProvider: BrowserPairingSessionProviding {
    private static let refreshMargin: TimeInterval = 30 * 60
    private let secrets: SecretStore; private let keys: DeviceKeyProviding; private let urlSession: URLSession
    private let deviceName: @Sendable () -> String
    private let lock = NSLock(); private var cached: (base: URL, ticket: SessionTicket)?

    public init(
        secrets: SecretStore = KeychainSecretStore(service: "com.github.kccarlos.herdr-sheepdog-mac.daemon", accessibleAfterFirstUnlock: true),
        keys: DeviceKeyProviding = SystemDeviceKeyProvider(), urlSession: URLSession? = nil,
        deviceName: @escaping @Sendable () -> String = { Host.current().localizedName ?? "Mac" }
    ) {
        self.secrets = secrets; self.keys = keys; self.deviceName = deviceName
        self.urlSession = urlSession ?? URLSession(configuration: DeviceCredentials.ephemeral(timeout: 8))
    }

    public func invalidateTicket() { lock.withLock { cached = nil } }

    public func sessionTicket(base: URL) async throws -> SessionTicket {
        if let hit = lock.withLock({ cached }), hit.base == base, hit.ticket.expires.timeIntervalSinceNow > Self.refreshMargin {
            return hit.ticket
        }
        let hello = try await fetchJSON(base.appendingPathComponent("api/hello"))
        guard let daemonID = (hello["computer"] as? [String: Any])?["id"] as? String, !daemonID.isEmpty,
              let pin = (hello["security"] as? [String: Any])?["pin"] as? String, !pin.isEmpty
        else { throw BrowserPairingError.notEnrolled }
        let auth = DeviceAuth(base: base, session: urlSession), account = "mac.device.\(daemonID)"
        if let deviceID = secrets.secret(for: account), let key = keys.load(daemonID: daemonID) {
            do { return cache(try await auth.signIn(daemonID: daemonID, pin: pin, deviceID: deviceID, key: key), for: base) }
            catch PairingProblem.revoked, PairingProblem.unknownDevice { secrets.setSecret(nil, for: account); invalidateTicket() }
            catch { throw BrowserPairingError.map(error) }
        }
        let (deviceID, key) = try await enroll(daemonID: daemonID, auth: auth)
        secrets.setSecret(deviceID, for: account)
        return cache(try await BrowserPairingError.run { try await auth.signIn(daemonID: daemonID, pin: pin, deviceID: deviceID, key: key) }, for: base)
    }

    private func enroll(daemonID: String, auth: DeviceAuth) async throws -> (String, SecKey) {
        let createdNew = keys.load(daemonID: daemonID) == nil
        let key = try await BrowserPairingError.run { try keys.loadOrCreate(daemonID: daemonID) }
        guard let pub = keys.publicKey(key) else {
            if createdNew { keys.delete(daemonID: daemonID) }
            throw BrowserPairingError.serverError(status: 0, message: "Couldn't read this Mac's public key.")
        }
        do { return (try await auth.enroll(code: "", publicKey: pub, name: deviceName(), kind: "macos", hardware: keys.isHardware).deviceID, key) }
        catch {
            if createdNew && PairingEnrollment.isDefiniteNonEnrollment(error) { keys.delete(daemonID: daemonID) }
            throw BrowserPairingError.map(error)
        }
    }

    private func cache(_ ticket: SessionTicket, for base: URL) -> SessionTicket {
        lock.withLock { cached = (base, ticket) }; return ticket
    }

    private func fetchJSON(_ url: URL) async throws -> [String: Any] {
        let (data, response) = try await BrowserPairingError.run { try await urlSession.data(from: url) }
        let status = (response as? HTTPURLResponse)?.statusCode ?? 0
        let obj = (try? JSONSerialization.jsonObject(with: data)) as? [String: Any] ?? [:]
        if status == 429 { throw BrowserPairingError.rateLimited }
        guard status == 200 else { throw BrowserPairingError.serverError(status: status, message: obj["error"] as? String ?? "") }
        return obj
    }
}

/// Shows a one-time browser pairing code with expiry and Copy Code button, or a plain-fix error alert.
@MainActor
public final class BrowserPairingAlertPresenter: BrowserPairingPresenting {
    public init() {}

    public func showCode(_ presentation: BrowserPairingPresentation) {
        let field = NSTextField(wrappingLabelWithString: presentation.code)
        field.font = .monospacedSystemFont(ofSize: NSFont.preferredFont(forTextStyle: .title1).pointSize, weight: .semibold)
        field.textColor = .labelColor; field.alignment = .center; field.isSelectable = true
        field.usesSingleLineMode = false; field.lineBreakMode = .byWordWrapping; field.preferredMaxLayoutWidth = 280
        field.setAccessibilityValue(presentation.codeAccessibilityLabel)
        let container = NSView(frame: NSRect(x: 0, y: 0, width: 280, height: max(36, ceil(field.fittingSize.height) + 8)))
        field.frame = container.bounds.insetBy(dx: 0, dy: 4); container.addSubview(field)
        let info = "Enter this code on the Sheepdog factory page in your browser.\n\(presentation.expiryText)"
        if runAlert(.informational, title: presentation.title, message: info, accessory: container, buttons: ["Done", "Copy Code"]) == .alertSecondButtonReturn {
            let item = NSPasteboardItem()
            item.setString(presentation.code, forType: .string)
            for type in ["org.nspasteboard.TransientType", "org.nspasteboard.ConcealedType"] { item.setData(Data(), forType: .init(type)) }
            NSPasteboard.general.clearContents(); NSPasteboard.general.writeObjects([item])
        }
    }

    public func showError(_ error: BrowserPairingError) {
        _ = runAlert(.warning, title: error.title, message: error.message, accessory: nil, buttons: ["OK"])
    }

    private func runAlert(_ style: NSAlert.Style, title: String, message: String, accessory: NSView?, buttons: [String]) -> NSApplication.ModalResponse {
        NSApp?.activate(ignoringOtherApps: true)
        let alert = NSAlert()
        alert.alertStyle = style; alert.messageText = title; alert.informativeText = message; alert.accessoryView = accessory
        buttons.forEach { alert.addButton(withTitle: $0) }
        return alert.runModal()
    }
}
