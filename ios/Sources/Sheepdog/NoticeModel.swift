import Foundation
import SheepdogKit
import SwiftUI

/// The things the app tells the user that are not the conversation: the
/// banner on the main screen, the "Noted" line after a feedback mark, the
/// daemon's update notice, and the scan-again problems. Split out of
/// `SessionViewModel` (phase 8), so a banner coming or going re-renders the
/// views that show it and nothing else.
@MainActor
@Observable
final class NoticeModel {
    var banner: SessionViewModel.Banner?
    /// "Noted" (or why a feedback mark did not land), shown for a moment.
    private(set) var feedbackNotice: String?
    /// `/api/state`'s update notice (Settings shows one line for it); nil
    /// on a daemon too old to report one.
    private(set) var update: UpdateInfo?
    /// Why the app must scan again, while it must.
    private(set) var pairingProblem: PairingProblem?
    /// The same, for Settings ▸ Connection when it is open.
    var daemonNoticeProblem: PairingProblem?
    /// Why Settings ▸ Daemon was opened for the user (a refused token, a
    /// pairing code nothing answered), shown there where the fix is. A
    /// banner on the main screen would sit under the Settings sheet.
    /// Consumed by DaemonSettingsView.
    var daemonNotice: SessionViewModel.Probe?

    /// Assigns only when it changed (an `@Observable` setter notifies on an
    /// equal value too).
    func setUpdate(_ next: UpdateInfo?) {
        if update != next { update = next }
    }

    func setPairingProblem(_ problem: PairingProblem?) {
        if pairingProblem != problem { pairingProblem = problem }
    }

    /// The computer changed: the update line was the old one's.
    func clearForSwitch() {
        update = nil
    }

    /// A line for 1.8 seconds, spoken to VoiceOver too.
    func showFeedback(_ text: String) {
        feedbackNotice = text
        AccessibilityNotification.Announcement(text).post()
        Task { [weak self] in
            try? await Task.sleep(nanoseconds: 1_800_000_000)
            if self?.feedbackNotice == text { self?.feedbackNotice = nil }
        }
    }
}
