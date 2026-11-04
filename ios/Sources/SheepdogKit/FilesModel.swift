import Foundation
import Observation

// The file browser's state: one folder's list with its search and sort, and
// one open file. Both take their reads as closures, so the tests drive them
// with plain values and the app hands them the current computer's client.
// Nothing is kept: a model lives as long as its sheet.

/// Why a read failed, in the words to show: the daemon said no, or the
/// request itself did not get through.
public enum FilesFailure: Equatable, Sendable {
    case problem(FileProblem)
    case request(RequestFailure)

    /// The computer did not answer (as opposed to answering no).
    public var isOffline: Bool {
        if case let .request(failure) = self { return failure.isUnreachable }
        return false
    }

    /// A message the user can act on once more.
    public var canRetry: Bool {
        switch self {
        case let .problem(problem): problem.canRetry
        case .request: true
        }
    }
}

/// One read, in its three stages.
public enum FilesLoad<Value: Equatable & Sendable>: Equatable, Sendable {
    case loading
    case loaded(Value)
    case failed(FilesFailure)

    public var value: Value? {
        if case let .loaded(value) = self { return value }
        return nil
    }
}

private func filesFailure(_ error: Error, describe: (Error) -> RequestFailure) -> FilesFailure? {
    if error is CancellationError { return nil }
    if error is ComputerFleet.Moved || error is ComputerModel.Stale { return nil }
    if let url = error as? URLError, url.code == .cancelled { return nil }
    if let refusal = error as? FilesRefusal { return .problem(refusal.problem) }
    return .request(describe(error))
}

// MARK: - The list

/// A folder of the browsed tree, and a name search under it.
@MainActor
@Observable
public final class FileBrowserModel {
    /// `GET …/files?path=&q=`; nil from a daemon without the routes.
    public typealias List = @MainActor (_ path: String, _ query: String?) async throws -> FileListing?

    /// Where a tapped link goes.
    public enum LinkTarget: Equatable, Sendable {
        case folder(String)
        case file
    }

    public private(set) var path = ""
    public private(set) var folder: FilesLoad<FileListing> = .loading
    /// A search under `path`; nil when the search field is empty.
    public private(set) var results: FilesLoad<FileListing>?
    /// The folder's own name, once a list has said it.
    public private(set) var rootName = ""
    public var sort: FileSort = .name

    /// The search field. Typing waits for a short pause before asking.
    public var query = "" {
        didSet { if query != oldValue { queryChanged() } }
    }

    @ObservationIgnored private let list: List
    @ObservationIgnored private let describe: (Error) -> RequestFailure
    @ObservationIgnored private let debounce: Duration
    @ObservationIgnored private let clock: () -> Date
    @ObservationIgnored private var searchTask: Task<Void, Never>?
    /// Each read takes a number; only the newest one applies.
    @ObservationIgnored private var folderRead = 0
    @ObservationIgnored private var searchRead = 0
    @ObservationIgnored private var linkTask: Task<Void, Never>?

    public init(
        list: @escaping List,
        describe: @escaping (Error) -> RequestFailure,
        debounce: Duration = .milliseconds(300),
        clock: @escaping () -> Date = Date.init
    ) {
        self.list = list
        self.describe = describe
        self.debounce = debounce
        self.clock = clock
    }

    // MARK: Reading what to show

    public var trimmedQuery: String { query.trimmingCharacters(in: .whitespacesAndNewlines) }
    public var isSearching: Bool { !trimmedQuery.isEmpty }

    /// The search while there is one, else the folder.
    public var visible: FilesLoad<FileListing> { results ?? folder }

    /// What the list shows: the visible entries in the chosen order.
    public var entries: [FileEntry] { sort.ordered(visible.value?.entries ?? []) }

    /// The recent order under its day words; nil in the name order.
    public var groups: [FileGroup]? {
        sort == .recent ? FileGroup.groups(entries, now: clock()) : nil
    }

    public var crumbs: [FileCrumb] { FileCrumb.crumbs(rootName: rootName.isEmpty ? "Files" : rootName, path: path) }

    /// The folder's name for words ("in shop"): the one the user is in.
    public var scopeName: String { path.isEmpty ? (rootName.isEmpty ? "this folder" : rootName) : FilePath.name(of: path) }

    public var searchSummary: String? {
        guard isSearching, let listing = results?.value else { return nil }
        return FileRowText.searchSummary(listing, query: trimmedQuery, scope: scopeName)
    }

    public var now: Date { clock() }

    // MARK: Actions

    /// The first read, when the sheet opens.
    public func start() async {
        await readFolder(path)
    }

