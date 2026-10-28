import Foundation

/// The daemon's notice items, told apart by their words until the daemon
/// tags them (design doc, phase 3). The one place that matches the text.
public enum DaemonNotice: Equatable, Sendable {
    /// "Sheepdog started."
    case restarted
    /// "New conversation".
    case reset
    /// "Voice context lost."
    case contextLost
    /// "Voice reconnected with a fresh session; earlier context was lost."
    case freshReconnect
    /// "Voice reconnected; the conversation continues."
    case resumedReconnect
    /// "Voice session closed: shutdown."
    case closed
    /// The one row that stands for a lost context and the reconnect after
    /// it, made by `TimelineContext` when it matters.
    case voiceRestarted
    /// Anything else: shown as the plumbing it is.
    case other

    public init(_ item: ConversationItem) {
        guard item.kind == .notice || item.kind == .unknown else { self = .other; return }
        if item.isResetNotice {
            self = .reset
            return
        }
        if item.subkind == ConversationItem.voiceRestartedSubkind {
            self = .voiceRestarted
            return
        }
        var text = item.text.trimmingCharacters(in: .whitespacesAndNewlines)
        while text.hasSuffix(".") { text.removeLast() }
        switch text {
        case "Sheepdog started": self = .restarted
        case "Voice context lost": self = .contextLost
        case "Voice reconnected with a fresh session; earlier context was lost": self = .freshReconnect
        case "Voice reconnected; the conversation continues": self = .resumedReconnect
        default: self = text.hasPrefix("Voice session closed") ? .closed : .other
        }
    }
}

extension ConversationItem {
    /// The `subkind` of the row that stands for a context loss the user
    /// would notice. Made by the app, never sent by the daemon.
    public static let voiceRestartedSubkind = "voice_restarted"
}

/// What the neighbours decide about the rows (design doc, sections 5.1,
/// 5.3 and 5.5): one pass over the list, so nothing else needs to look at
/// more than one row. Pure.
public struct TimelineContext: Equatable, Sendable {
    /// Rows to show in place of the row with this id.
    var promoted: [String: TimelineRow] = [:]
    /// Rows not listed on their own: they are `attached` to another row.
    var absorbed: Set<String> = []
    /// The rows folded into a row, by its id, oldest first.
    var attached: [String: [TimelineRow]] = [:]
}

extension ConversationTimeline {
    /// A reconnect matters only when the voice may have lost something
    /// you just said: this long after the last dialogue.
    public static let reconnectWindow: TimeInterval = 5 * 60
    /// A settled question's own push and restatement are looked for this
    /// long after it was asked.
    public static let questionWindow: TimeInterval = 2 * 60
    /// An orchestrator reply belongs to the voice's bubble this close to it.
    public static let replyWindow: TimeInterval = 2 * 60

    public static func context(of rows: [TimelineRow], timeFormatter: DateFormatter = defaultTimeFormatter) -> TimelineContext {
        var context = TimelineContext()
        promoteReconnects(rows, into: &context, timeFormatter: timeFormatter)
        foldReplies(rows, into: &context)
        foldSettledQuestions(rows, into: &context)
        return context
    }

    // MARK: - Reconnects

    /// "Voice context lost" and the fresh reconnect after it are one event,
    /// and it is shown, as one row, only when the voice was in a
    /// conversation: dialogue in the last 5 minutes. Dialogue means a
    /// client was attached (the voice cannot hear or speak without one),
    /// which the history does not say otherwise. Every other reconnect
    /// stays plumbing.
    private static func promoteReconnects(_ rows: [TimelineRow], into context: inout TimelineContext, timeFormatter: DateFormatter) {
        var lastDialogue: Date?
        for row in rows {
            if classify(row).category == .dialogue {
                lastDialogue = row.date
                continue
            }
            guard let item = daemonItem(row), DaemonNotice(item) == .freshReconnect,
                  let spoke = lastDialogue, item.ts.timeIntervalSince(spoke) <= reconnectWindow else { continue }
            var restarted = item
            restarted.subkind = ConversationItem.voiceRestartedSubkind
            let title = "Voice restarted — it may not remember the last minute · \(timeFormatter.string(from: item.ts))"
            context.promoted[row.id] = .divider(restarted, title: title)
        }
    }

    private static func daemonItem(_ row: TimelineRow) -> ConversationItem? {
        switch row {
        case let .divider(item, _), let .item(item): return item
        default: return nil
        }
    }

    // MARK: - Orchestrator replies

    /// An orchestrator reply the voice said folds into that bubble's
    /// "Details": the bubble right after the reply, or the one right
    /// before when a bubble opened before the reply came back and carries
    /// on. Pending replies and ones the voice never said stay cards.
    private static func foldReplies(_ rows: [TimelineRow], into context: inout TimelineContext) {
        for (index, row) in rows.enumerated() {
            guard case let .brain(reply, compact) = row, !compact, !reply.pending else { continue }
            let neighbours = [nextShown(after: index, in: rows), previousShown(before: index, in: rows)]
            for neighbour in neighbours {
                guard let at = neighbour, isVoiceSpeech(rows[at]),
                      abs(rows[at].date.timeIntervalSince(reply.ts)) <= replyWindow else { continue }
                context.attached[rows[at].id, default: []].append(row)
                context.absorbed.insert(row.id)
                break
            }
        }
    }

    private static func nextShown(after index: Int, in rows: [TimelineRow]) -> Int? {
        rows.indices.dropFirst(index + 1).first { classify(rows[$0]).shownByDefault }
    }

    private static func previousShown(before index: Int, in rows: [TimelineRow]) -> Int? {
        rows.indices.prefix(index).last { classify(rows[$0]).shownByDefault }
    }

