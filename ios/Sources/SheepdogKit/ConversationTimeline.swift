import Foundation

/// What the conversation screen lists, top to bottom: the history (items
/// with server ids) and the live, id-less bubbles of this session, in time
/// order, with a date divider wherever the day changes and the daemon's
/// notices turned into quiet dividers.
public enum TimelineRow: Identifiable, Equatable, Sendable {
    /// "Today", "Yesterday", "Monday 21 September" — the start of that day.
    case day(Date)
    /// A notice item: "Sheepdog restarted · 5:42 PM".
    case divider(ConversationItem, title: String)
    /// A shepherd, dog, push or approval item.
    case item(ConversationItem)
    /// A bubble of this session that has no server id (yet).
    case live(ConversationReducer.Bubble)
    /// What the brain told the voice. `compact` when the dog's bubble just
    /// above already says the words (a daemon without a voice): only the
    /// detail and options are left to show.
    case brain(BrainActivity.Reply, compact: Bool)
    /// A push the voice is holding for a listener, or dropped.
    case held(BrainActivity.HeldPush)

    public var id: String {
        switch self {
        case let .day(date): return "day.\(Int(date.timeIntervalSince1970))"
        case let .divider(item, _): return "item.\(item.id)"
        case let .item(item): return "item.\(item.id)"
        case let .live(bubble): return "live.\(bubble.id)"
        case let .brain(reply, _): return "brain.\(reply.id)"
        case let .held(push): return "held.\(push.id)"
        }
    }

    public var date: Date {
        switch self {
        case let .day(date): return date
        case let .divider(item, _), let .item(item): return item.ts
        case let .live(bubble): return bubble.at
        case let .brain(reply, _): return reply.ts
        case let .held(push): return push.ts
        }
    }
}

public enum ConversationTimeline {
    /// Builds the rows. `calendar` and `timeFormatter` are parameters so
    /// tests pin the time zone and the locale.
    public static func rows(
        history: [ConversationItem],
        live: [ConversationReducer.Bubble],
        activity: BrainActivity = BrainActivity(),
        calendar: Calendar = .current,
        timeFormatter: DateFormatter = ConversationTimeline.defaultTimeFormatter
    ) -> [TimelineRow] {
        // A stable merge by time: the history is already in id (= time)
        // order and so are the live bubbles.
        var merged: [TimelineRow] = []
        merged.reserveCapacity(history.count + live.count + 8)
        var h = 0, l = 0
        var previousApproval: ConversationItem?
        var sawContent = false

        func emitItem(_ item: ConversationItem) {
            switch item.kind {
            case .notice, .unknown:
                merged.append(.divider(item, title: dividerTitle(item, afterContent: sawContent, timeFormatter: timeFormatter)))
            case .push:
                // The daemon announces a new approval as a push with the same
                // words; the approval card already says it.
                if let approval = previousApproval, approval.text.trimmed == item.text.trimmed { return }
                merged.append(.item(item))
                sawContent = true
            case .shepherd, .dog, .approval:
                merged.append(.item(item))
                sawContent = true
            }
            previousApproval = item.kind == .approval ? item : nil
        }

        while h < history.count || l < live.count {
            if l >= live.count || (h < history.count && history[h].ts <= live[l].at) {
                emitItem(history[h])
                h += 1
            } else {
                merged.append(.live(live[l]))
                previousApproval = nil
                sawContent = true
                l += 1
            }
        }

        weave(activity, into: &merged)

        // Date dividers where the day changes (and above the first row).
        var out: [TimelineRow] = []
        out.reserveCapacity(merged.count + 4)
        var currentDay: Date?
        for row in merged {
            let day = calendar.startOfDay(for: row.date)
            if day != currentDay {
                out.append(.day(day))
                currentDay = day
            }
            out.append(row)
        }
        return out
    }

    /// Puts the brain's replies and the held pushes in place: a reply the
    /// dog's bubble already says goes right under that bubble (and only
    /// when it has a detail or options to add), everything else by time,
    /// after rows of the same moment.
    static func weave(_ activity: BrainActivity, into rows: inout [TimelineRow]) {
        guard !activity.isEmpty else { return }
        var timed: [TimelineRow] = []
        for reply in activity.replies {
            if reply.isAlsoADogItem {
                guard reply.hasMore, let at = rows.firstIndex(where: { row in
                    if case let .item(item) = row { return item.id == reply.id && item.kind == .dog }
                    return false
                }) else { continue }
                rows.insert(.brain(reply, compact: true), at: at + 1)
            } else {
                timed.append(.brain(reply, compact: false))
            }
        }
        timed += activity.held.filter(\.isShown).map(TimelineRow.held)
        for row in timed.sorted(by: { $0.date < $1.date }) {
            let at = rows.firstIndex { $0.date > row.date } ?? rows.endIndex
            rows.insert(row, at: at)
        }
    }

