import Foundation

@main enum WorkspaceCommandTests {
    static func main() throws {
        var checks = 0
        func expect(_ value: @autoclosure () -> Bool, _ message: String) {
            guard value() else { fatalError(message) }; checks += 1
        }
        let all = WorkspaceAction.allCases.map(\.defaultShortcut)
        expect(all.allSatisfy(\.isAllowed), "reserved default shortcut")
        expect(Set(all).count == all.count, "duplicate default shortcut")
        for key in ["c", "v", "x", "a", "z", "q", "w", "h", "m"] {
            expect(!WorkspaceShortcut(key: key).isAllowed, "native editing/window key was allowed")
        }
        expect(!WorkspaceShortcut(key: "p", shift: true).isAllowed, "fixed command panel shortcut was allowed")
        for key in ["", "xx", "😃", "A", "\n"] { expect(!WorkspaceShortcut(key: key).isAllowed, "invalid key accepted") }
        let value = WorkspaceShortcut(key: "g", shift: true, option: true)
        let restored = try JSONDecoder().decode(WorkspaceShortcut.self, from: JSONEncoder().encode(value))
        expect(restored == value && restored.label == "⌥⇧⌘G", "shortcut serialization lost modifiers")
        expect(WorkspaceProjectNavigation.matches(query: "  API ", title: "demo API", branch: "main"), "title filtering")
        expect(WorkspaceProjectNavigation.matches(query: "修复", title: "demo", branch: "feature/修复"), "branch filtering")
        expect(!WorkspaceProjectNavigation.matches(query: "missing", title: "demo", branch: "main"), "unmatched filter")
        expect(WorkspaceProjectNavigation.matches(query: " ", title: "demo", branch: "main"), "whitespace filter")
        let ids = ["b@two", "a@one"]
        expect(WorkspaceProjectNavigation.project(at: 1, in: ids) == "b@two", "visible order was independently resorted")
        expect(WorkspaceProjectNavigation.project(at: 2, in: ids) == "a@one", "second worktree mismatch")
        expect(WorkspaceProjectNavigation.project(at: 3, in: ids) == nil, "nonexistent worktree selected")
        expect(WorkspaceProjectNavigation.project(at: 1, in: []) == nil, "empty rail selected a worktree")
        expect(WorkspaceProjectNavigation.project(at: 0, in: ids) == nil, "invalid lower bound")
        expect(WorkspaceProjectNavigation.project(at: 10, in: Array(repeating: "x", count: 12)) == nil, "unsupported upper bound")
        print("WorkspaceCommandTests: \(checks) checks passed")
    }
}