    /// The voice's words (not a push card).
    private static func isVoiceSpeech(_ row: TimelineRow) -> Bool {
        switch row {
        case let .item(item): return item.kind == .dog
        case let .live(bubble): return bubble.speaker == .sheepdog && bubble.kind == .speech
        default: return false
        }
    }

    private static func isUserSpeech(_ row: TimelineRow) -> Bool {
        switch row {
        case let .item(item): return item.kind == .shepherd
        case let .live(bubble): return bubble.speaker == .shepherd && bubble.kind == .speech
        default: return false
        }
    }

    // MARK: - Settled questions

    /// A settled question is one line; the push that announced it, and the
    /// voice's restatement, fold into it (as `QuestionFocus` does while it
    /// is open). The restatement folds only when the user did not reply to
    /// it: a voice line you answered is dialogue.
    private static func foldSettledQuestions(_ rows: [TimelineRow], into context: inout TimelineContext) {
        for (index, row) in rows.enumerated() {
            guard case let .item(question) = row, question.kind == .approval, question.approvalOutcome != nil else { continue }
            let agent = question.agent?.trimmed.lowercased() ?? ""
            let summary = question.text.trimmed.lowercased()
            let end = question.ts.addingTimeInterval(questionWindow)
            var folded: [TimelineRow] = []

            // The announcing push can land a moment before the item.
            var start = index
            while start > 0, index - start < 2, question.ts.timeIntervalSince(rows[start - 1].date) <= questionWindow { start -= 1 }

            var sawVoice = false
            for at in start ..< rows.count where at != index {
                let other = rows[at]
                if at > index, other.date > end { break }
                if case .day = other { if at > index { break } else { continue } }
                guard !context.absorbed.contains(other.id) else { continue }
                if announces(other, summary: summary, agent: agent) {
                    folded.append(other)
                } else if at > index, !sawVoice, isVoiceSpeech(other) {
                    // Only the first thing the voice said afterwards.
                    sawVoice = true
                    let text = speech(other).trimmed.lowercased()
                    let mentions = text == summary || (!agent.isEmpty && text.contains(agent))
                    let replied = rows[(at + 1)...].prefix { $0.date <= end }.contains(where: isUserSpeech)
                    if mentions, !replied { folded.append(other) }
                }
            }
            guard !folded.isEmpty else { continue }
            context.attached[row.id, default: []].append(contentsOf: folded)
            context.absorbed.formUnion(folded.map(\.id))
        }
    }

    /// A push that says the question: its words, or an interrupt from the
    /// same agent. Plain news about the agent is not folded away.
    private static func announces(_ row: TimelineRow, summary: String, agent: String) -> Bool {
        switch row {
        case let .item(item) where item.kind == .push:
            if item.text.trimmed.lowercased() == summary { return true }
            return item.needsUser && !agent.isEmpty && item.agent?.trimmed.lowercased() == agent
        case let .live(bubble):
            guard case let .push(needsUser) = bubble.kind else { return false }
            if bubble.text.trimmed.lowercased() == summary { return true }
            return needsUser && !agent.isEmpty && bubble.text.lowercased().contains(agent)
        default:
            return false
        }
    }

    private static func speech(_ row: TimelineRow) -> String {
        switch row {
        case let .item(item): return item.text
        case let .live(bubble): return bubble.text
        default: return ""
        }
    }
}

// MARK: - A settled question as one line

extension ConversationItem {
    /// "Codex's question (allow Computer Use to use Finder)": whose
    /// question, and its first few words.
    public var questionTitle: String {
        let who = agent?.trimmed.nonEmpty.map { "\($0)'s question" } ?? "A question"
        guard let topic = questionTopic else { return who }
        return "\(who) (\(topic))"
    }

    /// The question's first few words, without who is asking:
    /// "Codex is asking allow Computer Use to use Finder." becomes
    /// "allow Computer Use to use Finder".
    var questionTopic: String? {
        var words = text.trimmed
        while let last = words.last, ".?!:".contains(last) { words.removeLast() }
        if let agent = agent?.trimmed.nonEmpty, words.lowercased().hasPrefix(agent.lowercased()) {
            words = String(words.dropFirst(agent.count))
            for lead in [" is asking to ", " is asking ", " asks to ", " asks ", " wants to ", " needs to ", " needs "]
            where words.lowercased().hasPrefix(lead) {
                words = String(words.dropFirst(lead.count))
                break
            }
        }
        words = words.trimmed
        guard !words.isEmpty else { return nil }
        let limit = 36
        guard words.count > limit else { return words }
        let cut = words.prefix(limit)
        let atWord = cut.lastIndex(of: " ").map { String(cut[..<$0]) } ?? String(cut)
        return atWord + "…"
    }

    /// How a settled question ended, in a word, for its one line:
    /// "closed". "Closed" rather than "no longer needed" until the daemon
    /// says why a prompt left the screen (design doc, phase 3). An answer
    /// is `approvalOutcome` itself.
    var endedWord: String? {
        guard kind == .approval else { return nil }
        switch resolution ?? .open {
        case .open, .confirmed: return nil
        case .expired: return "expired"
        case .cancelled, .unknown: return "closed"
        case .replaced: return "replaced"
        }
    }

    /// The settled question's line: "Codex's question (Finder access) —
    /// closed", or the answer, "You approved — Yes · by tap".
    public var settledLine: String? {
        guard let outcome = approvalOutcome else { return nil }
        guard let ended = endedWord else { return outcome }
        return "\(questionTitle) — \(ended)"
    }
}

extension String {
    var nonEmpty: String? { isEmpty ? nil : self }
}
