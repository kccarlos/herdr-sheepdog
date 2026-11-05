import SheepdogKit
import SwiftUI

/// One of the browser's quiet screens: nothing yet, gone, hidden, offline.
/// A plain headline, one sentence with the fix, and the one action that
/// helps (docs: every screen has its empty and error state). Not a scroll
/// view itself: callers wrap it (`scrolling`) where it is the whole screen.
struct BrowserState: View {
    var symbol: String?
    /// The sleeping mascot instead of a symbol (the computer is offline).
    var sleeping = false
    let title: String
    let message: String
    /// What to check, when the computer does not answer.
    var steps: [String] = []
    var action: Action?
    let identifier: String

    struct Action {
        let title: String
        let identifier: String
        let run: () -> Void
    }

    @Environment(\.dynamicTypeSize) private var dynamicTypeSize
    @ScaledMetric(relativeTo: .caption) private var stepDiameter = 20.0

    /// At the accessibility sizes the words already fill the screen, so the
    /// symbol, the mascot and the spacing give way: the title, the sentence
    /// and the action all stay in reach.
    private var compact: Bool { dynamicTypeSize.isAccessibilitySize }

    var body: some View {
        VStack(spacing: compact ? 6 : 10) {
            if sleeping {
                MascotCard(name: "MascotSleeping", width: compact ? 80 : 150)
            } else if let symbol {
                Image(systemName: symbol)
                    .font(.system(size: compact ? 26 : 40, weight: .light))
                    .foregroundStyle(Theme.muted)
                    .accessibilityHidden(true)
            }
            Text(title)
                .font(.system(compact ? .title3 : .title2, design: .rounded, weight: .bold))
                .multilineTextAlignment(.center)
                .accessibilityAddTraits(.isHeader)
            if !message.isEmpty {
                Text(message)
                    .font(.subheadline)
                    .foregroundStyle(Theme.muted)
                    .multilineTextAlignment(.center)
            }
            if !steps.isEmpty {
                VStack(alignment: .leading, spacing: 8) {
                    ForEach(Array(steps.enumerated()), id: \.offset) { index, step in
                        HStack(alignment: .firstTextBaseline, spacing: 10) {
                            Text("\(index + 1)")
                                .font(.caption.weight(.semibold))
                                .foregroundStyle(Theme.muted)
                                .frame(width: stepDiameter, height: stepDiameter)
                                .background(Circle().fill(Theme.fill))
                                .accessibilityHidden(true)
                            Text(step).font(.subheadline)
                        }
                        .accessibilityElement(children: .combine)
                        .accessibilityLabel("Step \(index + 1), \(step)")
                    }
                }
                .padding(14)
                .frame(maxWidth: .infinity, alignment: .leading)
                .background(Theme.card, in: RoundedRectangle(cornerRadius: Theme.Radius.card, style: .continuous))
                .overlay(RoundedRectangle(cornerRadius: Theme.Radius.card, style: .continuous).strokeBorder(Theme.line, lineWidth: 1))
                .padding(.top, 4)
            }
            if let action {
                Button(action.title, action: action.run)
                    .buttonStyle(PillButtonStyle(fullWidth: false))
                    .padding(.top, 6)
                    .accessibilityIdentifier(action.identifier)
            }
        }
        .padding(.horizontal, compact ? 20 : 28)
        .padding(.vertical, compact ? 12 : 32)
        .frame(maxWidth: 520)
        .frame(maxWidth: .infinity)
        .accessibilityElement(children: .contain)
        .accessibilityIdentifier(identifier)
    }

    /// The state as a whole screen, scrolling at the large text sizes.
    func scrolling() -> some View {
        ScrollView { self }.scrollBounceBehavior(.basedOnSize)
    }
}

/// The words of a failed read, as a `BrowserState`.
enum BrowserWords {
    static let offlineSteps = ["Is the Mac awake?", "Is sheepdog running there?", "Are this phone and the Mac on the same Tailscale network?"]

    /// A failure of a list or a file.
    /// - Parameters:
    ///   - subject: what was being opened.
    ///   - root: the folder's name ("shop"), for "outside shop".
    ///   - size: the file's size, once known.
    ///   - retry: reads again.
    ///   - back: leaves the file or goes up to the folder; nil where there
    ///     is nothing to go back to.
    ///   - changes: shows Changes, for a daemon without files.
    static func state(
        _ failure: FilesFailure, subject: FileProblem.Subject, root: String, name: String = "This file", size: Int? = nil,
        retry: @escaping () -> Void, back: (title: String, run: () -> Void)? = nil, changes: (() -> Void)? = nil
    ) -> BrowserState {
        switch failure {
        case let .problem(problem):
            let words = problem.words(subject: subject, root: root, name: name, size: size)
            var action: BrowserState.Action?
            if problem == .olderDaemon || problem == .filesOff, let changes {
                action = .init(title: "Show Changes", identifier: "browser.state.changes", run: changes)
            } else if problem.canRetry {
                action = .init(title: problem == .denied ? "Retry" : "Try again", identifier: "browser.state.retry", run: retry)
            } else if let back {
                action = .init(title: back.title, identifier: "browser.state.back", run: back.run)
            }
            return BrowserState(symbol: words.symbol, title: words.title, message: words.message, action: action, identifier: "browser.state.\(problem.rawValue)")
        case let .request(request):
            if failure.isOffline {
                return BrowserState(
                    sleeping: true, title: request.title, message: "",
                    steps: offlineSteps, action: .init(title: "Try again", identifier: "browser.state.retry", run: retry),
                    identifier: "browser.state.offline"
                )
            }
            return BrowserState(
                symbol: "exclamationmark.triangle", title: request.title, message: request.message,
                action: .init(title: "Try again", identifier: "browser.state.retry", run: retry), identifier: "browser.state.failed"
            )
        }
    }
}
