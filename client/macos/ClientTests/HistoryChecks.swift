import AppKit
import SwiftUI
import BerthTerminal
@testable import BerthClient

extension ClientChecks {
    static func historyClick(_ window: NSWindow, x: CGFloat, y: CGFloat) {
        for kind in [NSEvent.EventType.leftMouseDown, .leftMouseUp] {
            window.sendEvent(NSEvent.mouseEvent(with: kind, location: NSPoint(x: x, y: y), modifierFlags: [],
                timestamp: ProcessInfo.processInfo.systemUptime, windowNumber: window.windowNumber,
                context: nil, eventNumber: 1, clickCount: 1, pressure: 1)!)
        }
        pump()
    }
    static func historyChecks() throws {
        let base = FileManager.default.temporaryDirectory.appendingPathComponent("history-check-" + UUID().uuidString)
        let first = base.appendingPathComponent("first"), other = base.appendingPathComponent("other")
        for root in [first, other] { try FileManager.default.createDirectory(at: root, withIntermediateDirectories: true) }
        let registry = TerminalSessions()
        defer {
            for session in registry.sessions where session.isActive { session.stop(force: true) }
            eventually("history PTY fixtures did not exit") { registry.activeCount == 0 }
            for session in registry.sessions { registry.remove(session) }
            try? FileManager.default.removeItem(at: base)
        }
        func shell(_ root: URL, title: String) throws -> TerminalSession {
            try registry.add(worktree: root.path, title: title, executable: "/bin/zsh", arguments: ["-i"],
                environment: ["HOME": root.path, "ZDOTDIR": root.path, "PATH": "/usr/bin:/bin"], shellIntegration: true)
        }
        func command(_ session: TerminalSession, _ text: String, exit: Int) {
            let count = session.commandBlocks.count
            session.terminal.send(source: session.terminal, data: Array((text + "\n").utf8)[...])
            eventually("zsh did not finish history fixture command") {
                session.commandBlocks.count > count && session.commandBlocks.last?.exitCode == exit
            }
        }
        let a = try shell(first, title: "Alpha"), b = try shell(first, title: "Beta"), c = try shell(other, title: "Other")
        pump(0.25)
        command(a, "echo '历史 safe'", exit: 0)
        command(b, "echo '历史 safe'", exit: 0)
        command(c, "echo other-worktree", exit: 0)
        command(b, "false", exit: 1)
        let all = registry.commandHistory(in: first.path)
        require(all.count == 3 && all.first?.block.command == "false", "cross-session order lost commands")
        require(registry.commandHistory(in: first.path, query: "历史").count == 2, "Unicode or duplicate commands lost")
        require(registry.commandHistory(in: first.path, query: "ALPHA").count == 1, "case-insensitive session search failed")
        require(registry.commandHistory(in: first.path, query: "other-worktree").isEmpty, "other worktree leaked")
        require(registry.commandHistory(in: "").isEmpty, "invalid worktree produced history")
        let alias = base.appendingPathComponent("alias")
        try FileManager.default.createSymbolicLink(at: alias, withDestinationURL: first)
        require(registry.commandHistory(in: alias.path).map(\.id) == all.map(\.id), "canonical worktree alias changed scope")
        let board = NSPasteboard.withUniqueName()
        defer { board.releaseGlobally() }
        var parser = CommandBlockParser(nonce: "test")
        parser.consume(Array("\u{1b}]633;E;echo hello\\x0aecho world;test\u{7}\u{1b}]133;C;test\u{7}\u{1b}]133;D;bad;test\u{7}".utf8)[...])
        let unknown = CommandHistoryEntry(sessionID: UUID(), sessionTitle: "fixture", block: parser.blocks[0])
        require(unknown.status.contains("Unknown"), "unknown exit was reported as success")
        require(WorktreeHistorySheet.copy(unknown, to: board), "explicit copy failed")
        require(board.string(forType: .string) == "echo hello\necho world", "copy changed multiline literal bytes")
        let before = registry.commandHistory(in: first.path).map(\.id)
        let host = NSHostingView(rootView: WorktreeHistorySheet(root: first.path, sessions: registry, pasteboard: board))
        let window = NSWindow(contentRect: NSRect(x: 0, y: 0, width: 650, height: 480), styleMask: [.titled], backing: .buffered, defer: false)
        window.appearance = NSAppearance(named: UISettings.shared.colorScheme == .dark ? .darkAqua : .aqua)
        window.isReleasedWhenClosed = false; window.contentView = host; window.makeKeyAndOrderFront(nil); pump(0.2)
        defer { window.contentView = nil; window.close() }
        guard let field = find(NSTextField.self, in: host).first else { fatalError("native history search missing") }
        window.makeFirstResponder(field)
        field.stringValue = "历史"
        if let editor = field.currentEditor() as? NSTextView {
            editor.string = "历史"; NotificationCenter.default.post(name: NSText.didChangeNotification, object: editor)
        }
        field.delegate?.controlTextDidChange?(Notification(name: NSControl.textDidChangeNotification, object: field))
        pump(0.2)
        if let bitmap = host.bitmapImageRepForCachingDisplay(in: host.bounds), let berth = ProcessInfo.processInfo.environment["BERTH_HOME"] {
            host.cacheDisplay(in: host.bounds, to: bitmap)
            try bitmap.representation(using: .png, properties: [:])?.write(to: URL(fileURLWithPath: berth).appendingPathComponent("history-native.png"))
        }
        historyClick(window, x: 595, y: 304)
        require(board.string(forType: .string) == "echo '历史 safe'", "native filtered Copy failed or copied an unfiltered command")
        pump(0.1)
        require(registry.commandHistory(in: first.path).map(\.id) == before, "search/copy executed a command or changed source history")
        registry.clearCommandHistory(in: first.path)
        require(registry.commandHistory(in: first.path).isEmpty && c.commandBlocks.count == 1, "clear crossed worktree boundaries")
        command(a, "echo retained-until-close", exit: 0)
        a.stop(force: true); eventually("exited history fixture not observed") { !a.isActive }
        require(registry.commandHistory(in: first.path).count == 1, "exit discarded history before explicit close")
        registry.remove(a)
        require(registry.commandHistory(in: first.path).isEmpty, "closed session retained duplicate history")
        require(host.fittingSize.width <= 650 && host.fittingSize.height <= 480, "history exceeds native window")
        print("PASS: real zsh cross-session/canonical scope, Unicode/duplicates/order, native filtered Copy, multiline/unknown, scoped clear and close lifetime")
    }
}
