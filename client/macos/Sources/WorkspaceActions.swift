import AppKit
import SwiftUI

/// One action catalogue drives the menu and searchable command panel. Actions
/// never evaluate shell strings or call a provider implicitly.
enum WorkspaceAction: String, CaseIterable, Codable, Identifiable {
    case services, code, terminal, sessions, find, refresh, sidebar, settings
    var id: String { rawValue }
    var title: String {
        switch self {
        case .services: return "服务 / Services"
        case .code: return "代码 / Git"
        case .terminal: return "终端 / Terminal"
        case .sessions: return "会话管理 / Sessions"
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
    var modifiers: EventModifiers {
        var result: EventModifiers = .command
        if shift { result.insert(.shift) }; if option { result.insert(.option) }; if control { result.insert(.control) }
        return result
    }
    var equivalent: KeyEquivalent { KeyEquivalent(key.first ?? " ") }
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

@MainActor
final class WorkspaceShortcuts: ObservableObject {
    static let shared = WorkspaceShortcuts()
    @Published private(set) var bindings: [WorkspaceAction: WorkspaceShortcut] = [:]
    private let defaults: UserDefaults
    private let storageKey = "workspace.shortcuts.v1"
    init(defaults: UserDefaults = .standard) {
        self.defaults = defaults
        if let data = defaults.data(forKey: storageKey), data.count <= 8192,
           let saved = try? JSONDecoder().decode([String: WorkspaceShortcut].self, from: data) {
            let known = Dictionary(uniqueKeysWithValues: saved.compactMap { key, value in WorkspaceAction(rawValue: key).map { ($0, value) } })
            if Self.valid(known) { bindings = known }
        }
    }
    func shortcut(_ action: WorkspaceAction) -> WorkspaceShortcut { bindings[action] ?? action.defaultShortcut }
    static func valid(_ bindings: [WorkspaceAction: WorkspaceShortcut]) -> Bool {
        let all = WorkspaceAction.allCases.map { bindings[$0] ?? $0.defaultShortcut }
        return all.allSatisfy(\.isAllowed) && Set(all).count == all.count
    }
    func set(_ shortcut: WorkspaceShortcut, for action: WorkspaceAction) throws {
        var next = bindings; next[action] = shortcut
        guard Self.valid(next) else { throw ShortcutFailure.invalid }
        let data = try JSONEncoder().encode(Dictionary(uniqueKeysWithValues: next.map { ($0.key.rawValue, $0.value) }))
        defaults.set(data, forKey: storageKey); bindings = next
    }
    func reset() { defaults.removeObject(forKey: storageKey); bindings = [:] }
    enum ShortcutFailure: LocalizedError {
        case invalid
        var errorDescription: String? { "快捷键无效、重复或占用原生编辑快捷键 / Invalid, duplicate or reserved shortcut" }
    }
}

struct WorkspaceActionPanel: View {
    let perform: (WorkspaceAction) -> Void
    @ObservedObject var shortcuts: WorkspaceShortcuts
    @Environment(\.dismiss) private var dismiss
    @State private var query = ""
    @State private var selected = 0
    @State private var editingShortcuts = false
    @FocusState private var searchFocused: Bool
    private var actions: [WorkspaceAction] {
        WorkspaceAction.allCases.filter { query.isEmpty || ($0.title + " " + $0.rawValue).localizedCaseInsensitiveContains(query) }
    }
    var body: some View {
        VStack(alignment: .leading, spacing: 12) {
            HStack {
                Label("命令面板 / Command panel", systemImage: "command").font(Face.sans(14, .semibold))
                Spacer()
                Button("快捷键…") { editingShortcuts = true }
                Button("关闭") { dismiss() }.keyboardShortcut(.cancelAction)
            }
            TextField("搜索服务、Git、终端或操作", text: $query)
                .textFieldStyle(.roundedBorder).focused($searchFocused)
                .onChange(of: query) { _, _ in selected = 0 }
                .onSubmit {
                    guard (NSApp.keyWindow?.firstResponder as? NSTextView)?.hasMarkedText() != true else { return }
                    guard !actions.isEmpty else { return }; choose(actions[min(selected, actions.count - 1)])
                }
                .onMoveCommand { direction in
                    guard !actions.isEmpty else { return }
                    if direction == .down { selected = (selected + 1) % actions.count }
                    if direction == .up { selected = (selected + actions.count - 1) % actions.count }
                }
            VStack(spacing: 3) {
                ForEach(Array(actions.enumerated()), id: \.element.id) { index, action in
                    Button { choose(action) } label: {
                        HStack {
                            Image(systemName: action.symbol).frame(width: 20)
                            Text(action.title); Spacer()
                            Text(shortcuts.shortcut(action).label).font(Face.mono(11)).foregroundStyle(Ink.inkMuted)
                        }.padding(9).contentShape(Rectangle())
                            .background(index == selected ? Ink.accentSoft : Ink.surface)
                            .clipShape(RoundedRectangle(cornerRadius: 5))
                    }.buttonStyle(.plain).accessibilityIdentifier("workspace.action." + action.rawValue)
                }
                if actions.isEmpty { Text("没有匹配的操作 / No matching action").foregroundStyle(Ink.inkMuted).padding() }
            }
        }.padding(18).frame(width: 480).background(Ink.canvas).foregroundStyle(Ink.ink)
            .onAppear { searchFocused = true }
            .sheet(isPresented: $editingShortcuts) { WorkspaceShortcutEditor(shortcuts: shortcuts) }
    }
    private func choose(_ action: WorkspaceAction) {
        dismiss()
        DispatchQueue.main.async { perform(action) }
    }
}

struct WorkspaceShortcutEditor: View {
    @ObservedObject var shortcuts: WorkspaceShortcuts
    @Environment(\.dismiss) private var dismiss
    @State private var action: WorkspaceAction = .services
    @State private var key = "1"
    @State private var shift = false
    @State private var option = false
    @State private var control = false
    @State private var problem: String?
    var body: some View {
        VStack(alignment: .leading, spacing: 14) {
            Text("工作区快捷键 / Workspace shortcuts").font(Face.sans(14, .semibold))
            Picker("操作", selection: $action) { ForEach(WorkspaceAction.allCases) { Text($0.title).tag($0) } }
                .onChange(of: action) { _, _ in load() }
            HStack {
                Text("⌘"); TextField("按键", text: $key).frame(width: 50)
                Toggle("⇧", isOn: $shift); Toggle("⌥", isOn: $option); Toggle("⌃", isOn: $control)
            }
            Text("保留复制、粘贴、撤销及窗口关闭等原生快捷键。命令面板固定为 ⇧⌘P。")
                .font(Face.sans(11)).foregroundStyle(Ink.inkMuted).fixedSize(horizontal: false, vertical: true)
            if let problem { Text(problem).font(Face.sans(11)).foregroundStyle(Ink.ink) }
            HStack {
                Button("恢复默认") { shortcuts.reset(); load(); problem = nil }
                Spacer()
                Button("关闭") { dismiss() }
                Button("保存") {
                    do { try shortcuts.set(.init(key: key.lowercased(), shift: shift, option: option, control: control), for: action); problem = nil }
                    catch { problem = error.localizedDescription }
                }.keyboardShortcut(.defaultAction)
            }
        }.padding(20).frame(width: 460).background(Ink.canvas).foregroundStyle(Ink.ink)
            .onAppear { load() }
    }
    private func load() { let value = shortcuts.shortcut(action); key = value.key; shift = value.shift; option = value.option; control = value.control }
}
