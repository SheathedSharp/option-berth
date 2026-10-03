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

    var clean: Bool { staged == 0 && unstaged == 0 && untracked == 0 && conflicts == 0 }
    var branchName: String { detached ? "detached" : (branch ?? "(no branch)") }
    var shortHead: String { String((head ?? "").prefix(8)) }

    private enum CodingKeys: String, CodingKey {
        case root, branch, detached, head, upstream, ahead, behind, staged, unstaged, untracked, conflicts
        case lastCommit = "last_commit"
        case worktrees
    }

    init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        root = (try? c.decode(String.self, forKey: .root)) ?? ""
        branch = try c.decodeIfPresent(String.self, forKey: .branch)
        detached = try c.decodeIfPresent(Bool.self, forKey: .detached) ?? false
        head = try c.decodeIfPresent(String.self, forKey: .head)
        upstream = try c.decodeIfPresent(String.self, forKey: .upstream)
        ahead = (try? c.decode(Int.self, forKey: .ahead)) ?? 0
        behind = (try? c.decode(Int.self, forKey: .behind)) ?? 0
        staged = (try? c.decode(Int.self, forKey: .staged)) ?? 0
        unstaged = (try? c.decode(Int.self, forKey: .unstaged)) ?? 0
        untracked = (try? c.decode(Int.self, forKey: .untracked)) ?? 0
        conflicts = (try? c.decode(Int.self, forKey: .conflicts)) ?? 0
        lastCommit = try c.decodeIfPresent(GitCommit.self, forKey: .lastCommit)
        worktrees = (try? c.decode([GitWorktree].self, forKey: .worktrees)) ?? []
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
    var statusLabel: String {
        switch status {
        case "??": return "未跟踪"
        case "UU", "AA", "DD": return "冲突"
        case " M": return "未暂存"
        case "M ": return "已暂存"
        default: return status.trimmingCharacters(in: .whitespaces).isEmpty ? "变更" : status
        }
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
    }
}

/// `oberth git files --json` flattens the overview fields beside `files`.
struct GitTree: Decodable, Sendable {
    let overview: GitOverview
    let files: [GitFile]

    init(from decoder: Decoder) throws {
        overview = try GitOverview(from: decoder)
        let c = try decoder.container(keyedBy: CodingKeys.self)
        files = (try? c.decode([GitFile].self, forKey: .files)) ?? []
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
