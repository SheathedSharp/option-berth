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
        let before = TerminalSessions.shared.sessions.count
        let host = NSHostingView(rootView: TerminalPanel(root: root.path, workspace: TerminalSessions.shared.workspace(root.path)))
        let window = NSWindow(contentRect: NSRect(x: 0, y: 0, width: 700, height: 480), styleMask: [.titled], backing: .buffered, defer: false)
        window.isReleasedWhenClosed = false; window.contentView = host; window.makeKeyAndOrderFront(nil); pump(0.2)
        defer { if let sheet = window.attachedSheet { window.endSheet(sheet) }; window.contentView = nil; window.close() }
        require(window.attachedSheet == nil, "terminal opened history without request")
        let point = host.convert(NSPoint(x: 535, y: host.isFlipped ? 23 : host.bounds.height - 23), to: nil)
        historyClick(window, x: point.x, y: point.y)
        eventually("terminal history button did not open its actual sheet") { window.attachedSheet != nil }
        require(TerminalSessions.shared.sessions.count == before, "opening history created a terminal process")
        print("PASS: native terminal toolbar opens its history sheet without creating a process")
    }
}
