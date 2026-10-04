import AppKit
import SwiftUI
import BerthTerminal

struct TerminalSurface: NSViewRepresentable {
    let session: TerminalSession
    @ObservedObject private var settings = UISettings.shared
    func makeNSView(context: Context) -> HostedTerminalView {
        applyAppearance(session.terminal)
        return session.terminal
    }
    func updateNSView(_ view: HostedTerminalView, context: Context) { applyAppearance(view) }
    private func applyAppearance(_ view: HostedTerminalView) {
        let font = Face.nativeMono(12)
        if view.font.fontName != font.fontName || view.font.pointSize != font.pointSize { view.font = font }
        view.nativeBackgroundColor = NSColor(Ink.canvas)
        view.nativeForegroundColor = NSColor(Ink.ink)
    }
}

/// Two existing sessions at most, always in the same worktree. No implicit launch,
/// command broadcast, or re-parenting a view into two panes at the same time.
struct SessionCanvas: View {
    let primary: TerminalSession
    let secondary: TerminalSession?
    var frozen = false
    var body: some View {
        if let secondary, secondary.id != primary.id, secondary.worktree == primary.worktree {
            if frozen {
                // ImageRenderer cannot capture NSSplitView. Render only the
                // frozen geometry here; native divider behavior is tested separately.
                HStack(spacing: 0) {
                    SessionPane(session: primary, frozen: true)
                    Rectangle().fill(Ink.line).frame(width: 1)
                    SessionPane(session: secondary, frozen: true)
                }
            } else {
                HSplitView {
                    SessionPane(session: primary, frozen: false).frame(minWidth: 180)
                    SessionPane(session: secondary, frozen: false).frame(minWidth: 180)
                }
            }
        } else { SessionPane(session: primary, frozen: frozen) }
    }
}

private struct SessionPane: View {
    @ObservedObject var session: TerminalSession
    var frozen: Bool
    var body: some View {
        VStack(spacing: 0) {
            HStack(spacing: 6) {
                Text(session.title).font(Face.mono(10)).lineLimit(1).layoutPriority(1)
                Text(session.state).font(Face.mono(9)).foregroundStyle(Ink.inkMuted).lineLimit(1)
                Spacer(minLength: 0)
                Button { showTerminalFind(session.terminal) } label: { Image(systemName: "magnifyingglass") }
                    .help("查找这个窗格 / Find in this pane")
                Button {
                    if session.isActive { confirmSessionStop(session) }
                    else { TerminalSessions.shared.remove(session) }
                } label: { Image(systemName: session.isActive ? "stop.fill" : "xmark") }
                    .accessibilityLabel((session.isActive ? "结束 / End " : "关闭 / Close ") + session.title)
                    .help("仅操作 " + session.title)
            }.padding(7).disabled(frozen)
            Hairline()
            if frozen {
                Text("$ pwd\n/workspace/demo\n$ echo 'native session'\nnative session")
                    .font(Face.mono(11)).padding(10)
                    .frame(maxWidth: .infinity, maxHeight: .infinity, alignment: .topLeading)
            } else { TerminalSurface(session: session).id(session.id) }
        }.frame(maxWidth: .infinity, maxHeight: .infinity)
    }
}

func showTerminalFind(_ terminal: HostedTerminalView) {
    let item = NSMenuItem()
    item.tag = NSTextFinder.Action.showFindInterface.rawValue
    terminal.performTextFinderAction(item)
}