    /// Opens a folder of the tree (a crumb, a row) and leaves the search.
    public func open(folder newPath: String) async {
        linkTask?.cancel()
        folderRead += 1
        searchTask?.cancel()
        query = ""
        results = nil
        path = newPath
        folder = .loading
        await readFolder(newPath)
    }

    /// Reads the folder (and the search) again, keeping what is on screen
    /// until the answer comes.
    public func reload() async {
        await readFolder(path)
        if isSearching { await readSearch(trimmedQuery) }
    }

    /// Clears the search field.
    public func clearSearch() {
        query = ""
    }

    public func cancelPendingLink() {
        linkTask?.cancel()
        linkTask = nil
        folderRead += 1
    }

    /// A link is a folder or a file, and the list does not say which. A
    /// folder lists; a file's list is a plain "not found", and the viewer
    /// then reads it (or says why it cannot).
    public func resolve(link entry: FileEntry) async -> LinkTarget {
        guard !entry.isBlocked else { return .file }
        linkTask?.cancel()
        folderRead += 1
        let mine = folderRead
        do {
            if let listing = try await list(entry.path, nil) {
                guard mine == folderRead else { return .file }
                path = entry.path
                folder = .loaded(listing)
                if !listing.rootName.isEmpty { rootName = listing.rootName }
                return .folder(entry.path)
            }
        } catch is CancellationError { return .file }
        catch let url as URLError where url.code == .cancelled { return .file }
        catch let refusal as FilesRefusal where refusal.problem == .notFound { return .file }
        catch { if mine == folderRead { folder = .failed(filesFailure(error, describe: describe) ?? .request(describe(error))) } }
        return .file
    }

    // MARK: Reads

    private func readFolder(_ target: String) async {
        folderRead += 1
        let mine = folderRead
        do {
            guard let listing = try await list(target, nil) else {
                if mine == folderRead, path == target { folder = .failed(.problem(.olderDaemon)) }
                return
            }
            guard mine == folderRead, path == target else { return }
            if !listing.rootName.isEmpty { rootName = listing.rootName }
            folder = .loaded(listing)
        } catch {
            guard mine == folderRead, path == target, let failure = filesFailure(error, describe: describe) else { return }
            folder = .failed(failure)
        }
    }

    private func readSearch(_ text: String) async {
        searchRead += 1
        let mine = searchRead
        let target = path
        do {
            guard let listing = try await list(target, text) else {
                if mine == searchRead { results = .failed(.problem(.olderDaemon)) }
                return
            }
            guard mine == searchRead, text == trimmedQuery, target == path else { return }
            results = .loaded(listing)
        } catch {
            guard mine == searchRead, text == trimmedQuery, let failure = filesFailure(error, describe: describe) else { return }
            results = .failed(failure)
        }
    }

    private func queryChanged() {
        searchTask?.cancel()
        let text = trimmedQuery
        guard !text.isEmpty else {
            searchRead += 1
            results = nil
            return
        }
        // Keep the last results on screen while the new ones come.
        if results == nil { results = .loading }
        searchTask = Task { [debounce] in
            try? await Task.sleep(for: debounce)
            guard !Task.isCancelled else { return }
            await readSearch(text)
        }
    }
}

// MARK: - One file

/// An open file: its text, a picture, or why not.
///
/// The slow parts run off the main actor and the model keeps only what the
/// screen shows: a picture is decoded and cut down (the file's bytes are let
/// go at once), a code file is cut into lines (its source is not kept), and
/// a Markdown file keeps its source only to make its other view when asked.
@MainActor
@Observable
public final class FileViewerModel {
    public typealias Read = @MainActor (_ path: String) async throws -> FileContent?
    public typealias Image = @MainActor (_ path: String) async throws -> Data

    public enum Phase: Equatable, Sendable {
        case loading
        /// A text: `lines` or `blocks` (by `mode`) hold it.
        case text(truncated: Bool)
        case image(FileImage)
        /// Bytes the phone could not open as a picture.
        case unreadableImage
        case binary
        case failed(FilesFailure)
    }

    /// Markdown shows as a document or as its source.
    public enum Mode: String, CaseIterable, Identifiable, Sendable {
        case rendered, raw
        public var id: String { rawValue }
        public var title: String { self == .rendered ? "Rendered" : "Raw" }
    }

    public let path: String
    public private(set) var phase: Phase = .loading
    /// What the daemon said about the file (its size and time), once it did.
    public private(set) var content: FileContent?
    /// The numbered lines, in the raw view (every file but a document).
    public private(set) var lines: [CodeLine] = []
    /// The document's blocks, in the rendered view of a Markdown file.
    public private(set) var blocks: [MarkdownRenderer.Block] = []
    /// What is shown. Changed by `select`, once that view is ready.
    public private(set) var mode: Mode
    /// Lines wrap, or scroll sideways.
    public var wraps = true

