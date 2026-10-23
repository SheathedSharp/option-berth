import BerthAgent
import BerthTerminal
import Foundation

/// Durable work surface under a worktree. On disk:
///
/// ```
/// ~/.option-berth/workplaces/
///   index.json                 # worktree → activeID + ordered ids
///   <workplace-id>/
///     meta.json                # id, name, worktree, timestamps
///     blocks.json              # unified shell + agent-session stream
/// ```
///
/// Live PTY / agent processes are not owned here — only the isomorphic block
/// cards (input, output, status, time, kind) so a workplace can be recalled.

public enum WorkplaceBlockKind: String, Codable, Sendable, Equatable {
    case shell
    case agentSession = "agent-session"
}

public enum WorkplaceBlockStatus: String, Codable, Sendable, Equatable {
    case running
    case queued
    case completed
    case abnormal
}

public struct WorkplaceBlock: Codable, Equatable, Identifiable, Sendable {
    public var id: UUID
    public var kind: WorkplaceBlockKind
    public var input: String?
    public var output: String?
    public var status: WorkplaceBlockStatus
    public var startedAt: Date
    public var endedAt: Date?
    public var exitCode: Int?
    public var interrupted: Bool
    /// Agent provider id when kind == agentSession.
    public var provider: String?
    public var sessionTitle: String?
    public var agentSessionID: UUID?

    public init(id: UUID = UUID(), kind: WorkplaceBlockKind, input: String? = nil, output: String? = nil,
                status: WorkplaceBlockStatus, startedAt: Date, endedAt: Date? = nil, exitCode: Int? = nil,
                interrupted: Bool = false, provider: String? = nil, sessionTitle: String? = nil,
                agentSessionID: UUID? = nil) {
        self.id = id
        self.kind = kind
        self.input = input
        self.output = output
        self.status = status
        self.startedAt = startedAt
        self.endedAt = endedAt
        self.exitCode = exitCode
        self.interrupted = interrupted
        self.provider = provider
        self.sessionTitle = sessionTitle
        self.agentSessionID = agentSessionID
    }

    public static func fromShell(_ block: CommandBlock) -> WorkplaceBlock {
        let status: WorkplaceBlockStatus
        if block.isRunning { status = .running }
        else if block.interrupted { status = .abnormal }
        else if let code = block.exitCode { status = code == 0 ? .completed : .abnormal }
        else { status = .completed }
        return WorkplaceBlock(
            id: block.id, kind: .shell, input: block.command, output: block.capturedOutput,
            status: status, startedAt: block.startedAt, endedAt: block.endedAt,
            exitCode: block.exitCode, interrupted: block.interrupted)
    }

    public func asCommandBlock() -> CommandBlock {
        CommandBlock(
            id: id, command: input, startedAt: startedAt, endedAt: endedAt ?? (status == .running ? nil : startedAt),
            exitCode: exitCode, interrupted: interrupted || status == .abnormal && exitCode == nil,
            capturedOutput: output)
    }
}

public struct WorkplaceMeta: Codable, Equatable, Identifiable, Sendable {
    public var id: UUID
    public var name: String
    public var worktree: String
    public var createdAt: Date
    public var updatedAt: Date

    public init(id: UUID = UUID(), name: String, worktree: String, createdAt: Date = Date(), updatedAt: Date = Date()) {
        self.id = id
        self.name = name
        self.worktree = worktree
        self.createdAt = createdAt
        self.updatedAt = updatedAt
    }
}

struct WorkplaceIndex: Codable, Equatable {
    var schemaVersion: Int = 1
    /// Canonical worktree path → ordering + active workplace.
    var worktrees: [String: WorkplaceWorktreeEntry] = [:]
}

struct WorkplaceWorktreeEntry: Codable, Equatable {
    var activeID: UUID?
    var order: [UUID]
}

struct WorkplaceBlocksFile: Codable, Equatable {
    var schemaVersion: Int = 1
    var blocks: [WorkplaceBlock]
}

@MainActor
public final class WorkplaceStore: ObservableObject {
    public static let shared = WorkplaceStore()

    public static let schemaVersion = 1
    public static let maximumBlocks = 500

    @Published public private(set) var active: WorkplaceMeta?
    /// Blocks belonging to the active workplace (disk + live upserts).
    @Published public private(set) var blocks: [WorkplaceBlock] = []
    /// Wall clock when the active workplace was opened; live PTY/agent cards from
    /// before this moment stay foreign until the user creates them anew here.
    public private(set) var activeOpenedAt: Date = .distantPast

