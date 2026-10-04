import Foundation
import Darwin

/// Presentation metadata only. Deliberately no PID, process status, argv, draft,
/// command history, transcript, environment, credential, or automatic launch flag.
public struct SavedSession: Codable, Identifiable, Equatable {
    public let id: UUID
    public let worktree: String
    public let title: String
    public let kind: String
    public init(id: UUID, worktree: String, title: String, kind: String) {
        self.id = id; self.worktree = worktree; self.title = title; self.kind = kind
    }
}
public struct SavedWorkspace: Codable, Equatable {
    public let root: String
    public let agentMode: Bool
    public let providerID: String
    public let nativeExpanded: Bool
    public let terminalLayout: PaneLayout
    public let agentLayout: PaneLayout
    public init(root: String, agentMode: Bool, providerID: String, nativeExpanded: Bool,
                terminalLayout: PaneLayout, agentLayout: PaneLayout) {
        self.root = root; self.agentMode = agentMode; self.providerID = providerID; self.nativeExpanded = nativeExpanded
        self.terminalLayout = terminalLayout; self.agentLayout = agentLayout
    }
}
public struct WorkspaceArchive: Codable, Equatable {
    public let schemaVersion: Int
    public let savedAt: Date
    public let workspaces: [SavedWorkspace]
    public let sessions: [SavedSession]
    public init(workspaces: [SavedWorkspace], sessions: [SavedSession], savedAt: Date = Date()) {
        schemaVersion = 1; self.savedAt = savedAt; self.workspaces = workspaces; self.sessions = sessions
    }
    public func validate() throws {
        guard schemaVersion == 1, workspaces.count <= 128, sessions.count <= 64,
              Set(workspaces.map(\.root)).count == workspaces.count,
              Set(sessions.map(\.id)).count == sessions.count,
              savedAt.timeIntervalSince1970.isFinite else { throw WorkspaceArchiveFailure.invalid }
        let providers = Set(["codex", "claude", "opencode", "pi", "deepseek"])
        let roots = Set(workspaces.map(\.root))
        for session in sessions {
            let parts = session.kind.split(separator: ":").map(String.init)
            let kindOK = session.kind == "terminal" || (parts.count == 3 && parts[0] == "agent" && providers.contains(parts[1]) && ["native", "task"].contains(parts[2]))
            guard roots.contains(session.worktree), !session.title.isEmpty, session.title.count <= 64,
                  !session.title.unicodeScalars.contains(where: { CharacterSet.controlCharacters.contains($0) }), kindOK else {
                throw WorkspaceArchiveFailure.invalid
            }
        }
        for workspace in workspaces {
            guard workspace.root.hasPrefix("/"), workspace.root.utf8.count <= 4096, !workspace.root.utf8.contains(0),
                  URL(fileURLWithPath: workspace.root).standardizedFileURL.path == workspace.root,
                  providers.contains(workspace.providerID) else { throw WorkspaceArchiveFailure.invalid }
            let allowed = Set(sessions.filter { $0.worktree == workspace.root }.map(\.id))
            try workspace.terminalLayout.validate(allowed: allowed)
            try workspace.agentLayout.validate(allowed: allowed)
        }
    }
}

public enum WorkspaceArchiveFailure: LocalizedError {
    case invalid, unsafe, io(Int32)
    public var errorDescription: String? {
        switch self {
        case .invalid: return "恢复文件版本、大小或布局无效 / Invalid workspace archive"
        case .unsafe: return "恢复文件权限或类型不安全 / Unsafe workspace archive ownership, permissions or type"
        case .io(let code): return "无法读写恢复文件（\(code)）/ Workspace archive I/O failed"
        }
    }
}

/// A single user-private snapshot, written atomically. Every operation opens its
/// directory and file without following final-component symlinks. No raw archive
/// bytes are included in diagnostics. The caller owns serialisation of updates.
public struct WorkspaceArchiveStore {
    public let directory: URL
    public static let maximumBytes = 512 * 1024
    private let filename = "workspace-v1.json"
    public init(directory: URL) { self.directory = directory }

