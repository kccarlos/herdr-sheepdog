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
    /// A user, dog, push or approval item.
    case item(ConversationItem)
    /// A bubble of this session that has no server id (yet).
    case live(ConversationReducer.Bubble)
    /// What the orchestrator told the voice. `compact` when the voice's bubble just
    /// above already says the words (a daemon without a voice): only the
    /// detail and options are left to show.
    case brain(BrainActivity.Reply, compact: Bool)
    /// A push the voice is holding for a listener, or dropped.
    case held(BrainActivity.HeldPush)
    /// A card the socket delivered this session that the history does not
    /// have yet (a history card is an `.item` of kind `.card`).
    case card(ScreenCard)

    public var id: String {
        switch self {
        case let .day(date): return "day.\(Int(date.timeIntervalSince1970))"
        case let .divider(item, _): return "item.\(item.id)"
        case let .item(item): return "item.\(item.id)"
        case let .live(bubble): return "live.\(bubble.id)"
        case let .brain(reply, _): return "brain.\(reply.id)"
        case let .held(push): return "held.\(push.id)"
        case let .card(card): return "card.\(card.cardID)"
        }
    }

    public var date: Date {
        switch self {
        case let .day(date): return date
        case let .divider(item, _), let .item(item): return item.ts
        case let .live(bubble): return bubble.at
        case let .brain(reply, _): return reply.ts
        case let .held(push): return push.ts
        case let .card(card): return card.at ?? .distantFuture
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
        cards: [ScreenCard] = [],
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
        var lastVersion: String?

        func emitItem(_ item: ConversationItem) {
            switch item.kind {
            case .notice, .unknown:
                merged.append(.divider(item, title: dividerTitle(item, afterContent: sawContent, previousVersion: lastVersion, timeFormatter: timeFormatter)))
                if DaemonNotice(item) == .restarted, let version = item.version?.trimmed, !version.isEmpty { lastVersion = version }
            case .push:
                // The daemon announces a new approval as a push with the same
                // words; the approval card already says it.
                if let approval = previousApproval, approval.text.trimmed == item.text.trimmed { return }
                merged.append(.item(item))
                sawContent = true
            case .shepherd, .dog, .approval, .card:
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
        // Live cards by time, after rows of the same moment; one the
        // history already has is shown once, as its item.
        let historyCards = Set(history.compactMap { $0.kind == .card ? $0.cardID : nil })
        for card in cards where !historyCards.contains(card.cardID) {
            let row = TimelineRow.card(card)
            let at = merged.firstIndex { $0.date > row.date } ?? merged.endIndex
            merged.insert(row, at: at)
        }

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

    /// Puts the orchestrator's replies and the held pushes in place: a reply the
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

    /// "Sheepdog restarted · 5:42 PM", or "Sheepdog updated to v0.9.1 ·
    /// 5:42 PM" when the daemon says a version and the one before it (the
    /// previous start in the history) said another. An older daemon sends
    /// no version, so its restart is just "restarted".
    public static func dividerTitle(
        _ item: ConversationItem,
        afterContent: Bool,
        previousVersion: String? = nil,
        timeFormatter: DateFormatter = defaultTimeFormatter
    ) -> String {
        var text = item.text.trimmed
        if DaemonNotice(item) == .restarted {
            text = afterContent ? "Sheepdog restarted" : "Sheepdog started"
            if let version = item.version?.trimmed, !version.isEmpty, let previous = previousVersion, previous != version {
                text = "Sheepdog updated to \(displayVersion(version))"
            }
        }
        while text.hasSuffix(".") { text.removeLast() }
        if text.isEmpty { text = "Notice" }
        return "\(text) · \(timeFormatter.string(from: item.ts))"
    }

    /// "v0.9.1" for "0.9.1"; a name such as "dev" is left as it is.
    static func displayVersion(_ version: String) -> String {
        version.first?.isNumber == true ? "v\(version)" : version
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

