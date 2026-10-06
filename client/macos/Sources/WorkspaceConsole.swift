import AppKit
import BerthTerminal
import SwiftUI

/// A single worktree session surface. Creation is transient presentation above
/// the terminal, not another mode or a panel that resizes every running TUI.
struct WorkspaceConsole: View {
    let root: String
    var frozen = false
    @ObservedObject private var workspace: ConsoleWorkspace
    @ObservedObject private var sessions = TerminalSessions.shared
    @ObservedObject private var settings = UISettings.shared
    @StateObject private var launcher = AgentLaunchController()
    @State private var creating = false
    @State private var history = false
    @State private var problem: String?
    @State private var focusIntent: TerminalFocusIntent?
    @Environment(\.clientReduceMotion) private var reduced
    init(root: String, frozen: Bool = false, initialAgent: Bool = false) {
        self.root = root; self.frozen = frozen
        _workspace = ObservedObject(wrappedValue: frozen ? ConsoleWorkspace() : TerminalSessions.shared.workspace(root))
        _creating = State(initialValue: frozen && initialAgent)
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
                PaneWorkspaceView(primary: selected, workspace: workspace, agent: workspace.agentMode,
                                  focusIntent: focusIntent, compactSinglePane: true)
                    .frame(maxWidth: .infinity, maxHeight: .infinity)
            } else if frozen {
                Text("$ git status --short\n M Sources/API.swift\n$ _")
                    .font(Face.mono(12)).foregroundStyle(Ink.ink).padding(18)
                    .frame(maxWidth: .infinity, maxHeight: .infinity, alignment: .topLeading)
                    .overlay { if creating { launchSurface.frame(width: 440).background(Ink.canvas).clipShape(RoundedRectangle(cornerRadius: 10)).padding(12) } }
            } else {
                launchSurface.frame(maxWidth: 470).padding(20)
                    .frame(maxWidth: .infinity, maxHeight: .infinity)
            }
            if let problem { Text(problem).font(Face.sans(11)).foregroundStyle(Change.changed).textSelection(.enabled).padding(10) }
        }.background(Ink.canvas).foregroundStyle(Ink.ink)
            .sheet(isPresented: $history) { WorktreeHistorySheet(root: root, sessions: sessions) }
            .onAppear {
                guard !frozen else { return }
                WorkspaceRecovery.shared.watch(workspace)
                if let selected { focusIntent = TerminalFocusIntent(selected.id) }
                else { launcher.refresh(workspace) }
            }
            .onChange(of: creating) { _, value in
                if !value {
                    launcher.cancel()
                    if let selected { focusIntent = TerminalFocusIntent(selected.id) }
                }
            }
            .onDisappear { launcher.cancel() }
    }
    private var launchSurface: SessionLaunchPanel {
        SessionLaunchPanel(root: root, workspace: workspace, launcher: launcher, selected: selected,
            newShell: newShell, activate: activate, close: selected == nil ? nil : { creating = false }, frozen: frozen)
    }
    private var toolbar: some View {
        HStack(spacing: 9) {
            if frozen {
                Text("＋ 新会话"); Spacer(); Text("历史"); Image(systemName: "ellipsis")
            } else {
                if !scoped.isEmpty {
                    SessionLauncherPopover(isPresented: $creating, panel: launchSurface, reduced: reduced, beforeOpen: beginSession)
                        .fixedSize(horizontal: true, vertical: false).frame(height: 24)
                        .help("在当前 worktree 创建 Shell 或外部 Agent 会话")
                } else { Text("新会话").font(Face.sans(11, .medium)) }
                Spacer(minLength: 0)
                ConsoleToolbarAction(title: "历史", identifier: "terminal.history") { history = true }
                    .fixedSize(horizontal: true, vertical: false).frame(height: 24)
                    .help("搜索当前 worktree 的内存命令历史")
                Menu {
                    Button("新建 Shell", action: newShell)
                    Toggle("新 Shell 启用命令块", isOn: $settings.shellIntegration)
                        .disabled(settings.configuration.preferences.shellIntegration != nil || URL(fileURLWithPath: TerminalSession.shell).lastPathComponent != "zsh")
                    if let selected {
                        Divider()
                        Button("聚焦当前会话") { activate(selected) }
                        Button("查找当前终端") { showTerminalFind(selected.terminal) }
                        Button("在独立窗口显示") { TerminalWindows.shared.detach(selected) }
                        splitItems(selected)
                        Divider()
                        Button(selected.isActive ? "结束当前会话…" : "关闭当前会话") {
                            if selected.isActive { confirmSessionStop(selected) }
                            else { TerminalWindows.shared.bringBack(selected); sessions.remove(selected) }
                        }
                    }
                } label: { Image(systemName: "ellipsis") }
                    .menuStyle(.borderlessButton).fixedSize().help("会话操作")
            }
        }.font(Face.sans(11)).padding(.horizontal, 10).padding(.vertical, 6).background(Ink.surface)
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
                            .animation(Motion.selection(reduced: reduced), value: selected?.id == session.id)
                            .contextMenu {
                                Button("聚焦") { activate(session) }
                                Button("在独立窗口显示") { TerminalWindows.shared.detach(session) }
                                splitItems(session)
                            }
                    }
                }.padding(.horizontal, 10).padding(.vertical, 6)
            }.onChange(of: workspace.activeSelection) { _, id in if let id { proxy.scrollTo(id, anchor: .center) } }
        }
    }
    @ViewBuilder private func splitItems(_ target: TerminalSession) -> some View {
        ForEach([PaneLayout.Axis.horizontal, .vertical], id: \.self) { axis in
            Menu(axis == .horizontal ? "左右分屏" : "上下分屏") {
                ForEach(scoped.filter { !workspace.activeLayout.sessions.contains($0.id) }) { other in
                    Button(other.title) {
                        do { try workspace.split(other.id, beside: target.id, agent: workspace.agentMode, axis: axis) }
                        catch { problem = error.localizedDescription }
                    }
                }
            }
        }
    }
    private var frozenStrip: some View {
        HStack(spacing: 8) {
            Text("›_ Shell 1").padding(7).background(Ink.accentSoft)
            Text("◇ Codex 1").padding(7).background(Ink.surface)
            Spacer()
        }.font(Face.mono(10)).padding(.horizontal, 10).padding(.vertical, 6)
    }
    private func activate(_ session: TerminalSession) {
        guard !frozen, scoped.contains(where: { $0.id == session.id }) else { return }
        sessions.select(session); creating = false
        focusIntent = TerminalFocusIntent(session.id)
        if TerminalWindows.shared.windows[session.id] != nil { TerminalWindows.shared.detach(session) }
    }
    private func beginSession() {
        guard !frozen else { return }
        creating = true
        if launcher.providers.isEmpty { launcher.refresh(workspace) }
    }
    private func newShell() {
        guard !frozen else { return }
        launcher.cancel()
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
final class ConsoleSessionButton: NSButton {
    var activate: (() -> Void)?
    init() {
        super.init(frame: .zero)
        isBordered = false; bezelStyle = .inline; setButtonType(.momentaryPushIn)
        target = self; action = #selector(pressed)
    }
    required init?(coder: NSCoder) { fatalError("init(coder:) is not supported") }
    @objc private func pressed() { activate?() }
}

struct ConsoleToolbarAction: NSViewRepresentable {
    @Environment(\.isEnabled) private var enabled
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
        button.setAccessibilityIdentifier(identifier); button.activate = perform; button.isEnabled = enabled
    }
}