    private func openDirectory(create: Bool) throws -> Int32? {
        guard directory.isFileURL, directory.path.hasPrefix("/"), !directory.path.utf8.contains(0) else { throw WorkspaceArchiveFailure.unsafe }
        if create {
            try FileManager.default.createDirectory(at: directory, withIntermediateDirectories: true, attributes: [.posixPermissions: 0o700])
        }
        let fd = Darwin.open(directory.path, O_RDONLY | O_CLOEXEC | O_DIRECTORY | O_NOFOLLOW)
        if fd < 0 { if !create && errno == ENOENT { return nil }; throw WorkspaceArchiveFailure.io(errno) }
        var info = stat()
        guard fstat(fd, &info) == 0, info.st_uid == getuid(), info.st_mode & 0o077 == 0 else {
            Darwin.close(fd); throw WorkspaceArchiveFailure.unsafe
        }
        return fd
    }
    public func read() throws -> WorkspaceArchive? {
        guard let parent = try openDirectory(create: false) else { return nil }
        defer { Darwin.close(parent) }
        let fd = openat(parent, filename, O_RDONLY | O_CLOEXEC | O_NOFOLLOW | O_NONBLOCK)
        if fd < 0 { if errno == ENOENT { return nil }; throw WorkspaceArchiveFailure.io(errno) }
        defer { Darwin.close(fd) }
        var info = stat()
        guard fstat(fd, &info) == 0, info.st_mode & S_IFMT == S_IFREG, info.st_uid == getuid(), info.st_mode & 0o077 == 0 else {
            throw WorkspaceArchiveFailure.unsafe
        }
        guard info.st_size >= 0 && info.st_size <= Self.maximumBytes else { throw WorkspaceArchiveFailure.invalid }
        var bytes = [UInt8](repeating: 0, count: 16 * 1024), data = Data()
        while true {
            let count = Darwin.read(fd, &bytes, bytes.count)
            if count < 0 { if errno == EINTR { continue }; throw WorkspaceArchiveFailure.io(errno) }
            if count == 0 { break }
            guard count <= Self.maximumBytes - data.count else { throw WorkspaceArchiveFailure.invalid }
            data.append(contentsOf: bytes.prefix(count))
        }
        let archive: WorkspaceArchive
        do { archive = try JSONDecoder().decode(WorkspaceArchive.self, from: data) }
        catch { throw WorkspaceArchiveFailure.invalid }
        try archive.validate(); return archive
    }
    public func write(_ archive: WorkspaceArchive) throws {
        try archive.validate()
        let data = try JSONEncoder().encode(archive)
        guard data.count <= Self.maximumBytes else { throw WorkspaceArchiveFailure.invalid }
        guard let parent = try openDirectory(create: true) else { throw WorkspaceArchiveFailure.unsafe }
        defer { Darwin.close(parent) }
        let temporary = ".workspace-\(UUID().uuidString).tmp"
        let fd = openat(parent, temporary, O_WRONLY | O_CREAT | O_EXCL | O_CLOEXEC | O_NOFOLLOW, 0o600)
        guard fd >= 0 else { throw WorkspaceArchiveFailure.io(errno) }
        defer { Darwin.close(fd); unlinkat(parent, temporary, 0) }
        try data.withUnsafeBytes { raw in
            var written = 0
            while written < data.count {
                let count = Darwin.write(fd, raw.baseAddress!.advanced(by: written), data.count - written)
                if count < 0 { if errno == EINTR { continue }; throw WorkspaceArchiveFailure.io(errno) }
                guard count > 0 else { throw WorkspaceArchiveFailure.io(EIO) }
                written += count
            }
        }
        guard fsync(fd) == 0, renameat(parent, temporary, parent, filename) == 0 else { throw WorkspaceArchiveFailure.io(errno) }
    }
    public func remove() throws {
        guard let parent = try openDirectory(create: false) else { return }
        defer { Darwin.close(parent) }
        if unlinkat(parent, filename, 0) != 0 && errno != ENOENT { throw WorkspaceArchiveFailure.io(errno) }
    }
}
