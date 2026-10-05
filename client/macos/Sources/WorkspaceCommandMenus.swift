import SwiftUI
import AppKit

/// Shared with the native test application. No event monitor, alternate key
/// table, or interception of the standard Edit and Window menus is introduced.
struct WorkspaceCommandMenus: Commands {
    @ObservedObject var views: ViewState
    @ObservedObject var shortcuts: WorkspaceShortcuts
    @ObservedObject var services: ServicesStore
    let perform: (WorkspaceAction) -> Void

    var body: some Commands {
        CommandGroup(replacing: .newItem) { item(.connect) }
        CommandGroup(replacing: .printItem) {}
        CommandGroup(replacing: .sidebar) {}
        CommandGroup(after: .appInfo) {
            Button("检查更新… / Check for updates…") { send(.updates) }
        }
        CommandGroup(replacing: .appSettings) { item(.settings) }
        CommandGroup(replacing: .help) {
            Button("使用指引… / Getting started…") {
                if allowed { views.showingGuide = true }
            }
        }
        CommandMenu(MenuBar.viewTitle) {
            Button("命令面板… / Command panel…") {
                if allowed { views.showingActions = true }
            }.keyboardShortcut("p", modifiers: [.command, .shift]).disabled(!views.allowsCommands)
            Divider()
            ForEach(WorkspaceAction.allCases.filter { $0 != .settings && $0 != .connect && $0.ordinal == nil }) { action in
                item(action)
            }
        }
        CommandMenu("Worktrees") {
            ForEach(WorkspaceAction.worktrees) { action in
                item(action).disabled(worktreeName(action) == nil)
            }
        }
    }
    private var allowed: Bool { views.allowsCommands && WorkspaceInputContext.allowsNavigation(in: NSApp.keyWindow) }
    private func send(_ action: WorkspaceAction) { if allowed { perform(action) } }
    private func worktreeName(_ action: WorkspaceAction) -> String? {
        guard let ordinal = action.ordinal else { return nil }
        return WorkspaceProjectNavigation.project(at: ordinal, in: views.orderedProjects(services.projects).map(\.name))
    }
    private func item(_ action: WorkspaceAction) -> some View {
        let title = worktreeName(action).map { "\(action.ordinal!) · " + $0 } ?? action.title
        return Button(title) { send(action) }
            .keyboardShortcut(shortcuts.shortcut(action).equivalent, modifiers: shortcuts.shortcut(action).modifiers)
            .disabled(!views.allowsCommands)
    }
}
