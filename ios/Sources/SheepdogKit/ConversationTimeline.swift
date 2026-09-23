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

    public var id: String {
        switch self {
        case let .day(date): return "day.\(Int(date.timeIntervalSince1970))"
        case let .divider(item, _): return "item.\(item.id)"
        case let .item(item): return "item.\(item.id)"
        case let .live(bubble): return "live.\(bubble.id)"
        }
    }

    public var date: Date {
        switch self {
        case let .day(date): return date
        case let .divider(item, _), let .item(item): return item.ts
        case let .live(bubble): return bubble.at
        }
    }
}

public enum ConversationTimeline {
    /// Builds the rows. `calendar` and `timeFormatter` are parameters so
    /// tests pin the time zone and the locale.
    public static func rows(
        history: [ConversationItem],
        live: [ConversationReducer.Bubble],
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
