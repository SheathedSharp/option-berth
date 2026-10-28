import BerthAgent
import BerthTerminal
import Foundation

/// The work recorded under a worktree, read back from the engine.
///
/// The engine owns this record: `oberth start` and `oberth agent stream` write
/// it, and a front end that owns its own terminal reports the work it can see
/// through `oberth workplace record`. Nothing here is stored a second time —
/// `blocks` is a projection of `oberth workplace show --json`, refreshed after
/// each mutation. Live PTY / agent processes are still owned by the console;
/// only their reported cards live in the engine.
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

    /// The engine's block, projected onto the console's card model.
    init(engine block: EngineBlock) {
        let status: WorkplaceBlockStatus
        switch block.status {
        case "running": status = .running
        case "completed": status = .completed
        default: status = .abnormal
        }
        self.init(
            id: WorkplaceIdentity.uuid(for: block.id),
            kind: block.kind == "agent" ? .agentSession : .shell,
            input: block.input,
            // Only a reported block carries text; an engine block points at its
            // log, and the log stays the one copy of that output.
            output: block.output,
            status: status,
            startedAt: WorkplaceTime.date(block.startedAt) ?? Date(timeIntervalSince1970: 0),
            endedAt: WorkplaceTime.date(block.endedAt),
            exitCode: block.exitCode,
            interrupted: block.status == "interrupted",
            provider: block.provider)
    }

    /// What the engine accepts for this block when the console reports it.
    /// `queued` has no engine equivalent; a queued card is work that has not
    /// started, which the engine records as still running.
    var report: WorkplaceReport {
        let engineStatus: String
        switch status {
        case .running, .queued: engineStatus = "running"
        case .completed: engineStatus = "completed"
        case .abnormal: engineStatus = interrupted ? "interrupted" : "failed"
        }
        return WorkplaceReport(
            id: id,
            kind: kind == .agentSession ? "agent" : "shell",
            input: input,
            output: output,
            status: engineStatus,
            startedAt: startedAt,
            endedAt: endedAt,
            exitCode: exitCode,
            provider: provider)
    }
}

public struct WorkplaceMeta: Codable, Equatable, Identifiable, Sendable {
    /// The engine's identifier for this workplace. Empty only for the
    /// placeholder the console shows before the first engine read returns.
    public var engineID: String
    public var id: UUID
    public var name: String
    public var worktree: String
    public var createdAt: Date
    public var updatedAt: Date

    public init(engineID: String = "", id: UUID = UUID(), name: String, worktree: String,
                createdAt: Date = Date(), updatedAt: Date = Date()) {
        self.engineID = engineID
        self.id = id
        self.name = name
        self.worktree = worktree
        self.createdAt = createdAt
        self.updatedAt = updatedAt
    }

    init(engine workplace: EngineWorkplace) {
        self.init(
            engineID: workplace.id,
            id: WorkplaceIdentity.uuid(for: workplace.id),
            name: workplace.name,
            worktree: workplace.worktree,
            createdAt: WorkplaceTime.date(workplace.createdAt) ?? Date(timeIntervalSince1970: 0),
            updatedAt: WorkplaceTime.date(workplace.updatedAt) ?? Date(timeIntervalSince1970: 0))
    }
}

/// Reads and reports through the engine, and publishes the projection.
///
/// Every engine call blocks on a child process, so they run off the main actor;
/// the published properties are only ever written on the main actor.
@MainActor
public final class WorkplaceStore: ObservableObject {
    public static let shared = WorkplaceStore()

    public static let maximumBlocks = 500

    @Published public private(set) var active: WorkplaceMeta?
    /// Blocks belonging to the active workplace, as the engine last reported them.
    @Published public private(set) var blocks: [WorkplaceBlock] = []
    /// Wall clock when the active workplace was opened; live PTY/agent cards from
    /// before this moment stay foreign until the user creates them anew here.
    public private(set) var activeOpenedAt: Date = .distantPast

    private let binary: String?
    /// Last state handed to the engine for each block, so a refresh that changes
    /// nothing does not spawn a process.
    private var reported: [UUID: WorkplaceReport] = [:]
    private var pending: [UUID: WorkplaceReport] = [:]
    private var drainTask: Task<Void, Never>?
    private var cachedList: [String: [WorkplaceMeta]] = [:]

    /// `binary` and the initial listing are injectable so checks can run against
    /// a synthetic CLI with no daemon.
    public init(binary: String? = nil) {
        self.binary = binary
    }

    private func canonical(_ root: String) -> String {
        guard root.hasPrefix("/"), !root.utf8.contains(0) else { return "" }
        return URL(fileURLWithPath: root).standardizedFileURL.resolvingSymlinksInPath().path
    }

