import SheepdogKit
import SwiftUI
import UIKit

struct FactoryFixView: View {
    let fix: String
    let computerName: String
    @State private var copied = false

    var body: some View {
        let (command, rest) = FactorySettingRow.parseFix(fix)
        VStack(alignment: .leading, spacing: 6) {
            if let command {
                Text("Fix, in a terminal on \(computerName):")
                    .font(.caption.weight(.semibold)).foregroundStyle(Theme.muted)
                OptionFlow(spacing: 8) {
                    Text(command).font(.footnote.monospaced()).foregroundStyle(Theme.ink)
                    Button(copied ? "Copied" : "Copy") {
                        UIPasteboard.general.string = command
                        copied = true
                        AccessibilityNotification.Announcement("Copied").post()
                        Task {
                            try? await Task.sleep(for: .seconds(2))
                            copied = false
                        }
                    }
                    .buttonStyle(.bordered).controlSize(.large)
                    .accessibilityLabel(copied ? "Copied" : "Copy the command \(command)")
                }
                .padding(8)
                .frame(maxWidth: .infinity, alignment: .leading)
                .background(Theme.fill, in: .rect(cornerRadius: 8))
            }
            if !rest.isEmpty {
                Text(command == nil ? "Fix: \(rest)" : rest)
                    .font(.caption).foregroundStyle(Theme.muted)
            }
        }
    }
}
