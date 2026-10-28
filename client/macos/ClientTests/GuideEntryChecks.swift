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
        // The test window owns geometry. Without this the hosting view adopts
        // the content's intrinsic height (~902pt here), so coordinates computed
        // from the window fell outside the visible area and the click missed.
        board.sizingOptions = []
        let window = NSWindow(contentRect: NSRect(x: 0, y: 0, width: 1060, height: 720), styleMask: [.titled], backing: .buffered, defer: false)
        window.isReleasedWhenClosed = false
        window.contentView = board; window.makeKeyAndOrderFront(nil); pump(0.35)
        defer { navigation.showingGuide = false; window.contentView = nil; window.close() }
        require(!navigation.showingGuide && TerminalSessions.shared.sessions.count == before, "empty workspace performed an implicit action")
        // Density Metrics shrink chrome / empty-state padding, so a fixed (290,180)
        // point no longer lands on the guide control. Prefer the real AX id; fall
        // back to a denser-layout coordinate if SwiftUI has not mirrored it yet.
        func accessible(_ id: String, in root: NSView) -> NSView? {
            if root.accessibilityIdentifier() == id { return root }
            for child in root.subviews {
                if let hit = accessible(id, in: child) { return hit }
            }
            return nil
        }
        var guide = accessible("workspace.guide", in: board)
        if guide == nil {
            let end = Date().addingTimeInterval(2)
            while guide == nil, Date() < end {
                pump(0.05)
                guide = accessible("workspace.guide", in: board)
            }
        }
        let point: NSPoint
        if let guide {
            point = guide.convert(NSPoint(x: guide.bounds.midX, y: guide.bounds.midY), to: nil)
        } else {
            // The empty-state CTA is a plain SwiftUI Button, which is not an
            // NSView and has no accessibility element in a headless process, so
            // it has to be hit by coordinate. Measured against the current empty
            // layout: with the hosting view at 1060x752 (flipped), the guide row
            // centres at yTop=126 and the 「接入项目」 button sits below it. If
            // the empty state is re-laid-out, re-measure rather than guess.
            let yLocal: CGFloat = board.isFlipped ? 126 : board.bounds.height - 126
            point = board.convert(NSPoint(x: 280, y: yLocal), to: nil)
        }
        guideClick(window, x: point.x, y: point.y)
        eventually("empty workspace button did not open guide") { navigation.showingGuide && find(TourControlsView.self, in: board).count == 1 }
        navigation.showingGuide = false
        eventually("guide overlay did not close") { find(TourControlsView.self, in: board).isEmpty }
        require(TerminalSessions.shared.sessions.count == before, "guide entry created a process")
        print("PASS: real empty workspace guide button opens and dismisses actual spotlight overlay without process creation")
    }
}
