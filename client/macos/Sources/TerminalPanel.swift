import AppKit
import BerthTerminal
import SwiftUI

struct TerminalPanel: View {
    let root: String
    var frozen = false
    @ObservedObject private var sessions = TerminalSessions.shared
    @State private var selection: UUID?
    @State private var problem: String?
    private var scoped: [TerminalSession] { sessions.inWorktree(root).filter { $0.kind == "terminal" } }
    private var selected: TerminalSession? { scoped.first { $0.id == selection } ?? scoped.last }

    var body: some View {
        VStack(spacing: 0) {
            HStack(spacing: 10) {
                Label("Terminal", systemImage: "terminal")
                    .font(Face.sans(12, .semibold))
                Text("独立会话 · 原生输入 / Native input")
                    .font(Face.sans(10)).foregroundStyle(Ink.inkFaint)
                Spacer()
                Button("新建终端 / New terminal", action: newShell).disabled(frozen)
            }
            .padding(12)
            if !scoped.isEmpty {
                ScrollView(.horizontal, showsIndicators: false) {
                    HStack(spacing: 6) {
                        ForEach(scoped) { session in
                            Button { selection = session.id } label: {
                                Text(session.title + (session.isActive ? " ●" : " ○"))
                                    .font(Face.mono(11))
                                    .padding(.horizontal, 10).padding(.vertical, 5)
                                    .background(selected?.id == session.id ? Ink.accentSoft : Ink.surface)
                                    .clipShape(RoundedRectangle(cornerRadius: 4))
                            }.buttonStyle(.plain)
                        }
                    }.padding(.horizontal, 12)
                }
            }
            Hairline()
            if let selected {
                TerminalSurface(session: selected).id(selected.id)
                HStack {
                    Text(selected.state).font(Face.mono(10)).foregroundStyle(Ink.inkMuted)
                    Spacer()
                    Button(selected.isStopping ? "强制结束 / Force end" : (selected.isActive ? "结束会话 / End session" : "关闭 / Close")) { close(selected) }
                }.padding(10)
            } else {
                ViewThatFits(in: .vertical) {
                VStack(alignment: .leading, spacing: 14) {
                    Text("在这份 checkout 中工作。")
                        .font(Face.display(20, .medium)).foregroundStyle(Ink.ink)
                    Text("Start a native shell in this worktree.")
                        .font(Face.sans(12)).foregroundStyle(Ink.inkMuted)
                    Text(root).font(Face.mono(11)).foregroundStyle(Ink.inkFaint)
                        .textSelection(.enabled)
                    Text("会话不会因切换项目而迁移。关闭终端不是停止项目服务；服务仍由 oberth 管理。\nSessions keep their starting worktree. Closing a terminal does not replace oberth down.")
                        .font(Face.sans(11)).foregroundStyle(Ink.inkMuted).fixedSize(horizontal: false, vertical: true)
                    if frozen {
                        Text("$ pwd\n/workspace/example\n$ git status --short\n")
                            .font(Face.mono(12)).foregroundStyle(Ink.ink).padding(16)
                            .frame(maxWidth: .infinity, alignment: .leading).background(Ink.surface)
                    }
                }.padding(18).fixedSize(horizontal: false, vertical: true)
                    VStack(alignment: .leading, spacing: 8) {
                        Text("在当前 worktree 新建终端 / Open a terminal in this worktree")
                            .font(Face.sans(12))
                        Text(root).font(Face.mono(10)).lineLimit(1).truncationMode(.middle)
                        Text("会话不随项目切换迁移 / Sessions keep their initial worktree")
                            .font(Face.sans(10)).foregroundStyle(Ink.inkMuted)
                    }.padding(16).fixedSize(horizontal: false, vertical: true)
                }.frame(maxWidth: .infinity, maxHeight: .infinity, alignment: .topLeading)

            }
            if let problem {
                Text(problem).font(Face.sans(11)).foregroundStyle(Ink.ink)
                    .padding(10).frame(maxWidth: .infinity, alignment: .leading)
            }
        }
        .background(Ink.canvas)
    }

    private func newShell() {
        do {
            let session = try sessions.add(worktree: root, title: "Shell \(scoped.count + 1)",
                                           executable: TerminalSession.shell, arguments: ["-i"])
            selection = session.id
            problem = nil
            DispatchQueue.main.async { session.terminal.window?.makeFirstResponder(session.terminal) }
        } catch { problem = error.localizedDescription }
    }

    private func close(_ session: TerminalSession) {
        if session.isActive {
            let alert = NSAlert()
            alert.messageText = "结束这个终端会话？ / End this terminal session?"
            alert.informativeText = session.isStopping ? "将强制结束此会话的直接子进程（SIGKILL），未保存工作可能丢失。 / Force this session child to exit; unsaved work may be lost." : "会向此终端的直接子进程发送 SIGHUP。项目服务不会由这里停止；脱离终端的进程需单独管理。"
            alert.addButton(withTitle: "取消 / Cancel")
            alert.addButton(withTitle: "结束 / End")
            if alert.runModal() == .alertSecondButtonReturn { session.stop(force: session.isStopping) }
        } else {
            sessions.remove(session)
            selection = nil
        }
    }
}
