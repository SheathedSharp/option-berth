import AppKit
import BerthTerminal
import SwiftUI

struct SessionManager: View {
    @ObservedObject private var sessions = TerminalSessions.shared
    let onOpen: (TerminalSession) -> Void
    @Environment(\.dismiss) private var dismiss
    @State private var query = ""
    @State private var problem: String?
    @State private var streamRefresh = 0
    private var filtered: [TerminalSession] {
        sessions.sessions.filter {
            query.isEmpty || ($0.title + " " + $0.worktree + " " + $0.kind).localizedCaseInsensitiveContains(query)
        }
    }
    private var filteredStreams: [AgentStreamRegistry.Entry] {
        _ = streamRefresh
        return AgentStreamRegistry.all.filter {
            query.isEmpty || ($0.title + " " + $0.worktree + " stream").localizedCaseInsensitiveContains(query)
        }
    }
    var body: some View {
        VStack(alignment: .leading, spacing: Metrics.section) {
            HStack(spacing: Metrics.inline) {
                Text("会话 / Sessions").font(Face.sans(Metrics.display, .semibold))
                Spacer()
                Text("\(sessions.activeCount + AgentStreamRegistry.activeBridgeCount) running · \(sessions.sessions.count + AgentStreamRegistry.all.count)/16")
                    .font(Face.mono(Metrics.chrome))
                Button("完成 / Done") { dismiss() }.keyboardShortcut(.cancelAction)
            }
            TextField("搜索名称、worktree 或 agent / Search", text: $query)
                .textFieldStyle(.roundedBorder).font(Face.sans(Metrics.chrome))
            Text("仅显示本应用拥有的会话；项目清单被移除后仍可打开或结束。\nOnly this application's sessions. Removing a manifest does not orphan a terminal.")
                .font(Face.sans(Metrics.meta)).foregroundStyle(Ink.inkMuted)
            ScrollView {
                LazyVStack(alignment: .leading, spacing: Metrics.rowGap) {
                    ForEach(filteredStreams) { stream in
                        streamRow(stream)
                    }
                    ForEach(filtered) { session in
                        terminalRow(session)
                    }
                    if filtered.isEmpty && filteredStreams.isEmpty {
                        Text("没有匹配会话 / No matching sessions").font(Face.sans(Metrics.row)).padding(.vertical, Metrics.headerPad)
                    }
                }
            }
            if let problem { Text(problem).font(Face.sans(Metrics.chrome)) }
        }.padding(Metrics.panelPad).frame(minWidth: 620, idealWidth: 700, minHeight: 380, idealHeight: 460)
            .background(Ink.canvas)
            .onReceive(NotificationCenter.default.publisher(for: .agentStreamRegistryChanged)) { _ in
                streamRefresh += 1
            }
    }

    private func streamRow(_ stream: AgentStreamRegistry.Entry) -> some View {
        VStack(alignment: .leading, spacing: Metrics.stack) {
            HStack(spacing: Metrics.inline) {
                Text(stream.title).font(Face.sans(Metrics.row, .semibold)).lineLimit(1)
                Text("agent:stream").font(Face.mono(Metrics.meta)).foregroundStyle(Ink.inkMuted).lineLimit(1)
                Spacer(minLength: Metrics.unit)
                Button(stream.process.isRunning ? "结束 / End" : "关闭 / Close") {
                    if stream.process.isRunning { confirmStreamStop(stream.id) }
                    else { AgentStreamRegistry.untrack(id: stream.id) }
                }
            }
            Text(stream.worktree).font(Face.mono(Metrics.meta)).lineLimit(1).truncationMode(.middle)
                .textSelection(.enabled)
            Text(stream.process.isRunning ? "结构化流运行中 / Structured stream" : "结构化流已结束 / Stream ended")
                .font(Face.mono(Metrics.caption)).foregroundStyle(Ink.inkMuted).lineLimit(1)
        }
        .padding(.horizontal, Metrics.gutter).padding(.vertical, Metrics.headerPad)
        .frame(maxWidth: .infinity, alignment: .leading)
        .background(Ink.surface).clipShape(RoundedRectangle(cornerRadius: 4))
    }

    private func terminalRow(_ session: TerminalSession) -> some View {
        VStack(alignment: .leading, spacing: Metrics.stack) {
            HStack(spacing: Metrics.inline) {
                Text(session.title).font(Face.sans(Metrics.row, .semibold)).lineLimit(1)
                Text(session.kind).font(Face.mono(Metrics.meta)).foregroundStyle(Ink.inkMuted).lineLimit(1)
                Spacer(minLength: Metrics.unit)
                Button("打开 / Open") { onOpen(session); dismiss() }
                Button { rename(session) } label: { Image(systemName: "pencil") }
                    .help("重命名 / Rename")
                    .frame(minWidth: Metrics.hit, minHeight: Metrics.hit)
                Button(session.isActive ? "结束 / End" : "关闭 / Close") {
                    if session.isActive { confirmSessionStop(session) }
                    else { sessions.remove(session) }
                }
            }
            Text(session.worktree).font(Face.mono(Metrics.meta)).lineLimit(1).truncationMode(.middle)
                .textSelection(.enabled)
            Text(session.state).font(Face.mono(Metrics.caption)).foregroundStyle(Ink.inkMuted).lineLimit(1)
        }.padding(.horizontal, Metrics.gutter).padding(.vertical, Metrics.headerPad)
            .frame(maxWidth: .infinity, alignment: .leading)
            .background(Ink.surface).clipShape(RoundedRectangle(cornerRadius: 4))
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

    private func confirmStreamStop(_ id: UUID) {
        let alert = ClientAlert.make()
        alert.messageText = "结束结构化 Agent 会话？ / End structured agent session?"
        alert.informativeText = "将关闭 oberth agent stream 桥进程。provider 会话文件仍由 provider 保存。\nCloses the stream bridge; provider session files remain on disk."
        alert.addButton(withTitle: "取消 / Cancel")
        alert.addButton(withTitle: "结束 / End")
        if alert.runModal() == .alertSecondButtonReturn { AgentStreamRegistry.end(id) }
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
