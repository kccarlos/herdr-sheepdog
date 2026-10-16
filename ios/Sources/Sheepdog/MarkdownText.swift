import SheepdogKit
import SwiftUI

/// Markdown as one `Text` (`MarkdownRenderer`): inline styles, lists and
/// fenced code, headings as bold body text. Being a single `Text`, it takes
/// `lineLimit`, `textSelection` and VoiceOver like any other.
struct MarkdownText: View {
    let source: String
    private let rendered: AttributedString

    init(_ source: String) {
        self.source = source
        rendered = MarkdownRenderer.render(source)
    }

    var body: some View {
        Text(rendered)
    }
}
