import Foundation

/// One action catalogue drives the menu and searchable command panel. Actions
/// never evaluate shell strings or call a provider implicitly.
enum WorkspaceAction: String, CaseIterable, Codable, Identifiable {
    case services, code, terminal, sessions, recovery, updates, find, refresh, sidebar, settings
    var id: String { rawValue }
    var title: String {
        switch self {
        case .services: return "服务 / Services"
        case .code: return "代码 / Git"
        case .terminal: return "终端 / Terminal"
        case .sessions: return "会话管理 / Sessions"
        case .recovery: return "恢复工作区 / Recovery"
        case .updates: return "检查更新 / Updates"
        case .find: return "查找当前内容 / Find"
        case .refresh: return "刷新运行事实 / Refresh"
        case .sidebar: return "切换项目侧栏 / Sidebar"
        case .settings: return "设置 / Settings"
        }
    }
    var symbol: String {
        switch self {
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
        }
    }
    var defaultShortcut: WorkspaceShortcut {
        switch self {
        case .services: return .init(key: "1")
        case .code: return .init(key: "2")
        case .terminal: return .init(key: "3")
        case .sessions: return .init(key: "o", shift: true)
        case .recovery: return .init(key: "o", shift: true, option: true)
        case .updates: return .init(key: "u", option: true)
        case .find: return .init(key: "f")
        case .refresh: return .init(key: "r")
        case .sidebar: return .init(key: "s", option: true)
        case .settings: return .init(key: ",")
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
        // Preserve native text editing, window/app management and the fixed
        // panel shortcut. A custom workspace action must not intercept them.
        if !option && !control {
            if !shift && ["c", "v", "x", "a", "z", "q", "w", "h", "m"].contains(key) { return false }
            if shift && ["z", "q", "w", "p"].contains(key) { return false }
        }
        return true
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
