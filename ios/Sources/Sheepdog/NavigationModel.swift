import Foundation
import SwiftUI

/// Where the main screen is and where it is asked to go: the pane it shows,
/// the agent the Agents pane scrolls to, the navigation stack, the screen
/// Settings opens on, and the agent window a card asked for. Split out of
/// `SessionViewModel` (phase 8), so switching panes or pushing a window
/// re-renders the views that read these and nothing else. Views bind to
/// them with `Bindable(model.navigation)`.
@MainActor
@Observable
final class NavigationModel {
    /// Which pane the main screen shows; a notification tap switches it.
    var pane: MainPane = .conversation
    /// The agent the Agents pane should scroll to (a notification tap).
    var focusedAgentPaneID: String?
    /// The main screen's navigation stack (a card's "Open window" pushes
    /// onto it).
    var path = NavigationPath()
    /// Where Settings opens: nil for its top, `.models` from Health's
    /// "Change in Settings ›". Consumed (reset) by SettingsView.
    var settingsRoute: SettingsRoute?
    /// An agent's window the session asked for (a card's Open window, a
    /// Live Activity row): the presenting window's layout opens it (a push
    /// on iPhone, the detail on iPad), then clears it.
    var windowRequest: WindowRequest?
}
