import AppKit
import BerthTerminal

@MainActor
extension TerminalChecks {
    static func workspaceChecks(root: URL) throws {
        let registry = TerminalSessions()
        let a = registry.workspace(root.path)
        a.agentMode = true; a.providerID = "pi"; a.draft = "private in-memory draft"
        let other = root.appendingPathComponent("second")
        try FileManager.default.createDirectory(at: other, withIntermediateDirectories: false)
        let b = registry.workspace(other.path)
        try check(a !== b && b.draft.isEmpty && !b.agentMode, "worktree UI state was shared")
        let returned = registry.workspace(root.appendingPathComponent(".").path)
        try check(returned === a && returned.draft == a.draft && returned.providerID == "pi", "page navigation lost the draft or provider")
        let alias = root.appendingPathComponent("alias")
        try FileManager.default.createSymbolicLink(at: alias, withDestinationURL: other)
        try check(registry.workspace(alias.path) === b, "symlink aliases duplicated console state")
        let one = UUID(), two = UUID()
        a.terminalSelection = one; a.terminalSplit = two
        a.select(two, agent: false)
        try check(a.terminalSelection == two && a.terminalSplit == one, "selecting the secondary duplicated a pane")
        a.forget(two)
        try check(a.terminalSelection == one && a.terminalSplit == nil, "closing the primary lost the remaining pane")
        a.agentSelection = one; a.agentSplit = two; a.select(two, agent: true); a.forget(one)
        try check(a.agentSelection == two && a.agentSplit == nil && a.terminalSelection == nil, "split cleanup left stale ids")
        var owned: [TerminalSession] = []
        defer { for s in owned { s.stop(force: true) }; _ = until { owned.allSatisfy { !$0.isActive } } }
        let first = try registry.add(worktree: root.path, title: "first", kind: "agent:pi:native",
            executable: "/bin/sleep", arguments: ["20"], environment: ["PATH": "/usr/bin:/bin", "HOME": root.path])
        let second = try registry.add(worktree: other.path, title: "second", executable: "/bin/sleep", arguments: ["20"],
            environment: ["PATH": "/usr/bin:/bin", "HOME": root.path])
        owned = [first, second]
        try check(registry.activeCount == 2 && registry.inWorktree(root.path).count == 1, "session registry crossed worktrees")
        try check(!registry.remove(first), "a running session was silently discarded")
        try first.rename("Review API")
        for invalid in ["", "\u{1b}[31m", String(repeating: "x", count: 65)] {
            do { try first.rename(invalid); throw CheckFailure(reason: "invalid title accepted") }
            catch is TerminalFailure {}
        }
        try check(first.title == "Review API", "invalid rename mutated title")
        registry.forgetWorkspace(root.path)
        try check(registry.sessions.contains { $0.id == first.id }, "removing a project orphaned a process")
        registry.select(first)
        try check(registry.workspace(root.path).agentSelection == first.id && registry.workspace(root.path).agentMode,
                  "session manager cannot reopen a detached project's session")
        first.stop(force: true)
        try check(until { !first.isActive }, "owned first session did not exit")
        try check(second.isActive && registry.remove(first), "closing one pane affected the other worktree")
        for invalidEnv in [["bad=key": "value"], ["key": "bad\0value"]] {
            let invalid = TerminalSession(worktree: root.path)
            do { try invalid.start(executable: "/bin/sh", arguments: [], environment: invalidEnv); throw CheckFailure(reason: "invalid environment accepted") }
            catch is TerminalFailure {}
            try check(invalid.terminal.process == nil && !invalid.isActive, "invalid input launched a process")
        }
        // The global quit path may escalate only children already stopping.
        let stoppingRegistry = TerminalSessions()
        // Ignore SIGHUP in the controlled child, then exec a known system binary.
        let firstStopping = try stoppingRegistry.add(worktree: root.path, title: "stubborn", executable: "/bin/sh",
            arguments: ["-c", "trap '' HUP; printf READY; exec /bin/sleep 20"], environment: ["PATH": "/usr/bin:/bin"])
        owned.append(firstStopping)
        var output = ""
        firstStopping.terminal.onBytes = { output += String(decoding: $0, as: UTF8.self) }
        try check(until { output.contains("READY") }, "stubborn fixture not ready")
        firstStopping.stop()
        let fresh = try stoppingRegistry.add(worktree: other.path, title: "fresh", executable: "/bin/sleep", arguments: ["20"],
                                            environment: ["PATH": "/usr/bin:/bin"])
        owned.append(fresh)
        stoppingRegistry.stopAll(allowForce: true)
        try check(until { !firstStopping.isActive && !fresh.isActive }, "mixed quit did not observe both exits")
        try check(firstStopping.state.contains("Signal 9") && fresh.state.contains("Signal 1"), "global force escalated a fresh session")
        print("PASS: worktree draft/provider/mode retention, alias identity, split selection/removal, rename boundaries, orphan-session recovery, isolated close")
    }
}