    private let rootURL: URL
    private var index: WorkplaceIndex
    private var saveTask: Task<Void, Never>?
    private let encoder: JSONEncoder
    private let decoder: JSONDecoder

    public init(rootURL: URL? = nil) {
        let home = FileManager.default.homeDirectoryForCurrentUser
        self.rootURL = rootURL ?? home.appendingPathComponent(".option-berth/workplaces", isDirectory: true)
        let enc = JSONEncoder()
        enc.outputFormatting = [.prettyPrinted, .sortedKeys]
        enc.dateEncodingStrategy = .iso8601
        self.encoder = enc
        let dec = JSONDecoder()
        dec.dateDecodingStrategy = .iso8601
        self.decoder = dec
        self.index = WorkplaceIndex()
        try? FileManager.default.createDirectory(at: self.rootURL, withIntermediateDirectories: true)
        self.index = loadIndex()
    }

    private func canonical(_ root: String) -> String {
        guard root.hasPrefix("/"), !root.utf8.contains(0) else { return "" }
        return URL(fileURLWithPath: root).standardizedFileURL.resolvingSymlinksInPath().path
    }

    private func indexURL() -> URL { rootURL.appendingPathComponent("index.json") }
    private func dirURL(_ id: UUID) -> URL { rootURL.appendingPathComponent(id.uuidString, isDirectory: true) }
    private func metaURL(_ id: UUID) -> URL { dirURL(id).appendingPathComponent("meta.json") }
    private func blocksURL(_ id: UUID) -> URL { dirURL(id).appendingPathComponent("blocks.json") }

    private func loadIndex() -> WorkplaceIndex {
        let url = indexURL()
        guard let data = try? Data(contentsOf: url),
              let value = try? decoder.decode(WorkplaceIndex.self, from: data) else {
            return WorkplaceIndex()
        }
        return value
    }

    private func writeIndex() throws {
        try FileManager.default.createDirectory(at: rootURL, withIntermediateDirectories: true)
        let data = try encoder.encode(index)
        try data.write(to: indexURL(), options: .atomic)
    }

    private func readMeta(_ id: UUID) -> WorkplaceMeta? {
        guard let data = try? Data(contentsOf: metaURL(id)),
              let meta = try? decoder.decode(WorkplaceMeta.self, from: data) else { return nil }
        return meta
    }

    private func writeMeta(_ meta: WorkplaceMeta) throws {
        try FileManager.default.createDirectory(at: dirURL(meta.id), withIntermediateDirectories: true)
        let data = try encoder.encode(meta)
        try data.write(to: metaURL(meta.id), options: .atomic)
    }

    private func readBlocks(_ id: UUID) -> [WorkplaceBlock] {
        guard let data = try? Data(contentsOf: blocksURL(id)),
              let file = try? decoder.decode(WorkplaceBlocksFile.self, from: data) else { return [] }
        return file.blocks
    }

    private func writeBlocks(_ id: UUID, _ blocks: [WorkplaceBlock]) throws {
        try FileManager.default.createDirectory(at: dirURL(id), withIntermediateDirectories: true)
        var trimmed = blocks
        if trimmed.count > Self.maximumBlocks {
            trimmed = Array(trimmed.suffix(Self.maximumBlocks))
        }
        let file = WorkplaceBlocksFile(schemaVersion: Self.schemaVersion, blocks: trimmed)
        let data = try encoder.encode(file)
        try data.write(to: blocksURL(id), options: .atomic)
    }

    /// Open last workplace for this worktree, or create a default one.
    @discardableResult
    public func ensureActive(worktree: String) throws -> WorkplaceMeta {
        let key = canonical(worktree)
        guard !key.isEmpty else { throw TerminalFailure("工作目录无效 / Invalid worktree") }
        if let active, canonical(active.worktree) == key {
            return active
        }
        flushPendingSave()
        var entry = index.worktrees[key] ?? WorkplaceWorktreeEntry(activeID: nil, order: [])
        if let id = entry.activeID, let meta = readMeta(id), canonical(meta.worktree) == key {
            active = meta
            blocks = readBlocks(id)
            // Empty archive claims current live PTY history; non-empty stays isolated.
            activeOpenedAt = blocks.isEmpty ? .distantPast : Date()
            return meta
        }
        for id in entry.order.reversed() {
            if let meta = readMeta(id), canonical(meta.worktree) == key {
                entry.activeID = id
                index.worktrees[key] = entry
                try writeIndex()
                active = meta
                blocks = readBlocks(id)
                activeOpenedAt = blocks.isEmpty ? .distantPast : Date()
                return meta
            }
        }
        return try create(worktree: key, name: "默认", makeActive: true)
    }

