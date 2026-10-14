import Foundation

/// The offline demo's scripted world: four coding agents on a sample
/// project. One finishes, one asks for an approval, one fails, one finishes
/// later; the user can approve or decline the prompt and ask a few
/// questions. Nothing here touches the network or a model. `DemoDaemon`
/// serves it to the app over the same HTTP shapes a real daemon uses, so
/// the agent list, the approval card and the agent window are the real
/// views.
///
/// A value type with an injected clock: `advance(to:)` runs every scripted
/// step that is due, so the tests drive time by hand.
public struct DemoWorld: Sendable {
    /// Something the voice says: shown in the conversation and, while the
    /// session is on, read aloud on the device.
    public struct Line: Equatable, Sendable {
        public var text: String
        public var urgency: String
    }

    /// The questions offered as buttons, and understood when typed or said.
    public static let suggestedQuestions = [
        "What's going on?",
        "What failed?",
        "What is billing asking?",
        "What did auth do?",
    ]

    public static let greeting = "This is the demo. Four scripted agents are working on a sample project, and I'll tell you when one finishes, fails or needs you. Nothing leaves this phone."

    public private(set) var agents: [Agent]
    public private(set) var pending: PendingAction?
    public private(set) var events: [DaemonEvent] = []
    public private(set) var history = ConversationHistory()
    /// Lines the voice should say, oldest first, until `takeLines()`.
    public private(set) var lines: [Line] = []

    private let start: Date
    private var now: Date
    private var lastSeq: UInt64 = 0
    private var steps: [(at: TimeInterval, step: Step)]
    private var pendingCount = 0

    enum Step: Sendable {
        case finishAuth, askBilling, failMigrate, finishDocs, finishBilling
    }

    public static let billingPane = "demo-billing"

    public init(now: Date) {
        start = now
        self.now = now
        agents = [
            Agent(paneID: "demo-auth", alias: "auth", kind: "claude", workspaceLabel: "shop", state: .working, stateSince: now,
                  currentTask: "Fix the login redirect loop", spoken: "auth"),
            Agent(paneID: Self.billingPane, alias: "billing", kind: "codex", workspaceLabel: "shop", state: .working, stateSince: now,
                  currentTask: "Add retries to the payment webhook", spoken: "billing"),
            Agent(paneID: "demo-migrate", alias: "migrate", kind: "claude", workspaceLabel: "shop", state: .working, stateSince: now,
                  currentTask: "Run the database migration on the staging copy", spoken: "migrate"),
            Agent(paneID: "demo-docs", alias: "docs", kind: "codex", workspaceLabel: "shop", state: .working, stateSince: now,
                  currentTask: "Update the install section of the README", spoken: "docs"),
        ]
        steps = [(8, .finishAuth), (16, .askBilling), (26, .failMigrate), (36, .finishDocs)]
        log(source: "daemon", type: "daemon.start", payload: [:])
        say(Self.greeting, urgency: "normal")
    }

    // MARK: - Time

    /// Runs every scripted step due at `date`.
    public mutating func advance(to date: Date) {
        now = max(now, date)
        let elapsed = now.timeIntervalSince(start)
        while let index = steps.indices.min(by: { steps[$0].at < steps[$1].at }), steps[index].at <= elapsed {
            let step = steps.remove(at: index).step
            run(step)
        }
    }

    /// Seconds until the next scripted step, nil when the script is over.
    public var nextStepIn: TimeInterval? {
        steps.map(\.at).min().map { max(0, $0 - now.timeIntervalSince(start)) }
    }

    private mutating func run(_ step: Step) {
        switch step {
        case .finishAuth:
            finish("demo-auth", spoken: "auth finished: the login redirect loop is fixed, and the 42 auth tests pass.",
                   detail: "The session cookie was set on the wrong path, so every login bounced back to /login. auth moved it to /, added a regression test and ran the auth suite: 42 passed.",
                   files: ["app/session.ts", "test/login.test.ts"])
        case .finishDocs:
            finish("demo-docs", spoken: "docs finished: the README's install section now covers Homebrew and building from source.",
                   detail: "docs rewrote the install section with two paths, Homebrew and building from source, and checked every command in it.",
                   files: ["README.md"])
        case .finishBilling:
            finish(Self.billingPane, spoken: "billing finished: the payment webhook now retries three times with backoff, and its tests pass.",
                   detail: "billing installed p-retry, wrapped the webhook handler in three retries with exponential backoff, and added two tests for a flaky provider.",
                   files: ["package.json", "src/webhook.ts", "test/webhook.test.ts"])
        case .failMigrate:
            update("demo-migrate") { agent, at in
                agent.state = .idle
                agent.stateSince = at
                agent.lastTask = agent.currentTask
                agent.currentTask = nil
                agent.lastResult = Agent.Result(
                    spoken: "migrate failed: the staging migration stopped because the column users.plan already exists.",
                    detail: "Migration 0042 tried to add users.plan, which an earlier hotfix had already added by hand. Nothing was changed; the migration rolled back. It needs a decision: drop the column first, or mark 0042 as applied.",
                    at: at)
            }
            say("migrate failed: the staging migration stopped because the column users.plan already exists. Nothing was changed.", urgency: "interrupt")
        case .askBilling:
            pendingCount += 1
            let action = PendingAction(
                id: "demo-ap-\(pendingCount)", paneID: Self.billingPane, kind: "answer_prompt",
                spokenSummary: "billing asks to run npm install p-retry.",
                question: "Do you want to run this command?",
                options: [
                    PendingOption(index: 1, label: "Yes, proceed", keys: ["y"], isDefault: true),
                    PendingOption(index: 2, label: "No, and tell Codex what to do differently", keys: ["esc"]),
                ],
                agent: "billing",
                summary: "billing asks to run npm install p-retry.",
                command: "npm install p-retry"
            )
            pending = action
            update(Self.billingPane) { agent, at in
                agent.state = .blocked
                agent.stateSince = at
                agent.lastBlock = Agent.Block(question: "Do you want to run npm install p-retry?", seenAt: at, answered: false)
            }
            log(source: "supervisor", type: "pending.created", pane: Self.billingPane, payload: Self.json(action))
            say("billing wants to run npm install p-retry, to add a retry library. Approve or decline?", urgency: "interrupt")
        }
    }

