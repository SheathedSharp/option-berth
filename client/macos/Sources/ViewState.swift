import SwiftUI

/// Session-primary capability panels (v13). Not peer faces — overlays on the session stage.
enum CapabilityPanel: String, Equatable {
    case services
    case git
}

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

    /// Shared workbench focus: service + optional Git path for the current berth.
    @Published var selectedServiceKey: String?
    @Published var selectedGitPath: String?

    /// Services / Git as session capabilities (overlay). Nil = session canvas only.
    @Published var capabilityPanel: CapabilityPanel?

    init(scope: Scope = .terminal("")) { self.scope = scope }

    func rememberService(_ ref: ServiceRef?) {
        selectedServiceKey = ref?.key
    }

    func rememberGitPath(_ path: String?) {
        let trimmed = path?.trimmingCharacters(in: .whitespacesAndNewlines) ?? ""
        selectedGitPath = trimmed.isEmpty ? nil : trimmed
    }

    func openServicesPanel() {
        capabilityPanel = .services
    }

    func openGitPanel(path: String? = nil) {
        if let path { rememberGitPath(path) }
        capabilityPanel = .git
    }

    func dismissCapabilityPanel() {
        capabilityPanel = nil
    }

    /// Legacy helper — session-primary maps Git to a capability panel, not a peer face.
    func showCode(project: String, path: String? = nil) {
        scope = .terminal(project)
        openGitPanel(path: path)
    }

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
    /// Selecting a worktree always lands on the session stage (v13).
    func selectProject(_ name: String) {
        scope = .terminal(name)
        capabilityPanel = nil
        selectedServiceKey = nil
        selectedGitPath = nil
    }
    /// Module shortcuts open capability panels (or dismiss to session), never peer homes.
    func show(_ face: Face, projects: [String]) {
        guard let name = project.flatMap({ projects.contains($0) ? $0 : nil }) ?? projects.first else { return }
        scope = .terminal(name)
        switch face {
        case .services: capabilityPanel = .services
        case .code: capabilityPanel = .git
        case .terminal: capabilityPanel = nil
        }
    }
}
