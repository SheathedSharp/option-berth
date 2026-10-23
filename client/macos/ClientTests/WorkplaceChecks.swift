import Foundation
import BerthTerminal
import BerthAgent
@testable import BerthClient

extension ClientChecks {
    @MainActor
    static func workplaceChecks() throws {
        let fm = FileManager.default
        let root = fm.temporaryDirectory.appendingPathComponent("workplace-check-" + UUID().uuidString, isDirectory: true)
        try fm.createDirectory(at: root, withIntermediateDirectories: true)
        defer { try? fm.removeItem(at: root) }

        let store = WorkplaceStore(rootURL: root.appendingPathComponent("workplaces", isDirectory: true))
        let worktree = root.appendingPathComponent("tree", isDirectory: true).path
        try fm.createDirectory(at: URL(fileURLWithPath: worktree), withIntermediateDirectories: true)

        let first = try store.ensureActive(worktree: worktree)
        require(first.name == "默认", "default workplace name")
        require(store.list(worktree: worktree).count == 1, "one workplace after ensure")

        let shell = CommandBlock(
            id: UUID(), command: "ls", startedAt: Date(timeIntervalSince1970: 1_700_000_000),
            endedAt: Date(timeIntervalSince1970: 1_700_000_001), exitCode: 0, interrupted: false,
            capturedOutput: "a\nb")
        let agent = AgentRow(
            id: UUID(), sessionID: UUID(), provider: "opencode", transport: .terminal,
            title: "OpenCode 1", status: .completed, activity: nil,
            since: Date(timeIntervalSince1970: 1_700_000_002), unread: false, isTarget: false,
            queuedCount: 0, userPrompt: "hi", resultText: "hello", exitCode: nil)
        store.syncLive(shell: [shell], agents: [agent])
        store.flushPendingSave()
        require(store.blocks.count == 2, "synced two blocks")
        require(store.blocks.contains { $0.kind == .shell && $0.input == "ls" }, "shell persisted")
        require(store.blocks.contains { $0.kind == .agentSession && $0.input == "hi" }, "agent persisted")

        let dir = root.appendingPathComponent("workplaces").appendingPathComponent(first.id.uuidString)
        require(fm.fileExists(atPath: dir.appendingPathComponent("meta.json").path), "meta.json on disk")
        require(fm.fileExists(atPath: dir.appendingPathComponent("blocks.json").path), "blocks.json on disk")
        require(fm.fileExists(atPath: root.appendingPathComponent("workplaces/index.json").path), "index.json on disk")

        let second = try store.create(worktree: worktree, name: "探索", makeActive: true)
        require(second.name == "探索", "named workplace")
        require(store.blocks.isEmpty, "new workplace starts empty")
        require(store.list(worktree: worktree).count == 2, "two workplaces")

        let recalled = try store.switchTo(worktree: worktree, query: "默认")
        require(recalled.id == first.id, "switch by name")
        require(store.blocks.count == 2, "recalled blocks")
        require(store.historicalShellBlocks(excludingLive: []).count == 1, "historical shell")
        require(store.historicalAgentRows(excludingLive: []).count == 1, "historical agent")

        try store.delete(worktree: worktree, query: "探索")
        require(store.list(worktree: worktree).count == 1, "deleted workplace")
        require(store.active?.id == first.id, "still on default")

        print("PASS: workplace create/list/switch/delete + block load/save")
    }
}
