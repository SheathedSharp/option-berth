import AppKit
import SwiftUI

struct ClientConfigurationPanel: View {
    @ObservedObject var settings: UISettings
    @ObservedObject private var shortcuts = WorkspaceShortcuts.shared
    var frozen = false
    var body: some View {
        VStack(alignment: .leading, spacing: 10) {
            Label("纸张 / Paper · 用户配置", systemImage: "doc.text")
                .font(Face.sans(13, .semibold))
            Text("在应用内编辑；有效修改自动保存并生效。")
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
            if let diagnostic = shortcuts.problem, !settings.configuration.problems.contains(diagnostic) {
                Text(diagnostic).font(Face.sans(10)).foregroundStyle(Change.changed)
            }
        }
        .padding(12).frame(maxWidth: .infinity, alignment: .leading)
        .background(Ink.surface).clipShape(RoundedRectangle(cornerRadius: 7))
    }
    @ViewBuilder private var fileButtons: some View {
        ForEach(ConfigurationDocument.allCases) { document in
            Button(document.filename) { ConfigurationEditorWindows.shared.open(document, settings: settings) }.font(Face.mono(10))
                .accessibilityIdentifier("configuration.open." + document.rawValue)
                .anchorPreference(key: ConfigurationEntryAnchors.self, value: .bounds) { [document.rawValue: $0] }
                .help("在内置编辑器中修改，支持注释；打开不会改写文件")
        }
    }
}

/// Actual control geometry for native mouse-routing verification. Reading a
/// preference does not replace the SwiftUI Button or intercept its input.
struct ConfigurationEntryAnchors: PreferenceKey {
    static var defaultValue: [String: Anchor<CGRect>] = [:]
    static func reduce(value: inout [String: Anchor<CGRect>], nextValue: () -> [String: Anchor<CGRect>]) {
        value.merge(nextValue(), uniquingKeysWith: { _, next in next })
    }
}
