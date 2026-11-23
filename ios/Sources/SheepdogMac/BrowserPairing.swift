import Foundation
import SheepdogKit

/// One-time short code returned by `POST /api/pair/browser-code`.
public struct BrowserPairingCode: Equatable, Sendable {
    public var code: String
    public var expires: Date
    public var expiresIn: Int

    public init(code: String, expires: Date, expiresIn: Int) {
        self.code = code
        self.expires = expires
        self.expiresIn = expiresIn
    }
}

/// Failures when asking the local daemon for a browser pairing code.
public enum BrowserPairingError: Error, Equatable, Sendable {
    case daemonDown
    case notEnrolled
    case refused(status: Int, message: String)
    /// Placeholder thrown by the TDD step-1 stub until PR 6.5 is implemented.
    case unimplemented

    /// Plain-language title for the error alert or window.
    public var title: String { "" }

    /// Plain-language explanation and fix for the error alert or window.
    public var message: String { "" }
}

/// What the "Pair browser…" window or alert displays for a fresh short code.
public struct BrowserPairingPresentation: Equatable, Sendable {
    public var title: String
    public var code: String
    public var expiryText: String
    /// VoiceOver announcement for the short code (for example, spelling the
    /// characters so a screen reader does not read them as a word).
    public var codeAccessibilityLabel: String

    public init(code: BrowserPairingCode, now: Date = Date()) {
        // Stub: PR 6.5 formats the code, expiry and VoiceOver label.
        _ = code
        _ = now
        title = ""
        self.code = ""
        expiryText = ""
        codeAccessibilityLabel = ""
    }
}

/// Provides a paired-device session ticket for the local daemon.
public protocol BrowserPairingSessionProviding {
    func sessionTicket(base: URL) async throws -> SessionTicket
}

/// Shows a short browser-pairing code or a plain-fix error to the user.
@MainActor
public protocol BrowserPairingPresenting: AnyObject {
    func showCode(_ presentation: BrowserPairingPresentation)
    func showError(_ error: BrowserPairingError)
}

/// Requests a one-time browser-pairing short code from the local daemon over
/// this Mac app's authenticated device session (`POST /api/pair/browser-code`).
public final class BrowserPairingClient {
    public let locator: DaemonLocating
    public let sessionProvider: BrowserPairingSessionProviding
    public let urlSession: URLSession

    public init(
        locator: DaemonLocating,
        sessionProvider: BrowserPairingSessionProviding,
        urlSession: URLSession? = nil
    ) {
        self.locator = locator
        self.sessionProvider = sessionProvider
        self.urlSession = urlSession ?? URLSession(configuration: .ephemeral)
    }

    public func requestCode() async throws -> BrowserPairingCode {
        throw BrowserPairingError.unimplemented
    }
}
