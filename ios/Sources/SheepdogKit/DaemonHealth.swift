import Foundation

/// What the Health pane knows about the daemon beyond `/api/state`: when it
/// started, what GPT-Live has billed, and the recent problems in plain
/// words — folded from `/events` live and from `GET /api/events` (the
/// current run's ring) when the pane opens. Pure, so every line is tested.
public struct DaemonHealth: Equatable, Sendable {
    /// One problem worth a line: when, how bad, what, and the raw event.
    public struct Problem: Equatable, Identifiable, Sendable {
        public enum Severity: Equatable, Sendable {
            /// Something failed (a fallback, a connect error).
            case error
            /// Something was lost or cut (a dropped push, unheard audio).
            case warning
            /// Worth knowing, nothing failed (a held push, an echo caught).
            case info
        }

        /// The event id.
        public var id: Int64
        public var ts: Date
        public var type: String
        public var severity: Severity
        /// "Brain fell back: timeout".
        public var title: String
        /// One line of detail, or nil.
        public var detail: String?
        /// The payload, pretty-printed.
        public var raw: String
    }

    /// How many problems are kept, newest last.
    public static let capacity = 100

    public private(set) var problems: [Problem] = []
    /// The newest `daemon.start` seen.
    public private(set) var startedAt: Date?
    /// GPT-Live seconds billed so far this run, when the daemon reported it
    /// (`web.client.stats.voice_billed_seconds`, `voice.billed_seconds`).
    public private(set) var billedSeconds: Double?

    public init() {}

    /// The event types the pane lists.
    public static let problemTypes: Set<String> = [
        "brain.fallback", "brain.rate_limited", "voice.connect_error", "voice.push_held", "voice.push_dropped",
        "supervisor.echo_dropped", "web.audio_unheard", "voice.playback", "worker.session_missing",
    ]

    /// Folds one event; returns true when anything changed. Idempotent.
    @discardableResult
    public mutating func fold(_ event: DaemonEvent) -> Bool {
        let id = Int64(clamping: event.seq)
        let ts = event.ts ?? Date(timeIntervalSince1970: TimeInterval(max(0, id)) / 1_000_000)
        switch event.type {
        case "daemon.start":
            guard startedAt.map({ ts > $0 }) ?? true else { return false }
            startedAt = ts
            // A new run bills from zero.
            billedSeconds = nil
            return true
        case "web.client.stats":
            guard let billed = event.double("voice_billed_seconds"), billed != billedSeconds else { return false }
            billedSeconds = billed
            return true
        case "voice.billed_seconds":
            guard let billed = event.double("total_s"), billed != billedSeconds else { return false }
            billedSeconds = billed
            return true
        default:
            guard id > 0, let problem = DaemonHealth.problem(from: event, id: id, ts: ts) else { return false }
            if problems.contains(where: { $0.id == id }) { return false }
            let at = problems.firstIndex { $0.id > id } ?? problems.endIndex
            problems.insert(problem, at: at)
            if problems.count > DaemonHealth.capacity { problems.removeFirst(problems.count - DaemonHealth.capacity) }
            return true
        }
    }

    /// Repeats of one problem, shown as one row: the newest occurrence and
    /// how many there were (review B4).
    public struct ProblemGroup: Equatable, Identifiable, Sendable {
        public var latest: Problem
        public var count: Int
        public var id: Int64 { latest.id }
    }

    /// `problems` (any order) grouped by type and title, newest group first.
    public static func grouped(_ problems: [Problem]) -> [ProblemGroup] {
        var groups: [ProblemGroup] = []
        var index: [String: Int] = [:]
        for problem in problems.sorted(by: { $0.id > $1.id }) {
            let key = problem.type + "\u{1F}" + problem.title
            if let i = index[key] {
                groups[i].count += 1
            } else {
                index[key] = groups.count
                groups.append(ProblemGroup(latest: problem, count: 1))
            }
        }
        return groups
    }

    /// Problems since `date`, newest first — what the tab's badge counts.
    public func problems(since date: Date) -> [Problem] {
        problems.filter { $0.ts >= date }.reversed()
    }

    /// "2 h 5 min" since the daemon started, at `now`.
    public func uptime(at now: Date) -> String? {
        guard let startedAt else { return nil }
        return Agent.duration(now.timeIntervalSince(startedAt))
    }

    public mutating func clear() {
        self = DaemonHealth()
    }

    // MARK: - The words

    /// "not_reported", "ENOENT", "session.missing": one token of letters,
    /// digits and `_ . -` with an underscore or a dot, or all capitals.
    static func isCode(_ text: String) -> Bool {
        let t = text.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !t.isEmpty, !t.contains(" ") else { return false }
        let allowed = CharacterSet.alphanumerics.union(CharacterSet(charactersIn: "_.-"))
        guard t.unicodeScalars.allSatisfy({ allowed.contains($0) }) else { return false }
        return t.contains("_") || (t.contains(".") && t.lowercased() == t) || (t.uppercased() == t && t.count > 1 && t.rangeOfCharacter(from: .letters) != nil)
    }

