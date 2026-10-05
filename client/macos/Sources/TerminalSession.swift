import AppKit
import SwiftUI
import BerthTerminal

struct TerminalSurface: NSViewRepresentable {
    let session: TerminalSession
    var detached = false
    @ObservedObject private var settings = UISettings.shared
    @ObservedObject private var windows = TerminalWindows.shared
    func makeNSView(context: Context) -> TerminalHost {
        let host = TerminalHost(); host.present(session, detached: detached); applyAppearance(session.terminal); return host
    }
    func updateNSView(_ host: TerminalHost, context: Context) {
        host.present(session, detached: detached); applyAppearance(session.terminal)
    }
    static func dismantleNSView(_ host: TerminalHost, coordinator: ()) { host.releasePresentation() }
    private func applyAppearance(_ view: HostedTerminalView) {
        let font = Face.nativeMono(12)
        if view.font.fontName != font.fontName || view.font.pointSize != font.pointSize { view.font = font }
        view.nativeBackgroundColor = NSColor(settings.terminalBackground)
        view.nativeForegroundColor = NSColor(settings.terminalForeground)
    }
}

struct TerminalPaneBody: View {
    @ObservedObject var session: TerminalSession
    var frozen: Bool
    @ObservedObject private var windows = TerminalWindows.shared
    var body: some View {
        VStack(spacing: 0) {
            if !session.commandBlocks.isEmpty {
                CommandBlockStrip(session: session)
                Hairline()
            }
            if frozen {
                Text("$ pwd\n\(session.worktree)\n$ echo 'native session'\nnative session")
                    .font(Face.mono(11)).padding(10)
                    .frame(maxWidth: .infinity, maxHeight: .infinity, alignment: .topLeading)
            } else if windows.windows[session.id] != nil {
                VStack(spacing: 10) {
                    Image(systemName: "macwindow")
                    Text("此会话在独立窗口中 / Detached window")
                    Button("显示窗口") { windows.detach(session) }
                    Button("返回工作区") { windows.bringBack(session) }
                }.font(Face.sans(11)).frame(maxWidth: .infinity, maxHeight: .infinity)
            } else { TerminalSurface(session: session).id(session.id) }
        }.frame(maxWidth: .infinity, maxHeight: .infinity)
    }
}

func showTerminalFind(_ terminal: HostedTerminalView) {
    let item = NSMenuItem()
    item.tag = NSTextFinder.Action.showFindInterface.rawValue
    terminal.performTextFinderAction(item)
}


private struct CommandBlockStrip: View {
    @ObservedObject var session: TerminalSession
    var body: some View {
        HStack(spacing: 8) {
            if let block = session.commandBlocks.last {
                Image(systemName: block.isRunning ? "hourglass" : (block.exitCode == 0 ? "checkmark.circle" : "exclamationmark.circle"))
                Text(block.command ?? "命令文本不可用").font(Face.mono(10)).lineLimit(1)
                Spacer(minLength: 0)
                Text(block.isRunning ? "运行中" : (block.exitCode.map { "exit \($0)" } ?? "结果未知"))
                    .font(Face.mono(10)).foregroundStyle(Ink.inkMuted)
            }
            Menu("历史 \(session.commandBlocks.count)") {
                ForEach(session.commandBlocks.reversed()) { block in
                    Button((block.exitCode.map { "[\($0)] " } ?? "[?] ") + String((block.command ?? "未知命令").prefix(100))) {
                        guard let command = block.command else { return }
                        NSPasteboard.general.clearContents()
                        NSPasteboard.general.setString(command, forType: .string)
                    }.disabled(block.command == nil)
                }
                Divider()
                Button("清空内存历史") { session.clearCommandHistory() }
            }.help("选择历史条目只复制命令，不自动执行")
        }.padding(.horizontal, 8).padding(.vertical, 5).foregroundStyle(Ink.inkMuted)
    }
}