    private func offMain<T: Sendable>(_ work: @escaping @Sendable () throws -> T) async throws -> T {
        try await withCheckedThrowingContinuation { continuation in
            DispatchQueue.global(qos: .userInitiated).async {
                do { continuation.resume(returning: try work()) }
                catch { continuation.resume(throwing: error) }
            }
        }
    }

    // MARK: Reading

    /// Open the last workplace for this worktree, or create a default one.
    ///
    /// The engine's `list` already says which workplace is current, so this is
    /// one read plus, when there is nothing yet, one create.
    @discardableResult
    public func ensureActive(worktree: String) async throws -> WorkplaceMeta {
        let key = canonical(worktree)
        guard !key.isEmpty else { throw TerminalFailure("工作目录无效 / Invalid worktree") }
        if let active, canonical(active.worktree) == key {
            return active
        }
        let workplaces = try await offMain { [binary] in
            try WorkplaceAPI.list(worktree: key, binary: binary)
        }
        cachedList[key] = workplaces.map(WorkplaceMeta.init(engine:))
        if let current = workplaces.first(where: { $0.active }) {
            let meta = WorkplaceMeta(engine: current)
            await adopt(meta, worktree: key)
            return meta
        }
        if let last = workplaces.last {
            let used = try await offMain { [binary] in
                try WorkplaceAPI.use(worktree: key, query: last.id, binary: binary)
            }
            let meta = WorkplaceMeta(engine: used)
            await adopt(meta, worktree: key)
            return meta
        }
        return try await create(worktree: key, name: "默认", makeActive: true)
    }

    public func list(worktree: String) -> [WorkplaceMeta] {
        let key = canonical(worktree)
        if let cached = cachedList[key] { return cached }
        if let active, canonical(active.worktree) == key { return [active] }
        return []
    }

    public func refreshList(worktree: String) async throws {
        let key = canonical(worktree)
        guard !key.isEmpty else { throw TerminalFailure("工作目录无效 / Invalid worktree") }
        let workplaces = try await offMain { [binary] in
            try WorkplaceAPI.list(worktree: key, binary: binary)
        }
        cachedList[key] = workplaces.map(WorkplaceMeta.init(engine:))
    }

    private func adopt(_ meta: WorkplaceMeta, worktree: String) async {
        active = meta
        let shown = try? await offMain { [binary] in
            try WorkplaceAPI.show(worktree: worktree, binary: binary)
        }
        blocks = shown?.blocks.map(WorkplaceBlock.init(engine:)) ?? []
        activeOpenedAt = blocks.isEmpty ? .distantPast : Date()
    }

    // MARK: Writing

    @discardableResult
    public func create(worktree: String, name: String, makeActive: Bool = true) async throws -> WorkplaceMeta {
        let key = canonical(worktree)
        guard !key.isEmpty else { throw TerminalFailure("工作目录无效 / Invalid worktree") }
        flushPendingReports(waiting: true)
        let trimmed = name.trimmingCharacters(in: .whitespacesAndNewlines)
        let label = trimmed.isEmpty ? defaultName(for: key) : trimmed
        let created = try await offMain { [binary] in
            try WorkplaceAPI.create(worktree: key, name: label, binary: binary)
        }
        let meta = WorkplaceMeta(engine: created)
        cachedList[key, default: []].append(meta)
        if makeActive {
            reported.removeAll()
            await adopt(meta, worktree: key)
            activeOpenedAt = .distantPast
        }
        return meta
    }

    /// Switch to an existing workplace under this worktree (by id, name, or prefix).
    @discardableResult
    public func switchTo(worktree: String, query: String) async throws -> WorkplaceMeta {
        let key = canonical(worktree)
        guard !key.isEmpty else { throw TerminalFailure("工作目录无效 / Invalid worktree") }
        let q = query.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !q.isEmpty else { throw TerminalFailure("用法：/workplace 名称或 id / Usage: /workplace name-or-id") }
        if cachedList[key] == nil { try await refreshList(worktree: key) }
        let candidates = list(worktree: key)
        guard !candidates.isEmpty else { throw TerminalFailure("此 worktree 还没有 Workplace / No workplaces yet") }
        flushPendingReports(waiting: true)
        // The engine owns name/id/prefix matching and its ambiguity rules; the
        // client passes the user's query through instead of re-implementing them.
        let switched = try await offMain { [binary] in
            try WorkplaceAPI.use(worktree: key, query: q, binary: binary)
        }
        let meta = WorkplaceMeta(engine: switched)
        reported.removeAll()
        await adopt(meta, worktree: key)
        return meta
    }

