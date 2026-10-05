import Foundation

/// Read-only Git state for the selected worktree. The client decodes the
/// documents produced by `oberth git`; it does not run Git or maintain a
/// second repository model.
struct GitOverview: Decodable, Sendable {
    let root: String
    let branch: String?
    let detached: Bool
    let head: String?
    let upstream: String?
    let ahead: Int
    let behind: Int
    let staged: Int
    let unstaged: Int
    let untracked: Int
    let conflicts: Int
    let lastCommit: GitCommit?
    let worktrees: [GitWorktree]

    let countsComplete: Bool
    var clean: Bool { countsComplete && staged == 0 && unstaged == 0 && untracked == 0 && conflicts == 0 }
    var branchName: String { detached ? "detached" : (branch ?? "(no branch)") }
    var shortHead: String { String((head ?? "").prefix(8)) }

    private enum CodingKeys: String, CodingKey {
        case root, branch, detached, head, upstream, ahead, behind, staged, unstaged, untracked, conflicts
        case lastCommit = "last_commit"
        case worktrees
    }

    init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        root = try c.decode(String.self, forKey: .root)
        branch = try c.decodeIfPresent(String.self, forKey: .branch)
        detached = try c.decodeIfPresent(Bool.self, forKey: .detached) ?? false
        head = try c.decodeIfPresent(String.self, forKey: .head)
        upstream = try c.decodeIfPresent(String.self, forKey: .upstream)
        ahead = try c.decodeIfPresent(Int.self, forKey: .ahead) ?? 0
        behind = try c.decodeIfPresent(Int.self, forKey: .behind) ?? 0
        staged = try c.decodeIfPresent(Int.self, forKey: .staged) ?? 0
        unstaged = try c.decodeIfPresent(Int.self, forKey: .unstaged) ?? 0
        untracked = try c.decodeIfPresent(Int.self, forKey: .untracked) ?? 0
        conflicts = try c.decodeIfPresent(Int.self, forKey: .conflicts) ?? 0
        lastCommit = try c.decodeIfPresent(GitCommit.self, forKey: .lastCommit)
        worktrees = try c.decodeIfPresent([GitWorktree].self, forKey: .worktrees) ?? []
        countsComplete = try [.staged, .unstaged, .untracked, .conflicts].allSatisfy {
            try c.contains($0) && !c.decodeNil(forKey: $0)
        }
        guard [ahead, behind, staged, unstaged, untracked, conflicts].allSatisfy({ $0 >= 0 }),
              Set(worktrees.map(\.path)).count == worktrees.count else {
            throw DecodingError.dataCorrupted(.init(codingPath: decoder.codingPath, debugDescription: "Invalid Git overview counts or duplicate worktrees"))
        }
    }
}

struct GitCommit: Decodable, Sendable {
    let hash: String
    let subject: String
    let when: String?
}

struct GitWorktree: Decodable, Identifiable, Sendable {
    let path: String
    let head: String?
    let branch: String?
    let detached: Bool
    let bare: Bool
    let current: Bool

    var id: String { path }
    var displayBranch: String { detached ? "detached" : (branch ?? "(no branch)") }

    private enum CodingKeys: String, CodingKey { case path, head, branch, detached, bare, current }

    init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        path = try c.decode(String.self, forKey: .path)
        head = try c.decodeIfPresent(String.self, forKey: .head)
        branch = try c.decodeIfPresent(String.self, forKey: .branch)
        detached = try c.decodeIfPresent(Bool.self, forKey: .detached) ?? false
        bare = try c.decodeIfPresent(Bool.self, forKey: .bare) ?? false
        current = try c.decodeIfPresent(Bool.self, forKey: .current) ?? false
    }
}

struct GitFile: Decodable, Identifiable, Sendable {
    let path: String
    let oldPath: String?
    let status: String
    let additions: Int?
    let deletions: Int?
    let binary: Bool

    var id: String { path }
    var displayPath: String { oldPath.map { "\($0) → \(path)" } ?? path }
    var isConflict: Bool { ["DD", "AU", "UD", "UA", "DU", "AA", "UU"].contains(status) }
    var isUntracked: Bool { status == "??" }
    var isStaged: Bool { !isConflict && status.first.map { "MADTRC".contains($0) } == true }
    var isUnstaged: Bool { !isConflict && !isUntracked && status.last.map { "MADTRCm?".contains($0) } == true }
    var statusLabel: String {
        if isConflict { return "冲突" }
        if isUntracked { return "未跟踪" }
        if isStaged && isUnstaged { return "暂存 + 工作区" }
        if isStaged { return "已暂存" }
        if isUnstaged { return "工作区" }
        return status
    }

