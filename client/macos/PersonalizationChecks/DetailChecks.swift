import AppKit
import SwiftUI
@testable import BerthClient

extension PersonalizationChecks {
    static func detailChecks(settings: UISettings, root: URL) async throws {
        let file = root.appendingPathComponent("config/settings.json")
        try expect(settings.detailWidth(for: .services) == 412 && settings.detailWidth(for: .git) == 412,
                   "shared legacy width must be a read-only fallback for each pane")
        settings.resizeDetail(440, for: .services)
        try expect(settings.detailWidth(for: .services) == 440 && settings.detailWidth(for: .git) == 412,
                   "service drag changed Git preference")
        try expect(UserDefaults.standard.double(forKey: "detailWidth") == 412, "legacy shared width was overwritten")
        let reloaded = UISettings(defaults: .standard)
        try expect(reloaded.detailWidth(for: .services) == 440 && reloaded.detailWidth(for: .git) == 412,
                   "separate width preference did not survive reloading")
        func write(_ text: String) throws { try Data(text.utf8).write(to: file, options: .atomic) }
        let valid = #"{"schemaVersion":1,"servicesDetailWidth":470,"gitDetailWidth":360}"#
        try write(valid)
        try await eventually("file widths not applied") {
            settings.detailWidth(for: .services) == 470 && settings.detailWidth(for: .git) == 360
        }
        settings.resizeDetail(450, for: .services)
        try expect(settings.detailWidth(for: .git) == 360, "file-owned drag changed another module")
        let afterDrag = try Data(contentsOf: file)
        try expect(afterDrag == Data(valid.utf8), "detail drag overwrote user file")
        try expect(UserDefaults.standard.double(forKey: ClientDetailPane.services.storageKey) == 440,
                   "file-owned drag changed persistent fallback")
        try write(#"{"schemaVersion":1,"servicesDetailWidth":470,"gitDetailWidth":360,"reduceMotion":true}"#)
        try await eventually("unrelated file edit not observed") { settings.configuration.preferences.reduceMotion == true }
        try expect(settings.detailWidth(for: .services) == 450, "unrelated edit moved detail divider")
        try write(#"{"schemaVersion":1,"servicesDetailWidth":460,"gitDetailWidth":380}"#)
        try await eventually("changed width did not supersede local drag") {
            settings.detailWidth(for: .services) == 460 && settings.detailWidth(for: .git) == 380
        }
        try write(#"{"schemaVersion":1,"servicesDetailWidth":9999}"#)
        try await eventually("invalid width missing diagnostic") { !settings.configuration.problems.isEmpty }
        try expect(settings.detailWidth(for: .services) == 460 && settings.detailWidth(for: .git) == 380,
                   "invalid edit replaced last-good widths")
        try FileManager.default.removeItem(at: file)
        try await eventually("deletion did not restore independent fallback widths") {
            settings.detailWidth(for: .services) == 440 && settings.detailWidth(for: .git) == 412 && settings.configuration.problems.isEmpty
        }

        let project = try JSONDecoder().decode(BerthGroup.self, from: Data(#"{"name":"width-fixture","root_dir":"/fixture/widths","services":[],"members":[]}"#.utf8))
        let tree = try JSONDecoder().decode(GitTree.self, from: Data(#"{"root":"/fixture/widths","branch":"feature/layout","staged":0,"unstaged":0,"untracked":0,"conflicts":0,"files":[]}"#.utf8))
        let serviceHost = NSHostingView(rootView: ServicesView(store: ServicesStore(fixture: [project]), group: project, ports: []))
        let gitHost = NSHostingView(rootView: CodeView(git: GitStore(overview: tree.overview, tree: tree), project: project))
        for (pane, host) in [(ClientDetailPane.services, serviceHost as NSView), (.git, gitHost as NSView)] {
            let window = NSWindow(contentRect: NSRect(x: 0, y: 0, width: 1060, height: 500),
                                  styleMask: [.titled, .resizable], backing: .buffered, defer: false)
            window.isReleasedWhenClosed = false
            window.contentView = host; window.makeKeyAndOrderFront(nil)
            defer { window.contentView = nil; window.close() }
            try await eventually("detail test window not key") { window.isKeyWindow }
            host.layoutSubtreeIfNeeded()
            let before = settings.detailWidth(for: pane)
            let other: ClientDetailPane = pane == .services ? .git : .services
            let otherWidth = settings.detailWidth(for: other)
            let x = host.bounds.width - before - SplitHandle.hitWidth / 2
            for (index, kind) in [NSEvent.EventType.leftMouseDown, .leftMouseDragged, .leftMouseUp].enumerated() {
                guard let event = NSEvent.mouseEvent(with: kind, location: NSPoint(x: x + (index == 0 ? 0 : 24), y: 220),
                    modifierFlags: [], timestamp: ProcessInfo.processInfo.systemUptime + Double(index) * 0.02,
                    windowNumber: window.windowNumber, context: nil, eventNumber: index + 1, clickCount: 1, pressure: 1) else {
                    throw Failure(message: "detail drag event unavailable")
                }
                NSApp.postEvent(event, atStart: false)
            }
            try await eventually("rightward divider drag did not shrink its trailing pane") {
                settings.detailWidth(for: pane) < before - 10
            }
            try expect(settings.detailWidth(for: other) == otherWidth, "native drag changed the other detail module")
            try capture(host, at: root.appendingPathComponent("personalization-" + pane.rawValue + "-detail-native.png"))
            let preferred = settings.detailWidth(for: pane)
            window.setContentSize(NSSize(width: 600, height: 460))
            try await Task.sleep(nanoseconds: 30_000_000)
            host.layoutSubtreeIfNeeded()
            try expect(settings.detailWidth(for: pane) == preferred, "viewport resize overwrote preferred width")
            try capture(host, at: root.appendingPathComponent("personalization-" + pane.rawValue + "-narrow-native.png"))
        }
    }
}
