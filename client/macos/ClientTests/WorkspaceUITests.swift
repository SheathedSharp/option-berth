import AppKit
import SwiftUI
import BerthTerminal
@testable import BerthClient

@main
@MainActor
enum ClientChecks {
    static func require(_ value: @autoclosure () -> Bool, _ message: String) {
        if !value() { fatalError(message) }
    }
    static func refuses(_ message: String, _ operation: () throws -> Void) {
        do { try operation(); fatalError(message) } catch {}
    }
    static func pump(_ seconds: TimeInterval = 0.1) {
        let end = Date().addingTimeInterval(seconds)
        while Date() < end { RunLoop.main.run(until: Date().addingTimeInterval(0.005)) }
    }
    static func eventually(_ message: String, _ condition: () -> Bool) {
        let end = Date().addingTimeInterval(4)
        while !condition(), Date() < end { pump(0.01) }
        require(condition(), message)
    }
    static func main() throws {
        let app = NSApplication.shared
        app.setActivationPolicy(.accessory); app.finishLaunching()
        try shortcutChecks()
        try windowChecks()
        paletteChecks()
        try splitChecks()
        try recoveryChecks()
        try updateChecks()
        chromeChecks()
    }
    static func shortcutChecks() throws {
        let suite = "workspace-shortcut-test-" + UUID().uuidString
        let defaults = UserDefaults(suiteName: suite)!
        defer { defaults.removePersistentDomain(forName: suite) }
        let store = WorkspaceShortcuts(defaults: defaults)
        require(WorkspaceShortcuts.valid([:]), "invalid default shortcuts")
        for key in ["c", "v", "x", "a", "z", "q", "w", "h", "m"] {
            refuses("native shortcut intercepted") { try store.set(.init(key: key), for: .services) }
        }
        refuses("panel shortcut intercepted") { try store.set(.init(key: "p", shift: true), for: .services) }
        refuses("duplicate shortcut accepted") { try store.set(.init(key: "2"), for: .services) }
        require(store.bindings.isEmpty, "failed key edit was committed")
        try store.set(.init(key: "9", shift: true, option: true), for: .services)
        require(WorkspaceShortcuts(defaults: defaults).shortcut(.services) == store.shortcut(.services), "shortcut persistence failed")
        store.reset(); require(store.shortcut(.services) == WorkspaceAction.services.defaultShortcut, "reset failed")
        print("PASS: reserved editing/window keys, duplicate rejection, atomic settings and persisted custom shortcuts")
    }
    static func windowChecks() throws {
        let session = TerminalSession(worktree: FileManager.default.temporaryDirectory.path, title: "fixture terminal")
        let embedded = TerminalHost(frame: NSRect(x: 0, y: 0, width: 500, height: 300))
        embedded.present(session, detached: false)
        require(session.terminal.superview === embedded, "embedded presentation missing")
        TerminalWindows.shared.detach(session)
        defer { TerminalWindows.shared.bringBack(session); embedded.releasePresentation() }
        pump(0.15)
        guard let window = TerminalWindows.shared.windows[session.id] else { fatalError("detached window missing") }
        require(session.terminal.window === window, "same terminal not moved into detached window")
        embedded.present(session, detached: false)
        require(session.terminal.window === window, "embedded update stole detached terminal")
        require(session.terminal.process == nil, "presentation unexpectedly started a process")
        window.close(); pump()
        require(TerminalWindows.shared.windows[session.id] == nil, "closed window retained")
        embedded.present(session, detached: false)
        require(session.terminal.superview === embedded, "return to embedded presentation failed")
        print("PASS: real detached window, exclusive terminal presentation, close returns ownership without process launch")
    }
    static func paletteChecks() {
        let suite = "workspace-palette-test-" + UUID().uuidString
        let defaults = UserDefaults(suiteName: suite)!
        defer { defaults.removePersistentDomain(forName: suite) }
        var action: WorkspaceAction?
        let host = NSHostingView(rootView: WorkspaceActionPanel(perform: { action = $0 }, shortcuts: WorkspaceShortcuts(defaults: defaults)))
        let window = NSWindow(contentRect: NSRect(x: 0, y: 0, width: 480, height: 450), styleMask: [.titled], backing: .buffered, defer: false)
        window.isReleasedWhenClosed = false; window.contentView = host; window.makeKeyAndOrderFront(nil)
        defer { window.contentView = nil; window.close() }
        pump(0.2)
        require(action == nil, "opening palette executed action")
        // Submit through the native field editor, not a mocked callback.
        guard let field = find(NSTextField.self, in: host).first else { fatalError("native command search missing") }
        window.makeFirstResponder(field)
        field.stringValue = "terminal"
        if let editor = field.currentEditor() as? NSTextView {
            editor.string = "terminal"
            NotificationCenter.default.post(name: NSText.didChangeNotification, object: editor)
        }
        field.delegate?.controlTextDidChange?(Notification(name: NSControl.textDidChangeNotification, object: field))
        pump()
        let event = NSEvent.keyEvent(with: .keyDown, location: .zero, modifierFlags: [], timestamp: 0,
                                    windowNumber: window.windowNumber, context: nil, characters: "\r", charactersIgnoringModifiers: "\r", isARepeat: false, keyCode: 36)!
        window.sendEvent(event); pump()
        require(action == .terminal, "native filtered Return did not execute terminal action")
        print("PASS: native searchable command panel, explicit Return invokes only selected action")
    }
    static func find<T: NSView>(_ type: T.Type, in root: NSView) -> [T] {
        var result = (root as? T).map { [$0] } ?? []
        for child in root.subviews { result += find(type, in: child) }
        return result
    }
    static func splitChecks() throws {
        let root = FileManager.default.temporaryDirectory.appendingPathComponent("pane-ui-" + UUID().uuidString)
        try FileManager.default.createDirectory(at: root, withIntermediateDirectories: false)
        defer { try? FileManager.default.removeItem(at: root) }
        let registry = TerminalSessions.shared
        var sessions: [TerminalSession] = []
        defer {
            for session in sessions { if session.isActive { session.stop(force: true) } }
            eventually("fixture PTY cleanup") { sessions.allSatisfy { !$0.isActive } }
            for session in sessions { registry.remove(session) }
            registry.forgetWorkspace(root.path)
        }
        for index in 0..<3 {
            sessions.append(try registry.add(worktree: root.path, title: "pane-\(index)", executable: "/bin/sh", arguments: ["-c", "read value"],
                                             environment: ["HOME": root.path, "PATH": "/usr/bin:/bin"]))
        }
        let workspace = registry.workspace(root.path)
        workspace.single(sessions[0].id, agent: false)
        try workspace.split(sessions[1].id, beside: sessions[0].id, agent: false, axis: .horizontal)
        try workspace.split(sessions[2].id, beside: sessions[1].id, agent: false, axis: .vertical)
        let host = NSHostingView(rootView: PaneWorkspaceView(primary: sessions[0], workspace: workspace, agent: false))
        let window = NSWindow(contentRect: NSRect(x: 0, y: 0, width: 920, height: 600), styleMask: [.titled, .resizable], backing: .buffered, defer: false)
        window.isReleasedWhenClosed = false; window.contentView = host; window.makeKeyAndOrderFront(nil)
        defer { window.contentView = nil; window.close() }
        pump(0.25)
        let splits = find(NSSplitView.self, in: host)
        require(splits.count >= 2 && splits.contains { $0.isVertical } && splits.contains { !$0.isVertical }, "recursive native split views missing")
        for session in sessions {
            require(session.terminal.window === window, "terminal ownership lost inside recursive pane")
            window.makeFirstResponder(session.terminal)
            require(window.firstResponder === session.terminal, "pane focus could not be isolated")
        }
        let split = splits[0]
        split.setPosition(300, ofDividerAt: 0); split.adjustSubviews()
        require(split.subviews.allSatisfy { $0.frame.width > 0 && $0.frame.height > 0 }, "native divider collapsed a pane")
        print("PASS: real three-PTY recursive horizontal/vertical panes, independent focus and movable native divider")
    }
}
