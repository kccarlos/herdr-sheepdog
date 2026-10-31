import Foundation
import SheepdogKit
@testable import SheepdogMac

/// One fake per platform service protocol of the app layer.

final class FakeLocator: DaemonLocating {
    var endpoint: DaemonEndpoint
    init(host: String = "127.0.0.1", port: Int = 1) {
        endpoint = DaemonEndpoint(host: host, port: port)
    }
}

final class FakeLoginItem: LoginItemControlling {
    var isEnabled = false
    var failure: Error?
    private(set) var calls: [Bool] = []

    func setEnabled(_ enabled: Bool) throws {
        calls.append(enabled)
        if let failure { throw failure }
        isEnabled = enabled
    }
}

final class FakeLauncher: ProcessLaunching {
    struct Launch: Equatable {
        var executable: String
        var arguments: [String]
        var path: String?
    }

    var failure: Error?
    private(set) var launches: [Launch] = []

    func launch(_ executable: String, arguments: [String], environment: [String: String]) throws {
        launches.append(Launch(executable: executable, arguments: arguments, path: environment["PATH"]))
        if let failure { throw failure }
    }
}

final class FakePanel: PanelOpening {
    var failure: Error?
    private(set) var opened = 0

    func openPanel() throws {
        opened += 1
        if let failure { throw failure }
    }
}

final class FakeLog: AppLogging {
    private(set) var lines: [String] = []
    func log(_ message: String) { lines.append(message) }
}

final class FakeStatusSource: DaemonStatusSource {
    var onChange: (@MainActor (DaemonState?) -> Void)?
    private(set) var running = false

    func start() { running = true }
    func stop() { running = false }
}

struct Boom: Error {}
