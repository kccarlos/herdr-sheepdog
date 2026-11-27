import SheepdogKit
import SwiftUI

struct FactoryFixView: View {
    let fix: String
    let computerName: String?

    var body: some View {
        let (command, rest) = FactorySettingRow.parseFix(fix)
        let tail = command != nil && rest.hasPrefix(",") ? (String(rest.dropFirst()).trimmedOrNil ?? "") : rest
        let whereText = computerName?.trimmedOrNil.map { " on \($0)" } ?? ""
        VStack(alignment: .leading, spacing: 5) {
            if let command {
                Text("Fix, in a terminal\(whereText):").fontWeight(.semibold)
                FactoryCommandCopyRow(command: command)
                if !tail.isEmpty { Text(tail).foregroundStyle(Theme.muted) }
            } else if !tail.isEmpty {
                Text("Fix: ").fontWeight(.semibold) + Text(tail).foregroundStyle(Theme.muted)
            }
        }
        .font(.footnote)
    }
}