    private enum CodingKeys: String, CodingKey {
        case path, status, additions, deletions, binary
        case oldPath = "old_path"
    }

    init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        path = try c.decode(String.self, forKey: .path)
        oldPath = try c.decodeIfPresent(String.self, forKey: .oldPath)
        status = try c.decode(String.self, forKey: .status)
        additions = try c.decodeIfPresent(Int.self, forKey: .additions)
        deletions = try c.decodeIfPresent(Int.self, forKey: .deletions)
        binary = try c.decodeIfPresent(Bool.self, forKey: .binary) ?? false
        guard status.utf8.count == 2, !path.isEmpty,
              [additions, deletions].compactMap({ $0 }).allSatisfy({ $0 >= 0 }) else {
            throw DecodingError.dataCorrupted(.init(codingPath: decoder.codingPath, debugDescription: "Invalid Git file state"))
        }
    }
}

/// `oberth git files --json` flattens the overview fields beside `files`.
struct GitTree: Decodable, Sendable {
    let overview: GitOverview
    let files: [GitFile]

    init(from decoder: Decoder) throws {
        overview = try GitOverview(from: decoder)
        let c = try decoder.container(keyedBy: CodingKeys.self)
        guard c.contains(.files) else {
            throw DecodingError.keyNotFound(CodingKeys.files, .init(codingPath: decoder.codingPath, debugDescription: "Missing Git file list"))
        }
        files = try c.decodeIfPresent([GitFile].self, forKey: .files) ?? []
        guard Set(files.map(\.path)).count == files.count else {
            throw DecodingError.dataCorrupted(.init(codingPath: decoder.codingPath, debugDescription: "Duplicate Git file paths"))
        }
    }

    private enum CodingKeys: String, CodingKey { case files }
}

struct GitPatch: Decodable, Sendable {
    let root: String
    let files: [GitFileDiff]
}

struct GitFileDiff: Decodable, Identifiable, Sendable {
    let path: String
    let oldPath: String?
    let binary: Bool
    let lines: [GitPatchLine]

    var id: String { path }

    private enum CodingKeys: String, CodingKey {
        case path, binary, lines
        case oldPath = "old_path"
    }

    init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        path = try c.decode(String.self, forKey: .path)
        oldPath = try c.decodeIfPresent(String.self, forKey: .oldPath)
        binary = try c.decodeIfPresent(Bool.self, forKey: .binary) ?? false
        lines = try c.decode([GitPatchLine].self, forKey: .lines)
    }
}

struct GitPatchLine: Decodable, Sendable {
    let kind: String
    let text: String
}

/// A projection of existing CLI facts, not another Git reader. A dual-state file
/// can match two filters but appears exactly once in the all-files view.
enum GitReviewFilter: String, CaseIterable, Identifiable {
    case all, conflicts, staged, unstaged, untracked
    var id: String { rawValue }
    var title: String {
        switch self {
        case .all: return "全部"
        case .conflicts: return "冲突"
        case .staged: return "暂存"
        case .unstaged: return "工作区"
        case .untracked: return "未跟踪"
        }
    }
    func matches(_ file: GitFile) -> Bool {
        switch self {
        case .all: return true
        case .conflicts: return file.isConflict
        case .staged: return file.isStaged
        case .unstaged: return file.isUnstaged
        case .untracked: return file.isUntracked
        }
    }
    func files(_ files: [GitFile], query: String) -> [GitFile] {
        let term = query.trimmingCharacters(in: .whitespacesAndNewlines)
        return files.filter { matches($0) && (term.isEmpty || $0.displayPath.localizedCaseInsensitiveContains(term)) }
            .sorted { a, b in a.isConflict != b.isConflict ? a.isConflict : a.path < b.path }
    }
}

struct GitReviewLineTotals {
    private(set) var additions = 0
    private(set) var deletions = 0
    private(set) var uncounted = 0
    init(_ files: [GitFile]) {
        for file in files {
            guard !file.binary, !file.isConflict, let plus = file.additions, let minus = file.deletions else {
                uncounted += 1; continue
            }
            let a = additions.addingReportingOverflow(plus), d = deletions.addingReportingOverflow(minus)
            guard !a.overflow, !d.overflow else { uncounted += 1; continue }
            additions = a.partialValue; deletions = d.partialValue
        }
    }
}
