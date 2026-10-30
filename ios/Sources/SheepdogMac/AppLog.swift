import os

/// Where the app writes what it did, so a test can read it.
public protocol AppLogging {
    func log(_ message: String)
}

public struct AppLog: AppLogging {
    private let logger = Logger(subsystem: "com.github.kccarlos.herdr-sheepdog-mac", category: "app")

    public init() {}

    public func log(_ message: String) {
        logger.info("\(message, privacy: .public)")
    }
}
