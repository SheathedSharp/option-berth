import AppKit
import BerthAgent
import BerthTerminal
import SwiftUI

/// Owns only the cancellable external-agent launch plan. Live PTYs remain in the
/// sole TerminalSessions registry and are never owned by a composer/view mode.
@MainActor
final class AgentLaunchController: ObservableObject {
    @Published private(set) var providers: [ExternalAgent] = []
    private enum Phase { case idle, discovery, plan }
    @Published private var phase = Phase.idle
    var loading: Bool { phase != .idle }
    var planning: Bool { phase == .plan }
    @Published private(set) var problem: String?
    private var operation: Task<Void, Never>?
    private var generation = UUID()
    private var pending: (() -> Void)?
    deinit { operation?.cancel() }
    func provider(_ workspace: ConsoleWorkspace) -> ExternalAgent? { providers.first { $0.id == workspace.providerID } }
    func available(_ workspace: ConsoleWorkspace) -> Bool { !loading && provider(workspace)?.installed == true }
    func cancel() {
        generation = UUID(); operation?.cancel(); operation = nil; phase = .idle; pending = nil
    }

    /// Runs `body` once the provider catalogue is loaded. A chosen provider is not replaced.
    func whenReady(_ workspace: ConsoleWorkspace, _ body: @escaping () -> Void) {
        if !providers.isEmpty { body(); return }
        pending = body
        refresh(workspace)
    }

