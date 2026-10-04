import AppKit
import Foundation
import BerthTerminal
@testable import BerthClient

extension ClientChecks {
    static func recoveryChecks() throws {
        let fm = FileManager.default
        let root = fm.temporaryDirectory.appendingPathComponent("recovery-check-" + UUID().uuidString)
        try fm.createDirectory(at: root, withIntermediateDirectories: false, attributes: [.posixPermissions: 0o700])
        defer { try? fm.removeItem(at: root) }
        let suite = "recovery-consent-test-" + UUID().uuidString
        let defaults = UserDefaults(suiteName: suite)!
        defer { defaults.removePersistentDomain(forName: suite) }
        let directory = root.appendingPathComponent("saved")
        let file = directory.appendingPathComponent("workspace-v1.json")
        let oldID = UUID()
        let path = root.resolvingSymlinksInPath().path
        let snapshot = WorkspaceArchive(workspaces: [SavedWorkspace(root: path, agentMode: false, providerID: "codex", nativeExpanded: false,
                terminalLayout: PaneLayout(session: oldID), agentLayout: PaneLayout())],
                sessions: [SavedSession(id: oldID, worktree: path, title: "old shell", kind: "terminal")])
        try WorkspaceArchiveStore(directory: directory).write(snapshot)
        let originalBytes = try Data(contentsOf: file)
        let registry = TerminalSessions()
        let disabled = WorkspaceRecovery(defaults: defaults, directory: directory, sessions: registry)
        disabled.loadOnce(); require(disabled.archive == nil && !disabled.enabled, "metadata read without consent")
        defaults.set(true, forKey: "workspace.restore-layout-consent.v1")
        let recovery = WorkspaceRecovery(defaults: defaults, directory: directory, sessions: registry)
        recovery.loadOnce()
        require(recovery.reviewPending && recovery.archive != nil, "saved layout not offered for review")
        recovery.saveNow()
        let afterLoad = try Data(contentsOf: file)
        require(afterLoad == originalBytes, "startup overwrote pending snapshot with empty state")
        recovery.restoreLayout()
        require(registry.sessions.isEmpty && recovery.remembered.count == 1, "layout restore launched or fabricated a process")
        registry.workspace(path).draft = "private-draft-canary"
        recovery.saveNow()
        let withoutDraft = try String(contentsOf: file, encoding: .utf8)
        require(!withoutDraft.contains("private-draft-canary"), "draft was persisted")
        recovery.newShell(for: snapshot.sessions[0], executable: "/bin/sh", environment: ["HOME": root.path, "PATH": "/usr/bin:/bin"])
        defer {
            for session in registry.sessions { if session.isActive { session.stop(force: true) } }
            eventually("recovery fixture cleanup") { registry.sessions.allSatisfy { !$0.isActive } }
        }
        guard let fresh = registry.sessions.first else { fatalError(recovery.problem ?? "explicit replacement shell missing") }
        require(fresh.id != oldID && fresh.isActive && recovery.remembered.isEmpty, "old PID/session identity was reused")
        require(registry.workspace(path).terminalLayout.sessions == [fresh.id], "layout reference not remapped to new session")
        fresh.terminal.send(source: fresh.terminal, data: Array("exit\r".utf8)[...])
        eventually("new replacement shell exit") { !fresh.isActive }
        recovery.disableAndDelete()
        require(!recovery.enabled && !fm.fileExists(atPath: file.path), "opt-out failed to delete snapshot")
        print("PASS: consent-gated restore, pending snapshot preserved, no automatic process/secret retention, explicit fresh-shell remap and opt-out deletion")
    }
}
