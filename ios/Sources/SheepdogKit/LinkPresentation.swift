import Foundation

/// What every status surface shows for one computer's link: the chip, the
/// Settings row, the inline copy where the data would be, Health, and
/// whether the data is live and the buttons work
/// (docs/dev/design/ios-state-architecture.md 3.2 "What the UI shows" and
/// 3.10). The chip, Settings, the Agents empty state and the sidebar all
/// render this one value, so they cannot disagree.
///
/// With one computer the copy has no names ("your computer"); with several
/// the caller passes the computer's name.
public struct LinkPresentation: Equatable, Sendable {
    public enum Action: Equatable, Sendable {
        case tryNow
        case tryAgain
        case useAnotherAddress
        case scan
    }

    public struct Inline: Equatable, Sendable {
        public var message: String
        public var actions: [Action]
    }

    /// How the data on screen is shown.
    public enum DataStyle: Equatable, Sendable {
        case live
        /// The last data, shown normally (a blip, or connecting).
        case lastKnown
        /// The last data, dimmed, "as of" the last time anything succeeded.
        case dimmed(asOf: String?)
        /// Nothing to show: the credentials are gone.
        case cleared
    }

    /// Whether a control works, and the hint when it does not.
    public enum Availability: Equatable, Sendable {
        case enabled
        case disabled(String)
        case hidden
    }

    public var chip: String
    public var tint: ConnectionChip.Tint
    /// A spinner belongs next to the chip's word.
    public var isBusy: Bool
    /// Settings ▸ Connection, and another computer's row.
    public var statusLine: String
    public var inline: Inline?
    /// Health only (a degraded link still shows Connected).
    public var health: String?
    public var data: DataStyle
    /// Answer buttons on questions.
    public var answers: Availability
    /// The composer's Send.
    public var composer: Availability

    public static let reconnectToAnswer = "Reconnect to answer"
    public static let reconnectToSend = "Reconnect to send"

    /// - Parameters:
    ///   - name: the computer's name once a second computer is paired; nil
    ///     with one computer.
    ///   - voiceOn: the voice runs on this computer (Settings says
    ///     "Talking here").
    ///   - time: formats a moment as a short time ("9:12").
    public init(_ link: ComputerLink, name: String? = nil, voiceOn: Bool = false, time: (Date) -> String = LinkPresentation.shortTime) {
        let computer = name ?? "your computer"
        let lastSeen = link.lastSeen.map(time)
        let dimmed = DataStyle.dimmed(asOf: lastSeen)

        func offlineInline() -> Inline {
            var message = "Can't reach \(computer)."
            if let lastSeen { message += " Last seen \(lastSeen)." }
            message += " Is it awake, and is this phone on a network that reaches it?"
            return Inline(message: message, actions: [.tryNow])
        }

        health = nil
        inline = nil
        switch link.state {
        case .idle:
            chip = "Not connected"
            tint = .grey
            isBusy = false
            data = .lastKnown
            answers = .disabled(Self.reconnectToAnswer)
            composer = .disabled(Self.reconnectToSend)
            statusLine = chip
        case let .connecting(_, outage):
            if let outage, outage.long {
                chip = "Offline"
                tint = .red
                inline = offlineInline()
                data = dimmed
                answers = .disabled(Self.reconnectToAnswer)
                composer = .disabled(Self.reconnectToSend)
                statusLine = "Offline since \(time(outage.since))"
            } else if outage != nil {
                chip = "Reconnecting…"
                tint = .amber
                data = .lastKnown
                answers = .enabled
                composer = .enabled
                statusLine = chip
            } else {
                chip = "Connecting…"
                tint = .amber
                data = .lastKnown
                // Never act on cached questions.
                answers = .disabled(chip)
                composer = .enabled
                statusLine = chip
            }
            isBusy = true
        case let .online(address, _):
            chip = "Connected"
            tint = .green
            isBusy = false
            data = .live
            answers = .enabled
            composer = .enabled
            statusLine = voiceOn ? "Talking here · \(address)" : chip
        case let .degraded(address, _, why):
            chip = "Connected"
            tint = .green
            isBusy = false
            health = why == .liveUpdates ? "Live updates are reconnecting" : "Replies are slow"
            data = .live
            answers = .enabled
            composer = .enabled
            statusLine = voiceOn ? "Talking here · \(address)" : chip
        case .waiting:
            chip = "Reconnecting…"
            tint = .amber
            isBusy = true
            data = .lastKnown
            answers = .enabled
            composer = .enabled
            statusLine = chip
        case let .offline(_, _, outage):
            chip = "Offline"
            tint = .red
            isBusy = false
            inline = offlineInline()
            data = dimmed
            answers = .disabled(Self.reconnectToAnswer)
            composer = .disabled(Self.reconnectToSend)
            statusLine = "Offline since \(time(outage.since))"
        case .noNetwork:
            chip = "No network"
            tint = .grey
            isBusy = false
            inline = Inline(message: "This phone is offline. Sheepdog reconnects when the network is back.", actions: [])
            data = dimmed
            answers = .disabled(Self.reconnectToAnswer)
            composer = .disabled(Self.reconnectToSend)
            statusLine = chip
        case .untrustedAnswer:
            chip = "Not verified"
            tint = .red
            isBusy = false
            inline = Inline(message: "Something on this network answered instead of \(computer), so Sheepdog didn't connect.", actions: [.tryAgain, .useAnotherAddress])
            data = dimmed
            answers = .hidden
            composer = .disabled(Self.reconnectToSend)
            statusLine = chip
        case let .refused(problem):
            chip = "Scan again"
            tint = .red
            isBusy = false
            inline = Inline(message: problem.message, actions: [.scan])
            data = .cleared
            answers = .hidden
            composer = .disabled(Self.reconnectToSend)
            statusLine = chip
        case let .incompatible(message):
            chip = "Update needed"
            tint = .red
            isBusy = false
            inline = Inline(message: message, actions: [])
            data = .lastKnown
            answers = .disabled(Self.reconnectToAnswer)
            composer = .disabled(Self.reconnectToSend)
            statusLine = chip
        }
        if let name { statusLine = "\(name) · \(statusLine)" }
    }

    /// "9:12" in the user's locale.
    public static func shortTime(_ date: Date) -> String {
        date.formatted(date: .omitted, time: .shortened)
    }
}
