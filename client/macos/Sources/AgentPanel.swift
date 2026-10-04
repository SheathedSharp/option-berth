import AppKit
import BerthAgent
import BerthTerminal
import SwiftUI

struct WorkspaceConsole: View {
    let root: String
    var frozen = false
    var initialAgent = false
    @ObservedObject private var workspace: ConsoleWorkspace
    init(root: String, frozen: Bool = false, initialAgent: Bool = false) {
        self.root = root; self.frozen = frozen; self.initialAgent = initialAgent
        let state = frozen ? ConsoleWorkspace() : TerminalSessions.shared.workspace(root)
        if frozen { state.agentMode = initialAgent }
        _workspace = ObservedObject(wrappedValue: state)
    }
    var body: some View {
        VStack(spacing: 0) {
            HStack {
                if frozen {
                    HStack(spacing: 0) {
                        Text("Terminal").padding(.horizontal, 14).padding(.vertical, 5).background(workspace.agentMode ? Ink.surface : Ink.accentSoft)
                        Text("Agent session").padding(.horizontal, 14).padding(.vertical, 5).background(workspace.agentMode ? Ink.accentSoft : Ink.surface)
                    }.font(Face.sans(11)).background(Ink.surface).clipShape(RoundedRectangle(cornerRadius: 5))
                } else {
                    Picker("会话模式 / Session mode", selection: $workspace.agentMode) {
                        Text("Terminal").tag(false)
                        Text("Agent session").tag(true)
                    }.labelsHidden().pickerStyle(.segmented).frame(width: 235)
                }
                Spacer()
                Text(workspace.agentMode ? "⌘↩ 新会话" : "原生输入").font(Face.mono(10)).foregroundStyle(Ink.inkFaint)
            }.padding(.horizontal, 12).padding(.vertical, 8)
            Hairline()
            if workspace.agentMode { AgentPanel(root: root, frozen: frozen, workspace: workspace).id(root) }
            else { TerminalPanel(root: root, frozen: frozen, workspace: workspace).id(root) }
        }
        .onAppear { if !frozen { WorkspaceRecovery.shared.watch(workspace) } }
    }
}

struct AgentPanel: View {
    let root: String
    var frozen = false
    @ObservedObject private var sessions = TerminalSessions.shared
    @State private var providers: [ExternalAgent] = []
    @ObservedObject private var workspace: ConsoleWorkspace
    @State private var issue: String?
    @State private var loading = false
    @State private var planning: Task<Void, Never>?
    @State private var operationID = UUID()
    @State private var resumedIdentity: String?

    init(root: String, frozen: Bool = false, workspace: ConsoleWorkspace) {
        self.root = root; self.frozen = frozen
        self.workspace = workspace
        if frozen {
            _providers = State(initialValue: [ExternalAgent(id: "codex", name: "Codex", command: "codex", installed: true, nativePrompt: true, resumeFile: true)])
            workspace.draft = "检查当前 worktree 的服务状态，并说明失败原因。"
        }
    }

    private var provider: ExternalAgent? { providers.first { $0.id == workspace.providerID } }
    private var scoped: [TerminalSession] {
        sessions.inWorktree(root).filter { $0.kind.hasPrefix("agent:") }
    }
    private var selected: TerminalSession? { scoped.first { $0.id == workspace.agentSelection } ?? scoped.last }
    private var available: Bool { provider?.installed == true && !loading && !frozen }

