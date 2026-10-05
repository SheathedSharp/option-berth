import AppKit
import SwiftUI
@testable import BerthClient

@main @MainActor enum GitReviewChecks {
    static func require(_ value: @autoclosure () -> Bool, _ message: String) {
        guard value() else { fatalError(message) }
    }
    static func pump(_ seconds: TimeInterval = 0.1) {
        let end = Date().addingTimeInterval(seconds)
        while Date() < end { RunLoop.main.run(until: Date().addingTimeInterval(0.005)) }
    }
    static func find<T: NSView>(_ type: T.Type, in root: NSView) -> [T] {
        var result = (root as? T).map { [$0] } ?? []
        for child in root.subviews { result += find(type, in: child) }
        return result
    }
    static func main() throws {
        let app = NSApplication.shared
        app.setActivationPolicy(.accessory); app.finishLaunching()
        let project = try JSONDecoder().decode(BerthGroup.self, from: Data(#"{"name":"review-fixture","branch":"feature/review","root_dir":"/fixture/review","services":[],"members":[]}"#.utf8))
        let tree = try JSONDecoder().decode(GitTree.self, from: Data(#"{"root":"/fixture/review","branch":"feature/review","head":"abcdef0123456789","upstream":"origin/feature/review","ahead":2,"behind":1,"staged":1,"unstaged":0,"untracked":0,"conflicts":1,"worktrees":[{"path":"/fixture/review","branch":"feature/review","current":true}],"files":[{"path":"conflict.swift","status":"DU"},{"path":"new.swift","old_path":"旧文件.swift","status":"R ","additions":2,"deletions":0}]}"#.utf8))
        let git = GitStore(overview: tree.overview, tree: tree, selectedPath: "conflict.swift")
        var detailWidth: CGFloat = 0
        // Observe actual geometry, not a requested frame or preference value.
        func content(_ value: BerthGroup) -> some View {
            CodeView(git: git, project: value)
                .onPreferenceChange(GitReviewDetailWidthKey.self) { detailWidth = $0 }
        }
        let host = NSHostingView(rootView: content(project))
        host.sizingOptions = []
        let window = NSWindow(contentRect: NSRect(x: 0, y: 0, width: 950, height: 580), styleMask: [.titled, .resizable], backing: .buffered, defer: false)
        window.isReleasedWhenClosed = false; window.contentView = host; window.makeKeyAndOrderFront(nil)
        defer { window.contentView = nil; window.close() }
        pump(0.25)
        let settings = UISettings.shared
        require(abs(detailWidth - settings.detailWidth(for: .git)) < 1, "wide Git review ignored the effective configured width")
        let serviceWidth = settings.detailWidth(for: .services)
        settings.resizeDetail(450, for: .git); pump(0.15)
        require(abs(detailWidth - 450) < 1 && settings.detailWidth(for: .services) == serviceWidth,
                "Git width did not update actual geometry independently of services")
        NotificationCenter.default.post(name: .init("option-berth.git.find"), object: project.name)
        pump()
        guard let field = find(NSTextField.self, in: host).first(where: { $0.placeholderString == "筛选文件或原路径" }) else {
            fatalError("native Git search field is unavailable")
        }
        require(field.currentEditor() != nil, "Git command did not focus native search")
        field.stringValue = "旧文件"
        if let editor = field.currentEditor() as? NSTextView {
            editor.string = "旧文件"
            NotificationCenter.default.post(name: NSText.didChangeNotification, object: editor)
        }
        field.delegate?.controlTextDidChange?(Notification(name: NSControl.textDidChangeNotification, object: field))
        pump()
        require(git.selectedPath == nil, "filter retained a hidden file's stale diff selection")
        func capture(_ name: String) throws {
            guard let berth = ProcessInfo.processInfo.environment["BERTH_HOME"] else { fatalError("isolated BERTH_HOME required") }
            guard let image = host.bitmapImageRepForCachingDisplay(in: host.bounds) else { fatalError("native Git image unavailable") }
            host.cacheDisplay(in: host.bounds, to: image)
            try image.representation(using: .png, properties: [:])!.write(to: URL(fileURLWithPath: berth).appendingPathComponent(name + "-native.png"))
        }
        try capture("git-review-wide")
        window.setContentSize(NSSize(width: 560, height: 440)); pump(0.25)
        require(find(NSSplitView.self, in: host).contains { !$0.isVertical }, "narrow Git review did not switch to a draggable vertical layout")
        require(host.bounds.width <= 560 && detailWidth <= 560 && settings.detailWidth(for: .git) == 450,
                "narrow viewport rewrote preference or forced content wider")
        try capture("git-review-narrow")
        let other = try JSONDecoder().decode(BerthGroup.self, from: Data(#"{"name":"other-worktree","root_dir":"/fixture/other","services":[],"members":[]}"#.utf8))
        require(git.isFor(project) && !git.isFor(other), "cached facts lost their requested-root identity")
        host.rootView = content(other)
        pump(0.2)
        require(!find(NSTextField.self, in: host).contains { $0.placeholderString == "筛选文件或原路径" },
                "new worktree rendered the old worktree's searchable changes before reload")
        print("PASS: native Git find routing, Unicode filtering/selection reconciliation, live independent detail width, narrow native split and root ownership")
    }
}
