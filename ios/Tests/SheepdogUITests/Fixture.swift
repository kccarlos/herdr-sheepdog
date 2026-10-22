import Foundation

/// Where the UI tests find the fixture daemons. Defaults to the documented
/// ports; override them with `TEST_RUNNER_SHEEPDOG_FIXTURE_PORT`,
/// `TEST_RUNNER_SHEEPDOG_FIXTURE_PAIR_PORT` and
/// `TEST_RUNNER_SHEEPDOG_CONTROL_DIR` on the `xcodebuild` command line (the
/// `TEST_RUNNER_` prefix is how xcodebuild hands an environment variable to
/// the test runner), so two checkouts can each run their own daemon.
enum Fixture {
    private static let environment = ProcessInfo.processInfo.environment
    /// The `--voice=off` fixture daemon, as `host:port`.
    static let host = "127.0.0.1:" + (environment["SHEEPDOG_FIXTURE_PORT"] ?? "7799")
    /// The second daemon, bound to every address and advertising
    /// 127.0.0.1, for pairing by code over TLS.
    static let pairHost = "127.0.0.1:" + (environment["SHEEPDOG_FIXTURE_PAIR_PORT"] ?? "7798")
    /// `scripts/fixture-daemon-restarter.sh`'s control directory.
    static let controlDir = environment["SHEEPDOG_CONTROL_DIR"] ?? "/tmp/sd-bugs/control"
}
