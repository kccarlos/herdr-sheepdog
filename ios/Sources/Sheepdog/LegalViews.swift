import SheepdogKit
import SwiftUI

/// The privacy policy link, shared by About and the first-run Welcome
/// screen. A plain link to the public page; the app loads no web content
/// of its own, so Safari opens it.
struct PrivacyPolicyLink: View {
    var body: some View {
        Link(destination: SetupHelp.privacyURL) {
            Label {
                Text("Privacy policy").foregroundStyle(.primary)
            } icon: {
                Image(systemName: "hand.raised")
            }
        }
        .accessibilityHint("Opens the privacy policy in Safari")
        .accessibilityIdentifier("legal.privacy")
    }
}

/// One open-source package the app ships, with the licence text bundled in
/// `Licenses/`. Add a package here when `ios/project.yml` gains one.
struct LicenceEntry: Identifiable {
    let name: String
    let url: URL
    let resource: String
    var id: String { name }

    static let all: [LicenceEntry] = [
        LicenceEntry(name: "SwiftTerm", url: URL(string: "https://github.com/migueldeicaza/SwiftTerm")!, resource: "SwiftTerm"),
        LicenceEntry(name: "sheepdog", url: SetupHelp.projectURL, resource: "Sheepdog"),
    ]

    var text: String {
        guard let file = Bundle.main.url(forResource: resource, withExtension: "txt"),
              let text = try? String(contentsOf: file, encoding: .utf8) else {
            return "The licence text is at \(url.absoluteString)."
        }
        // The files are hard-wrapped at 70 columns; keep the paragraphs,
        // let the screen wrap the lines.
        return text.components(separatedBy: "\n\n")
            .map { $0.hasPrefix("Copyright") ? $0 : $0.replacingOccurrences(of: "\n", with: " ") }
            .joined(separator: "\n\n")
    }
}

/// Settings > About > Licences: every package the app is built from, with
/// its licence text.
struct LicencesView: View {
    var body: some View {
        List(LicenceEntry.all) { entry in
            NavigationLink {
                LicenceTextView(entry: entry)
            } label: {
                Text(entry.name)
            }
            .accessibilityIdentifier("legal.licence.\(entry.name)")
        }
        .navigationTitle("Licences")
        .navigationBarTitleDisplayMode(.inline)
    }
}

private struct LicenceTextView: View {
    let entry: LicenceEntry

    var body: some View {
        ScrollView {
            VStack(alignment: .leading, spacing: 12) {
                Link(entry.url.absoluteString, destination: entry.url)
                    .font(.footnote)
                    .tint(Theme.primaryText)
                Text(entry.text)
                    .font(.footnote)
                    .foregroundStyle(.secondary)
                    .fixedSize(horizontal: false, vertical: true)
                    .textSelection(.enabled)
                    .accessibilityIdentifier("legal.licence.text")
            }
            .padding()
        }
        .navigationTitle(entry.name)
        .navigationBarTitleDisplayMode(.inline)
    }
}