    func refresh(_ workspace: ConsoleWorkspace) {
        if loading, !providers.isEmpty { return }
        if loading { operation?.cancel(); operation = nil; phase = .idle }
        guard let binary = DaemonLaunch.binaryPath() else {
            problem = "找不到兼容的 oberth"
            pending = nil
            return
        }
        let id = UUID(); generation = id; phase = .discovery; problem = nil
        operation = Task { @MainActor in
            defer { if generation == id { phase = .idle; operation = nil } }
            do {
                let values = try await AgentBridge.providers(binary: binary, environment: TerminalSession.environment())
                try Task.checkCancellation()
                guard generation == id else { return }
                providers = values
                if !workspace.providerChosen,
                   !values.contains(where: { $0.id == workspace.providerID }),
                   let first = values.first(where: { $0.installed }) ?? values.first { workspace.providerID = first.id }
                if generation == id { phase = .idle; operation = nil }
                let follow = pending
                pending = nil
                follow?()
            } catch is CancellationError {
                if generation == id { pending = nil }
            } catch {
                if generation == id { problem = error.localizedDescription; pending = nil }
            }
        }
    }
    func launch(root: String, workspace: ConsoleWorkspace, prompt: Bool, resumeFile: String? = nil,
                didLaunch: @escaping (TerminalSession) -> Void) {
        guard available(workspace), let provider = provider(workspace), let binary = DaemonLaunch.binaryPath() else { return }
        let message = prompt ? workspace.draft : nil
        guard !prompt || !(message ?? "").trimmingCharacters(in: .whitespacesAndNewlines).isEmpty,
              resumeFile == nil || provider.supportsResume else { return }
        let mode = prompt && !provider.nativePrompt ? "task" : "native"
        let id = UUID(); generation = id; phase = .plan; problem = nil
        operation = Task { @MainActor in
            defer { if generation == id { phase = .idle; operation = nil } }
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

/// Shell and external agents share one creation surface. This is not a second
/// conversation protocol: after launch, the selected native PTY owns all input.
struct SessionLaunchPanel: View {
    let root: String
    @ObservedObject var workspace: ConsoleWorkspace
    @ObservedObject var launcher: AgentLaunchController
    let selected: TerminalSession?
    let newShell: () -> Void
    let activate: (TerminalSession) -> Void
    let close: (() -> Void)?
    var frozen = false
    /// Started from the live shell. The shell target is absent: this surface
    /// only plans a new external-agent PTY.
    var agentOnly = false
    @State private var shell = false
    private var target: Binding<String> {
        Binding(get: { shell ? "shell" : workspace.providerID }, set: {
            shell = $0 == "shell"
            if !shell { workspace.providerID = $0 }
        })
    }
    var body: some View {
        VStack(alignment: .leading, spacing: Metrics.section) {
            HStack(spacing: 8) {
                Image(systemName: shell ? "terminal" : "sparkle").foregroundStyle(Ink.accent)
                if frozen { Text("Codex ▾").font(Face.sans(12, .medium)) }
                else {
                    SessionTargetPicker(target: agentOnly ? Binding(get: { workspace.providerID }, set: { workspace.providerID = $0 }) : target,
                                        providers: launcher.providers, includeShell: !agentOnly)
                        .frame(height: 26).disabled(launcher.planning)
                }
                Spacer(minLength: 0)
                if let close {
                    ConsoleToolbarAction(title: "", identifier: "console.cancelLaunch", symbol: "xmark") { launcher.cancel(); close() }
                        .frame(width: 22, height: 22).help("取消并返回当前会话；草稿保留")
                }
            }
            if agentOnly && !frozen {
                Text("新的外部 Agent 会话。回到 Shell 后它继续运行。")
                    .font(Face.sans(10)).foregroundStyle(Ink.inkMuted)
                    .fixedSize(horizontal: false, vertical: true)
            }
            if !agentOnly && shell {
                Text(URL(fileURLWithPath: TerminalSession.shell).lastPathComponent)
                    .font(Face.mono(13)).foregroundStyle(Ink.inkMuted)
                    .frame(maxWidth: .infinity, minHeight: 76, alignment: .leading)
            } else if frozen {
                Text("检查当前改动，再运行相关测试。")
                    .font(Face.mono(12)).frame(maxWidth: .infinity, minHeight: 76, alignment: .topLeading)
            } else {
                launchDraftHint
                    .frame(maxWidth: .infinity, minHeight: 76, alignment: .topLeading)
                    .padding(10).background(Ink.surface)
                    .overlay(RoundedRectangle(cornerRadius: 6).stroke(Ink.line, lineWidth: 1))
            }
            if let problem = launcher.problem, !shell {
                Text(problem).font(Face.sans(10)).foregroundStyle(Change.changed).lineLimit(3).textSelection(.enabled)
            }
            HStack(spacing: 10) {
                if launcher.loading { ProgressView().controlSize(.small); Button("取消") { launcher.cancel() } }
                else if !frozen {
                    Button { launcher.refresh(workspace) } label: { Image(systemName: "arrow.clockwise") }
                        .buttonStyle(.plain).help("重新检测本机 Agent")
                    if !shell {
                        Menu {
                            Button(canFocusNative ? "返回当前原生会话" : "打开原生会话", action: openNative)
                                .disabled(!launcher.available(workspace) && !canFocusNative)
                            Button("从文件续接…", action: resume)
                                .disabled(!launcher.available(workspace) || launcher.provider(workspace)?.supportsResume != true)
                        } label: { Text("会话选项") }.menuStyle(.borderlessButton).fixedSize()
                    }
                }
                Spacer(minLength: 0)
                if frozen { Text("开始新会话 ⌘↩").font(Face.sans(11)) }
                else {
                    ConsoleToolbarAction(title: agentOnly ? "开始 Agent ⌘↩" : (shell ? "打开 Shell" : "开始新会话 ⌘↩"), identifier: "console.launch", perform: start)
                        .fixedSize().frame(height: 28).padding(.horizontal, 9).background(Ink.accentSoft)
                        .clipShape(RoundedRectangle(cornerRadius: 6))
                        .disabled(launcher.planning || (!shell && !launcher.available(workspace)))
                        .help("显式创建会话；后续输入与审批保留在原生终端")
                }
            }
        }.padding(Metrics.panelPad).background(Ink.canvas)
    }
    private var canFocusNative: Bool { selected?.isActive == true && selected?.kind == "agent:\(workspace.providerID):native" }
    @ViewBuilder private var launchDraftHint: some View {
        let trimmed = workspace.draft.trimmingCharacters(in: .whitespacesAndNewlines)
        if trimmed.isEmpty {
            Text("在底部输入框写初始任务（可选）。Enter 进 Shell，⌘↩ 开始 Agent。")
                .font(Face.sans(11)).foregroundStyle(Ink.inkMuted)
                .fixedSize(horizontal: false, vertical: true)
        } else {
            Text(trimmed).font(Face.mono(12)).foregroundStyle(Ink.ink).lineLimit(4).textSelection(.enabled)
        }
    }
    private func start() {
        guard !launcher.planning else { return }
        if shell && !agentOnly {
            newShell()
            DispatchQueue.main.async { close?() }
        } else { launcher.launch(root: root, workspace: workspace,
            prompt: !workspace.draft.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty, didLaunch: activate) }
    }
    private func openNative() {
        if canFocusNative, let selected { activate(selected) }
        else { launcher.launch(root: root, workspace: workspace, prompt: false, didLaunch: activate) }
    }
    private func resume() {
        guard launcher.available(workspace), launcher.provider(workspace)?.supportsResume == true else { return }
        let panel = NSOpenPanel()
        panel.canChooseFiles = true; panel.canChooseDirectories = false; panel.allowsMultipleSelection = false
        panel.prompt = "校验并续接"
        panel.message = "只续接当前 worktree 的原始会话文件。"
        guard panel.runModal() == .OK, let file = panel.url else { return }
        launcher.launch(root: root, workspace: workspace, prompt: false, resumeFile: file.path, didLaunch: activate)
    }
}

private struct SessionTargetPicker: NSViewRepresentable {
    @Binding var target: String
    let providers: [ExternalAgent]
    var includeShell = true
    @Environment(\.isEnabled) private var enabled
    func makeNSView(context: Context) -> SessionTargetButton { SessionTargetButton() }
    func updateNSView(_ button: SessionTargetButton, context: Context) {
        let values = (includeShell ? [("shell", "Shell", true)] : []) + providers.map { ($0.id, $0.name + ($0.installed ? "" : " · 未安装"), $0.installed) }
        let choices = values.contains(where: { $0.0 == target }) ? values : values + [(target, "检测 Agent…", false)]
        let signature = choices.map { "\($0.0):\($0.1):\($0.2)" }
        if button.signature != signature {
            button.removeAllItems()
            for (id, title, available) in choices {
                button.addItem(withTitle: title); button.lastItem?.representedObject = id; button.lastItem?.isEnabled = available
            }
            button.signature = signature
        }
        if let item = button.itemArray.first(where: { $0.representedObject as? String == target }) { button.select(item) }
        button.isEnabled = enabled; button.font = Face.nativeMono(12)
        button.changed = { target = $0 }
        button.setAccessibilityIdentifier("console.launchTarget")
        button.setAccessibilityLabel("新会话运行方式")
    }
}
private final class SessionTargetButton: NSPopUpButton {
    var signature: [String] = []
    var changed: ((String) -> Void)?
    init() {
        super.init(frame: .zero, pullsDown: false)
        autoenablesItems = false; target = self; action = #selector(selected)
    }
    required init?(coder: NSCoder) { fatalError("init(coder:) is not supported") }
    @objc private func selected() { if let id = selectedItem?.representedObject as? String { changed?(id) } }
}
