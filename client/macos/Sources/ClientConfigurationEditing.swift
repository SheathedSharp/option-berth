import Foundation
import Darwin

enum ConfigurationDocument: String, CaseIterable, Identifiable {
    case theme, settings, keybindings
    var id: String { rawValue }
    var filename: String { rawValue + ".json" }
    var title: String {
        switch self { case .theme: return "主题"; case .settings: return "工作区偏好"; case .keybindings: return "快捷键" }
    }
    var template: String {
        switch self {
        case .theme: return """
        {
          // 未指定的颜色继承 Paper；颜色格式为 #RRGGBB。
          "schemaVersion": 1,
          "appearance": "light", // light 或 dark，影响原生控件。
          "colors": {
            "canvas": "#F8F4EE", // 主背景
            "surface": "#EFE7DC", // 卡片和工具栏
            "ink": "#292521", // 正文
            "accent": "#C56A4A", // 强调色
            // 可添加 terminal.background / terminal.foreground。
            // 差异令牌：diff.added / diff.removed / diff.changed。
          },
          "interfaceScale": 1.0, // 0.75–2.0，界面字号
          "dataScale": 1.0, // 0.75–2.0，数据和终端字号
          "logScale": 1.0, // 0.75–2.0，日志字号
          // 字体可选：interfaceFont、dataFont（本机已安装字体名）。
        }
        """ + "\n"
        case .settings: return """
        {
          "schemaVersion": 1,
          // 可选项：删除该行即可恢复默认/原有偏好。
          // "reduceMotion": true, // 不会关闭系统的减弱动态效果
          // "sidebarWidth": 220, // 140–320 点
          // "servicesDetailWidth": 380, // 340–500 点
          // "gitDetailWidth": 420, // 340–500 点
          // "shellIntegration": true, // 仅影响新开的 zsh
          // "defaultAgent": "codex", // 也可 claude/opencode/deepseek/pi
        }
        """ + "\n"
        case .keybindings: return """
        {
          "schemaVersion": 1,
          // 全部快捷键自带 Command；不能覆盖复制/粘贴/撤销等原生键。
          "bindings": {
            // "services": {"key": "j", "option": true},
            // "terminal": {"key": "t", "option": true},
            // "worktree.1": {"key": "1"},
            // 可选修饰键：option、shift、control；不能与其它命令重复。
          },
        }
        """ + "\n"
        }
    }
    func validate(_ data: Data) throws {
        switch self {
        case .theme: _ = try ClientConfigurationIO.decode(ThemeConfiguration.self, data: data)
        case .settings: _ = try ClientConfigurationIO.decode(ClientPreferencesConfiguration.self, data: data)
        case .keybindings:
            let value = try ClientConfigurationIO.decode(ClientKeybindingsConfiguration.self, data: data)
            _ = try WorkspaceBindingPolicy.resolve(value.bindings ?? [:])
        }
    }
}

enum ConfigurationEditingError: LocalizedError {
    case conflict, unreadable, notLoaded
    var errorDescription: String? {
        switch self {
        case .conflict: return "文件已被外部修改，自动保存已暂停。保留草稿，或重新载入磁盘版本。"
        case .unreadable: return "无法读取 UTF-8 配置；未覆盖原文件。"
        case .notLoaded: return "配置尚未载入。"
        }
    }
}

/// Confined to one editor's utility queue. NSFileCoordinator serializes
/// cooperating editors; compare the bounded source bytes before every replace.
/// Arbitrary uncoordinated writers are not an OS-level compare-and-swap contract.
final class ConfigurationEditorFile {
    let document: ConfigurationDocument
    let url: URL
    private var baseline: Data?
    private var loaded = false
    init(document: ConfigurationDocument, directory: URL) {
        self.document = document; url = directory.appendingPathComponent(document.filename)
    }
    func load() throws -> String {
        let data = try ClientConfigurationIO.read(url)
        guard data == nil || String(data: data!, encoding: .utf8) != nil else { throw ConfigurationEditingError.unreadable }
        baseline = data; loaded = true
        return data.flatMap { String(data: $0, encoding: .utf8) } ?? document.template
    }
    func save(_ text: String) throws {
        guard loaded else { throw ConfigurationEditingError.notLoaded }
        let data = Data(text.utf8)
        try document.validate(data) // Invalid drafts never reach the filesystem.
        try FileManager.default.createDirectory(at: url.deletingLastPathComponent(), withIntermediateDirectories: true,
                                                attributes: [.posixPermissions: 0o700])
        var coordinationError: NSError?
        var outcome: Result<Void, Error>?
        NSFileCoordinator().coordinate(writingItemAt: url, options: .forReplacing, error: &coordinationError) { destination in
            outcome = Result {
                guard try ClientConfigurationIO.read(destination) == baseline else { throw ConfigurationEditingError.conflict }
                if data == baseline { return }
                // A private staging file in the same directory makes publication
                // atomic. The selected filename is an enum, never user input.
                let staged = destination.deletingLastPathComponent().appendingPathComponent(".oberth-edit-" + UUID().uuidString)
                let fd = open(staged.path, O_WRONLY | O_CREAT | O_EXCL | O_CLOEXEC | O_NOFOLLOW, 0o600)
                guard fd >= 0 else { throw CocoaError(.fileWriteUnknown) }
                let handle = FileHandle(fileDescriptor: fd, closeOnDealloc: true)
                defer { try? handle.close(); try? FileManager.default.removeItem(at: staged) }
                try handle.write(contentsOf: data)
                try handle.synchronize()
                // Recheck after staging, before the short atomic publication.
                guard try ClientConfigurationIO.read(destination) == baseline else { throw ConfigurationEditingError.conflict }
                guard rename(staged.path, destination.path) == 0 else { throw CocoaError(.fileWriteUnknown) }
                baseline = data
            }
        }
        if let coordinationError { throw coordinationError }
        guard let outcome else { throw CocoaError(.fileWriteUnknown) }
        try outcome.get()
    }
}