    /// One line per event type, from its payload; nil for an event that is
    /// not a problem (a `voice.playback` that was not cut).
    static func problem(from event: DaemonEvent, id: Int64, ts: Date) -> Problem? {
        func text(_ key: String) -> String? {
            guard let s = event.string(key)?.trimmingCharacters(in: .whitespacesAndNewlines), !s.isEmpty else { return nil }
            return s
        }
        func seconds(_ ms: Int) -> String { String(format: "%.1f s", Double(ms) / 1000) }
        func firstLine(_ s: String?) -> String? {
            s?.split(separator: "\n", maxSplits: 1).first.map(String.init)
        }

        let severity: Problem.Severity
        let title: String
        var detail: String?
        switch event.type {
        case "brain.fallback":
            severity = .error
            title = "Brain fell back: \(text("reason") ?? "unknown reason")"
            detail = firstLine(text("error")) ?? text("kind").map { "during an \($0) turn" }
        case "brain.rate_limited":
            severity = .warning
            title = "Brain rate-limited" + (text("model").map { " (\($0))" } ?? "")
            var parts: [String] = []
            if let key = event.int("key_index"), let keys = event.int("keys") { parts.append("key \(key + 1) of \(keys)") }
            if let wait = event.int("retry_after_ms"), wait > 0 { parts.append("retry in \(seconds(wait))") }
            detail = parts.isEmpty ? nil : parts.joined(separator: " · ")
        case "voice.connect_error":
            severity = .error
            title = "Voice could not connect"
            detail = firstLine(text("error"))
        case "voice.push_held":
            severity = .info
            title = "Push held — nobody listening"
            detail = text("text")
        case "voice.push_dropped":
            severity = .warning
            title = "Push dropped — nobody listened for 5 min"
            detail = text("text")
        case "supervisor.echo_dropped":
            severity = .info
            title = "Echo dropped — the dog heard itself"
            detail = text("heard").map { "heard “\($0)”" }
        case "web.audio_unheard":
            severity = .warning
            let bytes = event.int("bytes") ?? 0
            // 24 kHz PCM16: 48 000 bytes a second.
            title = "Audio played to nobody (\(seconds(bytes * 1000 / 48_000)))"
            detail = text("provider").map { "\($0), no client attached" }
        case "voice.playback":
            guard event.bool("cut") == true else { return nil }
            severity = .info
            let played = event.int("played_ms") ?? 0
            let run = event.int("run_ms") ?? 0
            title = "Reply cut off after \(seconds(played))" + (run > 0 ? " of \(seconds(run))" : "")
            detail = text("heard").map { "heard “\($0)”" }
        case "worker.session_missing":
            severity = .warning
            title = "Worker transcript not found" + (text("agent").map { " for \($0)" } ?? "")
            detail = text("reason")
        default:
            return nil
        }
        // A machine code ("not_reported") is not a sentence: it stays in
        // the raw event behind the chevron.
        if let d = detail, isCode(d) { detail = nil }
        return Problem(id: id, ts: ts, type: event.type, severity: severity, title: title, detail: detail, raw: BrainActivity.rawJSON(event.payload))
    }
}

/// `GET /api/brain/models` and `PUT /api/brain/model`: the brain's current
/// provider and model, the providers to choose from, and the model catalog
/// of `modelsProvider`. A discovery failure is not an HTTP error: `error`
/// says why the list is not fresh.
public struct BrainModels: Codable, Equatable, Sendable {
    public struct Provider: Codable, Equatable, Identifiable, Sendable {
        public var id: String
        public var label: String?
        public var available: Bool
        public var reason: String?

        public var displayName: String { label?.isEmpty == false ? label! : id }
    }

    public struct Model: Codable, Equatable, Identifiable, Sendable {
        public var name: String
        public var displayName: String?
        /// A plausible brain model (a text model of a known family).
        public var recommended: Bool?

        public var id: String { name }

        enum CodingKeys: String, CodingKey {
            case name, recommended
            case displayName = "display_name"
        }
    }

    public var provider: String
    public var providers: [Provider]
    public var model: String
    public var modelsProvider: String?
    public var models: [Model]
    public var stale: Bool?
    public var error: String?

    enum CodingKeys: String, CodingKey {
        case provider, providers, model, models, stale, error
        case modelsProvider = "models_provider"
    }

    /// The model to switch to when `modelsProvider`'s catalog was loaded
    /// for a provider change: its first recommended model, else its first.
    /// Never the current model, which belongs to the provider being left
    /// (the reply's `provider`/`model` are still the current ones), and
    /// nil when the catalog is empty or could not be fetched — the caller
    /// then leaves the brain alone rather than send a name the new
    /// provider does not have.
    public var defaultModelForSwitch: String? {
        guard error?.isEmpty ?? true else { return nil }
        return (models.first { $0.recommended == true } ?? models.first)?.name
    }

    /// The models to offer: the recommended ones first, and the current
    /// one even when the catalog does not list it (it may be stale).
    public var choices: [Model] {
        var list = models.filter { $0.recommended == true } + models.filter { $0.recommended != true }
        if !model.isEmpty, !list.contains(where: { $0.name == model }) {
            list.insert(Model(name: model, displayName: nil, recommended: nil), at: 0)
        }
        return list
    }
}

/// `GET/PUT /api/settings`: the push chattiness and its levels.
public struct DaemonSettings: Codable, Equatable, Sendable {
    public var chattiness: String
    public var levels: [String]

    public init(chattiness: String, levels: [String]) {
        self.chattiness = chattiness
        self.levels = levels
    }
}
