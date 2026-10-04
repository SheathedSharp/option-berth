import AppKit
import SwiftUI
import BerthTerminal
@testable import BerthClient

extension ClientChecks {
    static func chromeChecks() {
        for legacy in ["project:demo", "ports:demo", "services:demo"] {
            require(Scope(argument: legacy) == .services("demo"), "legacy CLI scope stopped resolving to services")
        }
        let state = ViewState(scope: .code("first"))
        state.selectProject("second"); require(state.scope == .code("second"), "worktree switch reset the selected Git tab")
        state.scope = .terminal("first"); state.selectProject("second")
        require(state.scope == .terminal("second"), "worktree switch reset the selected terminal tab")
        state.scope = .console("/fixture"); state.selectProject("second")
        require(state.scope == .terminal("second"), "standalone session navigation jumped to services")
        require(WorkspaceAction.allCases.contains(.recovery) && WorkspaceAction.allCases.contains(.updates), "new capabilities absent from action catalogue")
        let actions = WorkspaceAction.allCases.map { $0.title + " " + $0.rawValue }
        require(actions.contains { $0.localizedCaseInsensitiveContains("recovery") } && actions.contains { $0.localizedCaseInsensitiveContains("updates") }, "new actions not searchable")
        var calls = [String]()
        let host = NSHostingView(rootView: WorkspaceToolbar(sessionCount: 3, recoveryPending: true,
            openActions: { calls.append("actions") }, openSessions: { calls.append("sessions") },
            openRecovery: { calls.append("recovery") }, openUpdates: { calls.append("updates") }, openSettings: { calls.append("settings") }))
        let window = NSWindow(contentRect: NSRect(x: 0, y: 0, width: 700, height: 80), styleMask: [.titled], backing: .buffered, defer: false)
        window.isReleasedWhenClosed = false; window.contentView = host; window.makeKeyAndOrderFront(nil)
        defer { window.contentView = nil; window.close() }
        pump(0.15)
        require(calls.isEmpty, "toolbar appearance performed an action or update check")
        require(host.fittingSize.width <= 700 && host.fittingSize.height <= 80, "toolbar exceeds its native test window")
        print("PASS: compatibility aliases canonicalized, worktree tab retained, recovery/update actions discoverable, toolbar causes no implicit effects")
        do { try partialLayoutChecks() } catch { fatalError("partial layout fixture failed: \(error)") }
    }

    static func partialLayoutChecks() throws {
        let root = FileManager.default.temporaryDirectory.appendingPathComponent("partial-layout-" + UUID().uuidString)
        try FileManager.default.createDirectory(at: root, withIntermediateDirectories: false)
        defer { try? FileManager.default.removeItem(at: root) }
        let registry = TerminalSessions.shared
        let session = try registry.add(worktree: root.path, title: "restored-one", executable: "/bin/echo", arguments: ["fixture"],
                                       environment: ["HOME": root.path, "PATH": "/usr/bin:/bin"])
        eventually("partial layout fixture did not exit") { !session.isActive }
        defer { registry.remove(session); registry.forgetWorkspace(root.path) }
        let workspace = registry.workspace(root.path)
        let historical = UUID()
        var complete = PaneLayout(session: session.id)
        try complete.split(historical, beside: session.id, axis: .horizontal)
        workspace.terminalLayout = complete
        let host = NSHostingView(rootView: PaneWorkspaceView(primary: session, workspace: workspace, agent: false))
        let window = NSWindow(contentRect: NSRect(x: 0, y: 0, width: 600, height: 340), styleMask: [.titled], backing: .buffered, defer: false)
        window.isReleasedWhenClosed = false; window.contentView = host; window.makeKeyAndOrderFront(nil)
        defer { window.contentView = nil; window.close() }
        pump(0.2)
        require(workspace.terminalLayout == complete, "rendering alone modified historical layout")
        // Send a real click only to this fixture window's pane title. The second
        // ID is metadata, not a process; no implicit launch may make it valid.
        let point = NSPoint(x: 26, y: host.bounds.height - 16)
        for type in [NSEvent.EventType.leftMouseDown, .leftMouseUp] {
            let event = NSEvent.mouseEvent(with: type, location: point, modifierFlags: [], timestamp: ProcessInfo.processInfo.systemUptime,
                windowNumber: window.windowNumber, context: nil, eventNumber: 1, clickCount: 1, pressure: 1)!
            window.sendEvent(event)
        }
        pump(0.1)
        require(workspace.terminalLayout.focused == session.id, "native pane title click did not focus the registered session")
        require(Set(workspace.terminalLayout.sessions) == Set([session.id, historical]), "focusing one restored pane discarded an unopened historical reference")
        require(registry.inWorktree(root.path).count == 1, "presentation started a replacement session implicitly")
        print("PASS: native title click after partial restoration preserves unopened pane references without starting a process")
    }
}
