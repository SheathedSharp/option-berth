import AppKit
import BerthAgent
import BerthTerminal
import SwiftUI

struct WorkspaceConsole: View {
    let root: String
    var frozen = false
    var initialAgent = false
    @State private var agent: Bool
    init(root: String, frozen: Bool = false, initialAgent: Bool = false) {
        self.root = root; self.frozen = frozen; self.initialAgent = initialAgent
        _agent = State(initialValue: initialAgent)
    }
    var body: some View {
        VStack(spacing: 0) {
            HStack {
                if frozen {
                    HStack(spacing: 0) {
                        Text("Terminal").padding(.horizontal, 14).padding(.vertical, 5).background(agent ? Ink.surface : Ink.accentSoft)
                        Text("Agent session").padding(.horizontal, 14).padding(.vertical, 5).background(agent ? Ink.accentSoft : Ink.surface)
                    }.font(Face.sans(11)).background(Ink.surface).clipShape(RoundedRectangle(cornerRadius: 5))
                } else {
                    Picker("会话模式 / Session mode", selection: $agent) {
                        Text("Terminal").tag(false)
                        Text("Agent session").tag(true)
                    }.labelsHidden().pickerStyle(.segmented).frame(width: 235)
                }
                Spacer()
                Text("Worktree · Git · Runtime").font(Face.mono(10)).foregroundStyle(Ink.inkFaint)
            }.padding(.horizontal, 12).padding(.vertical, 8)
            Hairline()
            if agent { AgentPanel(root: root, frozen: frozen).id(root) }
            else { TerminalPanel(root: root, frozen: frozen).id(root) }
        }

    }
}

struct AgentPanel: View {
    let root: String
    var frozen = false
    @ObservedObject private var sessions = TerminalSessions.shared
    @State private var providers: [ExternalAgent] = []
    @State private var providerID = "codex"
    @State private var selection: UUID?
    @State private var prompt = ""
    @State private var issue: String?
    @State private var loading = false
    @State private var planning: Task<Void, Never>?

    init(root: String, frozen: Bool = false) {
        self.root = root; self.frozen = frozen
        if frozen {
            _providers = State(initialValue: [ExternalAgent(id: "codex", name: "Codex", command: "codex", installed: true, nativePrompt: true)])
            _prompt = State(initialValue: "检查当前 worktree 的服务状态，并说明失败原因。")
        }
    }

    private var provider: ExternalAgent? { providers.first { $0.id == providerID } }
    private var scoped: [TerminalSession] {
        sessions.inWorktree(root).filter { $0.kind.hasPrefix("agent:") }
    }
    private var selected: TerminalSession? { scoped.first { $0.id == selection } ?? scoped.last }
    private var available: Bool { provider?.installed == true && !loading && !frozen }

