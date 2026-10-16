import SwiftUI
import WidgetKit

@main
struct SheepdogWidgetsBundle: WidgetBundle {
    var body: some Widget {
        VoiceSessionLiveActivity()
        if #available(iOSApplicationExtension 18.0, *) {
            TalkToSheepdogControl()
        }
    }
}

/// Control Center, the lock screen's control slots and the Action Button:
/// one button that runs "Talk to Sheepdog".
@available(iOSApplicationExtension 18.0, *)
struct TalkToSheepdogControl: ControlWidget {
    var body: some ControlWidgetConfiguration {
        StaticControlConfiguration(kind: "com.github.kccarlos.herdr-sheepdog-ios.talk") {
            ControlWidgetButton(action: TalkToSheepdogIntent()) {
                Label("Talk to Sheepdog", systemImage: "waveform")
            }
        }
        .displayName("Talk to Sheepdog")
        .description("Opens Sheepdog and starts listening.")
    }
}
