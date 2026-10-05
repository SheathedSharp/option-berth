import SwiftUI

/// Shared navigation state for the project window, sidebar and command menu.
@MainActor
final class ViewState: ObservableObject {
    @Published var scope: Scope
    @Published var railVisible = true
    @Published var showingSettings = false
    @Published var showingSessions = false
    @Published var showingActions = false
    @Published var showingRecovery = false
    @Published var showingUpdates = false
    @Published var showingGuide = false
    @Published var guideTarget: TourTarget = .connect
    @Published var projectQuery = ""
    @Published private(set) var connectionRequest = UUID()
    @Published var projectOperationPending = false
    @Published var projectDialogPresented = false

    init(scope: Scope = .services("")) { self.scope = scope }

    enum Face { case services, code, terminal }
    var project: String? { scope.projectName }
    var allowsCommands: Bool {
        !showingGuide && !showingSettings && !showingSessions && !showingRecovery && !showingUpdates
            && !projectOperationPending && !projectDialogPresented
    }
    func requestConnection() {
        guard allowsCommands else { return }
        connectionRequest = UUID()
    }
    func orderedProjects(_ projects: [BerthGroup], filtered: Bool = true) -> [BerthGroup] {
        projects.filter { !filtered || WorkspaceProjectNavigation.matches(query: projectQuery, title: $0.displayName, branch: $0.branch) }
            .sorted { $0.name < $1.name }
    }
    func selectVisibleProject(at ordinal: Int, in projects: [BerthGroup]) {
        guard allowsCommands,
              let name = WorkspaceProjectNavigation.project(at: ordinal, in: orderedProjects(projects).map(\.name)) else { return }
        selectProject(name)
    }
    func selectProject(_ name: String) {
        switch scope {
        case .code: scope = .code(name)
        case .terminal, .console: scope = .terminal(name)
        case .services: scope = .services(name)
        }
    }
    func show(_ face: Face, projects: [String]) {
        guard let name = project.flatMap({ projects.contains($0) ? $0 : nil }) ?? projects.first else { return }
        switch face {
        case .services: scope = .services(name)
        case .code: scope = .code(name)
        case .terminal: scope = .terminal(name)
        }
    }
}
