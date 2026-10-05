import AppKit
import BerthTerminal
import SwiftUI

/// One worktree, one session strip, one native presentation. Creation controls
/// never own a PTY and never divide running sessions into separate mode pages.
struct WorkspaceConsole: View {
    let root: String
    var frozen = false
    @ObservedObject private var workspace: ConsoleWorkspace
    @ObservedObject private var sessions = TerminalSessions.shared
    @ObservedObject private var settings = UISettings.shared
    @StateObject private var launcher = AgentLaunchController()
    @State private var composing = false
    @State private var history = false
    @State private var problem: String?
    @State private var focusIntent: TerminalFocusIntent?
    init(root: String, frozen: Bool = false, initialAgent: Bool = false) {
        self.root = root; self.frozen = frozen
        _workspace = ObservedObject(wrappedValue: frozen ? ConsoleWorkspace() : TerminalSessions.shared.workspace(root))
        _composing = State(initialValue: frozen && initialAgent)
    }
    private var scoped: [TerminalSession] { frozen ? [] : sessions.inWorktree(root) }
    private var selected: TerminalSession? { scoped.first { $0.id == workspace.activeSelection } ?? scoped.last }
    var body: some View {
        VStack(spacing: 0) {
            toolbar
            Hairline()
            if frozen { frozenStrip }
            else if !scoped.isEmpty { sessionStrip }
            if let selected {
                PaneWorkspaceView(primary: selected, workspace: workspace, agent: workspace.agentMode, focusIntent: focusIntent)
                    .frame(maxWidth: .infinity, maxHeight: .infinity)
            } else if frozen {
                Text(composing ? "会话示例 · Codex\n› 正在查看当前 worktree…" : "$ git status --short\n M Sources/API.swift\n$ _")
                    .font(Face.mono(12)).foregroundStyle(Ink.ink).padding(18)
                    .frame(maxWidth: .infinity, maxHeight: .infinity, alignment: .topLeading)
            } else {
                VStack(spacing: 12) {
                    Image(systemName: "terminal").font(.system(size: 26)).foregroundStyle(Ink.inkFaint)
                    Text("开始工作").font(Face.sans(17, .semibold))
                    HStack {
                        Button("打开 Shell", action: newShell)
                        Button("新 Agent 会话", action: beginAgent)
                    }
                }.frame(maxWidth: .infinity, maxHeight: .infinity)
            }
            if composing {
                Hairline()
                AgentLaunchPanel(root: root, workspace: workspace, launcher: launcher, selected: selected,
                                 activate: { session in if composing { activate(session) } }, close: { composing = false }, frozen: frozen)
            }
            if let problem { Text(problem).font(Face.sans(11)).foregroundStyle(Change.changed).textSelection(.enabled).padding(10) }
        }.background(Ink.canvas).foregroundStyle(Ink.ink)
            .sheet(isPresented: $history) { WorktreeHistorySheet(root: root, sessions: sessions) }
            .onAppear {
                if !frozen {
                    WorkspaceRecovery.shared.watch(workspace)
                    if let selected { focusIntent = TerminalFocusIntent(selected.id) }
                }
            }
            .onDisappear { launcher.cancel() }
    }
    private var toolbar: some View {
        HStack(spacing: 9) {
            if frozen {
                Text("＋ Shell"); Text("＋ Agent")
                Spacer()
                Text("历史"); Image(systemName: "ellipsis")
            } else {
                ConsoleToolbarAction(title: "Shell", identifier: "console.newShell", symbol: "plus", perform: newShell)
                    .fixedSize(horizontal: true, vertical: false).frame(height: 24).help("在当前 worktree 新建原生 Shell")
                ConsoleToolbarAction(title: "Agent", identifier: "console.newAgent", symbol: "plus", perform: beginAgent)
                    .fixedSize(horizontal: true, vertical: false).frame(height: 24).help("展开新 Agent 会话输入，不执行草稿")
                if launcher.loading { ProgressView().controlSize(.small) }
                Spacer(minLength: 0)
                ConsoleToolbarAction(title: "历史", identifier: "terminal.history") { history = true }
                    .fixedSize(horizontal: true, vertical: false).frame(height: 24)
                    .help("仅搜索当前 worktree 的内存命令历史")
                Menu {
                    if launcher.loading { Button("取消 Agent 启动计划") { launcher.cancel() } }
                    Toggle("新 Shell 启用命令块", isOn: $settings.shellIntegration)
                        .disabled(settings.configuration.preferences.shellIntegration != nil || URL(fileURLWithPath: TerminalSession.shell).lastPathComponent != "zsh")
                        .help("settings.json 指定此项时请编辑文件；只影响新建 zsh，不重启已运行的会话")
                    if let selected {
                        Button("聚焦当前会话") { activate(selected) }
                        Button("在独立窗口显示") { TerminalWindows.shared.detach(selected) }
                    }
                } label: { Image(systemName: "ellipsis") }
                    .menuStyle(.borderlessButton).fixedSize()
                    .help("命令块只影响新 Shell，不修改用户 shell 配置；会话结束不等于停止服务")
            }
        }.font(Face.sans(11)).padding(.horizontal, 10).padding(.vertical, 8).background(Ink.surface)
    }
    private var sessionStrip: some View {
        ScrollViewReader { proxy in
            ScrollView(.horizontal, showsIndicators: false) {
                HStack(spacing: 5) {
                    ForEach(scoped) { session in
                        ConsoleSessionTab(session: session, selected: selected?.id == session.id) { activate(session) }
                            .fixedSize(horizontal: true, vertical: false).frame(height: 29).id(session.id)
                            .background(selected?.id == session.id ? Ink.accentSoft : Ink.surface)
                            .clipShape(RoundedRectangle(cornerRadius: 5))
                    }
                }.padding(.horizontal, 10).padding(.vertical, 6)
            }.onChange(of: workspace.activeSelection) { _, id in if let id { proxy.scrollTo(id, anchor: .center) } }
        }
    }
    private var frozenStrip: some View {
        HStack(spacing: 8) {
            Text("›_ Shell 1").padding(7).background(composing ? Ink.surface : Ink.accentSoft)
            Text("◇ Codex 1").padding(7).background(composing ? Ink.accentSoft : Ink.surface)
            Spacer()
        }.font(Face.mono(10)).padding(.horizontal, 10).padding(.vertical, 6)
    }
    private func activate(_ session: TerminalSession) {
        guard !frozen, scoped.contains(where: { $0.id == session.id }) else { return }
        sessions.select(session)
        composing = false
        focusIntent = TerminalFocusIntent(session.id)
        if TerminalWindows.shared.windows[session.id] != nil { TerminalWindows.shared.detach(session) }
    }
    private func beginAgent() {
        guard !frozen else { return }
        composing = true
        if launcher.providers.isEmpty { launcher.refresh(workspace) }
    }
    private func newShell() {
        guard !frozen else { return }
        do {
            let number = scoped.filter { $0.kind == "terminal" }.count + 1
            let session = try sessions.add(worktree: root, title: "Shell \(number)", executable: TerminalSession.shell,
                                           arguments: ["-i"], shellIntegration: settings.shellIntegration && URL(fileURLWithPath: TerminalSession.shell).lastPathComponent == "zsh")
            problem = nil; activate(session)
        } catch { problem = error.localizedDescription }
    }
}

