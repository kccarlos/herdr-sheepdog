import SwiftUI

private struct SplashShowingKey: EnvironmentKey {
    static let defaultValue = false
}

extension EnvironmentValues {
    /// The launch splash is still on screen: the first-run sheet waits for
    /// it to fade rather than sliding over it.
    var splashShowing: Bool {
        get { self[SplashShowingKey.self] }
        set { self[SplashShowingKey.self] = newValue }
    }
}