    /// "Sheepdog restarted · 5:42 PM".
    public static func dividerTitle(_ item: ConversationItem, afterContent: Bool, timeFormatter: DateFormatter = defaultTimeFormatter) -> String {
        var text = item.text.trimmed
        if text == "Sheepdog started." || text == "Sheepdog started" {
            text = afterContent ? "Sheepdog restarted" : "Sheepdog started"
        }
        while text.hasSuffix(".") { text.removeLast() }
        if text.isEmpty { text = "Notice" }
        return "\(text) · \(timeFormatter.string(from: item.ts))"
    }

    /// "Today", "Yesterday", or "Monday 21 September" (with the year when
    /// it is not this one).
    public static func dayTitle(_ day: Date, now: Date = Date(), calendar: Calendar = .current, locale: Locale = .current) -> String {
        if calendar.isDate(day, inSameDayAs: now) { return "Today" }
        if let yesterday = calendar.date(byAdding: .day, value: -1, to: now), calendar.isDate(day, inSameDayAs: yesterday) {
            return "Yesterday"
        }
        let f = DateFormatter()
        f.calendar = calendar
        f.timeZone = calendar.timeZone
        f.locale = locale
        let sameYear = calendar.component(.year, from: day) == calendar.component(.year, from: now)
        f.setLocalizedDateFormatFromTemplate(sameYear ? "EEEEdMMMM" : "EEEEdMMMMyyyy")
        return f.string(from: day)
    }

    public static let defaultTimeFormatter: DateFormatter = {
        let f = DateFormatter()
        f.dateStyle = .none
        f.timeStyle = .short
        return f
    }()
}

/// A row as the conversation screen draws it: a timeline row, or a run of
/// the daemon's pushes and notices folded into one "N updates" row that
/// opens on a tap. What the shepherd and the dog said is never folded.
public enum ConversationDisplayRow: Identifiable, Equatable, Sendable {
    case row(TimelineRow)
    /// Two or more consecutive pushes / notices, oldest first. The id is
    /// the first member's, so the row keeps its identity (and whether it
    /// is open) as later updates join it.
    case updates(id: String, rows: [TimelineRow])

    public var id: String {
        switch self {
        case let .row(row): return row.id
        case let .updates(id, _): return id
        }
    }
}

extension ConversationTimeline {
    /// A run needs at least this many updates to fold.
    public static let minimumGroup = 2

    /// Folds every run of consecutive daemon updates — pushes that are
    /// news (not a question that needs the shepherd), local or daemon
    /// notices and their dividers — into one `.updates` row. Speech, the
    /// approvals, the brain's replies, held pushes, day dividers and the
    /// restart / new-conversation dividers break a run and stay visible, and so does any row in `keepVisible` (the
    /// rows folded to a pointer at the pinned question).
    public static func grouped(_ rows: [TimelineRow], keepVisible: Set<String> = []) -> [ConversationDisplayRow] {
        var out: [ConversationDisplayRow] = []
        out.reserveCapacity(rows.count)
        var run: [TimelineRow] = []

        func closeRun() {
            if run.count >= minimumGroup {
                out.append(.updates(id: "updates.\(run[0].id)", rows: run))
            } else {
                out.append(contentsOf: run.map(ConversationDisplayRow.row))
            }
            run.removeAll()
        }

        for row in rows {
            if isUpdate(row), !keepVisible.contains(row.id) {
                run.append(row)
            } else {
                closeRun()
                out.append(.row(row))
            }
        }
        closeRun()
        return out
    }

    /// Whether a row is a daemon update that may fold.
    public static func isUpdate(_ row: TimelineRow) -> Bool {
        switch row {
        case let .divider(item, _):
            return !isBoundary(item)
        case let .item(item):
            switch item.kind {
            case .push: return !item.needsUser
            case .notice, .unknown: return !isBoundary(item)
            case .shepherd, .dog, .approval: return false
            }
        case let .live(bubble):
            switch bubble.kind {
            case .notice: return true
            case let .push(needsUser): return !needsUser
            case .speech: return false
            }
        case .day, .brain, .held:
            return false
        }
    }

    /// A notice that marks where something starts over — the daemon
    /// (re)started, or "New conversation" — stays a visible divider, like
    /// the day dividers.
    public static func isBoundary(_ item: ConversationItem) -> Bool {
        if item.subkind == "reset" { return true }
        let text = item.text.trimmingCharacters(in: .whitespacesAndNewlines)
        return text == "Sheepdog started." || text == "Sheepdog started"
    }

    /// The one line a folded run shows: the newest update's words.
    public static func updateText(_ row: TimelineRow) -> String {
        switch row {
        case let .divider(_, title): return title
        case let .item(item): return item.text.trimmingCharacters(in: .whitespacesAndNewlines)
        case let .live(bubble): return bubble.text.trimmingCharacters(in: .whitespacesAndNewlines)
        default: return ""
        }
    }

    /// "3 updates".
    public static func updatesTitle(_ count: Int) -> String {
        count == 1 ? "1 update" : "\(count) updates"
    }
}
