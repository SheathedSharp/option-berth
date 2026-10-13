import AppKit
import BerthTerminal
import SwiftUI

/// A single worktree session surface. Opening it presents that worktree's
/// interactive shell. An external agent is another PTY started from the shell;
/// leaving its view does not signal it. Creation stays a transient surface
/// above the terminal, not a second page that resizes every running TUI.
struct WorkspaceConsole: View {
    let root: String
    var frozen = false
    @ObservedObject private var workspace: ConsoleWorkspace
    @ObservedObject private var sessions = TerminalSessions.shared
    @ObservedObject private var settings = UISettings.shared
    @StateObject private var launcher = AgentLaunchController()
    @State private var creating = false
    @State private var startingAgent = false
    @State private var history = false
    @State private var problem: String?
    @State private var focusIntent: TerminalFocusIntent?
    @State private var switcherPresented = false
    @State private var switcherQuery = ""
    @State private var terminalReturnID: UUID?
    @State private var pickingProvider = false
    @State private var composerNotice = ""
    @State private var composerFocus = 0
    @State private var agentColumnWidth = 212.0
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
            if let selected {
                HStack(spacing: 0) {
                    sessionRail
                    Hairline(axis: .vertical)
                    VStack(spacing: 0) {
                        HStack(spacing: 0) {
                            PaneWorkspaceView(primary: selected, workspace: workspace, agent: workspace.agentMode,
                                              focusIntent: focusIntent, compactSinglePane: true)
                                .frame(maxWidth: .infinity, maxHeight: .infinity)
                            SplitHandle(width: $agentColumnWidth, range: 168...280, controlsTrailingPane: true)
                            AgentStatusColumn(sessions: agentSessions, resumeID: workspace.resumeAgentID, onSelect: inspectAgent)
                                .frame(width: CGFloat(agentColumnWidth))
                                .frame(maxHeight: .infinity)
                        }
                        Hairline()
                        composerBar
                    }
                }
                .frame(maxWidth: .infinity, maxHeight: .infinity)
                .overlay(alignment: .topLeading) {
                    ZStack(alignment: .topLeading) {
                        if switcherPresented {
                            SessionSwitcherPalette(root: root, sessions: scoped, query: $switcherQuery,
                                                   selected: selected, activate: activate,
                                                   close: closeSwitcher)
                                .frame(width: 430, height: 380)
                                .padding(.leading, 44).padding(.top, 10)
                                .transition(.opacity.combined(with: .move(edge: .top)))
                        }
                        if startingAgent {
                            // Floats above the live shell. The shell's frame does not change,
                            // and dismissing this does not signal either PTY.
                            agentLaunchSurface
                                .frame(width: 440, height: 300, alignment: .top)
                                .background(Ink.canvas)
                                .clipShape(RoundedRectangle(cornerRadius: 10))
                                .overlay(RoundedRectangle(cornerRadius: 10).stroke(Ink.lineStrong, lineWidth: 1))
                                .padding(.leading, 52).padding(.top, 8)
                        }
                    }
                }
            } else if frozen {
                HStack(spacing: 0) {
                    VStack(spacing: 0) {
                        Text("$ git status --short\n M Sources/API.swift\n$ _")
                            .font(Face.mono(Metrics.row)).foregroundStyle(Ink.ink).padding(Metrics.panelPad)
                            .frame(maxWidth: .infinity, maxHeight: .infinity, alignment: .topLeading)
                        Hairline()
                        SessionComposerFrozen(slash: creating)
                    }
                    Hairline(axis: .vertical)
                    SessionStatusFrozen(active: creating)
                }
                .frame(maxWidth: .infinity, maxHeight: .infinity)
            } else {
                shellFirstEmpty
            }
            if let problem { Text(problem).font(Face.sans(Metrics.chrome)).foregroundStyle(Change.changed).textSelection(.enabled).padding(Metrics.gutter) }
        }.background(Ink.canvas).foregroundStyle(Ink.ink)
            .animation(Motion.selection(reduced: reduced), value: switcherPresented)
            .sheet(isPresented: $history) { WorktreeHistorySheet(root: root, sessions: sessions).clientWindowChrome() }
            .onAppear {
                guard !frozen else { return }
                WorkspaceRecovery.shared.watch(workspace)
                ensureShellFirst()
                if let selected { focusIntent = TerminalFocusIntent(selected.id) }
            }
            .onChange(of: creating) { _, value in
                if !value, !startingAgent {
                    launcher.cancel()
                    if let selected { focusIntent = TerminalFocusIntent(selected.id) }
                }
            }
            .onChange(of: startingAgent) { _, value in
                // Launch focuses the new PTY itself. This only aborts a plan that
                // was still in flight when the surface closed.
                if !value, !creating { launcher.cancel() }
            }
            .background(alignment: .topLeading) {
                SessionShortcutDelivery(open: openSwitcher, cycle: cycleSession, escape: handleEscape,
                                        focusComposer: focusComposer, newAgent: { composerAct(.commandShift) },
                                        canCycle: { scoped.count > 1 },
                                        canEscape: { switcherPresented || startingAgent || selected?.kind != "terminal" },
                                        canFocusComposer: canUseComposerShortcut, canNewAgent: canUseComposerShortcut)
                    .frame(width: 1, height: 1)
                    .allowsHitTesting(false)
            }
            .onDisappear { launcher.cancel() }
    }
    private var launchSurface: SessionLaunchPanel { launchPanel(agentOnly: false) }
    private var agentLaunchSurface: SessionLaunchPanel { launchPanel(agentOnly: true) }
    private func launchPanel(agentOnly: Bool) -> SessionLaunchPanel {
        SessionLaunchPanel(root: root, workspace: workspace, launcher: launcher, selected: selected,
            newShell: { newShell() }, activate: activate,
            close: (agentOnly || selected != nil) ? {
                if agentOnly { startingAgent = false } else { creating = false }
                if let selected { focusIntent = TerminalFocusIntent(selected.id) }
            } : nil, frozen: frozen, agentOnly: agentOnly)
    }
    private var toolbar: some View {
        HStack(spacing: Metrics.inline) {
            if frozen {
                Text("＋ 新会话"); Text("输入 ⌘↩").foregroundStyle(Ink.inkMuted)
                Spacer(); Text("历史"); Image(systemName: "ellipsis")
            } else {
                if !scoped.isEmpty {
                    SessionLauncherPopover(isPresented: $creating, panel: launchSurface, reduced: reduced, beforeOpen: beginSession)
                        .fixedSize(horizontal: true, vertical: false).frame(height: 24)
                        .help("再开一个 Shell，或开始外部 Agent 会话")
                } else { Text("新会话").font(Face.sans(Metrics.chrome, .medium)) }
                sessionModeControl
                Spacer(minLength: 0)
                ConsoleToolbarAction(title: "历史", identifier: "terminal.history") { history = true }
                    .fixedSize(horizontal: true, vertical: false).frame(height: 24)
                    .help("搜索当前 worktree 的内存命令历史")
                Menu {
                    Button("新建 Shell") { newShell() }
                    Toggle("新 Shell 启用命令块", isOn: $settings.shellIntegration)
                        .disabled(settings.configuration.preferences.shellIntegration != nil || URL(fileURLWithPath: TerminalSession.shell).lastPathComponent != "zsh")
                    if let selected {
                        Divider()
                        if selected.kind == "terminal" { Button("聚焦输入") { focusComposer() } }
                        else { Button("返回 Shell") { returnToShell() } }
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
        }.font(Face.sans(Metrics.chrome)).padding(.horizontal, Metrics.gutter).padding(.vertical, Metrics.tabPadV).background(Ink.surface)
    }
    private var sessionRail: some View {
        VStack(spacing: 6) {
            Button { openSwitcher() } label: {
                Image(systemName: "rectangle.on.rectangle")
                    .font(.system(size: 13, weight: .semibold))
                    .foregroundStyle(switcherPresented ? Ink.accent : Ink.inkMuted)
                    .frame(width: 30, height: 28)
                    .background(switcherPresented ? Ink.accentSoft : Color.clear)
                    .clipShape(RoundedRectangle(cornerRadius: 6))
            }.buttonStyle(.plain).help("切换会话 / Switch session · ⌘⌥Tab")

            Hairline()
            ScrollView(.vertical, showsIndicators: false) {
                VStack(spacing: 5) {
                    ForEach(orderedSessions) { session in
                        SessionRailItem(session: session, selected: selected?.id == session.id,
                                        activate: { activate(session) })
                    }
                }.padding(.vertical, 2)
            }
            Spacer(minLength: 0)
            Text("⌘⌥")
                .font(Face.mono(8, .medium)).foregroundStyle(Ink.inkFaint)
                .rotationEffect(.degrees(-90))
                .frame(width: 30, height: 24)
        }
        .padding(.horizontal, 5).padding(.vertical, 7)
        .frame(width: 42)
        .background(Ink.surface)
        .accessibilityElement(children: .contain)
        .accessibilityLabel("当前 worktree 会话")
    }

    private var orderedSessions: [TerminalSession] {
        scoped.sorted { lhs, rhs in
            let lc = lhs.kind == "terminal" ? 0 : 1
            let rc = rhs.kind == "terminal" ? 0 : 1
            return lc == rc ? lhs.title.localizedStandardCompare(rhs.title) == .orderedAscending : lc < rc
        }
    }

    private func openSwitcher() {
        startingAgent = false
        switcherQuery = ""
        switcherPresented = true
    }

    private func handleEscape() {
        if startingAgent {
            startingAgent = false
            if let selected, selected.kind == "terminal" { focusIntent = TerminalFocusIntent(selected.id) }
            if switcherPresented { closeSwitcher() }
            return
        }
        closeSwitcher()
    }

    private func closeSwitcher() {
        let leaveAgent = selected?.kind != "terminal"
        switcherPresented = false
        switcherQuery = ""
        if leaveAgent { returnToShell() }
    }

    /// Show the shell again. The agent PTY stays in the registry, still running.
    private func returnToShell() {
        switcherPresented = false
        switcherQuery = ""
        startingAgent = false
        let shell = terminalReturnID.flatMap { id in scoped.first { $0.id == id && $0.kind == "terminal" } }
            ?? scoped.first { $0.kind == "terminal" }
        if let shell { activate(shell) }
        else { newShell() }
    }

    private func activate(_ session: TerminalSession) {
        guard !frozen, scoped.contains(where: { $0.id == session.id }) else { return }
        if session.kind != "terminal", let current = selected, current.kind == "terminal" {
            terminalReturnID = current.id
        }
        // Selection only. Stopping an agent is the explicit end-session action.
        sessions.select(session); creating = false; startingAgent = false; switcherPresented = false
        focusIntent = TerminalFocusIntent(session.id)
        if TerminalWindows.shared.windows[session.id] != nil { TerminalWindows.shared.detach(session) }
    }

    private func cycleSession(_ offset: Int) {
        guard !scoped.isEmpty else { return }
        let values = orderedSessions
        let current = values.firstIndex { $0.id == selected?.id } ?? 0
        let next = (current + offset + values.count) % values.count
        activate(values[next])
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
        HStack(spacing: Metrics.inline) {
            Text("›_ Shell 1").padding(7).background(Ink.accentSoft)
            Spacer()
        }.font(Face.mono(Metrics.meta)).padding(.horizontal, Metrics.gutter).padding(.vertical, Metrics.tabPadV)
    }
    private func beginSession() {
        guard !frozen else { return }
        launcher.cancel()
        startingAgent = false
        creating = true
        if launcher.providers.isEmpty { launcher.refresh(workspace) }
    }
    private var agentSessions: [TerminalSession] {
        scoped.filter { $0.kind.hasPrefix("agent:") }
            .sorted { $0.title.localizedStandardCompare($1.title) == .orderedAscending }
    }
    private var providerLabel: String {
        launcher.providers.first { $0.id == workspace.providerID }?.name ?? SessionComposerModel.providerName(workspace.providerID)
    }
    private var composerBar: some View {
        SessionComposerBar(draft: $workspace.draft, notice: composerNotice, providerLabel: providerLabel,
                           providerChosen: workspace.providerChosen,
                           commands: pickingProvider ? [] : SessionComposerModel.matches(workspace.draft),
                           providers: SessionProviderChoices.merge(launcher.providers),
                           pickingProvider: pickingProvider, enabled: !launcher.planning, focusToken: composerFocus,
                           onShell: { composerAct(.plain) }, onResume: { composerAct(.command) },
                           onNew: { composerAct(.commandShift) }, onEscape: composerEscape,
                           onCommand: runSlash, onProvider: chooseProvider, onPickProvider: {
                               pickingProvider = true
                               if launcher.providers.isEmpty { launcher.refresh(workspace) }
                           })
    }
    private func focusComposer() {
        guard !frozen, selected != nil else { return }
        creating = false
        startingAgent = false
        switcherPresented = false
        switcherQuery = ""
        composerFocus += 1
        if launcher.providers.isEmpty { launcher.refresh(workspace) }
    }
    private func canUseComposerShortcut() -> Bool {
        guard !frozen, !startingAgent, !switcherPresented, !creating, selected != nil else { return false }
        if selected?.terminal.window?.firstResponder is NSTextView { return false }
        return true
    }
    private func composerEscape() {
        if pickingProvider { pickingProvider = false; return }
        if workspace.draft.trimmingCharacters(in: .whitespacesAndNewlines).hasPrefix("/"),
           !SessionComposerModel.matches(workspace.draft).isEmpty {
            workspace.draft = ""
            return
        }
        if let shell = shellTarget() { focusIntent = TerminalFocusIntent(shell.id) }
    }
    private func composerAct(_ chord: SessionComposerModel.Chord) {
        guard !frozen, !launcher.planning else { return }
        let route = SessionComposerModel.route(text: workspace.draft, chord: chord,
                                               providerChosen: workspace.providerChosen,
                                               resumeReady: resumeTarget() != nil)
        apply(route)
    }
    private func apply(_ route: SessionComposerModel.Route) {
        switch route {
        case .none:
            break
        case .shell(let text):
            guard let shell = shellTarget() else { composerNotice = "没有正在运行的 Shell"; return }
            guard deliver(text, to: shell) else { return }
            workspace.draft = ""
            pickingProvider = false
            composerNotice = ""
        case .resume(let text):
            guard let agent = resumeTarget() else {
                composerNotice = "还没有可续接的 Agent 会话。⇧⌘↩ 会用这段文字新建。"
                return
            }
            guard deliver(text, to: agent) else { return }
            workspace.resumeAgentID = agent.id
            workspace.draft = ""
            pickingProvider = false
            composerNotice = "已送入 \(agent.title)"
        case .newAgent(let prompt):
            pickingProvider = false
            workspace.draft = prompt
            beginNewAgent()
        case .openProviders:
            pickingProvider = true
            composerNotice = "选择默认 Agent"
            if workspace.draft.hasPrefix("/") { workspace.draft = "" }
            if launcher.providers.isEmpty { launcher.refresh(workspace) }
        case .selectProvider(let id):
            chooseProvider(id)
        case .needProvider:
            pickingProvider = true
            composerNotice = "还没有默认 Agent。先选择，这段文字会保留。"
            if launcher.providers.isEmpty { launcher.refresh(workspace) }
        case .needSession:
            composerNotice = "还没有可续接的 Agent 会话。⇧⌘↩ 会用这段文字新建。"
        }
    }
    private func runSlash(_ command: SessionComposerModel.Command) {
        let argument = commandArgument(command.token)
        if command.token == "/agent-select" {
            apply(argument.isEmpty ? .openProviders : .selectProvider(argument))
        } else {
            apply(SessionComposerModel.route(text: command.token + (argument.isEmpty ? "" : " " + argument),
                                            chord: .plain, providerChosen: workspace.providerChosen,
                                            resumeReady: resumeTarget() != nil))
        }
    }
    private func commandArgument(_ token: String) -> String {
        let trimmed = workspace.draft.trimmingCharacters(in: .whitespacesAndNewlines)
        let parts = trimmed.split(separator: " ", maxSplits: 1, omittingEmptySubsequences: true)
        guard parts.first.map(String.init) == token, parts.count > 1 else { return "" }
        return String(parts[1]).trimmingCharacters(in: .whitespacesAndNewlines)
    }
    private func chooseProvider(_ id: String) {
        let known = SessionProviderChoices.merge(launcher.providers).contains { $0.id == id }
        guard known || SessionComposerModel.knownProviderIDs.contains(id) else {
            composerNotice = "未知的 Agent"
            return
        }
        if SessionProviderChoices.merge(launcher.providers).first(where: { $0.id == id })?.installed == false {
            composerNotice = "\(SessionComposerModel.providerName(id)) 未安装。不会自动安装。"
            return
        }
        workspace.providerID = id
        workspace.providerChosen = true
        pickingProvider = false
        if workspace.draft.hasPrefix("/") { workspace.draft = "" }
        composerNotice = "默认 Agent：" + (launcher.providers.first { $0.id == id }?.name ?? SessionComposerModel.providerName(id))
    }
    private func beginNewAgent() {
        guard workspace.providerChosen else {
            apply(.needProvider)
            return
        }
        let prompt = workspace.draft
        launcher.whenReady(workspace) {
            guard launcher.available(workspace) else {
                composerNotice = launcher.problem ?? "所选 Agent 未安装或还不能启动"
                return
            }
            launcher.launch(root: root, workspace: workspace,
                            prompt: !prompt.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty) { session in
                workspace.resumeAgentID = session.id
                composerNotice = "已新建 \(session.title)。⌘↩ 续接这一会话"
            }
        }
    }
    private func inspectAgent(_ session: TerminalSession) {
        workspace.resumeAgentID = session.id
        activate(session)
    }
    private func shellTarget() -> TerminalSession? {
        if let selected, selected.kind == "terminal" { return selected }
        if let id = terminalReturnID, let shell = scoped.first(where: { $0.id == id && $0.kind == "terminal" }) { return shell }
        return scoped.first { $0.kind == "terminal" }
    }
    private func resumeTarget() -> TerminalSession? {
        let agents = scoped.filter { $0.kind.hasPrefix("agent:") && $0.isActive }
        if let id = workspace.resumeAgentID, let match = agents.first(where: { $0.id == id }) { return match }
        if agents.count == 1 { return agents[0] }
        return nil
    }
    @discardableResult
    private func deliver(_ text: String, to session: TerminalSession) -> Bool {
        guard session.isActive, !text.utf8.contains(0), text.utf8.count <= 64 * 1024 else {
            composerNotice = "这段文字不能送入会话"
            return false
        }
        var bytes = Array(text.utf8)
        if bytes.last != 10 { bytes.append(10) }
        session.terminal.send(source: session.terminal, data: bytes[...])
        return true
    }
    @ViewBuilder private var sessionModeControl: some View {
        if let selected {
            if selected.kind == "terminal" {
                ConsoleToolbarAction(title: "输入", identifier: "console.focusComposer", symbol: "text.cursor", perform: focusComposer)
                    .fixedSize().frame(height: 24)
                    .help("聚焦会话输入。Enter 到 Shell，⌘↩ 续接 Agent，⇧⌘↩ 新建。")
                Text("⌘↩").font(Face.mono(9)).foregroundStyle(Ink.inkFaint)
                ForEach(backgroundAgents.prefix(2)) { agent in
                    ConsoleToolbarAction(title: agent.title, identifier: "console.reenter." + agent.id.uuidString,
                                         perform: { activate(agent) })
                        .fixedSize().frame(height: 24)
                        .help("重新进入此 Agent 会话。进程仍是离开前的那一个。")
                }
            } else {
                ConsoleToolbarAction(title: "返回 Shell", identifier: "console.returnToShell", symbol: "terminal",
                                     perform: returnToShell)
                    .fixedSize().frame(height: 24)
                    .help("回到 Shell。当前 Agent 继续在后台运行，可再次进入同一会话。")
            }
        }
    }
    private var backgroundAgents: [TerminalSession] {
        scoped.filter { $0.kind.hasPrefix("agent:") }
            .sorted { $0.title.localizedStandardCompare($1.title) == .orderedAscending }
    }
    /// The live canvas when this worktree has no session yet. The primary action
    /// is a shell; agent launch stays on that shell after it exists.
    private var shellFirstEmpty: some View {
        VStack(alignment: .leading, spacing: 12) {
            Text("Shell").font(Face.sans(16, .semibold))
            Text("此 worktree 的会话从交互式终端开始。Agent 在 Shell 里启动；回到 Shell 不会结束它。")
                .font(Face.sans(Metrics.row)).foregroundStyle(Ink.inkMuted)
                .fixedSize(horizontal: false, vertical: true)
            ConsoleToolbarAction(title: "打开 Shell", identifier: "console.openShell", perform: { newShell() })
                .fixedSize().frame(height: 28).padding(.horizontal, Metrics.gutter)
                .background(Ink.accentSoft).clipShape(RoundedRectangle(cornerRadius: 6))
                .help("在当前 worktree 启动交互式 Shell")
        }
        .frame(maxWidth: 470, alignment: .leading).padding(20)
        .frame(maxWidth: .infinity, maxHeight: .infinity, alignment: .topLeading)
    }

    /// One interactive shell is the default presentation. A missing directory or
    /// a full registry reports the existing error and does not leave a half-started
    /// session. An agent the user is already viewing stays in front; the shell is
    /// still started underneath so they can return to it.
    private func ensureShellFirst() {
        guard !frozen, root.hasPrefix("/") else { return }
        if Self.bootstrapping.contains(root) { return }
        let shells = scoped.filter { $0.kind == "terminal" }
        let showingAgent = workspace.agentMode && scoped.contains { $0.kind.hasPrefix("agent:") && $0.id == workspace.activeSelection }
        if !shells.isEmpty { return }
        Self.bootstrapping.insert(root)
        defer { Self.bootstrapping.remove(root) }
        newShell(select: !showingAgent)
    }

    private func newShell(select: Bool = true) {
        guard !frozen else { return }
        launcher.cancel()
        do {
            let number = scoped.filter { $0.kind == "terminal" }.count + 1
            let session = try sessions.add(worktree: root, title: "Shell \(number)", executable: TerminalSession.shell,
                arguments: ["-i"], shellIntegration: settings.shellIntegration && URL(fileURLWithPath: TerminalSession.shell).lastPathComponent == "zsh",
                select: select)
            problem = nil
            if select { activate(session) }
        } catch { problem = error.localizedDescription }
    }

    private static var bootstrapping: Set<String> = []
}

/// Native buttons keep keyboard/accessibility activation and hit testing inside
/// AppKit, while SwiftUI owns their identity and the shared worktree layout.
struct ConsoleSessionTab: NSViewRepresentable {
    let session: TerminalSession
    let selected: Bool
    var compact = false
    let activate: () -> Void
    func makeNSView(context: Context) -> ConsoleSessionButton { ConsoleSessionButton() }
    func updateNSView(_ button: ConsoleSessionButton, context: Context) {
        button.title = compact ? "" : session.title + (session.isActive ? " ●" : " ○")
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
