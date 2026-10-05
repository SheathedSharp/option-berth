import AppKit
import SwiftUI
import BerthTerminal
import Darwin
@testable import BerthClient

extension ClientChecks {
    static func guideEntryChecks() {
        let timeout = DispatchWorkItem { Darwin.exit(1) }
        DispatchQueue.global(qos: .utility).asyncAfter(deadline: .now() + 30, execute: timeout)
        defer { timeout.cancel() }
        let before = TerminalSessions.shared.sessions.count
        let navigation = ViewState()
        let board = NSHostingView(rootView: BoardView(store: BoardStore(fixture: []), services: ServicesStore(fixture: []), views: navigation))
        let window = NSWindow(contentRect: NSRect(x: 0, y: 0, width: 1060, height: 720), styleMask: [.titled], backing: .buffered, defer: false)
        window.isReleasedWhenClosed = false
        window.contentView = board; window.makeKeyAndOrderFront(nil); pump(0.25)
        defer { navigation.showingGuide = false; window.contentView = nil; window.close() }
        require(!navigation.showingGuide && TerminalSessions.shared.sessions.count == before, "empty workspace performed an implicit action")
        // Convert a point in the actual hosting view, whose flipped origin and
        // title-safe-area size vary across supported macOS versions.
        let point = board.convert(NSPoint(x: 290, y: board.isFlipped ? 180 : board.bounds.height - 180), to: nil)
        guideClick(window, x: point.x, y: point.y)
        eventually("empty workspace button did not open guide") { navigation.showingGuide && window.attachedSheet != nil }
        navigation.showingGuide = false
        eventually("guide sheet did not close") { window.attachedSheet == nil }
        require(TerminalSessions.shared.sessions.count == before, "guide entry created a process")
        print("PASS: real empty workspace guide button opens and dismisses actual sheet without process creation")
    }
}
