import AppKit
import SwiftUI
import BerthTerminal
@testable import BerthClient

@main @MainActor enum PersonalizationChecks {
    struct Failure: LocalizedError {
        let message: String
        var errorDescription: String? { message }
    }
    static var checks = 0
    static func expect(_ condition: @autoclosure () -> Bool, _ message: String) throws {
        guard condition() else { throw Failure(message: message) }; checks += 1
    }
    static func eventually(_ message: String, _ predicate: () -> Bool) async throws {
        let end = Date().addingTimeInterval(5)
        while !predicate(), Date() < end { try await Task.sleep(nanoseconds: 5_000_000) }
        try expect(predicate(), message)
    }
    static func main() {
        let environment = ProcessInfo.processInfo.environment
        guard environment["BERTH_PERSONALIZATION_TEST"] == "1",
              let home = environment["HOME"], environment["CFFIXED_USER_HOME"] == home,
              let berth = environment["BERTH_HOME"], berth.hasPrefix(home + "/") else {
            fputs("PersonalizationChecks requires an explicitly isolated HOME and BERTH_HOME\n", stderr); exit(2)
        }
        let app = NSApplication.shared
        // A real application activation policy is required for deterministic
        // key-window ownership on the user's newer macOS as well as CI.
        app.setActivationPolicy(.regular); app.finishLaunching()
        Task { @MainActor in
            do { try await run(home: home, berth: berth); print("PersonalizationChecks: \(checks) checks passed"); exit(0) }
            catch { fputs("PersonalizationChecks failed: \(error.localizedDescription)\n", stderr); exit(1) }
        }
        app.run()
    }
    static func run(home: String, berth: String) async throws {
        let root = URL(fileURLWithPath: berth)
        let directory = root.appendingPathComponent("config")
        try FileManager.default.createDirectory(at: directory, withIntermediateDirectories: true)
        let file = directory.appendingPathComponent("settings.json")
        let theme = directory.appendingPathComponent("theme.json")
        // This executable owns its preference domain. Establish the complete
        // legacy fixture before UISettings is initialized; macOS may retain
        // this test process's prior preferences across repeated invocations.
        for pane in [ClientDetailPane.services, .git] {
            UserDefaults.standard.removeObject(forKey: pane.storageKey)
        }
        UserDefaults.standard.set(188.0, forKey: "railWidth")
        UserDefaults.standard.set(412.0, forKey: "detailWidth")
        let settings = UISettings.shared
        let registry = TerminalSessions()
        var rail: CGFloat = 0
        var reduced = false
        var systemReduced = false
        let board = PreferenceBoard(settings: settings, registry: registry,
                                    railChanged: { rail = $0 }, motionChanged: { reduced = $0; systemReduced = $1 })
        let host = NSHostingView(rootView: board)
        let window = NSWindow(contentRect: NSRect(x: 0, y: 0, width: 1060, height: 720),
                              styleMask: [.titled, .resizable], backing: .buffered, defer: false)
        window.isReleasedWhenClosed = false; host.sizingOptions = []
        window.contentView = host; window.makeKeyAndOrderFront(nil)
        defer { window.contentView = nil; window.close() }
        try await eventually("legacy sidebar width was not rendered") { abs(rail - 188) < 1 }
        let existing = registry.workspace("/fixture/existing")
        existing.providerID = "opencode"; existing.draft = "preserve this draft"
        let layout = PaneLayout(session: UUID()); existing.terminalLayout = layout
        func write(_ text: String) throws { try Data(text.utf8).write(to: file, options: .atomic) }
        try write(#"{"schemaVersion":1,"reduceMotion":true,"sidebarWidth":220,"shellIntegration":true,"defaultAgent":"claude"}"#)
        try await eventually("live preference consumers did not update") {
            abs(rail - 220) < 1 && reduced && settings.shellIntegration && registry.defaultProviderID == "claude"
        }
        try expect(registry.workspace("/fixture/new").providerID == "claude", "new workspace ignored default agent")
        try expect(existing.providerID == "opencode" && existing.draft == "preserve this draft" && existing.terminalLayout == layout, "config mutated existing workspace")
        try expect(registry.sessions.isEmpty, "config implicitly launched an agent or shell")
        let originalBytes = try Data(contentsOf: file)
        settings.shellIntegration = false
        try expect(settings.shellIntegration, "GUI overrode a file-owned integration setting")
        NSApp.activate(ignoringOtherApps: true)
        window.makeKeyAndOrderFront(nil)
        try await eventually("native test window is not key") { window.isKeyWindow }
        let start = NSPoint(x: rail + SplitHandle.hitWidth / 2, y: 280)
        for (index, kind) in [NSEvent.EventType.leftMouseDown, .leftMouseDragged, .leftMouseUp].enumerated() {
            let point = NSPoint(x: start.x + (index == 0 ? 0 : 24), y: start.y)
            guard let event = NSEvent.mouseEvent(with: kind, location: point, modifierFlags: [],
                timestamp: ProcessInfo.processInfo.systemUptime + Double(index) * 0.02,
                windowNumber: window.windowNumber, context: nil, eventNumber: index + 1, clickCount: 1, pressure: 1) else {
                throw Failure(message: "native drag event unavailable")
            }
            NSApp.postEvent(event, atStart: false)
        }
        try await eventually("configured sidebar no longer supports native dragging") { settings.sidebarWidth > 225 && abs(rail - settings.sidebarWidth) < 1 }
        let dragged = settings.sidebarWidth
        let afterDragBytes = try Data(contentsOf: file)
        try expect(afterDragBytes == originalBytes, "native drag overwrote settings.json")
        try expect(UserDefaults.standard.double(forKey: "railWidth") == 188, "file-owned drag overwrote legacy width")
        try write(#"{"schemaVersion":1,"reduceMotion":true,"sidebarWidth":220,"shellIntegration":true,"defaultAgent":"pi"}"#)
        try await eventually("default-agent edit not applied") { registry.defaultProviderID == "pi" }
        try expect(settings.sidebarWidth == dragged, "unrelated edit moved the divider")
        try expect(registry.workspace("/fixture/new").providerID == "claude" && registry.workspace("/fixture/third").providerID == "pi", "default-agent change rewrote existing selection")
        try write(#"{"schemaVersion":1,"reduceMotion":false,"sidebarWidth":230,"shellIntegration":false,"defaultAgent":"pi"}"#)
        try await eventually("updated width did not supersede the local drag") { abs(rail - 230) < 1 && reduced == systemReduced && !settings.shellIntegration }
        for system in [false, true] {
            for preference: Bool? in [nil, false, true] {
                try expect(ClientMotionPolicy.reduced(system: system, preference: preference) == (system || preference == true), "Reduce Motion policy matrix")
            }
        }
        try write("{")
        try await eventually("invalid settings did not produce a visible diagnostic") { !settings.configuration.problems.isEmpty }
        try expect(settings.sidebarWidth == 230 && registry.defaultProviderID == "pi", "invalid edit lost last-good preferences")
        try FileManager.default.removeItem(at: file)
        try await eventually("deleting settings did not restore fallbacks") { settings.sidebarWidth == 188 && registry.defaultProviderID == "codex" && settings.configuration.problems.isEmpty }
        try expect(reduced == systemReduced && existing.providerID == "opencode" && existing.draft == "preserve this draft", "deletion changed system preference or existing draft")
        try await eventually("removed user motion override persisted") { reduced == systemReduced }
        try capture(host, at: root.appendingPathComponent("personalization-sidebar-native.png"))
        try await detailChecks(settings: settings, root: root)

        let session = TerminalSession(worktree: home, title: "Personalization fixture")
        let terminalHost = NSHostingView(rootView: TerminalSurface(session: session))
        let terminalWindow = NSWindow(contentRect: NSRect(x: 0, y: 0, width: 680, height: 360), styleMask: [.titled, .resizable], backing: .buffered, defer: false)
        terminalWindow.isReleasedWhenClosed = false
        terminalWindow.contentView = terminalHost; terminalWindow.makeKeyAndOrderFront(nil)
        defer { terminalWindow.contentView = nil; terminalWindow.close() }
        var failure: Error?
        do {
            try session.start(executable: "/bin/sh", arguments: ["-c", "printf 'Native PTY: theme changes preserve this process.\\n'; read value"],
                              environment: ["HOME":home, "PATH":"/usr/bin:/bin"])
            let pid = session.terminal.process?.shellPid
            try expect(pid != nil && pid! > 0, "test PTY was not started")
            try Data(##"{"schemaVersion":1,"colors":{"canvas":"#F1F4F8","surface":"#E1E8F2","ink":"#17263D","accent":"#305A91","terminal.background":"#1C222B","terminal.foreground":"#D2E2F4"}}"##.utf8).write(to: theme, options: .atomic)
            try await eventually("live native terminal did not receive theme tokens") {
                guard let color = session.terminal.nativeBackgroundColor.usingColorSpace(.sRGB) else { return false }
                return abs(color.redComponent - 28.0 / 255) < 0.001 && abs(color.greenComponent - 34.0 / 255) < 0.001
            }
            try expect(session.isActive && session.terminal.process?.shellPid == pid, "theme reload restarted the PTY")
            try capture(terminalHost, at: root.appendingPathComponent("personalization-terminal-native.png"))
            try await checkTerminalComposition(terminalWindow, output: root.appendingPathComponent("personalization-terminal-composited-native.png"))
            try expect(session.isActive && session.terminal.process?.shellPid == pid, "composition capture changed PTY ownership")
            try capture(host, at: root.appendingPathComponent("personalization-custom-native.png"))
        } catch { failure = error }
        session.stop(force: true)
        try await eventually("owned PTY was not reaped") { !session.isActive }
        if let failure { throw failure }
    }
    static func capture(_ host: NSView, at url: URL) throws {
        host.layoutSubtreeIfNeeded()
        guard let bitmap = host.bitmapImageRepForCachingDisplay(in: host.bounds) else { throw Failure(message: "native bitmap unavailable") }
        host.cacheDisplay(in: host.bounds, to: bitmap)
        guard let png = bitmap.representation(using: .png, properties: [:]) else { throw Failure(message: "PNG unavailable") }
        try png.write(to: url)
    }
}

private struct MotionProbe: View {
    @Environment(\.accessibilityReduceMotion) private var systemReduced
    @Environment(\.clientReduceMotion) private var reduced
    let changed: (Bool, Bool) -> Void
    var body: some View {
        Color.clear.frame(width: 1, height: 1)
            .onChange(of: reduced, initial: true) { _, value in changed(value, systemReduced) }
            .onChange(of: systemReduced) { _, value in changed(reduced, value) }
    }
}
private struct PreferenceBoard: View {
    @ObservedObject var settings: UISettings
    let registry: TerminalSessions
    let railChanged: (CGFloat) -> Void
    let motionChanged: (Bool, Bool) -> Void
    @StateObject private var views = ViewState()
    @StateObject private var store = BoardStore(fixture: [])
    @StateObject private var services = ServicesStore(fixture: [])
    @StateObject private var git = GitStore(overview: nil)
    var body: some View {
        BoardView(store: store, services: services, git: git, views: views, settings: settings)
            .overlayPreferenceValue(TourAnchors.self) { anchors in
                GeometryReader { geometry in
                    let width = anchors[.worktrees].map { geometry[$0].width } ?? 0
                    Color.clear.onChange(of: width, initial: true) { _, value in railChanged(value) }
                }.allowsHitTesting(false)
            }
            .overlay(alignment: .bottomTrailing) { MotionProbe(changed: motionChanged).allowsHitTesting(false) }
            .modifier(ClientMotionPreferences(settings: settings))
            .onReceive(settings.$configuration) { ClientSessionPreferences.apply($0.preferences, to: registry) }
    }
}