    // MARK: - The user

    public enum AnswerError: Error, Equatable { case gone }

    /// `POST /api/pending/answer`: option 1 approves, anything else
    /// declines. Returns what the voice says.
    @discardableResult
    public mutating func answer(pendingID: String, option: Int, via: String = "tap") throws -> String {
        guard let action = pending, action.id == pendingID,
              let chosen = action.options?.first(where: { $0.index == option })
        else { throw AnswerError.gone }
        pending = nil
        log(source: "supervisor", type: "pending.confirmed", pane: action.paneID, payload: [
            "id": .string(action.id), "answer": .string(chosen.label), "option": .number(Double(option)), "via": .string(via),
        ])
        let approved = option == 1
        update(action.paneID) { agent, at in
            agent.lastBlock?.answered = true
            agent.stateSince = at
            agent.state = approved ? .working : .idle
            if !approved {
                agent.lastTask = agent.currentTask
                agent.currentTask = nil
                agent.lastResult = Agent.Result(spoken: "billing did not install the package and is waiting for new instructions.", at: at)
            }
        }
        if approved {
            steps.append((now.timeIntervalSince(start) + 8, .finishBilling))
            return "Approved. billing is installing p-retry; I'll tell you when it's done."
        }
        return "Declined. billing did not install the package and is waiting for new instructions."
    }

    /// `POST /api/inject/utterance`: a question typed, tapped or said.
    /// Logs it and the answer the way a real daemon logs a typed turn, and
    /// returns the answer.
    @discardableResult
    public mutating func ask(_ text: String) -> String {
        let question = text.trimmingCharacters(in: .whitespacesAndNewlines)
        log(source: "web", type: "web.utterance", payload: ["text": .string(question), "via": .string("typed")])
        let reply = respond(to: question)
        log(source: "voice", type: "voice.tool_result", payload: ["spoken": .string(reply), "via": .string("typed")])
        return reply
    }

    /// The scripted answer for a question, from the world as it is now.
    mutating func respond(to question: String) -> String {
        let q = question.lowercased()
        func has(_ words: String...) -> Bool { words.contains { q.contains($0) } }

        if let action = pending, has("approve", "yes", "go ahead", "proceed", "do it") {
            return (try? answer(pendingID: action.id, option: 1, via: "brain")) ?? "There is nothing to approve right now."
        }
        if let action = pending, has("decline", "deny", "reject") || q == "no" || q.hasPrefix("no,") || q.hasPrefix("no ") {
            return (try? answer(pendingID: action.id, option: 2, via: "brain")) ?? "There is nothing to decline right now."
        }
        if has("fail", "wrong", "error", "broke", "problem") { return failures }
        if has("ask", "need", "wait", "approv", "billing", "blocked") { return billingStatus }
        if has("auth", "login") { return detail(of: "demo-auth") }
        if has("docs", "readme") { return detail(of: "demo-docs") }
        if has("migrat", "database") { return detail(of: "demo-migrate") }
        if has("going on", "status", "what's up", "everyone", "agents", "summary", "how", "what") { return status }
        return "This demo knows a few questions, such as \"What's going on?\" or \"What failed?\". With your own sheepdog daemon you can ask anything about your agents, and tell them what to do."
    }

    private var status: String {
        let parts = agents.map { agent -> String in
            switch agent.state {
            case .working: return "\(agent.alias) is working on \(lowercasedFirst(agent.currentTask ?? "its task"))"
            case .blocked: return "\(agent.alias) is waiting for your approval to run \(pending?.command ?? "a command")"
            case .done: return "\(agent.alias) is done"
            default:
                if agent.lastResult?.spoken?.contains("failed") == true { return "\(agent.alias) failed" }
                return "\(agent.alias) is idle"
            }
        }
        return parts.joined(separator: "; ") + "."
    }

