import AVFoundation
import XCTest
@testable import Sheepdog
@testable import SheepdogKit

/// The iOS layer's mapping from an `AVAudioSession.Port` to an `AudioRoute`
/// (`AudioRoute+Port.swift`). There is no audio session on macOS, so this
/// file is not in the macOS test target.
final class AudioRoutePortTests: XCTestCase {
    func testTheRouteGlyph() {
        XCTAssertEqual(AudioRoute(port: .builtInSpeaker), .speaker)
        XCTAssertEqual(AudioRoute(port: .builtInReceiver), .receiver)
        XCTAssertEqual(AudioRoute(port: .headphones), .headphones)
        XCTAssertEqual(AudioRoute(port: .usbAudio), .headphones)
        XCTAssertEqual(AudioRoute(port: .bluetoothA2DP), .bluetooth)
        XCTAssertEqual(AudioRoute(port: .bluetoothHFP), .bluetooth)
        XCTAssertEqual(AudioRoute(port: .carAudio), .car)
        XCTAssertEqual(AudioRoute(port: .airPlay), .airPlay)
        XCTAssertEqual(AudioRoute(port: AVAudioSession.Port(rawValue: "SomethingNew")), .speaker)
    }
}
