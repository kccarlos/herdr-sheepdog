import Foundation

/// What a failed request says where the user tapped (a sheet, a card): one
/// slow answer is not an unreachable computer
/// (docs/dev/design/ios-state-architecture.md 3.5, "Errors"). With the
/// computer's link online, a timeout says "taking longer than usual, try
/// again"; only a link that is down says "couldn't reach".
public struct RequestFailure: Equatable, Sendable {
    public var title: String
    public var message: String
    /// The computer answers otherwise; this one request ran out of time.
    public var isSlow: Bool

    public static let slowTitle = "Taking longer than usual"

    /// - Parameters:
    ///   - linkUsable: the computer's link is online (or degraded) right
    ///     now, so a timeout is one slow answer.
    ///   - name: the computer's name once a second computer is paired.
    public init(_ error: Error, linkUsable: Bool, name: String? = nil) {
        let computer = name ?? "your computer"
        let daemonError = error as? DaemonError
        isSlow = false
        switch daemonError {
        case .timedOut? where linkUsable:
            title = Self.slowTitle
            message = "\(DaemonError.slowAnswer) Try again."
            isSlow = true
        case .timedOut?, .unreachable?, .untrusted?:
            title = "Couldn't reach \(computer)"
            message = [daemonError?.message, daemonError?.hint].compactMap { $0 }.joined(separator: " ")
        case let known?:
            title = "Something went wrong"
            message = known.message
        case nil:
            title = "Something went wrong"
            message = error.localizedDescription
        }
    }
}
