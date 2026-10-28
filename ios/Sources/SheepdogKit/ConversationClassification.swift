import Foundation

/// The six kinds of thing the conversation can show (docs/dev/design/
/// conversation-timeline.md, section 4). Every row has exactly one; the
/// timeline, the grouping and "Show technical events" all read it from
/// `ConversationTimeline.classify`.
public enum TimelineCategory: String, CaseIterable, Sendable {
    /// You and the voice: speech and the orchestrator's replies.
    case dialogue
    /// An open question, or a push that interrupts.
    case needsYou
    /// What agents did: finished, found, decided, missed.
    case outcome
    /// Where you are: days, restarts, a new conversation, the computer.
    case context
    /// Plumbing: reconnects, audio routes, the voice closing.
    case system
    /// Rows the screen adds itself (the folded group).
    case screen

    /// The category's one icon.
    public var symbol: String {
        switch self {
        case .dialogue: return "bubble.left"
        case .needsYou: return "arrow.down.circle"
        case .outcome: return "terminal"
        case .context: return "location"
        case .system: return "gearshape"
        case .screen: return "ellipsis.circle"
        }
    }

    /// For the technical view's per-row caption.
    public var title: String {
        switch self {
        case .dialogue: return "Dialogue"
        case .needsYou: return "Needs you"
        case .outcome: return "Agent update"
        case .context: return "Context"
        case .system: return "System"
        case .screen: return "Screen"
        }
    }
}

/// What the timeline does with one row.
public struct RowClass: Equatable, Sendable {
    public var category: TimelineCategory
    /// Shown without "Show technical events". Only plumbing is ever hidden.
    public var shownByDefault = true
    /// May fold into an "N agent updates" row with its neighbours.
    public var folds = false
    /// A run of updates continues across it (a restart divider).
    public var bridgesRuns = false

    static func plain(_ category: TimelineCategory) -> RowClass { RowClass(category: category) }
}

extension TimelineRow {
    public var classification: RowClass { ConversationTimeline.classify(self) }
    public var category: TimelineCategory { classification.category }
}

/// A row as the conversation screen draws it: a timeline row, or a run of
/// the agents' updates folded into one "N agent updates" row that opens on
/// a tap. What the user and the voice said, and the questions, are never
/// folded.
public enum ConversationDisplayRow: Identifiable, Equatable, Sendable {
    case row(TimelineRow)
    /// Three or more consecutive agent updates, oldest first, plus any
    /// restart divider between them. The id is the first update's, so the
    /// row keeps its identity (and whether it is open) as later updates
    /// join it.
    case updates(id: String, rows: [TimelineRow])

    public var id: String {
        switch self {
        case let .row(row): return row.id
        case let .updates(id, _): return id
        }
    }

    public var category: TimelineCategory {
        switch self {
        case let .row(row): return row.category
        case .updates: return .screen
        }
    }
}

extension ConversationTimeline {
    /// A run needs at least this many updates to fold.
    public static let minimumGroup = 3

    /// The one table of row kind → category, and so of what is shown,
    /// folded and hidden.
    public static func classify(_ row: TimelineRow) -> RowClass {
        switch row {
        case .day:
            return .plain(.context)
        case let .divider(item, _), let .item(item):
            return classify(item)
        case let .live(bubble):
            return classify(bubble)
        case .brain:
            return .plain(.dialogue)
        case .held, .card:
            return .plain(.outcome)
        }
    }

    public static func classify(_ item: ConversationItem) -> RowClass {
        switch item.kind {
        case .shepherd, .dog:
            return .plain(.dialogue)
        case .push:
            return classifyPush(needsUser: item.needsUser)
        case .approval:
            // Open: a question. Settled: what happened to it.
            return .plain(item.approvalOutcome == nil ? .needsYou : .outcome)
        case .card:
            return .plain(.outcome)
        case .notice, .unknown:
            if item.isResetNotice { return .plain(.context) }
            if isRestart(item) { return RowClass(category: .context, bridgesRuns: true) }
            // The daemon's reconnects, context losses and shutdowns.
            return RowClass(category: .system, shownByDefault: false)
        }
    }

    public static func classify(_ bubble: ConversationReducer.Bubble) -> RowClass {
        switch bubble.kind {
        case .speech:
            return .plain(.dialogue)
        case let .push(needsUser):
            return classifyPush(needsUser: needsUser)
        case .notice:
            // This phone's own notices, text-matched until they are tagged
            // (design doc, phase 3). One not recognised stays visible: it
            // may be asking for a tap.
            if bubble.text.hasPrefix("Talking to ") { return .plain(.context) }
            return RowClass(category: .system, shownByDefault: !isInformationalNotice(bubble.text))
        }
    }

    private static func classifyPush(needsUser: Bool) -> RowClass {
        needsUser ? .plain(.needsYou) : RowClass(category: .outcome, folds: true)
    }

