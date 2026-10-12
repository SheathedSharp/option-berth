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
        // Hit the real control by accessibility id. Density / Metrics changes move
        // the empty-state stack; a fixed (290, 180) point no longer lands on it.
        guard let guide = find(NSButton.self, in: board).first(where: { $0.accessibilityIdentifier() == "workspace.guide" }) else {
            fatalError("empty workspace guide button missing")
        }
        let point = guide.convert(NSPoint(x: guide.bounds.midX, y: guide.bounds.midY), to: nil)
        guideClick(window, x: point.x, y: point.y)
        eventually("empty workspace button did not open guide") { navigation.showingGuide && find(TourControlsView.self, in: board).count == 1 }
        navigation.showingGuide = false
        eventually("guide overlay did not close") { find(TourControlsView.self, in: board).isEmpty }
        require(TerminalSessions.shared.sessions.count == before, "guide entry created a process")
        print("PASS: real empty workspace guide button opens and dismisses actual spotlight overlay without process creation")
    }
}
