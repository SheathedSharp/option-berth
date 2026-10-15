import Foundation

public struct BerthGitPathRequest: Equatable {
    public let worktree: String
    public let path: String
    public init(worktree: String, path: String) {
        self.worktree = worktree
        self.path = path
    }
}

extension Notification.Name {
    static let berthOpenGitPath = Notification.Name("berth.openGitPath")
}
