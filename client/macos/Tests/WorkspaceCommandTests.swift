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
        expect(WorkspaceAction.connect.defaultShortcut == .init(key: "n"), "connect default")
        for (index, command) in WorkspaceAction.worktrees.enumerated() {
            expect(command.ordinal == index + 1 && command.defaultShortcut == .init(key: String(index + 1)), "ordinal shortcut ordering")
        }
        expect(WorkspaceAction.services.defaultShortcut == .init(key: "s", option: true), "services mnemonic")
        expect(WorkspaceAction.code.defaultShortcut == .init(key: "g", option: true), "Git mnemonic")
        expect(WorkspaceAction.terminal.defaultShortcut == .init(key: "t", option: true), "console mnemonic")
        expect(WorkspaceAction.sidebar.defaultShortcut == .init(key: "b"), "sidebar default")
        let migrated = WorkspaceBindingPolicy.migrate([.services: .init(key: "1"), .code: .init(key: "2"),
            .terminal: .init(key: "3"), .sidebar: .init(key: "s", option: true), .find: .init(key: "f", option: true)])
        expect(migrated.count == 1 && migrated[.find] == .init(key: "f", option: true), "legacy defaults or custom edits lost")
        expect(WorkspaceBindingPolicy.valid(migrated), "migrated bindings invalid")
        func resolve(_ text: String) throws -> [WorkspaceAction: WorkspaceShortcut] {
            let document = try ClientConfigurationIO.decode(ClientKeybindingsConfiguration.self, data: Data(text.utf8))
            return try WorkspaceBindingPolicy.resolve(document.bindings ?? [:])
        }
        let overrides = try resolve(#"{"schemaVersion":1,"bindings":{"services":{"key":"j","option":true}}}"#)
        expect(overrides[.services] == .init(key: "j", option: true), "file override not resolved")
        for invalid in [
            #"{"schemaVersion":1,"bindings":{"services":{"key":"j","shfit":true}}}"#,
            #"{"schemaVersion":1,"bindings":{"unknown":{"key":"j"}}}"#,
            #"{"schemaVersion":1,"bindings":{"services":{"key":"1"}}}"#,
            #"{"schemaVersion":1,"bindings":{"services":{"key":"c"}}}"#,
            #"{"schemaVersion":1,"bindings":{"services":{"key":"p","shift":true}}}"#,
            #"{"schemaVersion":1,"bindings":{"services":{"key":"j","option":1}}}"#
        ] {
            do { _ = try resolve(invalid); fatalError("invalid key document accepted") } catch { checks += 1 }
        }
        print("WorkspaceCommandTests: \(checks) checks passed")
    }
}
