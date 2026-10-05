import AppKit
import SwiftUI
@testable import BerthClient

@main @MainActor enum GitReviewChecks {
    struct Failure: LocalizedError {
        let message: String
        var errorDescription: String? { message }
    }
    static var checks = 0
    static func require(_ value: @autoclosure () -> Bool, _ message: String) throws {
        guard value() else { throw Failure(message: message) }; checks += 1
    }
    static func eventually(_ message: String, _ condition: () -> Bool) async throws {
        let end = Date().addingTimeInterval(5)
        while !condition(), Date() < end { try await Task.sleep(nanoseconds: 5_000_000) }
        try require(condition(), message)
    }
    static func find<T: NSView>(_ type: T.Type, in root: NSView) -> [T] {
        var result = (root as? T).map { [$0] } ?? []
        for child in root.subviews { result += find(type, in: child) }
        return result
    }
    static func main() {
        let env = ProcessInfo.processInfo.environment
        guard env["BERTH_GIT_REVIEW_TEST"] == "1", let home = env["HOME"], env["CFFIXED_USER_HOME"] == home,
              let berth = env["BERTH_HOME"], berth.hasPrefix(home + "/") else {
            fputs("GitReviewChecks requires explicit isolated HOME and BERTH_HOME\n", stderr); exit(2)
        }
        let app = NSApplication.shared
        app.setActivationPolicy(.accessory); app.finishLaunching()
        Task { @MainActor in
            do { try await run(berth: berth); print("GitReviewChecks: \(checks) checks passed"); exit(0) }
            catch { fputs("GitReviewChecks failed: \(error.localizedDescription)\n", stderr); exit(1) }
        }
        // Preference callbacks and focus updates run through the same native
        // event loop as the app, not a nested synchronous Foundation run loop.
        app.run()
    }
    static func run(berth: String) async throws {
        let project = try JSONDecoder().decode(BerthGroup.self, from: Data(#"{"name":"review-fixture","branch":"feature/review","root_dir":"/fixture/review","services":[],"members":[]}"#.utf8))
        let tree = try JSONDecoder().decode(GitTree.self, from: Data(#"{"root":"/fixture/review","branch":"feature/review","head":"abcdef0123456789","upstream":"origin/feature/review","ahead":2,"behind":1,"staged":1,"unstaged":0,"untracked":0,"conflicts":1,"worktrees":[{"path":"/fixture/review","branch":"feature/review","current":true}],"files":[{"path":"conflict.swift","status":"DU"},{"path":"new.swift","old_path":"旧文件.swift","status":"R ","additions":2,"deletions":0}]}"#.utf8))
        let git = GitStore(overview: tree.overview, tree: tree, selectedPath: "conflict.swift")
        var detailWidth: CGFloat = 0
        func content(_ value: BerthGroup) -> some View {
            CodeView(git: git, project: value).onPreferenceChange(GitReviewDetailWidthKey.self) { detailWidth = $0 }
        }
        let host = NSHostingView(rootView: content(project))
        host.sizingOptions = []
        let window = NSWindow(contentRect: NSRect(x: 0, y: 0, width: 950, height: 580), styleMask: [.titled, .resizable], backing: .buffered, defer: false)
        window.isReleasedWhenClosed = false; window.contentView = host
        NSApp.activate(ignoringOtherApps: true); window.makeKeyAndOrderFront(nil)
        func capture(_ name: String) throws {
            host.layoutSubtreeIfNeeded()
            guard let image = host.bitmapImageRepForCachingDisplay(in: host.bounds),
                  image.pixelsWide > 0 else { throw Failure(message: "native Git image unavailable") }
            host.cacheDisplay(in: host.bounds, to: image)
            guard let png = image.representation(using: .png, properties: [:]) else { throw Failure(message: "native Git PNG unavailable") }
            try png.write(to: URL(fileURLWithPath: berth).appendingPathComponent(name + "-native.png"))
        }
        defer { try? capture("git-review-final"); window.contentView = nil; window.close() }
        try await eventually("native Git window is not key") { window.isKeyWindow }
        let settings = UISettings.shared
        try await eventually("wide Git review ignored the effective configured width") { abs(detailWidth - settings.detailWidth(for: .git)) < 1 }
        let serviceWidth = settings.detailWidth(for: .services)
        settings.resizeDetail(450, for: .git)
        try await eventually("Git width did not update actual geometry independently of services") {
            abs(detailWidth - 450) < 1 && settings.detailWidth(for: .services) == serviceWidth
        }
        NotificationCenter.default.post(name: .init("option-berth.git.find"), object: project.name)
        try await eventually("native Git search field is unavailable") { find(NSTextField.self, in: host).contains { $0.placeholderString == "筛选文件或原路径" && $0.currentEditor() != nil } }
        let field = find(NSTextField.self, in: host).first { $0.placeholderString == "筛选文件或原路径" }!
        field.stringValue = "旧文件"
        if let editor = field.currentEditor() as? NSTextView {
            editor.string = "旧文件"
            NotificationCenter.default.post(name: NSText.didChangeNotification, object: editor)
        }
        field.delegate?.controlTextDidChange?(Notification(name: NSControl.textDidChangeNotification, object: field))
        try await eventually("filter retained a hidden file's stale diff selection") { git.selectedPath == nil }
        try capture("git-review-wide")
        window.setContentSize(NSSize(width: 560, height: 440))
        try await eventually("narrow Git review did not switch to a draggable vertical layout") {
            find(NSSplitView.self, in: host).contains { !$0.isVertical } && detailWidth <= 560
        }
        try require(host.bounds.width <= 560 && settings.detailWidth(for: .git) == 450,
                    "narrow viewport rewrote preference or forced content wider")
        try capture("git-review-narrow")
        let other = try JSONDecoder().decode(BerthGroup.self, from: Data(#"{"name":"other-worktree","root_dir":"/fixture/other","services":[],"members":[]}"#.utf8))
        try require(git.isFor(project) && !git.isFor(other), "cached facts lost their requested-root identity")
        host.rootView = content(other)
        try await eventually("new worktree rendered another worktree's searchable changes") {
            !find(NSTextField.self, in: host).contains { $0.placeholderString == "筛选文件或原路径" }
        }
    }
}