    var body: some View {
        VStack(spacing: 0) {
            HStack(spacing: 10) {
                if frozen {
                    Label(provider?.name ?? "Codex", systemImage: "chevron.down")
                        .font(Face.sans(12)).padding(.horizontal, 10).padding(.vertical, 5).background(Ink.surface)
                } else {
                    Picker("Coding agent", selection: $workspace.providerID) {
                        ForEach(providers) { provider in
                            Text(provider.name + (provider.installed ? "" : " · 未安装 / Missing")).tag(provider.id)
                        }
                    }.labelsHidden().frame(maxWidth: 185).disabled(loading)
                }
                Spacer()

                if loading {
                    ProgressView().controlSize(.small)
                    Button("取消") { planning?.cancel() }.help("取消当前计划读取，不结束已运行会话")
                }
                Button("续接…", action: chooseResumeFile)
                    .disabled(!available || provider?.supportsResume != true)
                    .help("选择当前 worktree 的原始会话文件，不使用最近对话 / Resume explicit session file")
                    .accessibilityIdentifier("workspace.agent.resume")
                nativeButton
            }.padding(12)
            if !scoped.isEmpty {
                ScrollView(.horizontal, showsIndicators: false) {
                    HStack(spacing: 6) {
                        ForEach(scoped) { session in
                            Button {
                                workspace.select(session.id, agent: true)
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
            if let resumedIdentity {
                Label(resumedIdentity, systemImage: "arrow.uturn.backward")
                    .font(Face.mono(10)).foregroundStyle(Ink.inkMuted)
                    .lineLimit(1).padding(.horizontal, 12).padding(.bottom, 8)
            }
            Hairline()
            if let selected {
                PaneWorkspaceView(primary: selected, workspace: workspace, agent: true)
                    .frame(maxWidth: .infinity, maxHeight: .infinity)

            } else {
                emptyState
            }
            Hairline()
            if !workspace.nativeExpanded || selected == nil {
            VStack(alignment: .leading, spacing: 7) {
                Text(workspace.providerID == "deepseek" ? "DeepSeek：消息使用 headless；原生模式要求已有 tui profile。" : "⌘↩ 新会话 / New session · 后续输入交给原生 agent")
                    .font(Face.sans(10)).foregroundStyle(Ink.inkFaint)
                Group {
                    if frozen {
                        Text(workspace.draft).frame(maxWidth: .infinity, maxHeight: .infinity, alignment: .topLeading)
                    } else {
                        AgentComposer(text: $workspace.draft, enabled: !loading,
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
                    Text("新消息新建会话；续接须选择原始文件")
                        .font(Face.sans(9.5)).foregroundStyle(Ink.inkFaint).lineLimit(1).truncationMode(.tail).layoutPriority(-1)
                    Spacer()
                    if !loading && !frozen { Button("刷新 / Refresh") {
                        refreshProviders()
                    } }
                    sendButton
                }
            }.padding(12)
            }
            if let issue {
                Text(issue).font(Face.sans(11)).foregroundStyle(Ink.ink).textSelection(.enabled)
                    .padding(10).frame(maxWidth: .infinity, alignment: .leading).background(Ink.surface)
            }
        }
        .background(Ink.canvas)
        .onAppear { refreshProviders() }
        .onDisappear { planning?.cancel(); planning = nil; operationID = UUID(); loading = false }
    }

    private var emptyState: some View {
        // The input and actions must remain reachable in the full workspace,
        // not only when the console is rendered without its surrounding chrome.
        ViewThatFits(in: .vertical) {
            VStack(alignment: .leading, spacing: 10) {
                Text("你的 agent，它自己的原生会话。")
                    .font(Face.sans(19, .semibold))
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
        Button(workspace.nativeExpanded ? "消息框 / Compose" : "原生 / Native") {
            if workspace.nativeExpanded { workspace.nativeExpanded = false }
            else { openNative() }
        }
            .disabled(!workspace.nativeExpanded && !available && !(selected?.isActive == true && selected?.kind.hasSuffix(":native") == true))
    }

    private var sendButton: some View {
        Button("发送到新会话 / Send ⌘↩") { launch(withPrompt: true) }
            .disabled(!available || workspace.draft.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty)
    }

    private func refreshProviders() {
        guard !loading, !frozen else { return }
        guard let binary = DaemonLaunch.binaryPath() else {
            issue = "需要支持 agent list/plan 的 oberth / A compatible oberth is required"; return
        }
        let id = UUID(); operationID = id
        loading = true; issue = nil
        planning = Task { @MainActor in
            defer { if operationID == id { loading = false; planning = nil } }
            do {
                let values = try await AgentBridge.providers(binary: binary, environment: TerminalSession.environment())
                try Task.checkCancellation()
                guard operationID == id else { return }
                providers = values
                if !values.contains(where: { $0.id == workspace.providerID }),
                   let first = values.first(where: { $0.installed }) ?? values.first { workspace.providerID = first.id }
            } catch is CancellationError {} catch {
                if operationID == id { issue = error.localizedDescription }
            }
        }
    }

    private func chooseResumeFile() {
        guard available, provider?.supportsResume == true else { return }
        let panel = NSOpenPanel()
        panel.canChooseFiles = true; panel.canChooseDirectories = false
        panel.allowsMultipleSelection = false; panel.canCreateDirectories = false
        panel.prompt = "校验并续接"
        panel.message = "选择原始会话文件或 OpenCode 导出。只续接当前 worktree；登录、审批和后续输入留在 agent 的原生界面。"
        guard panel.runModal() == .OK, let source = panel.url else { return }
        launch(withPrompt: false, resumeFile: source.path)
    }

    private func launch(withPrompt: Bool, resumeFile: String? = nil) {
        guard available, let provider, let binary = DaemonLaunch.binaryPath() else { return }
        if withPrompt && workspace.draft.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty { return }
        if resumeFile != nil && provider.supportsResume != true { return }
        let message = withPrompt ? workspace.draft : nil
        let mode = withPrompt && !provider.nativePrompt ? "task" : "native"
        let selectedRoot = root
        let id = UUID(); operationID = id
        loading = true
        issue = nil
        planning?.cancel()
        planning = Task { @MainActor in
            defer { if operationID == id { loading = false; planning = nil } }
            do {
                let plan = try await AgentBridge.plan(binary: binary, provider: provider.id, root: selectedRoot,
                                                     mode: mode, prompt: message, environment: TerminalSession.environment(), resumeFile: resumeFile)
                try Task.checkCancellation()
                guard operationID == id else { return }
                let title = "\(provider.name) \(scoped.count + 1)" + (resumeFile != nil ? " · 续接" : (mode == "task" ? " · task" : ""))
                let session = try sessions.add(worktree: plan.worktree, title: title,
                                                kind: "agent:\(provider.id):\(mode)", executable: plan.executable, arguments: plan.arguments)
                resumedIdentity = plan.resume.map { "\(provider.name) · \($0.sessionID.prefix(12)) · 原生审批" }
                workspace.agentSelection = session.id
                if withPrompt && workspace.draft == message { workspace.draft = "" }
                workspace.nativeExpanded = mode == "native"
                focus(session)
            } catch is CancellationError {
                if operationID == id { issue = "已取消，未启动 agent / Cancelled before agent launch" }
            } catch { if operationID == id { issue = error.localizedDescription } }
        }
    }

    private func openNative() {
        if let selected, selected.isActive, selected.kind.hasSuffix(":native") { workspace.nativeExpanded = true; focus(selected) }
        else { launch(withPrompt: false) }
    }
    private func focus(_ session: TerminalSession) {
        DispatchQueue.main.async { session.terminal.window?.makeFirstResponder(session.terminal) }
    }
}
