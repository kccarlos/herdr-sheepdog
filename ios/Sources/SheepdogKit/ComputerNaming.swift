import Foundation

/// A computer's name as the daemon reports it is the Mac's macOS
/// ComputerName, which on a managed Mac is an asset tag ("M944QD4VY"). The
/// pairing screen then suggests a name a person would say
/// (docs/dev/design/conversation-timeline.md, section 10, decision 4).
public enum ComputerNaming {
    /// What the pairing screen suggests.
    public static let suggestion = "Work Mac"

    /// No space, and letters mixed with digits: a machine ID, not a name.
    public static func looksMachineMade(_ name: String) -> Bool {
        let name = name.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !name.isEmpty, !name.contains(where: \.isWhitespace) else { return false }
        return name.contains(where: \.isLetter) && name.contains(where: \.isNumber)
    }

    /// The suggestion for `name`, or nil when the name is fine as it is.
    public static func suggestion(for name: String) -> String? {
        looksMachineMade(name) ? suggestion : nil
    }
}
