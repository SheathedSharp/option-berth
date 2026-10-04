import SwiftUI

/// Shared navigation state for the project window and its menu.
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

    init(scope: Scope = .services("")) {
        self.scope = scope
    }

    enum Face {
        case services
        case code
        case terminal
    }

    var project: String? { scope.projectName }

    func selectProject(_ name: String) {
        switch scope {
        case .code: scope = .code(name)
        case .terminal, .console: scope = .terminal(name)
        case .services: scope = .services(name)
        }
    }

    func show(_ face: Face, projects: [String]) {
        guard let name = project ?? projects.first else { return }
        switch face {
        case .services: scope = .services(name)
        case .code: scope = .code(name)
        case .terminal: scope = .terminal(name)
        }
    }
}