/// Native buttons keep keyboard/accessibility activation and hit testing inside
/// AppKit, while SwiftUI owns their identity and the shared worktree layout.
private struct ConsoleSessionTab: NSViewRepresentable {
    let session: TerminalSession
    let selected: Bool
    let activate: () -> Void
    func makeNSView(context: Context) -> ConsoleSessionButton { ConsoleSessionButton() }
    func updateNSView(_ button: ConsoleSessionButton, context: Context) {
        button.title = session.title + (session.isActive ? " ●" : " ○")
        button.font = Face.nativeMono(11)
        button.image = NSImage(systemSymbolName: session.kind == "terminal" ? "terminal" : "sparkle", accessibilityDescription: nil)
        button.imagePosition = .imageLeading
        button.contentTintColor = NSColor(selected ? Ink.accent : Ink.inkMuted)
        button.toolTip = session.title + " · " + session.state
        button.setAccessibilityIdentifier("console.session." + session.id.uuidString)
        button.setAccessibilityValue(selected ? "选中" : "")
        button.activate = activate
    }
}
private final class ConsoleSessionButton: NSButton {
    var activate: (() -> Void)?
    init() {
        super.init(frame: .zero)
        isBordered = false; bezelStyle = .inline; setButtonType(.momentaryPushIn)
        target = self; action = #selector(pressed)
    }
    required init?(coder: NSCoder) { fatalError("init(coder:) is not supported") }
    @objc private func pressed() { activate?() }
}

private struct ConsoleToolbarAction: NSViewRepresentable {
    let title: String
    let identifier: String
    var symbol: String? = nil
    let perform: () -> Void
    func makeNSView(context: Context) -> ConsoleSessionButton { ConsoleSessionButton() }
    func updateNSView(_ button: ConsoleSessionButton, context: Context) {
        let settings = UISettings.shared
        let size = 11 * settings.interfaceScale
        button.title = title
        button.font = settings.interfaceFontName == "__system__" ? NSFont.systemFont(ofSize: size)
            : (NSFont(name: settings.interfaceFontName, size: size) ?? NSFont.systemFont(ofSize: size))
        button.image = symbol.flatMap { NSImage(systemSymbolName: $0, accessibilityDescription: nil) }
        button.imagePosition = symbol == nil ? .noImage : .imageLeading
        button.contentTintColor = NSColor(Ink.inkMuted)
        button.setAccessibilityIdentifier(identifier); button.activate = perform
    }
}
