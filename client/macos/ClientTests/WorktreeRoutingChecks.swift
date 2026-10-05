import AppKit
import SwiftUI
@testable import BerthClient

extension ClientChecks {
    static func worktreeRoutingChecks() throws {
        func project(_ name: String, _ branch: String) throws -> BerthGroup {
            let data = try JSONSerialization.data(withJSONObject: ["name":name, "repo":name, "worktree":"feature",
                "branch":branch, "root_dir":"/fixture/" + name, "services":[], "members":[]])
            return try JSONDecoder().decode(BerthGroup.self, from: data)
        }
        let a = try project("alpha", "feature/first"), b = try project("beta", "fix/second")
        let projects = [b, a]
        let views = ViewState(scope: .code(a.name))
        views.selectVisibleProject(at: 2, in: projects)
        require(views.scope == .code(b.name), "ordinal route changed module or used incoming rather than visible order")
        views.projectQuery = "FIRST"
        views.selectVisibleProject(at: 1, in: projects)
        require(views.scope == .code(a.name), "filtered worktree navigation differs from rail")
        views.selectVisibleProject(at: 2, in: projects)
        require(views.scope == .code(a.name), "out-of-range key changed selection")
        views.projectQuery = "missing"
        views.selectVisibleProject(at: 1, in: projects)
        require(views.scope == .code(a.name), "empty filter changed selection")
        views.projectQuery = ""
        views.showingSettings = true
        views.selectVisibleProject(at: 2, in: projects)
        let before = views.connectionRequest
        views.requestConnection()
        require(views.scope == .code(a.name) && before == views.connectionRequest, "modal commands escaped settings")
        views.showingSettings = false
        views.projectOperationPending = true; views.requestConnection()
        require(before == views.connectionRequest, "project connection overlapped pending operation")
        views.projectOperationPending = false; views.requestConnection()
        require(before != views.connectionRequest, "explicit connect command was lost")
        views.scope = .terminal(b.name); views.selectVisibleProject(at: 1, in: projects)
        require(views.scope == .terminal(a.name), "console context was not preserved")

        let suite = "workspace-file-keys-test-" + UUID().uuidString
        let defaults = UserDefaults(suiteName: suite)!
        defer { defaults.removePersistentDomain(forName: suite) }
        let shortcuts = WorkspaceShortcuts(defaults: defaults)
        var snapshot = ClientConfigurationSnapshot()
        snapshot.keybindingsFilePresent = true
        snapshot.keybindings.bindings = ["services": .init(key: "j", option: true)]
        shortcuts.apply(snapshot)
        require(shortcuts.shortcut(.services) == .init(key: "j", option: true), "file keybinding not applied")
        refuses("file-owned settings were overwritten by GUI") { try shortcuts.set(.init(key: "k", option: true), for: .services) }
        snapshot.keybindings.bindings = ["services": .init(key: "1")]
        shortcuts.apply(snapshot)
        require(shortcuts.problem != nil && shortcuts.shortcut(.services) == .init(key: "j", option: true), "invalid file discarded last valid binding")
        snapshot.problems = ["keybindings.json: invalid configuration"]
        snapshot.keybindings.bindings = [:]
        shortcuts.apply(snapshot)
        require(shortcuts.shortcut(.services) == .init(key: "j", option: true), "parse error replaced bindings")
        shortcuts.apply(ClientConfigurationSnapshot())
        require(!shortcuts.fileManaged && shortcuts.problem == nil && shortcuts.bindings.isEmpty, "deletion did not restore GUI fallback")
        require(defaults.data(forKey: "workspace.shortcuts.v1") == nil, "file application wrote legacy preferences")
        print("PASS: shared visible ordering/filter, module preservation, modal/pending guards, file overrides, last-good rejection and no user-file writes")
    }
}
