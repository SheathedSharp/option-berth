import AppKit
import SwiftUI
import BerthTerminal
import Darwin
@testable import BerthClient

extension ClientChecks {
    static func historyEntryChecks() throws {
        let timeout = DispatchWorkItem { Darwin.exit(1) }
        DispatchQueue.global(qos: .utility).asyncAfter(deadline: .now() + 30, execute: timeout)
        defer { timeout.cancel() }
        let root = FileManager.default.temporaryDirectory.appendingPathComponent("history-entry-" + UUID().uuidString)
        try FileManager.default.createDirectory(at: root, withIntermediateDirectories: false)
        defer { TerminalSessions.shared.forgetWorkspace(root.path); try? FileManager.default.removeItem(at: root) }
        let host = NSHostingView(rootView: WorkspaceConsole(root: root.path))
        let window = NSWindow(contentRect: NSRect(x: 0, y: 0, width: 700, height: 480), styleMask: [.titled], backing: .buffered, defer: false)
        window.isReleasedWhenClosed = false; window.contentView = host; window.makeKeyAndOrderFront(nil); pump(0.2)
        defer {
            if let sheet = window.attachedSheet { window.endSheet(sheet) }
            window.contentView = nil; window.close()
            let registry = TerminalSessions.shared
            for session in registry.inWorktree(root.path) {
                TerminalWindows.shared.bringBack(session)
                if session.isActive { session.stop(force: true) }
            }
            eventually("shell-first history fixture did not exit") { registry.inWorktree(root.path).allSatisfy { !$0.isActive } }
            for session in registry.inWorktree(root.path) { registry.remove(session) }
            registry.forgetWorkspace(root.path)
        }
        eventually("opening the worktree session did not present a shell") {
            TerminalSessions.shared.inWorktree(root.path).contains { $0.kind == "terminal" && $0.isActive }
        }
        let shells = TerminalSessions.shared.inWorktree(root.path).filter { $0.kind == "terminal" }
        require(shells.count == 1 && !TerminalSessions.shared.inWorktree(root.path).contains { $0.kind.hasPrefix("agent:") },
                "shell-first open created an agent or a second shell")
        require(window.attachedSheet == nil, "terminal opened history without request")
        guard let button = find(NSButton.self, in: host).first(where: { $0.accessibilityIdentifier() == "terminal.history" }) else {
            fatalError("native console history control is missing")
        }
        let point = button.convert(NSPoint(x: button.bounds.midX, y: button.bounds.midY), to: nil)
        historyClick(window, x: point.x, y: point.y)
        eventually("terminal history button did not open its actual sheet") { window.attachedSheet != nil }
        require(TerminalSessions.shared.inWorktree(root.path).filter { $0.kind == "terminal" }.count == 1,
                "opening history created another terminal process")
        print("PASS: worktree session opens one shell; history sheet does not create another process")
    }
}
