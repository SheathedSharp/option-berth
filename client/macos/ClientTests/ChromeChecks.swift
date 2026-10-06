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
        popoverChromeChecks()
        print("PASS: compatibility aliases canonicalized, worktree tab retained, recovery/update actions discoverable, toolbar causes no implicit effects")
        do { try worktreeRoutingChecks() } catch { fatalError("worktree routing fixture failed: \(error)") }
        do { try partialLayoutChecks() } catch { fatalError("partial layout fixture failed: \(error)") }
    }

    static func popoverChromeChecks() {
        let popover = NSPopover()
        let controller = NSViewController()
        controller.view = NSView(frame: NSRect(x: 0, y: 0, width: 460, height: 260))
        popover.contentViewController = controller
        SessionLauncherPopover.Coordinator.applyNativeAppearance(to: popover, settings: .shared)
        require(popover.appearance != nil, "session launcher popover did not receive a native appearance")
        require(controller.view.wantsLayer && controller.view.layer?.backgroundColor != nil,
                "session launcher popover content did not receive the configured canvas")
        print("PASS: session launcher popover inherits the configured native appearance and canvas")
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
        let pane = PaneWorkspaceView(primary: session, workspace: workspace, agent: false)
        let host = NSHostingView(rootView: pane)
        let window = NSWindow(contentRect: NSRect(x: 0, y: 0, width: 600, height: 340), styleMask: [.titled], backing: .buffered, defer: false)
        window.isReleasedWhenClosed = false; window.contentView = host; window.makeKeyAndOrderFront(nil)
        defer { window.contentView = nil; window.close() }
        pump(0.2)
        require(workspace.terminalLayout == complete, "rendering alone modified historical layout")
        require(session.terminal.window === window, "available terminal was lost while another pane is historical")
        // Exercise the same mutation entry point used by the title/split/drop
        // actions, rather than relying on OS-version-specific title coordinates.
        // Existing native key/divider/window checks remain separate.
        try pane.applyLayoutChange { $0.focus(session.id) }
        window.makeFirstResponder(session.terminal)
        require(window.firstResponder === session.terminal, "available terminal cannot receive native focus")
        require(workspace.terminalLayout.focused == session.id, "pane action did not focus the registered session")
        require(Set(workspace.terminalLayout.sessions) == Set([session.id, historical]), "focusing one restored pane discarded an unopened historical reference")
        let before = workspace.terminalLayout
        refuses("pane accepted an unknown new reference") {
            try pane.applyLayoutChange { try $0.split(UUID(), beside: session.id, axis: .horizontal) }
        }
        require(workspace.terminalLayout == before, "invalid pane change partially mutated historical layout")
        require(registry.inWorktree(root.path).count == 1, "presentation started a replacement session implicitly")
        // An explicit single-pane action is intentionally allowed to discard
        // hidden references; ordinary focus is not equivalent to that action.
        try pane.applyLayoutChange { $0 = PaneLayout(session: session.id) }
        require(workspace.terminalLayout.sessions == [session.id], "explicit single-pane action failed")
        print("PASS: actual pane mutation preserves unopened references, rejects unknown edits atomically, and keeps native focus without implicit process launch")
    }
}