    var body: some View {
        VStack(spacing: 0) {
            HStack(spacing: 10) {
                if frozen {
                    Label(provider?.name ?? "Codex", systemImage: "chevron.down")
                        .font(Face.sans(12)).padding(.horizontal, 10).padding(.vertical, 5).background(Ink.surface)
                } else {
                    Picker("Coding agent", selection: $providerID) {
                        ForEach(providers) { provider in
                            Text(provider.name + (provider.installed ? "" : " · 未安装 / Missing")).tag(provider.id)
                        }
                    }.labelsHidden().frame(maxWidth: 260)
                }
                Spacer()
                nativeButton
            }.padding(12)
            if !scoped.isEmpty {
                ScrollView(.horizontal, showsIndicators: false) {
                    HStack(spacing: 6) {
                        ForEach(scoped) { session in
                            Button {
                                selection = session.id
                                focus(session)
                            } label: {
                                Text(session.title + (session.isActive ? " ●" : " ○"))
                                    .font(Face.mono(10)).padding(6)
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
                    .frame(maxWidth: .infinity, maxHeight: .infinity)
                HStack {
                    Text(selected.state).font(Face.mono(10)).foregroundStyle(Ink.inkMuted)
                    Spacer()
                    Button(selected.isStopping ? "强制结束 / Force end" : (selected.isActive ? "结束 / End" : "关闭 / Close")) {
                        close(selected)
                    }
                }.padding(.horizontal, 12).padding(.vertical, 6)
            } else {
                emptyState
            }
            Hairline()
            VStack(alignment: .leading, spacing: 7) {
                Text(providerID == "deepseek" ? "DeepSeek：消息使用 headless；原生模式要求已有 tui profile。" : "⌘↩ 新会话 / New session · 后续输入交给原生 agent")
                    .font(Face.sans(10)).foregroundStyle(Ink.inkFaint)
                Group {
                    if frozen {
                        Text(prompt).frame(maxWidth: .infinity, maxHeight: .infinity, alignment: .topLeading)
                    } else {
                        AgentComposer(text: $prompt, enabled: !loading,
                                      font: Face.nativeMono(12),
                                      foreground: NSColor(Ink.ink),
                                      onSubmit: { launch(withPrompt: true) }, onNative: openNative)
                    }
                }
                .font(Face.mono(12)).frame(minHeight: 54, maxHeight: 76)
                .padding(5).background(Ink.surface)
                .overlay(RoundedRectangle(cornerRadius: 4).stroke(Ink.line, lineWidth: 1))
                .accessibilityLabel("给 coding agent 的新会话消息 / New agent-session message")
                HStack {
                    Text("不自动恢复其他 worktree 会话 · No implicit resume")
                        .font(Face.sans(9.5)).foregroundStyle(Ink.inkFaint).lineLimit(1).truncationMode(.tail).layoutPriority(-1)
                    Spacer()
                    if loading { Button("取消 / Cancel") { planning?.cancel() } }
                    if !loading && !frozen { Button("刷新 / Refresh") {
                        planning = Task { await loadProviders() }
                    } }
                    sendButton
                }
                if let issue { Text(issue).font(Face.sans(10)).foregroundStyle(Ink.ink).textSelection(.enabled) }
            }.padding(12)
        }
        .background(Ink.canvas)
        .task(id: root) { await loadProviders() }
        .onDisappear { planning?.cancel(); planning = nil }
    }

    private var emptyState: some View {
        // The input and actions must remain reachable in the full workspace,
        // not only when the console is rendered without its surrounding chrome.
        ViewThatFits(in: .vertical) {
            VStack(alignment: .leading, spacing: 10) {
                Text("你的 agent，它自己的原生会话。")
                    .font(Face.display(19, .medium))
                Text("Your coding agent. Its own native session.")
                    .font(Face.sans(12)).foregroundStyle(Ink.inkMuted)
                Text("消息建立新会话；后续交流在原生终端中继续。\nA message starts a new session; continue in the native terminal.")
                    .font(Face.sans(11)).foregroundStyle(Ink.inkMuted)
                Text(root).font(Face.mono(10)).foregroundStyle(Ink.inkFaint)
                    .lineLimit(1).truncationMode(.middle)
            }.padding(16).fixedSize(horizontal: false, vertical: true)
            VStack(alignment: .leading, spacing: 5) {
                Text("新消息，新会话 / New message, new session").font(Face.sans(12))
                Text("后续在原生界面继续 / Continue in the native terminal")
                    .font(Face.sans(10)).foregroundStyle(Ink.inkMuted)
            }.padding(12).fixedSize(horizontal: false, vertical: true)
            Text("新会话 / New session").font(Face.sans(11)).padding(8)
        }.frame(maxWidth: .infinity, maxHeight: .infinity, alignment: .topLeading)
    }

    private var nativeButton: some View {
        Button("原生界面 / Native UI ⇧⌘↩") { openNative() }
            .disabled(!available && !(selected?.isActive == true && selected?.kind.hasSuffix(":native") == true))
    }

    private var sendButton: some View {
        Button("发送到新会话 / Send ⌘↩") { launch(withPrompt: true) }
            .disabled(!available || prompt.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty)
    }

    private func loadProviders() async {
        if frozen {
            providers = [ExternalAgent(id: "codex", name: "Codex", command: "codex", installed: true, nativePrompt: true),
                         ExternalAgent(id: "deepseek", name: "DeepSeek Harness", command: "dsh", installed: false, nativePrompt: false)]
            prompt = "检查当前 worktree 的服务状态，并说明失败原因。"
            return
        }
        loading = true
        issue = nil
        defer { loading = false }
        guard let binary = DaemonLaunch.binaryPath() else {
            issue = "需要支持 agent list/plan 的 oberth / A compatible oberth is required"
            return
        }
        do {
            let values = try await AgentBridge.providers(binary: binary, environment: TerminalSession.environment())
            try Task.checkCancellation()
            providers = values
            if !values.contains(where: { $0.id == providerID && $0.installed }),
               let first = values.first(where: { $0.installed }) ?? values.first { providerID = first.id }
        } catch is CancellationError {} catch { issue = error.localizedDescription }
    }

    private func launch(withPrompt: Bool) {
        guard available, let provider, let binary = DaemonLaunch.binaryPath() else { return }
        if withPrompt && prompt.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty { return }
        let message = withPrompt ? prompt : nil
        let mode = withPrompt && !provider.nativePrompt ? "task" : "native"
        let selectedRoot = root
        loading = true
        issue = nil
        planning?.cancel()
        planning = Task { @MainActor in
            defer { loading = false }
            do {
                let plan = try await AgentBridge.plan(binary: binary, provider: provider.id, root: selectedRoot,
                                                     mode: mode, prompt: message, environment: TerminalSession.environment())
                try Task.checkCancellation()
                let title = "\(provider.name) \(scoped.count + 1)" + (mode == "task" ? " · task" : "")
                let session = try sessions.add(worktree: plan.worktree, title: title,
                                                kind: "agent:\(provider.id):\(mode)", executable: plan.executable, arguments: plan.arguments)
                selection = session.id
                if withPrompt { prompt = "" }
                focus(session)
            } catch is CancellationError {
                issue = "已取消，未启动 agent / Cancelled before agent launch"
            } catch { issue = error.localizedDescription }
        }
    }

    private func openNative() {
        if let selected, selected.isActive, selected.kind.hasSuffix(":native") { focus(selected) }
        else { launch(withPrompt: false) }
    }
    private func focus(_ session: TerminalSession) {
        DispatchQueue.main.async { session.terminal.window?.makeFirstResponder(session.terminal) }
    }
    private func close(_ session: TerminalSession) {
        if !session.isActive { sessions.remove(session); selection = nil; return }
        let alert = NSAlert()
        alert.messageText = "结束这个 agent 会话？ / End this agent session?"
        alert.informativeText = session.isStopping ? "强制结束可能丢失未保存工作。 / Force termination may lose unsaved work." : "只结束这个会话的直接子进程，不停止项目服务。 / This does not stop project services."
        alert.addButton(withTitle: "取消 / Cancel")
        alert.addButton(withTitle: "结束 / End")
        if alert.runModal() == .alertSecondButtonReturn { session.stop(force: session.isStopping) }
    }
}
