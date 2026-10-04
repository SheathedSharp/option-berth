import AppKit
import BerthTerminal

@MainActor
extension TerminalChecks {
    static func mixedPaneChecks(root: URL) throws {
        let registry = TerminalSessions()
        let environment = ["PATH": "/usr/bin:/bin", "HOME": root.path]
        let agent = try registry.add(worktree: root.path, title: "fixture agent", kind: "agent:fixture:native",
                                     executable: "/bin/echo", arguments: ["fixture"], environment: environment)
        let shell = try registry.add(worktree: root.path, title: "fixture shell",
                                     executable: "/bin/echo", arguments: ["fixture"], environment: environment)
        try check(until { !agent.isActive && !shell.isActive }, "short-lived pane fixtures did not exit")
        registry.select(agent)
        let state = registry.workspace(root.path)
        state.agentSplit = shell.id
        try check(registry.remove(agent), "exited primary could not close")
        try check(!state.agentMode && state.terminalSelection == shell.id && state.agentSelection == nil,
                  "surviving shell disappeared after closing a mixed agent primary")
        try check(registry.remove(shell) && registry.sessions.isEmpty, "exited fixtures were not released")
        print("PASS: mixed agent/shell primary promotion changes mode without hiding the remaining session")
    }
}