    public func list(worktree: String) -> [WorkplaceMeta] {
        let key = canonical(worktree)
        guard !key.isEmpty else { return [] }
        let order = index.worktrees[key]?.order ?? []
        return order.compactMap { id -> WorkplaceMeta? in
            guard let meta = readMeta(id), canonical(meta.worktree) == key else { return nil }
            return meta
        }
    }

    @discardableResult
    public func create(worktree: String, name: String, makeActive: Bool = true) throws -> WorkplaceMeta {
        let key = canonical(worktree)
        guard !key.isEmpty else { throw TerminalFailure("工作目录无效 / Invalid worktree") }
        flushPendingSave()
        let trimmed = name.trimmingCharacters(in: .whitespacesAndNewlines)
        let label = trimmed.isEmpty ? defaultName(for: key) : trimmed
        let meta = WorkplaceMeta(name: label, worktree: key)
        try writeMeta(meta)
        try writeBlocks(meta.id, [])
        var entry = index.worktrees[key] ?? WorkplaceWorktreeEntry(activeID: nil, order: [])
        entry.order.append(meta.id)
        if makeActive {
            if let previous = active, canonical(previous.worktree) == key {
                try persistActiveNow()
            }
            entry.activeID = meta.id
            active = meta
            blocks = []
            activeOpenedAt = .distantPast
        }
        index.worktrees[key] = entry
        try writeIndex()
        return meta
    }

    /// Switch to an existing workplace under this worktree (by id, name, or prefix).
    @discardableResult
    public func switchTo(worktree: String, query: String) throws -> WorkplaceMeta {
        let key = canonical(worktree)
        guard !key.isEmpty else { throw TerminalFailure("工作目录无效 / Invalid worktree") }
        let candidates = list(worktree: key)
        guard !candidates.isEmpty else { throw TerminalFailure("此 worktree 还没有 Workplace / No workplaces yet") }
        let q = query.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !q.isEmpty else { throw TerminalFailure("用法：/workplace 名称或 id / Usage: /workplace name-or-id") }
        let match: WorkplaceMeta?
        if let exact = candidates.first(where: { $0.name.caseInsensitiveCompare(q) == .orderedSame }) {
            match = exact
        } else if let byID = candidates.first(where: { $0.id.uuidString.lowercased() == q.lowercased() }) {
            match = byID
        } else if let prefix = candidates.first(where: {
            $0.id.uuidString.lowercased().hasPrefix(q.lowercased()) || $0.name.lowercased().hasPrefix(q.lowercased())
        }) {
            match = prefix
        } else if let contains = candidates.first(where: { $0.name.lowercased().contains(q.lowercased()) }) {
            match = contains
        } else {
            match = nil
        }
        guard let target = match else {
            throw TerminalFailure("找不到 Workplace：\(q)")
        }
        if active?.id == target.id { return target }
        flushPendingSave()
        try persistActiveNow()
        var entry = index.worktrees[key] ?? WorkplaceWorktreeEntry(activeID: nil, order: candidates.map(\.id))
        entry.activeID = target.id
        if !entry.order.contains(target.id) { entry.order.append(target.id) }
        index.worktrees[key] = entry
        try writeIndex()
        active = target
        blocks = readBlocks(target.id)
        activeOpenedAt = blocks.isEmpty ? .distantPast : Date()
        return target
    }

    public func delete(worktree: String, query: String) throws {
        let key = canonical(worktree)
        guard !key.isEmpty else { throw TerminalFailure("工作目录无效 / Invalid worktree") }
        let candidates = list(worktree: key)
        let q = query.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !q.isEmpty else { throw TerminalFailure("用法：/workplace-delete 名称或 id") }
        guard let target = candidates.first(where: {
            $0.name.caseInsensitiveCompare(q) == .orderedSame
                || $0.id.uuidString.lowercased() == q.lowercased()
                || $0.id.uuidString.lowercased().hasPrefix(q.lowercased())
                || $0.name.lowercased().contains(q.lowercased())
        }) else {
            throw TerminalFailure("找不到 Workplace：\(q)")
        }
        flushPendingSave()
        let wasActive = active?.id == target.id
        try? FileManager.default.removeItem(at: dirURL(target.id))
        var entry = index.worktrees[key] ?? WorkplaceWorktreeEntry(activeID: nil, order: [])
        entry.order.removeAll { $0 == target.id }
        if entry.activeID == target.id { entry.activeID = entry.order.last }
        index.worktrees[key] = entry
        try writeIndex()
        if wasActive {
            if let nextID = entry.activeID, let meta = readMeta(nextID) {
                active = meta
                blocks = readBlocks(nextID)
                activeOpenedAt = blocks.isEmpty ? .distantPast : Date()
            } else if entry.order.isEmpty {
                _ = try create(worktree: key, name: "默认", makeActive: true)
            } else {
                active = nil
                blocks = []
                activeOpenedAt = .distantPast
            }
        }
    }

