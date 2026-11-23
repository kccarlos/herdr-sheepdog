import AppKit
import Foundation
import Security
import SheepdogKit

/// One-time short code returned by `POST /api/pair/browser-code`.
public struct BrowserPairingCode: Equatable, Sendable {
    public var code: String
    public var expires: Date
    public var expiresIn: Int
    public init(code: String, expires: Date, expiresIn: Int) {
        self.code = code; self.expires = expires; self.expiresIn = expiresIn
    }
}

/// Failures when asking the local daemon for a browser pairing code.
public enum BrowserPairingError: Error, Equatable, Sendable {
    case daemonDown, notEnrolled, rateLimited, serverError(status: Int, message: String)

    public var title: String {
        switch self {
        case .daemonDown: return "Sheepdog isn't running"
        case .notEnrolled: return "This Mac app isn't paired with Sheepdog"
        case .rateLimited: return ""
        case .serverError: return ""
        }
    }

    public var message: String {
        switch self {
        case .daemonDown:
            return "Sheepdog isn't running on this Mac. Start the sheepdog daemon, then choose Pair browser… again."
        case .notEnrolled:
            return "Sheepdog couldn't verify this Mac app's device session. Enable pairing in sheepdog setup, then choose Pair browser… again."
        case .rateLimited:
            return ""
        case .serverError:
            return ""
        }
    }
}

/// What the "Pair browser…" alert displays for a fresh short code.
public struct BrowserPairingPresentation: Equatable, Sendable {
    public var title, code, expiryText, codeAccessibilityLabel: String

    public init(code: BrowserPairingCode, now: Date = Date()) {
        self.title = "Pair a browser"
        self.code = code.code
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
    public let locator: DaemonLocating
    public let sessionProvider: BrowserPairingSessionProviding
    public let urlSession: URLSession

    public init(locator: DaemonLocating, sessionProvider: BrowserPairingSessionProviding, urlSession: URLSession? = nil) {
        self.locator = locator; self.sessionProvider = sessionProvider
        self.urlSession = urlSession ?? URLSession(configuration: .ephemeral)
    }

    public func requestCode() async throws -> BrowserPairingCode {
        guard let base = locator.endpoint.url(path: ""), let url = locator.endpoint.url(path: "/api/pair/browser-code") else {
            throw BrowserPairingError.daemonDown
        }
        let ticket = try await Self.mapErrors { try await sessionProvider.sessionTicket(base: base) }
        var req = URLRequest(url: url)
        req.httpMethod = "POST"
        req.setValue(ticket.header, forHTTPHeaderField: "Authorization")
        let (data, response): (Data, URLResponse)
        do { (data, response) = try await urlSession.data(for: req) }
        catch { throw BrowserPairingError.daemonDown }
        let status = (response as? HTTPURLResponse)?.statusCode ?? 0
        let obj = (try? JSONSerialization.jsonObject(with: data)) as? [String: Any] ?? [:]
        if status == 200, let code = obj["code"] as? String, !code.isEmpty,
           let expires = (obj["expires"] as? String).flatMap(Self.parseDate) {
            return BrowserPairingCode(code: code, expires: expires, expiresIn: obj["expires_in"] as? Int ?? max(0, Int(expires.timeIntervalSinceNow.rounded())))
        }
        if status == 401 || (status == 403 && (obj["reason"] as? String) == "device_session_required") { throw BrowserPairingError.notEnrolled }
        throw BrowserPairingError.serverError(status: status, message: obj["error"] as? String ?? "")
    }

    fileprivate static func mapErrors<T>(_ work: () async throws -> T) async throws -> T {
        do { return try await work() }
        catch let err as BrowserPairingError { throw err }
        catch { throw error is URLError ? BrowserPairingError.daemonDown : BrowserPairingError.notEnrolled }
    }

    private static func parseDate(_ raw: String) -> Date? {
        let f = ISO8601DateFormatter(); f.formatOptions = [.withInternetDateTime, .withFractionalSeconds]
        return f.date(from: raw) ?? ISO8601DateFormatter().date(from: raw)
    }
}

/// Enrols `Sheepdog.app` using the shared Keychain `DeviceKey`, `KeychainSecretStore`, and `DeviceAuth`.
public final class MacDeviceSessionProvider: BrowserPairingSessionProviding {
    private let secrets: SecretStore
    private let keys: DeviceKeyProviding
    private let urlSession: URLSession
    private let deviceName: @Sendable () -> String

    public init(
        secrets: SecretStore = KeychainSecretStore(service: "com.github.kccarlos.herdr-sheepdog-mac.daemon", accessibleAfterFirstUnlock: true),
        keys: DeviceKeyProviding = SystemDeviceKeyProvider(),
        urlSession: URLSession? = nil,
        deviceName: @escaping @Sendable () -> String = { Host.current().localizedName ?? "Mac" }
    ) {
        self.secrets = secrets; self.keys = keys; self.urlSession = urlSession ?? URLSession(configuration: .ephemeral); self.deviceName = deviceName
    }

