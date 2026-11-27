import Foundation

/// One CLI/model/effort candidate in a `models.<role>` setting value.
public struct FactoryModelCandidate: Equatable, Sendable {
    public var cli: String
    public var model: String
    public var effort: String
    public static let efforts = ["low", "medium", "high"]

    public init(cli: String, model: String, effort: String = "medium") {
        self.cli = cli
        self.model = model
        self.effort = effort
    }

    public static func parse(_ raw: String) -> [FactoryModelCandidate] {
        raw.split(separator: ",").compactMap { item in
            let trimmed = item.trimmingCharacters(in: .whitespacesAndNewlines)
            guard !trimmed.isEmpty else { return nil }
            let parts = trimmed.split(separator: "/", omittingEmptySubsequences: false)
                .map { $0.trimmingCharacters(in: .whitespaces) }
            guard parts.count >= 3 else { return FactoryModelCandidate(cli: "", model: trimmed) }
            let model = parts.dropFirst().dropLast().joined(separator: "/")
            let effort = parts.last?.nonEmpty ?? "medium"
            return FactoryModelCandidate(cli: parts[0], model: model, effort: effort)
        }
    }

    public static func format(_ candidates: [FactoryModelCandidate], separator: String = ", ") -> String {
        candidates
            .map { "\($0.cli.trimmed)/\($0.model.trimmed)/\($0.effort.trimmed)" }
            .joined(separator: separator)
    }

    public static func ordinal(_ index: Int) -> String {
        let names = ["first", "second", "third", "fourth", "fifth", "sixth", "seventh", "eighth", "ninth"]
        return index < names.count ? "\(names[index]) choice" : "#\(index + 1) choice"
    }
}