    /// Upsert live shell + agent cards into the active workplace and schedule a disk write.
    public func syncLive(shell: [CommandBlock], agents: [AgentRow]) {
        guard active != nil else { return }
        var map = Dictionary(uniqueKeysWithValues: blocks.map { ($0.id, $0) })
        let known = Set(map.keys)
        let opened = activeOpenedAt.addingTimeInterval(-1)
        for block in shell {
            // Skip live PTY history that predates this workplace open, unless already archived.
            if !known.contains(block.id), block.startedAt < opened { continue }
            let next = WorkplaceBlock.fromShell(block)
            map[next.id] = next
        }
        for row in agents {
            let since = row.since ?? Date()
            if !known.contains(row.id), since < opened { continue }
            let status: WorkplaceBlockStatus
            switch row.status {
            case .starting, .working: status = .running
            case .queued: status = .queued
            case .completed, .idle, .ended: status = .completed
            case .failed, .interrupted, .lost, .needsYou: status = .abnormal
            }
            let input = row.userPrompt ?? row.title
            let output = row.resultText ?? row.activity
            let ended: Date? = (status == .running || status == .queued) ? nil : (row.since ?? Date())
            let next = WorkplaceBlock(
                id: row.id, kind: .agentSession, input: input, output: output, status: status,
                startedAt: row.since ?? Date(), endedAt: ended, exitCode: row.exitCode,
                interrupted: status == .abnormal && row.exitCode == nil,
                provider: row.provider, sessionTitle: row.title, agentSessionID: row.sessionID)
            map[next.id] = next
        }
        let merged = map.values.sorted { lhs, rhs in
            if lhs.startedAt != rhs.startedAt { return lhs.startedAt < rhs.startedAt }
            return lhs.id.uuidString < rhs.id.uuidString
        }
        if merged != blocks {
            blocks = merged
            scheduleSave()
        }
    }

    /// Shell cards restored from disk that are not already in the live PTY list.
    public func historicalShellBlocks(excludingLive live: [CommandBlock]) -> [CommandBlock] {
        let liveIDs = Set(live.map(\.id))
        return blocks.filter { $0.kind == .shell && !liveIDs.contains($0.id) }.map { $0.asCommandBlock() }
    }

    /// Agent cards restored from disk that are not already projected live.
    public func historicalAgentRows(excludingLive liveIDs: Set<UUID>) -> [AgentRow] {
        blocks.compactMap { block -> AgentRow? in
            guard block.kind == .agentSession, !liveIDs.contains(block.id) else { return nil }
            let status: AgentDisplayStatus
            switch block.status {
            case .running: status = .working
            case .queued: status = .queued
            case .completed: status = .completed
            case .abnormal: status = .failed
            }
            return AgentRow(
                id: block.id,
                sessionID: block.agentSessionID ?? block.id,
                provider: block.provider ?? "agent",
                transport: .terminal,
                title: block.sessionTitle ?? (block.input ?? "Agent"),
                status: status,
                activity: block.output,
                since: block.startedAt,
                unread: false,
                isTarget: false,
                queuedCount: 0,
                userPrompt: block.input,
                resultText: block.output,
                exitCode: block.exitCode)
        }
    }

    public func scheduleSave() {
        saveTask?.cancel()
        saveTask = Task { @MainActor in
            try? await Task.sleep(nanoseconds: 350_000_000)
            guard !Task.isCancelled else { return }
            try? persistActiveNow()
        }
    }

    public func flushPendingSave() {
        saveTask?.cancel()
        saveTask = nil
        try? persistActiveNow()
    }

    private func persistActiveNow() throws {
        guard var meta = active else { return }
        meta.updatedAt = Date()
        active = meta
        try writeMeta(meta)
        try writeBlocks(meta.id, blocks)
        let key = canonical(meta.worktree)
        var entry = index.worktrees[key] ?? WorkplaceWorktreeEntry(activeID: meta.id, order: [meta.id])
        entry.activeID = meta.id
        if !entry.order.contains(meta.id) { entry.order.append(meta.id) }
        index.worktrees[key] = entry
        try writeIndex()
    }

    private func defaultName(for worktree: String) -> String {
        let existing = list(worktree: worktree).count
        return existing == 0 ? "默认" : "Workplace \(existing + 1)"
    }
}
