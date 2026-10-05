import AppKit
import SwiftUI

struct ClientConfigurationPanel: View {
    @ObservedObject var settings: UISettings
    var frozen = false
    @State private var problem: String?
    var body: some View {
        VStack(alignment: .leading, spacing: 10) {
            Label("纸张 / Paper · 用户配置", systemImage: "doc.text")
                .font(Face.sans(13, .semibold))
            Text("默认值 + 文件覆盖；保存文件即生效。")
                .font(Face.sans(11)).foregroundStyle(Ink.inkMuted)
            if frozen {
                Text("theme.json    settings.json    keybindings.json").font(Face.mono(10))
            } else {
                ViewThatFits(in: .horizontal) {
                    HStack { fileButtons }
                    VStack(alignment: .leading) { fileButtons }
                }
            }
            ForEach(settings.configuration.problems, id: \.self) { message in
                Text(message).font(Face.sans(10)).foregroundStyle(Change.changed).textSelection(.enabled)
            }
            if let problem { Text(problem).font(Face.sans(10)).foregroundStyle(Change.changed) }
        }
        .padding(12).frame(maxWidth: .infinity, alignment: .leading)
        .background(Ink.surface).clipShape(RoundedRectangle(cornerRadius: 7))
    }
    @ViewBuilder private var fileButtons: some View {
        ForEach(["theme.json", "settings.json", "keybindings.json"], id: \.self) { name in
            Button(name) { open(name) }.font(Face.mono(10))
                .help("打开配置；不存在时创建最小模板，不覆盖已有文件")
        }
    }
    private func open(_ name: String) {
        guard let directory = settings.configurationDirectory else { return }
        do {
            try FileManager.default.createDirectory(at: directory, withIntermediateDirectories: true)
            let url = directory.appendingPathComponent(name)
            if !FileManager.default.fileExists(atPath: url.path) {
                // An editor or another window can win creation. Never overwrite it.
                do { try Data("{\n  \"schemaVersion\": 1\n}\n".utf8).write(to: url, options: .withoutOverwriting) }
                catch { guard FileManager.default.fileExists(atPath: url.path) else { throw error } }
            }
            _ = try ClientConfigurationIO.read(url)
            guard NSWorkspace.shared.open(url) else { throw ClientConfigurationError.invalid("没有可用的 JSON 编辑器 / No JSON editor is available") }
            problem = nil
        } catch { problem = error.localizedDescription }
    }
}