    /// Audio notices that ask nothing of the user.
    private static func isInformationalNotice(_ text: String) -> Bool {
        text.hasPrefix("Audio moved to ") || text == "The audio route changed."
            || text == "Paused for a call or Siri." || text == "Listening again."
    }

    /// The rows without the plumbing, and without a day heading left over
    /// it. With `showTechnical` every row comes back unchanged.
    public static func visibleRows(_ rows: [TimelineRow], showTechnical: Bool) -> [TimelineRow] {
        guard !showTechnical else { return rows }
        let shown = rows.filter { classify($0).shownByDefault }
        return shown.enumerated().compactMap { i, row in
            guard case .day = row else { return row }
            guard i + 1 < shown.count else { return nil }
            if case .day = shown[i + 1] { return nil }
            return row
        }
    }

    /// Folds every run of three or more consecutive agent updates into one
    /// `.updates` row. Only rows that fold join a run, so a run never mixes
    /// categories; speech, questions, cards, other dividers and day
    /// dividers end it and stay visible, and so does any row in
    /// `keepVisible` (the rows folded to a pointer at the pinned question).
    /// A restart divider does not end a run: it sits inside the group, or
    /// after it when no update follows.
    public static func grouped(_ rows: [TimelineRow], keepVisible: Set<String> = []) -> [ConversationDisplayRow] {
        var out: [ConversationDisplayRow] = []
        out.reserveCapacity(rows.count)
        var run: [TimelineRow] = []
        var updates = 0

        func closeRun() {
            var tail: [TimelineRow] = []
            while let last = run.last, !classify(last).folds { tail.insert(run.removeLast(), at: 0) }
            if updates >= minimumGroup {
                out.append(.updates(id: "updates.\(run[0].id)", rows: run))
            } else {
                out.append(contentsOf: run.map(ConversationDisplayRow.row))
            }
            out.append(contentsOf: tail.map(ConversationDisplayRow.row))
            run.removeAll()
            updates = 0
        }

        for row in rows {
            let kind = classify(row)
            if kind.folds, !keepVisible.contains(row.id) {
                run.append(row)
                updates += 1
            } else if kind.bridgesRuns, !run.isEmpty {
                run.append(row)
            } else {
                closeRun()
                out.append(.row(row))
            }
        }
        closeRun()
        return out
    }

    /// How many updates a folded run holds (its restart dividers do not
    /// count).
    public static func updateCount(_ rows: [TimelineRow]) -> Int {
        rows.filter { classify($0).folds }.count
    }

    /// The daemon's "Sheepdog started." notice.
    static func isRestart(_ item: ConversationItem) -> Bool {
        let text = item.text.trimmingCharacters(in: .whitespacesAndNewlines)
        return text == "Sheepdog started." || text == "Sheepdog started"
    }

    /// The lines a folded run shows: its newest two updates, oldest first.
    public static func previewLines(_ rows: [TimelineRow]) -> [String] {
        rows.filter { classify($0).folds }.suffix(2).map(updateText).filter { !$0.isEmpty }
    }

    /// One update's words.
    public static func updateText(_ row: TimelineRow) -> String {
        switch row {
        case let .divider(_, title): return title
        case let .item(item): return item.text.trimmingCharacters(in: .whitespacesAndNewlines)
        case let .live(bubble): return bubble.text.trimmingCharacters(in: .whitespacesAndNewlines)
        default: return ""
        }
    }

    /// "3 agent updates".
    public static func updatesTitle(_ count: Int) -> String {
        count == 1 ? "1 agent update" : "\(count) agent updates"
    }
}

extension TimelineRow {
    /// The row's source as pretty JSON, for "Copy raw JSON" in the
    /// technical view: the daemon's item as the app holds it, or the live
    /// bubble, the orchestrator's payload, the held push or the card.
    public var rawJSON: String {
        let encoder = JSONEncoder()
        encoder.outputFormatting = [.prettyPrinted, .sortedKeys, .withoutEscapingSlashes]
        encoder.dateEncodingStrategy = .iso8601
        func json(_ value: some Encodable) -> String {
            (try? encoder.encode(value)).map { String(decoding: $0, as: UTF8.self) } ?? ""
        }
        func time(_ date: Date) -> String { ISO8601DateFormatter.sheepdog.string(from: date) }
        switch self {
        case let .day(date):
            return json(["day": time(date)])
        case let .divider(item, _), let .item(item):
            return json(item)
        case let .live(bubble):
            return json([
                "id": String(bubble.id), "speaker": "\(bubble.speaker)", "kind": "\(bubble.kind)",
                "text": bubble.text, "final": "\(bubble.isFinal)", "at": time(bubble.at),
            ])
        case let .brain(reply, _):
            return reply.raw
        case let .held(push):
            return json([
                "key": push.key, "id": String(push.id), "ts": time(push.ts),
                "text": push.text, "urgency": push.urgency ?? "", "fate": "\(push.fate)",
            ])
        case let .card(card):
            return json(card)
        }
    }
}
