import AppKit
import BerthTerminal
import SwiftUI

struct SessionManager: View {
    @ObservedObject private var sessions = TerminalSessions.shared
    let onOpen: (TerminalSession) -> Void
    @Environment(\.dismiss) private var dismiss
    @State private var query = ""
    @State private var problem: String?
    private var filtered: [TerminalSession] {
        sessions.sessions.filter {
            query.isEmpty || ($0.title + " " + $0.worktree + " " + $0.kind).localizedCaseInsensitiveContains(query)
        }
    }
    var body: some View {
        VStack(alignment: .leading, spacing: 14) {
            HStack {
                Text("会话 / Sessions").font(Face.display(20))
                Spacer()
                Text("\(sessions.activeCount) running · \(sessions.sessions.count)/16").font(Face.mono(11))
                Button("完成 / Done") { dismiss() }.keyboardShortcut(.cancelAction)
            }
            TextField("搜索名称、worktree 或 agent / Search", text: $query)
            Text("仅显示本应用拥有的会话；项目清单被移除后仍可打开或结束。\nOnly this application's sessions. Removing a manifest does not orphan a terminal.")
                .font(Face.sans(11)).foregroundStyle(Ink.inkMuted)
            ScrollView {
                LazyVStack(alignment: .leading, spacing: 12) {
                    ForEach(filtered) { session in
                        VStack(alignment: .leading, spacing: 5) {
                            HStack {
                                Text(session.title).font(Face.sans(12, .semibold)).lineLimit(1)
                                Text(session.kind).font(Face.mono(10)).foregroundStyle(Ink.inkMuted)
                                Spacer()
                                Button("打开 / Open") { onOpen(session); dismiss() }
                                Button { rename(session) } label: { Image(systemName: "pencil") }
                                    .help("重命名 / Rename")
                                Button(session.isActive ? "结束 / End" : "关闭 / Close") {
                                    if session.isActive { confirmSessionStop(session) }
                                    else { sessions.remove(session) }
                                }
                            }
                            Text(session.worktree).font(Face.mono(10)).lineLimit(1).truncationMode(.middle)
                                .textSelection(.enabled)
                            Text(session.state).font(Face.mono(10)).foregroundStyle(Ink.inkMuted)
                        }.padding(10).background(Ink.surface).clipShape(RoundedRectangle(cornerRadius: 6))
                    }
                    if filtered.isEmpty { Text("没有匹配会话 / No matching sessions").font(Face.sans(12)).padding(.vertical) }
                }
            }
            if let problem { Text(problem).font(Face.sans(11)) }
        }.padding(18).frame(minWidth: 620, idealWidth: 700, minHeight: 380, idealHeight: 460)
            .background(Ink.canvas)
    }
    private func rename(_ session: TerminalSession) {
        let alert = ClientAlert.make()
        alert.messageText = "会话名称 / Session name"
        let field = NSTextField(string: session.title)
        field.frame = NSRect(x: 0, y: 0, width: 280, height: 26)
        alert.accessoryView = field
        alert.addButton(withTitle: "保存 / Save")
        alert.addButton(withTitle: "取消 / Cancel")
        alert.window.initialFirstResponder = field
        if alert.runModal() == .alertFirstButtonReturn {
            do { try session.rename(field.stringValue); problem = nil }
            catch { problem = error.localizedDescription }
        }
    }
}

@MainActor
func confirmSessionStop(_ session: TerminalSession) {
    guard session.isActive else { return }
    let force = session.isStopping
    let alert = ClientAlert.make()
    alert.messageText = force ? "强制结束此会话？ / Force end this session?" : "结束此会话？ / End this session?"
    alert.informativeText = force
        ? "未保存的工作可能丢失。只操作这个会话，不停止项目服务。 / Unsaved work may be lost. Project services are unaffected."
        : "向本会话的直接子进程发送 SIGHUP 并等待退出，不等同于 oberth down。 / Signal this child and wait for exit."
    alert.addButton(withTitle: "取消 / Cancel")
    alert.addButton(withTitle: force ? "强制结束 / Force end" : "结束 / End")
    if alert.runModal() == .alertSecondButtonReturn { session.stop(force: force) }
}
