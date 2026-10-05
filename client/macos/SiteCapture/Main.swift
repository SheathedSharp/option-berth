import AppKit
import SwiftUI
import BerthTerminal
@testable import BerthClient

/// Developer-only capture runner: real stores, PTYs and native NSWindows.
/// The wrapper owns the isolated daemon; no frozen models or ImageRenderer.
@main @MainActor enum SiteCapture {
    struct Failure: LocalizedError {
        let text: String
        init(_ text: String) { self.text = text }
        var errorDescription: String? { text }
    }
    static func pump(_ seconds: Double = 0.15) {
        let end = Date().addingTimeInterval(seconds)
        while Date() < end { RunLoop.main.run(until: Date().addingTimeInterval(0.01)) }
    }
    static func wait(_ message: String, _ predicate: () -> Bool) throws {
        let end = Date().addingTimeInterval(15)
        while !predicate() && Date() < end { pump(0.05) }
        if !predicate() { throw Failure(message) }
    }
    static func capture(_ view: some View, to output: URL, size: NSSize) throws {
        let host = NSHostingView(rootView: view)
        let window = NSWindow(contentRect: NSRect(origin: .zero, size: size), styleMask: [.titled, .resizable, .fullSizeContentView], backing: .buffered, defer: false)
        window.isReleasedWhenClosed = false
        window.titleVisibility = .hidden; window.titlebarAppearsTransparent = true
        window.appearance = NSAppearance(named: UISettings.shared.colorScheme == .dark ? .darkAqua : .aqua)
        window.contentView = host; window.makeKeyAndOrderFront(nil)
        defer { window.contentView = nil; window.close() }
        pump(0.6)
        guard let bitmap = host.bitmapImageRepForCachingDisplay(in: host.bounds) else { throw Failure("Native bitmap unavailable") }
        host.cacheDisplay(in: host.bounds, to: bitmap)
        guard let png = bitmap.representation(using: .png, properties: [:]) else { throw Failure("PNG conversion failed") }
        try png.write(to: output)
        print("Captured \(output.lastPathComponent) \(bitmap.pixelsWide)x\(bitmap.pixelsHigh)")
    }
    static func main() throws {
        let env = ProcessInfo.processInfo.environment
        guard let root = env["BERTH_SITE_ROOT"], let out = env["BERTH_SITE_OUTPUT"], let home = env["HOME"],
              let berth = env["BERTH_HOME"], root.hasPrefix(home + "/"), berth.hasPrefix(home + "/"), env["BERTH_BIN"] != nil else {
            throw Failure("Requires explicit isolated HOME, BERTH_HOME, BERTH_BIN, project and output")
        }
        let output = URL(fileURLWithPath: out)
        try FileManager.default.createDirectory(at: output, withIntermediateDirectories: true)
        let app = NSApplication.shared
        app.setActivationPolicy(.accessory); app.finishLaunching()
        let settings = UISettings.shared; settings.theme = .paper
        let store = BoardStore(), services = ServicesStore(), git = GitStore()
        let registry = TerminalSessions.shared
        store.start(); services.start()
        defer {
            for session in registry.sessions where session.isActive { session.stop(force: true) }
            try? wait("Owned capture shells did not exit") { registry.activeCount == 0 }
            for session in registry.sessions { registry.remove(session) }
            services.focus(nil); services.stop(); store.stop()
        }
        try wait("Demo project not observed by the real daemon") { services.projects.contains { $0.rootDir == root } }
        guard let project = services.projects.first(where: { $0.rootDir == root }) else { throw Failure("Missing project") }
        try wait("Demo services are not actually running") { services.liveCount(in: project) == 2 }
        let views = ViewState(scope: .services(project.name))
        let board = BoardView(store: store, services: services, git: git, views: views)
        services.focus(ServiceRef(group: project.name, service: "worker"))
        try wait("Demo worker has no real log output") { !services.logRecords.isEmpty }
        try capture(board, to: output.appendingPathComponent("services.png"), size: NSSize(width: 1240, height: 800))
        views.scope = .code(project.name); git.loadTree(project: project, force: true)
        try wait("Real Git tree unavailable") { !(git.tree?.files.isEmpty ?? true) }
        guard let file = git.tree?.files.first else { throw Failure("No demo diff") }
        git.select(file, project: project)
        try wait("Real Git diff unavailable") { git.patch != nil }
        try capture(board, to: output.appendingPathComponent("git.png"), size: NSSize(width: 1240, height: 800))
        let shellEnv = ["HOME": home, "ZDOTDIR": home, "PATH": env["PATH"] ?? "/usr/bin:/bin", "BERTH_HOME": berth,
                        "BERTH_SOCKET": env["BERTH_SOCKET"] ?? "", "LC_ALL": "en_US.UTF-8"]
        let a = try registry.add(worktree: root, title: "Shell · feature/checkout", executable: "/bin/zsh", arguments: ["-i"], environment: shellEnv, shellIntegration: true)
        let b = try registry.add(worktree: root, title: "Shell · inspection", executable: "/bin/zsh", arguments: ["-i"], environment: shellEnv, shellIntegration: true)
        pump(0.3)
        func command(_ session: TerminalSession, _ text: String) throws {
            let count = session.commandBlocks.count
            session.terminal.send(source: session.terminal, data: Array((text + "\n").utf8)[...])
            try wait("Capture command did not finish") { session.commandBlocks.count > count && session.commandBlocks.last?.endedAt != nil }
        }
        try command(a, "git status --short")
        try command(a, "git diff --stat")
        try command(b, "git branch --show-current")
        try command(b, "python3 -m py_compile api.py worker.py")
        let workspace = registry.workspace(root)
        workspace.single(a.id, agent: false)
        try workspace.split(b.id, beside: a.id, agent: false, axis: .horizontal)
        views.scope = .terminal(project.name); settings.theme = .midnight
        try capture(board, to: output.appendingPathComponent("terminal.png"), size: NSSize(width: 1240, height: 800))
        settings.theme = .paper
        try capture(WorktreeHistorySheet(root: root, sessions: registry), to: output.appendingPathComponent("history.png"), size: NSSize(width: 650, height: 480))
        views.scope = .services(project.name); views.showingGuide = true; views.guideTarget = .facts
        try capture(board, to: output.appendingPathComponent("guide.png"), size: NSSize(width: 1240, height: 800))
        views.showingGuide = false
        print("Native capture complete; owned sessions are being reclaimed.")
    }
}
