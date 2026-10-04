import AppKit
import SwiftUI
import TipKit
import BerthTerminal
@testable import BerthClient

extension ClientChecks {
    // Events go only to our own synthetic window, never the system mouse.
    static func guideClick(_ window: NSWindow, x: CGFloat, y: CGFloat = 30) {
        for kind in [NSEvent.EventType.leftMouseDown, .leftMouseUp] {
            window.sendEvent(NSEvent.mouseEvent(with: kind, location: NSPoint(x: x, y: y), modifierFlags: [],
                timestamp: ProcessInfo.processInfo.systemUptime, windowNumber: window.windowNumber,
                context: nil, eventNumber: 1, clickCount: 1, pressure: 1)!)
        }
        pump()
    }
    static func gettingStartedChecks() throws {
        guard let berth = ProcessInfo.processInfo.environment["BERTH_HOME"], berth.hasPrefix("/") else {
            fatalError("onboarding checks require isolated BERTH_HOME")
        }
        require(OnboardingTips.configure(), "native TipKit configuration failed")
        require(FileManager.default.fileExists(atPath: berth + "/client-tips"), "TipKit escaped isolated BERTH_HOME")
        require(WorkspaceShortcuts.valid([:]), "onboarding changed default shortcuts")
        let before = TerminalSessions.shared.sessions.count
        var closeCount = 0, connectCount = 0
        let window = NSWindow(contentRect: NSRect(x: 0, y: 0, width: 600, height: 470), styleMask: [.titled], backing: .buffered, defer: false)
        window.isReleasedWhenClosed = false
        defer { window.contentView = nil; window.close() }
        func mount() -> NSHostingView<GettingStartedGuide> {
            let host = NSHostingView(rootView: GettingStartedGuide(onClose: { closeCount += 1 }, onConnect: { connectCount += 1 }))
            window.contentView = host; window.makeKeyAndOrderFront(nil); pump(0.2)
            return host
        }
        let host = mount()
        if let bitmap = host.bitmapImageRepForCachingDisplay(in: host.bounds) {
            host.cacheDisplay(in: host.bounds, to: bitmap)
            try bitmap.representation(using: .png, properties: [:])?.write(to: URL(fileURLWithPath: berth).appendingPathComponent("guide-native.png"))
        }
        require(closeCount == 0 && connectCount == 0, "opening guide performed an action")
        guideClick(window, x: 445) // Back disabled on the first page.
        guideClick(window, x: 545) // Next -> worktree.
        guideClick(window, x: 445) // Back -> setup.
        guideClick(window, x: 545) // Next -> worktree again.
        guideClick(window, x: 210) // Explicit Connect.
        require(connectCount == 1 && closeCount == 0, "native Next/Back/Connect did not request exactly one picker")
        for _ in 0..<3 { guideClick(window, x: 545) }
        require(closeCount == 0 && connectCount == 1, "reading steps performed a side effect")
        guideClick(window, x: 545)
        require(closeCount == 1, "native Done did not close the guide")
        let reopened = mount()
        guideClick(window, x: 545)
        guideClick(window, x: 210)
        require(connectCount == 2 && closeCount == 1, "reopened guide did not start at first step")
        guideClick(window, x: 65)
        require(closeCount == 2 && connectCount == 2, "native Later invoked a setup action")
        require(TerminalSessions.shared.sessions.count == before, "guide created a shell or agent")
        require(reopened.fittingSize.width <= 600 && reopened.fittingSize.height <= 470, "guide exceeds its native window")
        for step in GettingStartedStep.allCases {
            require(!step.chinese.isEmpty && !step.english.isEmpty, "missing bilingual guidance")
        }
        print("PASS: isolated native TipKit; real Next/Back/Connect/Done/Later/reopen; no implicit processes or changed shortcuts")
    }
}
