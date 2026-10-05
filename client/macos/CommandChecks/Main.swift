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
    var performed: [WorkspaceAction] = []
    var checks = 0
    init() {
        // Declared worktrees require config_path; an inferred directory must not
        // be promoted into the sidebar merely because it appears in a fixture.
        let data = Data(#"[{"name":"beta","repo":"beta","worktree":"feature","branch":"fix/second","root_dir":"/fixture/beta","config_path":"/fixture/beta/oberth.yaml","services":[],"members":[]},{"name":"alpha","repo":"alpha","worktree":"feature","branch":"feature/first","root_dir":"/fixture/alpha","config_path":"/fixture/alpha/oberth.yaml","services":[],"members":[]}]"#.utf8)
        services = ServicesStore(fixture: try! JSONDecoder().decode([BerthGroup].self, from: data))
    }
    func perform(_ action: WorkspaceAction) {
        guard views.allowsCommands, WorkspaceInputContext.allowsNavigation(in: NSApp.keyWindow, action: action) else { return }
        performed.append(action)
        if let ordinal = action.ordinal { views.selectVisibleProject(at: ordinal, in: services.projects); return }
        let names = views.orderedProjects(services.projects, filtered: false).map(\.name)
        switch action {
        case .connect: views.requestConnection()
        case .services: views.show(.services, projects: names)
        case .code: views.show(.code, projects: names)
        case .terminal: views.show(.terminal, projects: names)
        case .sidebar: views.railVisible.toggle()
        case .find:
            if let terminal = HostedTerminalView.containing(NSApp.keyWindow?.firstResponder) { showTerminalFind(terminal) }
            else if case .code = views.scope { NotificationCenter.default.post(name: .init("option-berth.git.find"), object: views.project) }
        default: break
        }
    }
    struct Failure: LocalizedError {
        let message: String
        var errorDescription: String? { message }
    }
    func expect(_ condition: @autoclosure () -> Bool, _ message: String) throws {
        guard condition() else { throw Failure(message: message) }; checks += 1
    }
    func eventually(_ message: String, _ condition: () -> Bool) async throws {
        let end = Date().addingTimeInterval(5)
        while !condition(), Date() < end { try await Task.sleep(nanoseconds: 5_000_000) }
        try expect(condition(), message)
    }
    func items(_ menu: NSMenu?) -> [NSMenuItem] {
        guard let menu else { return [] }
        return menu.items.flatMap { [$0] + items($0.submenu) }
    }
    func descendants(_ view: NSView) -> [NSView] { [view] + view.subviews.flatMap(descendants) }
    @discardableResult func key(_ text: String, modifiers: NSEvent.ModifierFlags = .command) -> Bool {
        let codes: [String: UInt16] = ["1":18,"2":19,"9":25,"s":1,"g":5,"t":17,"b":11,"j":38,"p":35,"n":45,"c":8,"v":9,"a":0,"z":6,"f":3]
        let event = NSEvent.keyEvent(with: .keyDown, location: .zero, modifierFlags: modifiers,
            timestamp: ProcessInfo.processInfo.systemUptime, windowNumber: NSApp.keyWindow?.windowNumber ?? 0,
            context: nil, characters: modifiers.contains(.shift) ? text.uppercased() : text,
            charactersIgnoringModifiers: text, isARepeat: false, keyCode: codes[text] ?? 0)!
        NSApp.mainMenu?.update()
        return NSApp.mainMenu?.performKeyEquivalent(with: event) ?? false
    }
    func rejected(_ text: String, _ message: String, modifiers: NSEvent.ModifierFlags = .command) async throws {
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
        let deadline = DispatchWorkItem { fputs("CommandChecks: native event deadline exceeded\n", stderr); exit(1) }
        DispatchQueue.global().asyncAfter(deadline: .now() + 90, execute: deadline)
        defer { deadline.cancel() }
        do { try await run(home: home, berth: berth); print("CommandChecks: \(checks) checks passed"); exit(0) }
        catch {
            fputs("CommandChecks failed: \(error.localizedDescription)\n", stderr)
            // Only this synthetic test application's menu is included.
            for item in items(NSApp.mainMenu) { print("MENU \(item.title) | \(item.keyEquivalent) | \(item.isEnabled)") }
            exit(1)
        }
    }
}