    public func invalidateTicket() {}

    public func sessionTicket(base: URL) async throws -> SessionTicket {
        let hello = try await fetchJSON(base.appendingPathComponent("api/hello"))
        guard let daemonID = (hello["computer"] as? [String: Any])?["id"] as? String, !daemonID.isEmpty,
              let pin = (hello["security"] as? [String: Any])?["pin"] as? String, !pin.isEmpty
        else { throw BrowserPairingError.notEnrolled }
        let auth = DeviceAuth(base: base, session: urlSession), account = "mac.device.\(daemonID)"
        if let deviceID = secrets.secret(for: account), let key = DeviceKey.load(daemonID: daemonID) {
            do { return try await auth.signIn(daemonID: daemonID, pin: pin, deviceID: deviceID, key: key) }
            catch PairingProblem.revoked, PairingProblem.unknownDevice { secrets.setSecret(nil, for: account) }
            catch { throw error is URLError ? BrowserPairingError.daemonDown : BrowserPairingError.notEnrolled }
        }
        let deviceID = try await enroll(base: base, daemonID: daemonID, auth: auth)
        secrets.setSecret(deviceID, for: account)
        guard let key = DeviceKey.load(daemonID: daemonID) else { throw BrowserPairingError.notEnrolled }
        return try await BrowserPairingClient.mapErrors { try await auth.signIn(daemonID: daemonID, pin: pin, deviceID: deviceID, key: key) }
    }

    private func enroll(base: URL, daemonID: String, auth: DeviceAuth) async throws -> String {
        let pairObj = try await fetchJSON(base.appendingPathComponent("api/pair"))
        guard let link = pairObj["link"] as? String, let pairing = PairingCode(link), pairing.daemonID == daemonID else {
            throw BrowserPairingError.notEnrolled
        }
        let lease = try await BrowserPairingClient.mapErrors { try PairingEnrollment.KeyLease.acquire(daemonID: daemonID) }
        guard let pub = lease.publicKey else {
            lease.release(definiteNonEnrollment: true, isComputerPaired: false); throw BrowserPairingError.notEnrolled
        }
        do {
            let enrolled = try await auth.enroll(code: pairing.code, publicKey: pub, name: deviceName(), kind: "ios", hardware: DeviceKey.isHardware)
            lease.markCommitted(); lease.release(definiteNonEnrollment: false, isComputerPaired: true)
            return enrolled.deviceID
        } catch {
            lease.release(definiteNonEnrollment: PairingEnrollment.isDefiniteNonEnrollment(error), isComputerPaired: false)
            throw error is URLError ? BrowserPairingError.daemonDown : BrowserPairingError.notEnrolled
        }
    }

    private func fetchJSON(_ url: URL) async throws -> [String: Any] {
        let (data, response) = try await BrowserPairingClient.mapErrors { try await urlSession.data(from: url) }
        guard (response as? HTTPURLResponse)?.statusCode == 200,
              let obj = try? JSONSerialization.jsonObject(with: data) as? [String: Any]
        else { throw BrowserPairingError.notEnrolled }
        return obj
    }
}

/// Shows a one-time browser pairing code with expiry and Copy Code button, or a plain-fix error alert.
@MainActor
public final class BrowserPairingAlertPresenter: BrowserPairingPresenting {
    public init() {}

    public func showCode(_ presentation: BrowserPairingPresentation) {
        let field = NSTextField(labelWithString: presentation.code)
        field.font = .monospacedSystemFont(ofSize: NSFont.preferredFont(forTextStyle: .title1).pointSize, weight: .semibold)
        field.textColor = .labelColor; field.alignment = .center; field.isSelectable = true
        field.setAccessibilityLabel(presentation.codeAccessibilityLabel)
        field.sizeToFit()
        let info = "Enter this code on the Sheepdog factory page in your browser.\n\(presentation.expiryText)"
        if runAlert(.informational, title: presentation.title, message: info, accessory: field, buttons: ["Copy Code", "Done"]) == .alertFirstButtonReturn {
            NSPasteboard.general.clearContents(); NSPasteboard.general.setString(presentation.code, forType: .string)
        }
    }

    public func showError(_ error: BrowserPairingError) {
        _ = runAlert(.warning, title: error.title, message: error.message, accessory: nil, buttons: ["OK"])
    }

    private func runAlert(_ style: NSAlert.Style, title: String, message: String, accessory: NSView?, buttons: [String]) -> NSApplication.ModalResponse {
        NSApp.activate(ignoringOtherApps: true)
        let alert = NSAlert()
        alert.alertStyle = style; alert.messageText = title; alert.informativeText = message; alert.accessoryView = accessory
        buttons.forEach { alert.addButton(withTitle: $0) }
        return alert.runModal()
    }
}
