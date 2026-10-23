import Foundation

/// The paired computers (docs/dev/design/app-redesign.md sections 3, 4 and
/// 10): each keyed by its daemon id, with its own record, pins, addresses
/// and ticket (`DeviceCredentials`), and which one is current, the one the
/// voice talks to.
///
/// - The order is the pairing order unless the user reorders them
///   (Settings ▸ Computers); the first is the default current computer.
/// - The app never changes the current computer on its own: pairing
///   another computer adds it, and only the first one becomes current.
/// - Records live in the keychain (one item per computer); the order, the
///   current id and the address in use per computer in `UserDefaults`.
/// - `showsComputerLabels` is the one switch for every computer label in
///   the app: with one computer the UI looks exactly as it always did.
public final class ComputerStore: @unchecked Sendable {
    /// The app's store: the keychain, readable after the first unlock (a
    /// notification's Approve works on a locked phone), and standard
    /// defaults.
    public static let shared = ComputerStore(defaults: .standard, secrets: KeychainSecretStore(accessibleAfterFirstUnlock: true))

    enum Key {
        static let order = "computers.order"
        static let current = "computers.current"
        static func address(_ id: String) -> String { "computers.address.\(id)" }
        /// The address with no paired computer behind it: a loopback
        /// fixture daemon in the tests (`-sheepdogAddress`). Before
        /// several computers it was also the paired computer's address in
        /// use (moved by `migrate`).
        static let looseAddress = "daemon.address"
        /// The one record the app kept before several computers.
        static let legacyRecord = "daemon.computer"
    }

    private let defaults: UserDefaults
    private let secrets: SecretStore
    private let lock = NSLock()
    private var instances: [String: DeviceCredentials] = [:]

    public init(defaults: UserDefaults, secrets: SecretStore) {
        self.defaults = defaults
        self.secrets = secrets
        migrate()
    }

    // MARK: - The list

    /// The computers' ids in their order.
    public var ids: [String] {
        (defaults.stringArray(forKey: Key.order) ?? []).filter { credentials(for: $0).computer != nil }
    }

    /// The paired computers in their order.
    public var computers: [PairedComputer] { ids.compactMap { credentials(for: $0).computer } }

    public var count: Int { ids.count }

    /// Whether the app names computers anywhere (labels, Talk to, section
    /// headers, ⌘K, VoiceOver, notification titles): only once a second
    /// computer is paired.
    public var showsComputerLabels: Bool { count > 1 }

    public func computer(_ id: String) -> PairedComputer? {
        guard ids.contains(id) else { return nil }
        return credentials(for: id).computer
    }

    /// The credentials of one computer: one instance per id for the whole
    /// process, so a ticket is signed for once.
    public func credentials(for id: String) -> DeviceCredentials {
        lock.lock()
        defer { lock.unlock() }
        if let existing = instances[id] { return existing }
        let made = DeviceCredentials(daemonID: id, secrets: secrets)
        instances[id] = made
        return made
    }

    // MARK: - The current computer

    /// The computer the voice talks to: the one the user last picked, else
    /// the first. Nil with none paired.
    public var currentID: String? {
        get {
            let all = ids
            if let picked = defaults.string(forKey: Key.current), all.contains(picked) { return picked }
            return all.first
        }
        set { defaults.set(newValue, forKey: Key.current) }
    }

    public var current: PairedComputer? { currentID.flatMap(computer) }

    // MARK: - Addresses and endpoints

    /// The address the app uses for a computer: the one that last
    /// answered, else the first from its code.
    public func address(for id: String) -> String {
        if let used = defaults.string(forKey: Key.address(id)), !used.isEmpty { return used }
        return computer(id)?.addresses.first ?? ""
    }

    public func setAddress(_ address: String, for id: String) {
        defaults.set(address, forKey: Key.address(id))
    }

    /// The address with no paired computer behind it (a loopback fixture).
    public var looseAddress: String {
        get { defaults.string(forKey: Key.looseAddress) ?? "" }
        set { defaults.set(newValue, forKey: Key.looseAddress) }
    }

    /// The endpoint of a computer: TLS pinned to its key with a ticket, or
    /// plain HTTP for a loopback fixture.
    public func endpoint(for id: String) -> DaemonEndpoint? {
        guard let computer = computer(id) else { return nil }
        let creds = credentials(for: id)
        return DaemonEndpoint(text: address(for: id), secure: !computer.isLoopbackFixture, credentials: creds)
    }

    // MARK: - Changes

    /// Saves a computer from a pairing: a re-pair of a known daemon id
    /// updates it (keeping its local name and place), a new id is added at
    /// the end. Returns true when it was added.
    @discardableResult
    public func save(_ computer: PairedComputer, address: String) -> Bool {
        var record = computer
        let creds = credentials(for: computer.daemonID)
        let known = creds.computer
        if let known, record.localName == nil { record.localName = known.localName }
        creds.computer = record
        setAddress(address, for: computer.daemonID)
        var order = defaults.stringArray(forKey: Key.order) ?? []
        let added = !order.contains(computer.daemonID)
        if added {
            order.append(computer.daemonID)
            defaults.set(order, forKey: Key.order)
        }
        return added
    }

    /// Forgets a computer, this phone's key for it and its address. When it
    /// was current, the first one left becomes current.
    public func remove(_ id: String) {
        credentials(for: id).forget()
        defaults.removeObject(forKey: Key.address(id))
        var order = defaults.stringArray(forKey: Key.order) ?? []
        order.removeAll { $0 == id }
        defaults.set(order, forKey: Key.order)
        if defaults.string(forKey: Key.current) == id { defaults.removeObject(forKey: Key.current) }
    }

    /// Forgets every computer (the tests' clean slate).
    public func removeAll() {
        for id in defaults.stringArray(forKey: Key.order) ?? [] { remove(id) }
        defaults.removeObject(forKey: Key.order)
        defaults.removeObject(forKey: Key.current)
        looseAddress = ""
    }

    /// The name the user gives a computer on this phone; nil or blank goes
    /// back to the daemon's.
    public func rename(_ id: String, to name: String?) {
        let trimmed = name?.trimmingCharacters(in: .whitespacesAndNewlines)
        credentials(for: id).update { $0.localName = trimmed?.isEmpty == false ? trimmed : nil }
    }

    /// Moves computers in the list (Settings ▸ Computers' Edit).
    public func move(fromOffsets source: IndexSet, toOffset destination: Int) {
        var order = ids
        let moving = source.sorted().map { order[$0] }
        let before = source.filter { $0 < destination }.count
        for index in source.sorted(by: >) { order.remove(at: index) }
        order.insert(contentsOf: moving, at: max(0, min(order.count, destination - before)))
        defaults.set(order, forKey: Key.order)
    }

    // MARK: - Migration

    /// The app before several computers kept one record under one keychain
    /// account and its address in use as the loose address. That computer
    /// becomes the first and current one, so an existing user stays paired
    /// after the update. The device key is already tagged by daemon id.
    func migrate() {
        guard let text = secrets.secret(for: Key.legacyRecord) else { return }
        defer { secrets.setSecret(nil, for: Key.legacyRecord) }
        guard let record = try? JSONDecoder().decode(PairedComputer.self, from: Data(text.utf8)), !record.daemonID.isEmpty else { return }
        let address = looseAddress
        save(record, address: address.isEmpty ? (record.addresses.first ?? "") : address)
        if defaults.string(forKey: Key.current) == nil { currentID = record.daemonID }
        looseAddress = ""
    }
}
