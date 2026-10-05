import Foundation

/// A transient projection of existing command blocks. Strings retain their
/// original bytes; selecting an entry is never an execution request.
public struct CommandHistoryEntry: Identifiable, Equatable {
    public let sessionID: UUID
    public let sessionTitle: String
    public let block: CommandBlock
    public var id: String { sessionID.uuidString + ":" + block.id.uuidString }
    public init(sessionID: UUID, sessionTitle: String, block: CommandBlock) {
        self.sessionID = sessionID; self.sessionTitle = sessionTitle; self.block = block
    }
    public var status: String {
        if block.isRunning { return "运行中 / Running" }
        if block.interrupted { return "结果未知 / Unknown" }
        return block.exitCode.map { "exit \($0)" } ?? "结果未知 / Unknown"
    }
    public func matches(_ query: String) -> Bool {
        query.isEmpty || sessionTitle.localizedCaseInsensitiveContains(query)
            || (block.command?.localizedCaseInsensitiveContains(query) ?? false)
    }
    public static func newestFirst(_ left: Self, _ right: Self) -> Bool {
        if left.block.startedAt != right.block.startedAt { return left.block.startedAt > right.block.startedAt }
        return left.id < right.id
    }
}

@MainActor
extension TerminalSessions {
    /// At most the registry's 16 sessions x parser's 256 blocks. There is no
    /// disk read, durable index, new session, or second ownership model here.
    public func commandHistory(in root: String, query: String = "") -> [CommandHistoryEntry] {
        inWorktree(root).filter { $0.kind == "terminal" }.flatMap { session in
            session.commandBlocks.map { CommandHistoryEntry(sessionID: session.id, sessionTitle: session.title, block: $0) }
        }.filter { $0.matches(query) }.sorted(by: CommandHistoryEntry.newestFirst)
    }
    /// Call only after explicit worktree-scoped confirmation. Neither external
    /// provider history nor another worktree's in-memory blocks participate.
    public func clearCommandHistory(in root: String) {
        for session in inWorktree(root) where session.kind == "terminal" { session.clearCommandHistory() }
    }
}
