import AppKit
import Combine
import SwiftUI

extension WorkspaceShortcut {
    var modifiers: EventModifiers {
        var result: EventModifiers = .command
        if shift { result.insert(.shift) }; if option { result.insert(.option) }; if control { result.insert(.control) }
        return result
    }
    var equivalent: KeyEquivalent { KeyEquivalent(key.first ?? " ") }
}

@MainActor
final class WorkspaceShortcuts: ObservableObject {
    static let shared: WorkspaceShortcuts = {
        if CommandLine.arguments.contains("--render-states") || CommandLine.arguments.contains("--write-icon") {
            return WorkspaceShortcuts(defaults: UserDefaults(suiteName: "option-berth-keys-render-" + UUID().uuidString)!)
        }
        return WorkspaceShortcuts(configuration: .shared)
    }()
    @Published private(set) var bindings: [WorkspaceAction: WorkspaceShortcut] = [:]
    @Published private(set) var fileManaged = false
    @Published private(set) var problem: String?
    private let defaults: UserDefaults
    private let storageKey = "workspace.shortcuts.v1"
    private var legacy: [WorkspaceAction: WorkspaceShortcut] = [:]
    private var legacyProblem: String?
    private var observation: AnyCancellable?
    init(defaults: UserDefaults = .standard, configuration: UISettings? = nil) {
        self.defaults = defaults
        if let data = defaults.data(forKey: storageKey) {
            if data.count <= 8192, let saved = try? JSONDecoder().decode([String: WorkspaceShortcut].self, from: data),
               saved.keys.allSatisfy({ WorkspaceAction(rawValue: $0) != nil }) {
                let known = Dictionary(uniqueKeysWithValues: saved.map { (WorkspaceAction(rawValue: $0.key)!, $0.value) })
                let migrated = WorkspaceBindingPolicy.migrate(known)
                if Self.valid(migrated) { legacy = migrated }
                else { legacyProblem = "旧快捷键与新映射冲突，已使用默认值；原始偏好未删除。" }
            } else { legacyProblem = "旧快捷键配置无法读取，已使用默认值；原始偏好未删除。" }
        }
        bindings = legacy; problem = legacyProblem
        if let configuration {
            observation = configuration.$configuration.sink { [weak self] in self?.apply($0) }
        }
    }
    func shortcut(_ action: WorkspaceAction) -> WorkspaceShortcut { bindings[action] ?? action.defaultShortcut }
    static func valid(_ bindings: [WorkspaceAction: WorkspaceShortcut]) -> Bool { WorkspaceBindingPolicy.valid(bindings) }
    func apply(_ snapshot: ClientConfigurationSnapshot) {
        fileManaged = snapshot.keybindingsFilePresent
        guard fileManaged else { bindings = legacy; problem = legacyProblem; return }
        if let error = snapshot.problems.first(where: { $0.hasPrefix("keybindings.json:") }) { problem = error; return }
        do { bindings = try WorkspaceBindingPolicy.resolve(snapshot.keybindings.bindings ?? [:]); problem = nil }
        catch { problem = error.localizedDescription }
    }
    func set(_ shortcut: WorkspaceShortcut, for action: WorkspaceAction) throws {
        guard !fileManaged else { throw ShortcutFailure.fileManaged }
        var next = bindings; next[action] = shortcut
        guard Self.valid(next) else { throw ShortcutFailure.invalid }
        let data = try JSONEncoder().encode(Dictionary(uniqueKeysWithValues: next.map { ($0.key.rawValue, $0.value) }))
        defaults.set(data, forKey: storageKey); legacy = next; bindings = next; legacyProblem = nil; problem = nil
    }
    func reset() {
        guard !fileManaged else { return }
        defaults.removeObject(forKey: storageKey); legacy = [:]; bindings = [:]; legacyProblem = nil; problem = nil
    }
    enum ShortcutFailure: LocalizedError {
        case invalid, fileManaged
        var errorDescription: String? {
            switch self {
            case .invalid: return "快捷键无效、重复或占用原生编辑快捷键 / Invalid, duplicate or reserved shortcut"
            case .fileManaged: return "请编辑 keybindings.json；客户端不会覆盖用户文件。"
            }
        }
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
            ScrollViewReader { proxy in
                ScrollView {
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
                    }.buttonStyle(.plain).id(action.id).accessibilityIdentifier("workspace.action." + action.rawValue)
                }
                if actions.isEmpty { Text("没有匹配的操作 / No matching action").foregroundStyle(Ink.inkMuted).padding() }
            }
                }.frame(height: min(340, CGFloat(max(1, actions.count)) * 40))
                    .onChange(of: selected) { _, index in
                        if actions.indices.contains(index) { proxy.scrollTo(actions[index].id, anchor: .center) }
                    }
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
            if shortcuts.fileManaged {
                Text("快捷键由 keybindings.json 管理；请在设置 → 外观中打开文件。")
                    .font(Face.sans(11)).foregroundStyle(Ink.inkMuted)
            }
            if let diagnostic = shortcuts.problem { Text(diagnostic).font(Face.sans(11)).foregroundStyle(Change.changed) }
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
                Button("恢复默认") { shortcuts.reset(); load(); problem = nil }.disabled(shortcuts.fileManaged)
                Spacer()
                Button("关闭") { dismiss() }
                Button("保存") {
                    do { try shortcuts.set(.init(key: key.lowercased(), shift: shift, option: option, control: control), for: action); problem = nil }
                    catch { problem = error.localizedDescription }
                }.keyboardShortcut(.defaultAction).disabled(shortcuts.fileManaged)
            }
        }.padding(20).frame(width: 460).background(Ink.canvas).foregroundStyle(Ink.ink)
            .onAppear { load() }
    }
    private func load() { let value = shortcuts.shortcut(action); key = value.key; shift = value.shift; option = value.option; control = value.control }
}
