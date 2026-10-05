import AppKit
import BerthAgent
import BerthTerminal
import SwiftUI

/// Owns only the cancellable external-agent launch plan. Live PTYs remain in the
/// sole TerminalSessions registry and are never owned by a composer/view mode.
@MainActor
final class AgentLaunchController: ObservableObject {
    @Published private(set) var providers: [ExternalAgent] = []
    @Published private(set) var loading = false
    @Published private(set) var problem: String?
    private var operation: Task<Void, Never>?
    private var generation = UUID()
    deinit { operation?.cancel() }
    func provider(_ workspace: ConsoleWorkspace) -> ExternalAgent? { providers.first { $0.id == workspace.providerID } }
    func available(_ workspace: ConsoleWorkspace) -> Bool { !loading && provider(workspace)?.installed == true }
    func cancel() { generation = UUID(); operation?.cancel(); operation = nil; loading = false }

    func refresh(_ workspace: ConsoleWorkspace) {
        guard !loading else { return }
        guard let binary = DaemonLaunch.binaryPath() else { problem = "找不到兼容的 oberth"; return }
        let id = UUID(); generation = id; loading = true; problem = nil
        operation = Task { @MainActor in
            defer { if generation == id { loading = false; operation = nil } }
            do {
                let values = try await AgentBridge.providers(binary: binary, environment: TerminalSession.environment())
                try Task.checkCancellation()
                guard generation == id else { return }
                providers = values
                if !values.contains(where: { $0.id == workspace.providerID }),
                   let first = values.first(where: { $0.installed }) ?? values.first { workspace.providerID = first.id }
            } catch is CancellationError {} catch { if generation == id { problem = error.localizedDescription } }
        }
    }
    func launch(root: String, workspace: ConsoleWorkspace, prompt: Bool, resumeFile: String? = nil,
                didLaunch: @escaping (TerminalSession) -> Void) {
        guard available(workspace), let provider = provider(workspace), let binary = DaemonLaunch.binaryPath() else { return }
        let message = prompt ? workspace.draft : nil
        guard !prompt || !(message ?? "").trimmingCharacters(in: .whitespacesAndNewlines).isEmpty,
              resumeFile == nil || provider.supportsResume else { return }
        let mode = prompt && !provider.nativePrompt ? "task" : "native"
        let id = UUID(); generation = id; loading = true; problem = nil
        operation = Task { @MainActor in
            defer { if generation == id { loading = false; operation = nil } }
            do {
                let plan = try await AgentBridge.plan(binary: binary, provider: provider.id, root: root, mode: mode,
                    prompt: message, environment: TerminalSession.environment(), resumeFile: resumeFile)
                try Task.checkCancellation()
                guard generation == id else { return }
                let count = TerminalSessions.shared.inWorktree(root).filter { $0.kind.hasPrefix("agent:") }.count
                let title = "\(provider.name) \(count + 1)" + (resumeFile == nil ? (mode == "task" ? " · task" : "") : " · 续接")
                let session = try TerminalSessions.shared.add(worktree: plan.worktree, title: title,
                    kind: "agent:\(provider.id):\(mode)", executable: plan.executable, arguments: plan.arguments, select: false)
                if prompt && workspace.draft == message { workspace.draft = "" }
                workspace.nativeExpanded = mode == "native"
                didLaunch(session)
            } catch is CancellationError {} catch { if generation == id { problem = error.localizedDescription } }
        }
    }
}

struct AgentLaunchPanel: View {
    let root: String
    @ObservedObject var workspace: ConsoleWorkspace
    @ObservedObject var launcher: AgentLaunchController
    let selected: TerminalSession?
    let activate: (TerminalSession) -> Void
    let close: () -> Void
    var frozen = false
    var body: some View {
        VStack(alignment: .leading, spacing: 8) {
            HStack(spacing: 8) {
                if frozen { Text("Codex ▾").font(Face.sans(11)) }
                else {
                    Picker("Agent", selection: $workspace.providerID) {
                        ForEach(launcher.providers) { item in Text(item.name + (item.installed ? "" : " · 未安装")).tag(item.id) }
                    }.labelsHidden().frame(maxWidth: 180).disabled(launcher.loading)
                }
                Text("新会话").font(Face.sans(10)).foregroundStyle(Ink.inkMuted)
                Spacer(minLength: 0)
                if launcher.loading { ProgressView().controlSize(.small); Button("取消") { launcher.cancel() } }
                if frozen { Text("原生  ·  续接…").font(Face.sans(10)) }
                else {
                    Button("原生", action: openNative).disabled(!launcher.available(workspace) && !canFocusNative)
                        .help("进入所选 Agent 的原生界面，或显式启动新的原生会话 · ⇧⌘↩")
                    Button("续接…", action: resume).disabled(!launcher.available(workspace) || launcher.provider(workspace)?.supportsResume != true)
                        .accessibilityIdentifier("workspace.agent.resume")
                    Button { launcher.cancel(); close() } label: { Image(systemName: "xmark") }.buttonStyle(.plain)
                        .accessibilityLabel("收起新会话输入")
                }
            }
            if frozen {
                Text("检查当前 worktree 的改动和服务状态。")
                    .font(Face.mono(12)).frame(maxWidth: .infinity, minHeight: 44, alignment: .topLeading)
            } else {
                AgentComposer(text: $workspace.draft, enabled: !launcher.loading, font: Face.nativeMono(12),
                    foreground: NSColor(Ink.ink), focusOnAttach: true,
                    onSubmit: send, onNative: openNative)
                    .frame(minHeight: 44, maxHeight: 72).padding(4).background(Ink.surface)
                    .overlay(RoundedRectangle(cornerRadius: 5).stroke(Ink.line, lineWidth: 1))
            }
            HStack(spacing: 8) {
                if let problem = launcher.problem { Text(problem).font(Face.sans(10)).foregroundStyle(Change.changed).lineLimit(2).textSelection(.enabled) }
                Spacer(minLength: 0)
                if frozen { Text("发送到新会话 ⌘↩").font(Face.sans(11)) }
                else {
                    Button { launcher.refresh(workspace) } label: { Image(systemName: "arrow.clockwise") }
                        .buttonStyle(.plain).disabled(launcher.loading).help("重新检测已安装的 Agent")
                    Button("发送 ⌘↩", action: send)
                        .disabled(!launcher.available(workspace) || workspace.draft.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty)
                        .help("建立新会话；后续消息在 Agent 原生终端继续")
                }
            }
        }.padding(10).background(Ink.canvas)
    }
    private var canFocusNative: Bool { selected?.isActive == true && selected?.kind == "agent:\(workspace.providerID):native" }
    private func send() { launcher.launch(root: root, workspace: workspace, prompt: true, didLaunch: activate) }
    private func openNative() {
        if canFocusNative, let selected { activate(selected) }
        else { launcher.launch(root: root, workspace: workspace, prompt: false, didLaunch: activate) }
    }
    private func resume() {
        guard launcher.available(workspace), launcher.provider(workspace)?.supportsResume == true else { return }
        let panel = NSOpenPanel()
        panel.canChooseFiles = true; panel.canChooseDirectories = false; panel.allowsMultipleSelection = false
        panel.prompt = "校验并续接"
        panel.message = "只续接当前 worktree 的原始会话文件；审批与后续输入保留在原生 Agent。"
        guard panel.runModal() == .OK, let file = panel.url else { return }
        launcher.launch(root: root, workspace: workspace, prompt: false, resumeFile: file.path, didLaunch: activate)
    }
}