    public func delete(worktree: String, query: String) async throws {
        let key = canonical(worktree)
        guard !key.isEmpty else { throw TerminalFailure("工作目录无效 / Invalid worktree") }
        let q = query.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !q.isEmpty else { throw TerminalFailure("用法：/workplace-delete 名称或 id") }
        flushPendingReports(waiting: true)
        let removing = active?.engineID
        try await offMain { [binary] in
            try WorkplaceAPI.delete(worktree: key, query: q, binary: binary)
        }
        try await refreshList(worktree: key)
        if let removing, active?.engineID == removing {
            reported.removeAll()
            if let next = cachedList[key]?.last {
                let used = try await offMain { [binary] in
                    try WorkplaceAPI.use(worktree: key, query: next.engineID, binary: binary)
                }
                await adopt(WorkplaceMeta(engine: used), worktree: key)
            } else {
                active = nil
                blocks = []
                activeOpenedAt = .distantPast
                _ = try await create(worktree: key, name: "默认", makeActive: true)
            }
        }
    }

    // MARK: Reporting the console's own work

    /// Upsert live shell + agent cards into the active workplace.
    ///
    /// Only the cards this console owns are reported: the engine already records
    /// what it started. Reports are coalesced per block and sent off the main
    /// actor, so a fast-finishing command costs one report, not two.
    public func syncLive(shell: [CommandBlock], agents: [AgentRow]) {
        guard active != nil else { return }
        let opened = activeOpenedAt.addingTimeInterval(-1)
        var latest: [UUID: WorkplaceBlock] = [:]
        for block in shell {
            if block.startedAt < opened, reported[block.id] == nil { continue }
            latest[block.id] = WorkplaceBlock.fromShell(block)
        }
        for row in agents {
            let since = row.since ?? Date()
            if since < opened, reported[row.id] == nil { continue }
            latest[row.id] = Self.block(fromAgentRow: row)
        }
        var changed = false
        for (id, block) in latest {
            let report = block.report
            if reported[id] == report { continue }
            pending[id] = report
            reported[id] = report
            changed = true
        }
        if changed { drainReports() }
    }

    private static func block(fromAgentRow row: AgentRow) -> WorkplaceBlock {
        let status: WorkplaceBlockStatus
        switch row.status {
        case .starting, .working: status = .running
        case .queued: status = .queued
        case .completed, .idle, .ended: status = .completed
        case .failed, .interrupted, .lost, .needsYou: status = .abnormal
        }
        let ended: Date? = (status == .running || status == .queued) ? nil : (row.since ?? Date())
        return WorkplaceBlock(
            id: row.id, kind: .agentSession, input: row.userPrompt ?? row.title,
            output: row.resultText ?? row.activity, status: status,
            startedAt: row.since ?? Date(), endedAt: ended, exitCode: row.exitCode,
            interrupted: status == .abnormal && row.exitCode == nil,
            provider: row.provider, sessionTitle: row.title, agentSessionID: row.sessionID)
    }

    private func drainReports() {
        guard drainTask == nil else { return }
        drainTask = Task { @MainActor in
            defer { drainTask = nil }
            while let (id, report) = pending.first {
                pending.removeValue(forKey: id)
                guard let worktree = active?.worktree else { continue }
                // A failed report is a diagnostic, not a command failure: the
                // engine keeps the block as it last heard it, and the card stays
                // visible in the console either way.
                _ = try? await offMain { [binary] in
                    try WorkplaceAPI.record(report, worktree: worktree, binary: binary)
                }
            }
        }
    }

    /// Send whatever is still queued before the process goes away, then stop.
    ///
    /// Reports after this are dropped rather than silently deferred: the engine
    /// keeps the last state it heard, and an unobserved end stays `running`
    /// instead of being claimed as finished.
    public func flushPendingSave() {
        flushPendingReports(waiting: true)
    }

    private func flushPendingReports(waiting: Bool) {
        drainTask?.cancel()
        drainTask = nil
        guard waiting, let worktree = active?.worktree else { return }
        let binary = self.binary
        while let (id, report) = pending.first {
            pending.removeValue(forKey: id)
            // Go through the same call the async drain uses: it is the one that
            // writes output to a file instead of putting it on argv.
            _ = try? WorkplaceAPI.record(report, worktree: worktree, binary: binary)
        }
    }

    // MARK: Projections for the console

    /// Shell cards restored from the engine that are not already in the live PTY list.
    public func historicalShellBlocks(excludingLive live: [CommandBlock]) -> [CommandBlock] {
        let liveIDs = Set(live.map(\.id))
        return blocks.filter { $0.kind == .shell && !liveIDs.contains($0.id) }.map { $0.asCommandBlock() }
    }

    /// Agent cards restored from the engine that are not already projected live.
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

    private func defaultName(for worktree: String) -> String {
        let existing = list(worktree: worktree).count
        return existing == 0 ? "默认" : "Workplace \(existing + 1)"
    }
}
