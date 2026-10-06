import Foundation

/// Stable command IDs are shared by menus, the palette and user keybindings.
/// They are data, never shell strings or implicit provider invocations.
enum WorkspaceAction: String, CaseIterable, Codable, Identifiable {
    case connect, services, code, terminal, sessions, recovery, updates, find, refresh, sidebar, settings
    case worktree1 = "worktree.1", worktree2 = "worktree.2", worktree3 = "worktree.3"
    case worktree4 = "worktree.4", worktree5 = "worktree.5", worktree6 = "worktree.6"
    case worktree7 = "worktree.7", worktree8 = "worktree.8", worktree9 = "worktree.9"
    var id: String { rawValue }
    var ordinal: Int? { rawValue.hasPrefix("worktree.") ? Int(rawValue.dropFirst(9)) : nil }
    static var worktrees: [Self] { allCases.filter { $0.ordinal != nil } }
    static var modules: [Self] { [.services, .code, .terminal] }
    var title: String {
        if let ordinal { return "第 \(ordinal) 个 Worktree" }
        switch self {
        case .connect: return "接入项目… / Connect project…"
        case .services: return "服务 / Services"
        case .code: return "Git / Review"
        case .terminal: return "会话 / Sessions"
        case .sessions: return "会话管理 / Sessions"
        case .recovery: return "恢复工作区 / Recovery"
        case .updates: return "检查更新 / Updates"
        case .find: return "查找当前内容 / Find"
        case .refresh: return "刷新当前工作区 / Refresh"
        case .sidebar: return "切换项目侧栏 / Sidebar"
        case .settings: return "设置 / Settings"
        default: return rawValue
        }
    }
    var symbol: String {
        if let ordinal { return "\(ordinal).circle" }
        switch self {
        case .connect: return "plus"
        case .services: return "server.rack"
        case .code: return "chevron.left.forwardslash.chevron.right"
        case .terminal: return "terminal"
        case .sessions: return "rectangle.on.rectangle"
        case .recovery: return "clock.arrow.circlepath"
        case .updates: return "arrow.down.circle"
        case .find: return "magnifyingglass"
        case .refresh: return "arrow.clockwise"
        case .sidebar: return "sidebar.left"
        case .settings: return "gearshape"
        default: return "command"
        }
    }
    var defaultShortcut: WorkspaceShortcut {
        if let ordinal { return .init(key: String(ordinal)) }
        switch self {
        case .connect: return .init(key: "n")
        case .services: return .init(key: "s", option: true)
        case .code: return .init(key: "g", option: true)
        case .terminal: return .init(key: "t", option: true)
        case .sessions: return .init(key: "o", shift: true)
        case .recovery: return .init(key: "o", shift: true, option: true)
        case .updates: return .init(key: "u", option: true)
        case .find: return .init(key: "f")
        case .refresh: return .init(key: "r")
        case .sidebar: return .init(key: "b")
        case .settings: return .init(key: ",")
        default: preconditionFailure("worktree command without ordinal")
        }
    }
}

struct WorkspaceShortcut: Codable, Equatable, Hashable {
    var key: String
    var shift = false
    var option = false
    var control = false
    var label: String { (control ? "⌃" : "") + (option ? "⌥" : "") + (shift ? "⇧" : "") + "⌘" + key.uppercased() }
    var isAllowed: Bool {
        guard key.utf8.count == 1, "abcdefghijklmnopqrstuvwxyz0123456789,./;[]=-".contains(key) else { return false }
        if !option && !control {
            if !shift && ["c", "v", "x", "a", "z", "q", "w", "h", "m"].contains(key) { return false }
            if shift && ["z", "q", "w", "p"].contains(key) { return false }
        }
        return true
    }
}

enum WorkspaceBindingPolicy {
    static func valid(_ bindings: [WorkspaceAction: WorkspaceShortcut]) -> Bool {
        let all = WorkspaceAction.allCases.map { bindings[$0] ?? $0.defaultShortcut }
        return all.allSatisfy(\.isAllowed) && Set(all).count == all.count
    }
    static func resolve(_ bindings: [String: ClientKeybinding]) throws -> [WorkspaceAction: WorkspaceShortcut] {
        var result: [WorkspaceAction: WorkspaceShortcut] = [:]
        for (id, binding) in bindings {
            guard let command = WorkspaceAction(rawValue: id) else {
                throw ClientConfigurationError.invalid("keybindings.json: unknown command ID: " + id)
            }
            result[command] = WorkspaceShortcut(key: binding.key, shift: binding.shift ?? false,
                                               option: binding.option ?? false, control: binding.control ?? false)
        }
        guard valid(result) else { throw ClientConfigurationError.invalid("keybindings.json: duplicate, invalid or native reserved shortcut") }
        return result
    }
    /// Discard only saved copies of retired defaults. Do not rewrite user files
    /// or silently discard genuine custom bindings that conflict with new keys.
    static func migrate(_ saved: [WorkspaceAction: WorkspaceShortcut]) -> [WorkspaceAction: WorkspaceShortcut] {
        let retired: [WorkspaceAction: WorkspaceShortcut] = [
            .services: .init(key: "1"), .code: .init(key: "2"),
            .terminal: .init(key: "3"), .sidebar: .init(key: "s", option: true)
        ]
        return saved.filter { retired[$0.key] != $0.value }
    }
}

/// Shared by the side rail and command routing; no independently cached order.
enum WorkspaceProjectNavigation {
    static func matches(query: String, title: String, branch: String) -> Bool {
        let value = query.trimmingCharacters(in: .whitespacesAndNewlines)
        return value.isEmpty || (title + " " + branch).localizedCaseInsensitiveContains(value)
    }
    static func project(at ordinal: Int, in visibleIDs: [String]) -> String? {
        guard (1...9).contains(ordinal), visibleIDs.indices.contains(ordinal - 1) else { return nil }
        return visibleIDs[ordinal - 1]
    }
}
