import ServiceManagement

/// Launch at login. Off by default; the menu toggles it
/// (docs/dev/design/desktop-client.md section 5.4).
public protocol LoginItemControlling: AnyObject {
    var isEnabled: Bool { get }
    func setEnabled(_ enabled: Bool) throws
}

/// `SMAppService.mainApp`: no LaunchAgent plist.
public final class LoginItem: LoginItemControlling {
    private let service = SMAppService.mainApp

    public init() {}

    public var isEnabled: Bool { service.status == .enabled }

    public func setEnabled(_ enabled: Bool) throws {
        if enabled {
            try service.register()
        } else {
            try service.unregister()
        }
    }
}
