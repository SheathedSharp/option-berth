import AppKit
import BerthTerminal
import SwiftUI

struct TerminalPanel: View {
    let root: String
    var frozen = false
    @ObservedObject private var sessions = TerminalSessions.shared
    @ObservedObject var workspace: ConsoleWorkspace
    @State private var problem: String?
    @State private var integrationEnabled = false
    @State private var showingHistory = false
    private var scoped: [TerminalSession] { sessions.inWorktree(root).filter { $0.kind == "terminal" } }
    private var selected: TerminalSession? { scoped.first { $0.id == workspace.terminalSelection } ?? scoped.last }

    var body: some View {
        VStack(spacing: 0) {
            HStack(spacing: 10) {
                Label("Shell", systemImage: "terminal")
                    .font(Face.sans(12, .semibold))
                Text("输入互不广播")
                    .font(Face.sans(10)).foregroundStyle(Ink.inkFaint).lineLimit(1).layoutPriority(-1)
                Spacer()

                if frozen { Label("命令块", systemImage: "square").font(Face.sans(10)).foregroundStyle(Ink.inkMuted) }
                else {
                Toggle("命令块", isOn: $integrationEnabled)
                    .toggleStyle(.checkbox).help("仅为新建 zsh 启用临时集成，不修改 shell 配置；命令仅留在内存")
                    .disabled(frozen || URL(fileURLWithPath: TerminalSession.shell).lastPathComponent != "zsh")
                }
                if frozen {
                    Text("搜索历史… / History…").font(Face.sans(11)).foregroundStyle(Ink.inkMuted)
                    Text("新建终端").font(Face.sans(11)).foregroundStyle(Ink.inkMuted)
                } else {
                    Button("搜索历史… / History…") { showingHistory = true }
                        .accessibilityIdentifier("terminal.history")
                    Button("新建终端", action: newShell)
                }
            }
            .padding(12)
            if !scoped.isEmpty {
                ScrollView(.horizontal, showsIndicators: false) {
                    HStack(spacing: 6) {
                        ForEach(scoped) { session in
                            Button { workspace.select(session.id, agent: false) } label: {
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
                PaneWorkspaceView(primary: selected, workspace: workspace, agent: false)

            } else {
                ViewThatFits(in: .vertical) {
                VStack(alignment: .leading, spacing: 14) {
                    Text("在这份 checkout 中工作。")
                        .font(Face.sans(20, .semibold)).foregroundStyle(Ink.ink)
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
        .sheet(isPresented: $showingHistory) { WorktreeHistorySheet(root: root, sessions: sessions) }
    }

    private func newShell() {
        do {
            let session = try sessions.add(worktree: root, title: "Shell \(scoped.count + 1)",
                                           executable: TerminalSession.shell, arguments: ["-i"], shellIntegration: integrationEnabled)
            workspace.terminalSelection = session.id
            problem = nil
            DispatchQueue.main.async { session.terminal.window?.makeFirstResponder(session.terminal) }
        } catch { problem = error.localizedDescription }
    }

}