    @ObservationIgnored private let read: Read
    @ObservationIgnored private let image: Image
    @ObservationIgnored private let describe: (Error) -> RequestFailure
    @ObservationIgnored private var reads = 0
    /// A Markdown file's text, for the view not made yet; nil otherwise.
    @ObservationIgnored private var markdown: String?
    @ObservationIgnored private var hasLines = false
    @ObservationIgnored private var hasBlocks = false

    public init(path: String, read: @escaping Read, image: @escaping Image, describe: @escaping (Error) -> RequestFailure) {
        self.path = path
        self.read = read
        self.image = image
        self.describe = describe
        mode = FileKind.of(name: FilePath.name(of: path)) == .markdown ? .rendered : .raw
    }

    public var name: String { FilePath.name(of: path) }
    public var folder: String { FilePath.parent(of: path) }
    public var isMarkdown: Bool { FileKind.of(name: name) == .markdown }

    /// What the symbol and words of a refusal name: a picture, or a file.
    public var failureSubject: FileProblem.Subject {
        (content?.kind == .image || FileKind.of(name: name) == .image) ? .image : .file
    }

    public func load() async {
        reads += 1
        let mine = reads
        do {
            guard let found = try await read(path) else {
                if mine == reads { phase = .failed(.problem(.olderDaemon)) }
                return
            }
            guard mine == reads else { return }
            content = found
            switch found.kind {
            case .text:
                guard await showText(found.text ?? "", truncated: found.truncated, loadNumber: mine) else { return }
            case .binary:
                phase = .binary
            case .image:
                // Say so before fetching what would be refused.
                guard found.size <= FileBrowser.imageLimit else {
                    phase = .failed(.problem(.tooLarge))
                    return
                }
                let decoded = try await fetchImage()
                guard mine == reads else { return }
                phase = decoded.map(Phase.image) ?? .unreadableImage
            }
        } catch {
            guard mine == reads, let failure = filesFailure(error, describe: describe) else { return }
            phase = .failed(failure)
        }
    }

    /// Shows the other view of a Markdown file. It is made first, off the
    /// main actor, and the old view stays until it is ready.
    public func select(_ newMode: Mode) async {
        guard newMode != mode, isMarkdown else { return }
        guard let text = markdown else {
            mode = newMode
            return
        }
        let mine = reads
        let ready = newMode == .raw ? hasLines : hasBlocks
        if !ready {
            let made = await Self.make(text, name: name, mode: newMode)
            guard mine == reads else { return }
            adopt(made)
        }
        mode = newMode
    }

    // MARK: Making the views

    /// What a view of the text is made of: one of the two, as the mode asks.
    private struct Made: Sendable {
        var lines: [CodeLine]?
        var blocks: [MarkdownRenderer.Block]?
    }

    /// Makes the view `mode` shows off the main actor, and keeps the text
    /// only when the other view may be asked for. False when a newer read
    /// has taken over.
    private func showText(_ text: String, truncated: Bool, loadNumber: Int) async -> Bool {
        let made = await Self.make(text, name: name, mode: mode)
        guard loadNumber == reads else { return false }
        // The new views replace the old ones only now, so a refresh does
        // not blank the screen while it works.
        lines = []
        blocks = []
        hasLines = false
        hasBlocks = false
        markdown = isMarkdown ? text : nil
        adopt(made)
        phase = .text(truncated: truncated)
        return true
    }

    private func adopt(_ made: Made) {
        if let newLines = made.lines {
            lines = newLines
            hasLines = true
        }
        if let newBlocks = made.blocks {
            blocks = newBlocks
            hasBlocks = true
        }
        if hasLines, hasBlocks { markdown = nil }
    }

    nonisolated private static func make(_ text: String, name: String, mode: Mode) async -> Made {
        guard !Task.isCancelled else { return Made() }
        switch mode {
        case .rendered:
            let blocks = MarkdownRenderer.blocks(text)
            return Task.isCancelled ? Made() : Made(blocks: blocks)
        case .raw:
            let language = SyntaxLanguage.of(name: name)
            let lines = CodeLines.split(text, language: language)
            return Task.isCancelled ? Made() : Made(lines: lines)
        }
    }

    /// The picture, decoded and cut down. The file's bytes live only in
    /// here: they are gone when this returns.
    private func fetchImage() async throws -> FileImage? {
        let bytes = try await image(path)
        return await FileImage.decode(bytes, maxPixel: FileImage.viewerMaxPixel)
    }
}
