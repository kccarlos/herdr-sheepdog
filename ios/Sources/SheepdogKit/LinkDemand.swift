import Foundation

/// Whether the app is in front of the user.
public enum AppPhase: Equatable, Sendable {
    case active
    case background
}

/// A channel to one computer. Each has its own socket, but none backs off
/// on its own any more: a lost channel tells the `ComputerLink`, which
/// probes once and says when to open it again.
public enum LinkChannel: String, CaseIterable, Hashable, Sendable {
    /// The voice's `/ws` socket (opened by the voice, only on the current
    /// computer).
    case ws
    /// The `/events` stream.
    case events
    /// The terminal's `/term` socket.
    case term
}

/// The kind of network the phone is on. The address that won the last race
/// is remembered per kind (docs/dev/design/ios-state-architecture.md 3.3,
/// decision 10): the LAN address at home on Wi-Fi, another address on
/// cellular.
public enum NetworkInterface: String, CaseIterable, Hashable, Sendable {
    case wifi
    case cellular
    case wired
    case other
}

/// What `NWPathMonitor` last said, as a value.
public struct NetworkPath: Equatable, Sendable {
    /// The path can carry traffic.
    public var satisfied: Bool
    /// The interface in use, nil when unknown.
    public var interface: NetworkInterface?
    /// Low Data Mode or an expensive path: other computers refresh on the
    /// 30 s timer instead of streaming `/events`.
    public var constrained: Bool

    public init(satisfied: Bool = true, interface: NetworkInterface? = nil, constrained: Bool = false) {
        self.satisfied = satisfied
        self.interface = interface
        self.constrained = constrained
    }
}

/// Who needs a computer's link right now, and what may be open on it: the
/// foreground and background table of
/// docs/dev/design/ios-state-architecture.md 3.2, as one value.
///
/// A link works only while something needs it. With no demand it is idle
/// and opens nothing, so nothing keeps polling or retrying in the
/// background.
public struct LinkDemand: Equatable, Sendable {
    /// This is the current computer (the one the voice talks to).
    public var isCurrent: Bool
    public var phase: AppPhase
    /// The voice session runs on this computer.
    public var voiceOn: Bool
    /// Settings ▸ Keep listening with the screen locked.
    public var keepListening: Bool
    /// An action (an answer, a typed line…) is still in flight for this
    /// computer; the app holds a background task for it, at most 30 s.
    public var actionsInFlight: Bool
    /// The voice runs on another computer: this one backs off to 60 s
    /// instead of 15 s.
    public var voiceOnAnotherComputer: Bool

    public init(isCurrent: Bool = true, phase: AppPhase = .active, voiceOn: Bool = false, keepListening: Bool = true, actionsInFlight: Bool = false, voiceOnAnotherComputer: Bool = false) {
        self.isCurrent = isCurrent
        self.phase = phase
        self.voiceOn = voiceOn
        self.keepListening = keepListening
        self.actionsInFlight = actionsInFlight
        self.voiceOnAnotherComputer = voiceOnAnotherComputer
    }

    /// The voice keeps this computer's link up in the background.
    var voiceKeepsAlive: Bool { isCurrent && voiceOn && keepListening }

    /// Something needs the link.
    public var needed: Bool {
        if actionsInFlight { return true }
        switch phase {
        case .active: return true
        case .background: return voiceKeepsAlive
        }
    }

    /// The longest wait between attempts.
    public var backoffCap: TimeInterval {
        !isCurrent && voiceOnAnotherComputer ? LinkBackoff.otherComputerCap : LinkBackoff.cap
    }

    /// The channels that may be open while the link is online:
    /// - in the foreground, `/events` and `/term`, plus `/ws` on the
    ///   current computer while the voice is on; another computer on a
    ///   constrained path gets no `/events` (it refreshes every 30 s);
    /// - in the background, only the voice's `/ws`, and only with Keep
    ///   listening on;
    /// - an action in flight needs no channel, only REST.
    public func channels(on path: NetworkPath) -> Set<LinkChannel> {
        switch phase {
        case .active:
            var channels: Set<LinkChannel> = [.term]
            if isCurrent || !path.constrained { channels.insert(.events) }
            if isCurrent && voiceOn { channels.insert(.ws) }
            return channels
        case .background:
            return voiceKeepsAlive ? [.ws] : []
        }
    }
}

/// One jittered backoff per computer (full jitter):
/// `delay = random(0, min(cap, base × 2^attempt))`.
public enum LinkBackoff {
    public static let base: TimeInterval = 0.5
    /// The cap in the foreground.
    public static let cap: TimeInterval = 15
    /// The cap for another computer while the voice is on the current one.
    public static let otherComputerCap: TimeInterval = 60

    /// The upper bound of the delay after `attempt` failed attempts in a
    /// row (0 for the first failure). The exponent is clamped, so a long
    /// outage cannot overflow.
    public static func ceiling(attempt: Int, cap: TimeInterval) -> TimeInterval {
        let exponent = Double(min(max(attempt, 0), 32))
        return min(cap, base * pow(2, exponent))
    }

    /// The delay for a `jitter` fraction drawn uniformly from 0...1.
    public static func delay(attempt: Int, cap: TimeInterval, jitter: Double) -> TimeInterval {
        ceiling(attempt: attempt, cap: cap) * min(max(jitter, 0), 1)
    }
}
