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
        "Why did the migration fail?",
        "What is codex-payments asking?",
        "What did claude-auth do?",
        "Show me the agents",
    ]

    public static let greeting = "This is the demo. Four scripted agents are working on a sample project, and I'll tell you when one finishes, fails or needs you. Nothing leaves this phone."

    public private(set) var agents: [Agent]
    public private(set) var pending: PendingAction?
    public private(set) var events: [DaemonEvent] = []
    public private(set) var history = ConversationHistory()
    /// Lines the voice should say, oldest first, until `takeLines()`.
    public private(set) var lines: [Line] = []
    /// Cards to put on the screen (the `show` tool's), until `takeCards()`.
    public private(set) var cards: [ScreenCard] = []
    private var cardCount = 0

    private let start: Date
    private var now: Date
    private var lastSeq: UInt64 = 0
    private var steps: [(at: TimeInterval, step: Step)]
    private var pendingCount = 0

    enum Step: Sendable {
        case finishAuth, askPayments, failMigration, finishDocs, finishPayments
    }

    public static let authPane = "demo-claude-auth"
    public static let paymentsPane = "demo-codex-payments"
    public static let migrationPane = "demo-claude-migration"
    public static let docsPane = "demo-codex-docs"

    /// What a working agent is doing, as the status answer says it (short,
    /// to be read aloud) and as a question about that agent answers it.
    private static let doing: [String: (short: String, long: String)] = [
        authPane: ("is still fixing the login", "is still fixing the login redirect loop."),
        paymentsPane: ("is adding retries", "is adding retries to the payment webhook."),
        migrationPane: ("is still migrating", "is still running the database migration on the staging copy."),
        docsPane: ("is still writing", "is still writing the install section of the README."),
    ]

    public init(now: Date) {
        start = now
        self.now = now
        agents = [
            Agent(paneID: Self.authPane, alias: "claude-auth", kind: "claude", workspaceLabel: "shop", state: .working, stateSince: now,
                  currentTask: "Fix the login redirect loop", spoken: "claude-auth", cwd: "~/code/shop"),
            Agent(paneID: Self.paymentsPane, alias: "codex-payments", kind: "codex", workspaceLabel: "shop", state: .working, stateSince: now,
                  currentTask: "Add retries to the payment webhook", spoken: "codex-payments", cwd: "~/code/shop"),
            Agent(paneID: Self.migrationPane, alias: "claude-migration", kind: "claude", workspaceLabel: "shop", state: .working, stateSince: now,
                  currentTask: "Run the database migration on the staging copy", spoken: "claude-migration", cwd: "~/code/shop"),
            Agent(paneID: Self.docsPane, alias: "codex-docs", kind: "codex", workspaceLabel: "shop", state: .working, stateSince: now,
                  currentTask: "Update the install section of the README", spoken: "codex-docs", cwd: "~/code/shop"),
        ]
        // The migration fails before the approval, so the status answer
        // after it has news (done, failed, still writing).
        steps = [(8, .finishAuth), (12, .failMigration), (16, .askPayments), (36, .finishDocs)]
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
            finish(Self.authPane, spoken: "claude-auth finished: the login redirect loop is fixed, and the 42 auth tests pass.",
                   detail: "The session cookie was set on the wrong path, so every login bounced back to /login. claude-auth moved it to /, added a regression test and ran the auth suite: 42 passed.",
                   files: ["app/session.ts", "test/login.test.ts"])
        case .finishDocs:
            finish(Self.docsPane, spoken: "codex-docs finished: the README's install section now covers Homebrew and building from source.",
                   detail: "codex-docs rewrote the install section with two paths, Homebrew and building from source, and checked every command in it.",
                   files: ["README.md"])
        case .finishPayments:
            finish(Self.paymentsPane, spoken: "codex-payments finished: the payment webhook now retries three times with backoff, and its tests pass.",
                   detail: "codex-payments installed p-retry, wrapped the webhook handler in three retries with exponential backoff, and added two tests for a flaky provider.",
                   files: ["package.json", "src/webhook.ts", "test/webhook.test.ts"])
        case .failMigration:
            update(Self.migrationPane) { agent, at in
                agent.state = .idle
                agent.stateSince = at
                agent.lastTask = agent.currentTask
                agent.currentTask = nil
                agent.lastResult = Agent.Result(
                    spoken: "claude-migration failed: the column users.plan already exists.",
                    detail: Self.migrationWhy,
                    at: at)
            }
            say("claude-migration failed: the column users.plan already exists. Nothing was changed.", urgency: "interrupt")
        case .askPayments:
            pendingCount += 1
            let action = PendingAction(
                id: "demo-ap-\(pendingCount)", paneID: Self.paymentsPane, kind: "answer_prompt",
                spokenSummary: "codex-payments asks to run npm install p-retry.",
                question: "Do you want to run this command?",
                options: [
                    PendingOption(index: 1, label: "Yes, proceed", keys: ["y"], isDefault: true),
                    PendingOption(index: 2, label: "No, and tell Codex what to do differently", keys: ["esc"]),
                ],
                agent: "codex-payments",
                summary: "codex-payments asks to run npm install p-retry.",
                command: "npm install p-retry"
            )
            pending = action
            update(Self.paymentsPane) { agent, at in
                agent.state = .blocked
                agent.stateSince = at
                agent.lastBlock = Agent.Block(question: "Do you want to run npm install p-retry?", seenAt: at, answered: false)
            }
            log(source: "supervisor", type: "pending.created", pane: Self.paymentsPane, payload: Self.json(action))
            say(Self.askLine, urgency: "interrupt")
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
            agent.lastBlock?.answer = chosen.label
            agent.lastBlock?.via = via
            agent.stateSince = at
            agent.state = approved ? .working : .idle
            if !approved {
                agent.lastTask = agent.currentTask
                agent.currentTask = nil
                agent.lastResult = Agent.Result(spoken: "codex-payments did not install the package and is waiting for new instructions.", at: at)
            }
        }
        if approved {
            steps.append((now.timeIntervalSince(start) + 8, .finishPayments))
            return "Approved. codex-payments is installing p-retry. I'll tell you when it's done."
        }
        return "Declined. codex-payments did not install the package and is waiting for new instructions."
    }

    /// `POST /api/utterance`: a question typed, tapped or said.
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
        if has("show", "list", "table", "on my screen", "on the screen") { return showAgents() }
        if has("fail", "wrong", "error", "broke", "problem") { return failures(why: has("why")) }
        if has("ask", "need", "wait", "approv", "payment", "blocked") { return paymentsStatus }
        if has("auth", "login") { return detail(of: Self.authPane) }
        if has("docs", "readme") { return detail(of: Self.docsPane) }
        if has("migrat", "database") { return detail(of: Self.migrationPane) }
        if has("going on", "status", "what's up", "everyone", "agents", "summary", "how", "what") { return status }
        return "This demo knows a few questions, such as \"What's going on?\" or \"Why did the migration fail?\". With your own sheepdog daemon you can ask anything about your agents, and tell them what to do."
    }

    /// The spoken status: one short clause per agent, in a sentence that
    /// reads well aloud. An agent whose question you answered in the last
    /// 30 seconds and that is now at work on it is left out: you just heard
    /// about it.
    private var status: String {
        let parts = agents.compactMap { agent -> String? in
            switch agent.state {
            case .working:
                if agent.lastBlock?.answered == true, let since = agent.stateSince, now.timeIntervalSince(since) < 30 { return nil }
                return "\(agent.alias) \(Self.doing[agent.paneID]?.short ?? "is still working")"
            case .blocked: return "\(agent.alias) is waiting for your approval to run \(pending?.command ?? "a command")"
            case .done: return "\(agent.alias) is done"
            default:
                if agent.lastResult?.spoken?.contains("failed") == true { return "\(agent.alias) failed" }
                return "\(agent.alias) is idle"
            }
        }
        return parts.joined(separator: ", ") + "."
    }

    /// Why the migration failed, as the voice says it.
    static let migrationWhy = "The column already existed. It rolled back, nothing changed."
    /// The approval, as the voice asks it (and the notification says it).
    static let askLine = "codex-payments wants to run npm install p-retry. Approve?"

    /// "Why did … fail?" gets the reason; "What failed?" also names who.
    private func failures(why: Bool) -> String {
        let failed = agents.filter { $0.lastResult?.spoken?.contains("failed") == true }
        guard !failed.isEmpty else { return "Nothing has failed so far." }
        return failed.map { agent in
            let reason = agent.lastResult?.detail ?? ""
            return why ? reason : "\(agent.alias) failed. \(reason)"
        }.joined(separator: " ")
    }

    private var paymentsStatus: String {
        if let action = pending {
            return "codex-payments wants to run \(action.command ?? "a command"), to add a retry library to the payment webhook. Say approve or decline, or tap a button on the card."
        }
        return detail(of: Self.paymentsPane)
    }

    private func detail(of pane: String) -> String {
        guard let agent = agents.first(where: { $0.paneID == pane }) else { return "I don't know that agent." }
        switch agent.state {
        case .working:
            if pane == Self.paymentsPane, agent.lastBlock?.answered == true { return "codex-payments is installing p-retry and adding the retries." }
            return "\(agent.alias) \(Self.doing[pane]?.long ?? "is still working.")"
        case .blocked: return paymentsStatus
        default: return agent.lastResult?.detail ?? agent.lastResult?.spoken ?? "\(agent.alias) is idle."
        }
    }

    // MARK: - Cards

    /// "Show me the agents": a card with the agents as a table and what
    /// needs the user as a list, and one spoken sentence with the gist.
    private mutating func showAgents() -> String {
        var rows: [String] = []
        var next: [String] = []
        var counts: [String: Int] = [:]
        for agent in agents {
            let failed = agent.lastResult?.spoken?.contains("failed") == true
            let word: String
            switch agent.state {
            case .blocked: word = "Waiting for you"
            case .working: word = "Working"
            case .done: word = "Done"
            default: word = failed ? "Failed" : "Idle"
            }
            counts[word, default: 0] += 1
            rows.append("| \(agent.alias) | \(word) | \(Self.since(agent.stateSince, now: now)) |")
            if agent.state == .blocked { next.append("**\(agent.alias)** wants to run `\(pending?.command ?? "a command")`") }
            if failed { next.append("**\(agent.alias)** failed: the column `users.plan` already exists") }
        }
        var markdown = "| Agent | State | For |\n|---|---|---:|\n" + rows.joined(separator: "\n")
        if !next.isEmpty {
            markdown += "\n\n### Needs you\n" + next.map { "- \($0)" }.joined(separator: "\n")
        } else {
            markdown += "\n\nNothing needs you right now."
        }
        cardCount += 1
        let card = ScreenCard(cardID: "demo-card-\(cardCount)", kind: .card, at: now, title: "Your agents", markdown: markdown)
        show(card)
        let gist = ["Done", "Working", "Waiting for you", "Failed", "Idle"].compactMap { word -> String? in
            guard let n = counts[word] else { return nil }
            return "\(n) \(word.lowercased())"
        }
        return "It's on your screen: " + gist.joined(separator: ", ") + "."
    }

    /// "now", "8 s", "1 min", "12 min", "2 h".
    static func since(_ date: Date?, now: Date) -> String {
        guard let date else { return "—" }
        let seconds = Int(now.timeIntervalSince(date))
        if seconds < 60 { return seconds < 1 ? "now" : "\(seconds) s" }
        let minutes = seconds / 60
        if minutes < 60 { return "\(minutes) min" }
        return "\(minutes / 60) h"
    }

    /// Puts a card on the screen the way the daemon does: the card to
    /// deliver, a `card.shown` event, and the conversation item with it.
    mutating func show(_ card: ScreenCard) {
        log(source: "supervisor", type: "card.shown", payload: [
            "card_id": .string(card.cardID), "kind": .string(card.kind.rawValue), "agent": .string(card.agent ?? ""),
        ])
        if let event = events.last {
            history.upsert(ConversationItem(id: Int64(clamping: event.seq), ts: now, kind: .card, text: card.title,
                                            agent: card.agent, cardID: card.cardID, card: card))
        }
        cards.append(card)
    }

    /// Cards to show since the last call.
    public mutating func takeCards() -> [ScreenCard] {
        defer { cards.removeAll() }
        return cards
    }

    // MARK: - The agent window

    /// `GET /api/agents/{pane}/screen`: a few scripted terminal lines.
    public func screen(paneID: String) -> AgentScreen? {
        guard let agent = agents.first(where: { $0.paneID == paneID }) else { return nil }
        let task = agent.currentTask ?? agent.lastTask ?? ""
        var text = "> \(task)\n\n"
        switch (agent.paneID, agent.state) {
        case (Self.migrationPane, .working):
            // The same command before and after it fails (12 s in), so the
            // window reads the same whenever it opens.
            text += "$ make migrate-staging\nApplying 0042_add_user_plan…\n"
        case (_, .working):
            text += "● Reading the code…\n● Editing files…\n"
        case (Self.paymentsPane, .blocked):
            text += "● I'll add p-retry for the retries.\n\nWould you like to run the following command?\n\n  $ npm install p-retry\n\n› 1. Yes, proceed (y)\n  2. No, and tell Codex what to do differently (esc)\n"
        case (Self.migrationPane, _):
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
        DaemonState(agents: agents, pendingAction: pending, questions: pending.map { [$0] } ?? [],
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

    static func json<T: Encodable>(_ value: T) -> [String: JSONValue] {
        guard let data = try? JSONEncoder().encode(value),
              let object = try? JSONDecoder().decode([String: JSONValue].self, from: data)
        else { return [:] }
        return object
    }
}
