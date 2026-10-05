import AppKit
import SwiftUI
import BerthTerminal
@testable import BerthClient

@main struct CommandChecks: App {
    @StateObject private var fixture = CommandFixture()
    var body: some Scene {
        Window("Command checks", id: "command-checks") {
            BoardView(store: fixture.store, services: fixture.services, git: fixture.git,
                      views: fixture.views, settings: fixture.settings, performAction: fixture.perform)
                .frame(minWidth: 900, minHeight: 640)
                .task { await fixture.runOnce() }
        }
        .defaultSize(width: 1060, height: 720)
        .commands {
            WorkspaceCommandMenus(views: fixture.views, shortcuts: fixture.shortcuts,
                                  services: fixture.services, perform: fixture.perform)
        }
    }
}

@MainActor final class CommandFixture: ObservableObject {
    let store = BoardStore(fixture: [])
    let services: ServicesStore
    let git = GitStore(overview: nil)
    let views = ViewState(scope: .services("alpha"))
    let settings = UISettings.shared
    let shortcuts = WorkspaceShortcuts.shared
    private var started = false
    private var performed: [WorkspaceAction] = []
    private var checks = 0
    init() {
        let data = Data(#"[{"name":"beta","repo":"beta","worktree":"feature","branch":"fix/second","root_dir":"/fixture/beta","services":[],"members":[]},{"name":"alpha","repo":"alpha","worktree":"feature","branch":"feature/first","root_dir":"/fixture/alpha","services":[],"members":[]}]"#.utf8)
        services = ServicesStore(fixture: try! JSONDecoder().decode([BerthGroup].self, from: data))
    }
    func perform(_ action: WorkspaceAction) {
        performed.append(action)
        if let ordinal = action.ordinal { views.selectVisibleProject(at: ordinal, in: services.projects); return }
        let names = views.orderedProjects(services.projects, filtered: false).map(\.name)
        switch action {
        case .connect: views.requestConnection()
        case .services: views.show(.services, projects: names)
        case .code: views.show(.code, projects: names)
        case .terminal: views.show(.terminal, projects: names)
        case .sidebar: views.railVisible.toggle()
        default: break
        }
    }
    private struct Failure: LocalizedError {
        let message: String
        var errorDescription: String? { message }
    }
    private func expect(_ condition: @autoclosure () -> Bool, _ message: String) throws {
        guard condition() else { throw Failure(message: message) }; checks += 1
    }
    private func eventually(_ message: String, _ condition: () -> Bool) async throws {
        let end = Date().addingTimeInterval(5)
        while !condition(), Date() < end { try await Task.sleep(nanoseconds: 5_000_000) }
        try expect(condition(), message)
    }
    private func items(_ menu: NSMenu?) -> [NSMenuItem] {
        guard let menu else { return [] }
        return menu.items.flatMap { [$0] + items($0.submenu) }
    }
    @discardableResult private func key(_ text: String, modifiers: NSEvent.ModifierFlags = .command) -> Bool {
        let codes: [String: UInt16] = ["1":18,"2":19,"9":25,"s":1,"g":5,"t":17,"b":11,"j":38,"p":35,"n":45,"c":8,"v":9,"a":0,"z":6]
        let event = NSEvent.keyEvent(with: .keyDown, location: .zero, modifierFlags: modifiers,
            timestamp: ProcessInfo.processInfo.systemUptime, windowNumber: NSApp.keyWindow?.windowNumber ?? 0,
            context: nil, characters: modifiers.contains(.shift) ? text.uppercased() : text,
            charactersIgnoringModifiers: text, isARepeat: false, keyCode: codes[text] ?? 0)!
        NSApp.mainMenu?.update()
        return NSApp.mainMenu?.performKeyEquivalent(with: event) ?? false
    }
    private func rejected(_ text: String, _ message: String, modifiers: NSEvent.ModifierFlags = .command) async throws {
        let count = performed.count
        key(text, modifiers: modifiers)
        try await Task.sleep(nanoseconds: 80_000_000)
        try expect(performed.count == count, message)
    }
    func runOnce() async {
        guard !started else { return }; started = true
        let env = ProcessInfo.processInfo.environment
        guard env["BERTH_COMMAND_TEST"] == "1", let home = env["HOME"],
              env["CFFIXED_USER_HOME"] == home, let berth = env["BERTH_HOME"], berth.hasPrefix(home + "/") else {
            fputs("CommandChecks requires an explicitly isolated HOME and BERTH_HOME\n", stderr); exit(2)
        }
        do { try await run(home: home, berth: berth); print("CommandChecks: \(checks) checks passed"); exit(0) }
        catch { fputs("CommandChecks failed: \(error.localizedDescription)\n", stderr); exit(1) }
    }
    private func run(home: String, berth: String) async throws {
        NSApp.setActivationPolicy(.regular); NSApp.activate(ignoringOtherApps: true)
        try await eventually("native command menu not installed") { items(NSApp.mainMenu).contains { $0.title == "Worktrees" } && NSApp.keyWindow != nil }
        let window = NSApp.keyWindow!
        try expect(items(NSApp.mainMenu).contains { $0.title == "1 · alpha" && $0.keyEquivalent == "1" }, "menu order does not match the visible rail")
        key("2")
        try await eventually("Cmd+2 did not select the second worktree") { views.scope == .services("beta") }
        key("g", modifiers: [.command, .option])
        try await eventually("Git mnemonic did not use the native menu") { views.scope == .code("beta") }
        key("1")
        try await eventually("worktree selection reset the Git module") { views.scope == .code("alpha") }
        views.projectQuery = "SECOND"
        try await eventually("filtered rail did not update the native menu") { items(NSApp.mainMenu).contains { $0.title == "1 · beta" } }
        key("1")
        try await eventually("filtered Cmd+1 selected a hidden project") { views.scope == .code("beta") }
        try await rejected("9", "out-of-range worktree command executed")
        views.projectQuery = ""
        key("t", modifiers: [.command, .option])
        try await eventually("Console mnemonic failed") { views.scope == .terminal("beta") }
        key("s", modifiers: [.command, .option])
        try await eventually("Services mnemonic failed") { views.scope == .services("beta") }
        key("b")
        try await eventually("sidebar mnemonic failed") { !views.railVisible }
        key("b")
        try await eventually("sidebar mnemonic failed to restore rail") { views.railVisible }

        let directory = URL(fileURLWithPath: berth).appendingPathComponent("config")
        try FileManager.default.createDirectory(at: directory, withIntermediateDirectories: true)
        let file = directory.appendingPathComponent("keybindings.json")
        try Data(#"{"schemaVersion":1,"bindings":{"code":{"key":"j","option":true}}}"#.utf8).write(to: file, options: .atomic)
        try await eventually("file keybinding did not reach the native menu") {
            items(NSApp.mainMenu).contains { $0.title == WorkspaceAction.code.title && $0.keyEquivalent == "j" }
        }
        try await rejected("g", "retired default remained active", modifiers: [.command, .option])
        key("j", modifiers: [.command, .option])
        try await eventually("custom key failed to activate Git") { views.scope == .code("beta") }

        let editor = NSTextView(frame: NSRect(x: 20, y: 10, width: 240, height: 60))
        editor.isEditable = true; editor.allowsUndo = true
        window.contentView!.addSubview(editor); window.makeFirstResponder(editor)
        editor.setMarkedText("拼音", selectedRange: NSRange(location: 2, length: 0), replacementRange: NSRange(location: NSNotFound, length: 0))
        try expect(editor.hasMarkedText(), "native editor did not enter composition")
        try await rejected("1", "worktree key consumed editor composition")
        editor.unmarkText(); editor.string = "native edit"; editor.selectAll(nil)
        key("c")
        try expect(NSPasteboard.general.string(forType: .string) == "native edit", "native Copy shortcut was lost")
        editor.string = ""; key("v")
        try await eventually("native Paste shortcut was lost") { editor.string == "native edit" }
        editor.removeFromSuperview()

        let session = TerminalSession(worktree: home, title: "IME fixture")
        session.terminal.frame = NSRect(x: 20, y: 10, width: 280, height: 70)
        window.contentView!.addSubview(session.terminal); window.makeFirstResponder(session.terminal)
        session.terminal.setMarkedText("中文输入", selectedRange: NSRange(location: 4, length: 0), replacementRange: NSRange(location: NSNotFound, length: 0))
        try expect(session.terminal.hasMarkedText(), "terminal did not enter native composition")
        try await rejected("1", "worktree key consumed terminal composition")
        session.terminal.unmarkText(); session.terminal.removeFromSuperview(); window.makeFirstResponder(nil)
        try expect(!session.isActive, "IME test launched a process")

        let sheet = NSWindow(contentRect: NSRect(x: 0, y: 0, width: 300, height: 100), styleMask: [.titled], backing: .buffered, defer: false)
        sheet.isReleasedWhenClosed = false
        window.beginSheet(sheet)
        try await eventually("native sheet did not attach") { window.attachedSheet === sheet }
        try await rejected("1", "workspace navigation escaped a native sheet")
        window.endSheet(sheet); sheet.orderOut(nil)
        window.makeKeyAndOrderFront(nil)
        try await eventually("native sheet did not detach") { window.attachedSheet == nil && window.isKeyWindow }

        // A timer registered in modal-panel mode can cancel the genuine directory
        // chooser while its nested AppKit loop is running. Never choose a path.
        var observedPanel = false
        var correctPanel = false
        let before = views.scope
        let deadline = Date().addingTimeInterval(5)
        let timer = Timer(timeInterval: 0.02, repeats: true) { [weak self] timer in
            MainActor.assumeIsolated {
                if let panel = NSApp.modalWindow as? NSOpenPanel {
                    observedPanel = true
                    correctPanel = panel.canChooseDirectories && !panel.canChooseFiles && !panel.allowsMultipleSelection
                    _ = self?.key("1")
                    panel.cancel(nil); timer.invalidate()
                } else if Date() >= deadline {
                    fputs("CommandChecks: directory chooser deadline exceeded\n", stderr); exit(1)
                }
            }
        }
        RunLoop.main.add(timer, forMode: .common); RunLoop.main.add(timer, forMode: .modalPanel)
        defer { timer.invalidate() }
        key("n")
        try await eventually("Cmd+N did not open and cancel the native directory chooser") { observedPanel && !views.projectOperationPending }
        try expect(correctPanel && views.scope == before, "directory chooser allowed file selection or concurrent navigation")
        try expect(TerminalSessions.shared.sessions.isEmpty, "command navigation started a PTY")
        try FileManager.default.removeItem(at: file)
        try await eventually("deleting keybindings did not restore native defaults") { items(NSApp.mainMenu).contains { $0.title == WorkspaceAction.code.title && $0.keyEquivalent == "g" } }
        window.contentView?.layoutSubtreeIfNeeded()
        if let host = window.contentView, let bitmap = host.bitmapImageRepForCachingDisplay(in: host.bounds) {
            host.cacheDisplay(in: host.bounds, to: bitmap)
            if let png = bitmap.representation(using: .png, properties: [:]) {
                try png.write(to: URL(fileURLWithPath: berth).appendingPathComponent("commands-native.png"))
            }
        }
    }
}
