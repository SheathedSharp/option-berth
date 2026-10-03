import SwiftUI

/// Shared navigation state for the project window and its menu.
@MainActor
final class ViewState: ObservableObject {
    @Published var scope: Scope
    @Published var railVisible = true
    @Published var showingSettings = false

    init(scope: Scope = .services("")) {
        self.scope = scope
    }

    enum Face {
        case services
        case code
    }

    var project: String? { scope.projectName }

    func show(_ face: Face, projects: [String]) {
        guard let name = project ?? projects.first else { return }
        switch face {
        case .services: scope = .services(name)
        case .code: scope = .code(name)
        }
    }
}
