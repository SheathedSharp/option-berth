import AppKit
import SwiftUI
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
    }
}