    private var failures: String {
        let failed = agents.filter { $0.lastResult?.spoken?.contains("failed") == true }
        guard !failed.isEmpty else { return "Nothing has failed so far." }
        return failed.compactMap { $0.lastResult?.detail ?? $0.lastResult?.spoken }.joined(separator: " ")
    }

    private var billingStatus: String {
        if let action = pending {
            return "billing wants to run \(action.command ?? "a command") to add a retry library for the payment webhook. Say approve or decline, or tap a button on the card."
        }
        return detail(of: Self.billingPane)
    }

    private func detail(of pane: String) -> String {
        guard let agent = agents.first(where: { $0.paneID == pane }) else { return "I don't know that agent." }
        switch agent.state {
        case .working: return "\(agent.alias) is still working on \(lowercasedFirst(agent.currentTask ?? "its task"))."
        case .blocked: return billingStatus
        default: return agent.lastResult?.detail ?? agent.lastResult?.spoken ?? "\(agent.alias) is idle."
        }
    }

    // MARK: - The agent window

    /// `GET /api/agents/{pane}/screen`: a few scripted terminal lines.
    public func screen(paneID: String) -> AgentScreen? {
        guard let agent = agents.first(where: { $0.paneID == paneID }) else { return nil }
        let task = agent.currentTask ?? agent.lastTask ?? ""
        var text = "> \(task)\n\n"
        switch (agent.paneID, agent.state) {
        case (_, .working):
            text += "● Reading the code…\n● Editing files…\n"
        case (Self.billingPane, .blocked):
            text += "● I'll add p-retry for the retries.\n\nWould you like to run the following command?\n\n  $ npm install p-retry\n\n› 1. Yes, proceed (y)\n  2. No, and tell Codex what to do differently (esc)\n"
        case ("demo-migrate", _):
            text += "$ make migrate-staging\nApplying 0042_add_user_plan…\nERROR: column \"plan\" of relation \"users\" already exists\nRolled back. No changes were made.\n"
        default:
            let files = agent.lastWorkerMessage?.filesTouched ?? []
            text += (agent.lastResult?.detail ?? "Done.") + "\n"
            if !files.isEmpty { text += "\nFiles changed: " + files.joined(separator: ", ") + "\n" }
        }
        text += "\n(Demo: a scripted screen. With your own daemon this is the agent's live terminal.)"
        return AgentScreen(paneID: agent.paneID, alias: agent.alias, spoken: agent.spoken, kind: agent.kind, state: agent.state,
                           stateSince: agent.stateSince, text: text, lines: text.split(separator: "\n").count, truncated: false,
                           hash: String(text.hashValue, radix: 16))
    }

    // MARK: - Snapshots

    public var state: DaemonState {
        DaemonState(agents: agents, pendingAction: pending,
                    voice: .init(mode: "demo", name: "Sheepdog", status: "connected", clientConnected: true),
                    herdr: "demo", version: "demo",
                    conversationStartedAt: start)
    }

    /// Lines to say since the last call.
    public mutating func takeLines() -> [Line] {
        defer { lines.removeAll() }
        return lines
    }

    // MARK: - Helpers

    private mutating func finish(_ pane: String, spoken: String, detail: String, files: [String]) {
        update(pane) { agent, at in
            agent.state = .done
            agent.stateSince = at
            agent.lastTask = agent.currentTask
            agent.currentTask = nil
            agent.lastResult = Agent.Result(spoken: spoken, detail: detail, at: at)
            agent.journal = [Agent.JournalEntry(at: at, event: "done", lastAssistant: detail, instruction: agent.lastTask, filesTouched: files)]
        }
        say(spoken, urgency: "normal")
    }

    private mutating func update(_ pane: String, _ change: (inout Agent, Date) -> Void) {
        guard let index = agents.firstIndex(where: { $0.paneID == pane }) else { return }
        var agent = agents[index]
        change(&agent, now)
        agents[index] = agent
        log(source: "herdr", type: "herdr.state", pane: pane, payload: ["state": .string(agents[index].state.rawValue)])
    }

    private mutating func say(_ text: String, urgency: String) {
        log(source: "voice", type: "voice.push", payload: ["text": .string(text), "urgency": .string(urgency), "agent": .string("")])
        lines.append(Line(text: text, urgency: urgency))
    }

    private mutating func log(source: String, type: String, pane: String? = nil, payload: [String: JSONValue]) {
        // Ids are microseconds since 1970, as the daemon's are; strictly
        // increasing even within one microsecond.
        let seq = max(lastSeq + 1, UInt64(now.timeIntervalSince1970 * 1_000_000))
        lastSeq = seq
        let event = DaemonEvent(seq: seq, ts: now, source: source, type: type, paneID: pane, payload: payload)
        events.append(event)
        _ = history.fold(event)
    }

    private func lowercasedFirst(_ s: String) -> String {
        guard let first = s.first else { return s }
        return first.lowercased() + s.dropFirst()
    }

    static func json<T: Encodable>(_ value: T) -> [String: JSONValue] {
        guard let data = try? JSONEncoder().encode(value),
              let object = try? JSONDecoder().decode([String: JSONValue].self, from: data)
        else { return [:] }
        return object
    }
}
