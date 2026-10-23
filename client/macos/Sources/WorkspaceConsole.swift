import AppKit
import BerthAgent
import BerthTerminal
import SwiftUI

/// A single worktree session surface. Opening it presents that worktree's
/// interactive shell. An external agent is another PTY started from the shell;
/// leaving its view does not signal it. Creation stays a transient surface
/// above the terminal, not a second page that resizes every running TUI.
enum FrozenConsoleScene: Equatable {
    case slashMenu
    case noProvider
    case nativeAgent
    case streamDetail
    case streamMixed
}

struct WorkspaceConsole: View {
    let root: String
    var frozen = false
    var frozenScene: FrozenConsoleScene = .slashMenu
    /// Session-primary: open Services / Git capability panels hosted by BoardView.
    var openServicesPanel: () -> Void = {}
    var openGitPanel: () -> Void = {}
    @ObservedObject private var workspace: ConsoleWorkspace
    @ObservedObject private var sessions = TerminalSessions.shared
    @ObservedObject private var settings = UISettings.shared
    @StateObject private var launcher = AgentLaunchController()
    @StateObject private var streamController = AgentStreamsController()
    @State private var creating = false
    @State private var history = false
    @State private var problem: String?
    @State private var focusIntent: TerminalFocusIntent?
    @State private var switcherPresented = false
    @State private var switcherQuery = ""
    @State private var terminalReturnID: UUID?
    @State private var pickingProvider = false
    @State private var composerNotice = ""
    @State private var composerFocus = 0
    @State private var slashHighlight = 0
    @State private var previewAgentID: UUID?
    /// When true, the full-width canvas shows the structured Agent stream (not Shell).
    @State private var focusStreamOnCanvas = false
    /// Warp-like: Shell vs Agent is input mode on one surface, not a second pane.
    @State private var composerInputMode: ComposerInputMode = .shell
    @State private var historyCursor = 0
    @State private var commandHeld = false
    @State private var pendingChord: SessionComposerModel.Chord?
    @State private var shellAttachment: ComposerShellAttachment?
    @State private var shellTabReadGeneration: UInt = 0
    @Environment(\.clientReduceMotion) private var reduced
    init(root: String, frozen: Bool = false, initialAgent: Bool = false, frozenScene: FrozenConsoleScene = .slashMenu,
         openServicesPanel: @escaping () -> Void = {}, openGitPanel: @escaping () -> Void = {}) {
        self.root = root; self.frozen = frozen; self.frozenScene = frozenScene
        self.openServicesPanel = openServicesPanel
        self.openGitPanel = openGitPanel
        _workspace = ObservedObject(wrappedValue: frozen ? ConsoleWorkspace() : TerminalSessions.shared.workspace(root))
        _creating = State(initialValue: frozen && (initialAgent || frozenScene == .slashMenu))
    }
    private var scoped: [TerminalSession] { frozen ? [] : sessions.inWorktree(root) }
    private var selected: TerminalSession? { scoped.first { $0.id == workspace.activeSelection } ?? scoped.last }
    var body: some View {
        VStack(spacing: 0) {
            // v15: no dedicated 「历史」 chrome row — history via /history or ⌘Y.
            if frozen {
                toolbar
                Hairline()
                frozenStrip
            }
            if let selected {
                VStack(spacing: 0) {
                    // v15 A: no Shell|Pi peer tabs. Thin rail only for 2+ Shells.
                    if showSessionRail {
                        ShellSessionBar(
                            shells: shellSessions,
                            // Never peer-tab Agent/Pi next to Shell — blocks live in the scroll.
                            agents: [],
                            streamRows: [],
                            selectedID: selected.id,
                            streamSelectedID: focusStreamOnCanvas ? (previewAgentID ?? workspace.resumeStreamID) : nil,
                            providerChosen: workspace.providerChosen,
                            onPickProvider: {
                                pickingProvider = true
                                if launcher.providers.isEmpty { launcher.refresh(workspace) }
                            },
                            onSelect: { session in
                                focusStreamOnCanvas = false
                                composerInputMode = .shell
                                activate(session)
                            },
                            onSelectStream: { id in
                                focusStreamOnCanvas = true
                                composerInputMode = .agent
                                selectStreamTarget(id)
                            },
                            onNewShell: { newShell() },
                            onNewAgent: { composerAct(.commandShift) },
                            onOpenSwitcher: openSwitcher
                        )
                        Hairline()
                    }
                    // One continuous session surface: block stream + live PTY + one input.
                    VStack(spacing: 0) {
                        singleCanvas(for: selected)
                            .frame(maxWidth: .infinity, maxHeight: .infinity)
                        if focusStreamOnCanvas || selected.kind == "terminal" {
                            composerBar
                        } else {
                            nativeAgentInputStrip(for: selected)
                        }
                    }
                    .frame(maxWidth: .infinity, maxHeight: .infinity)
                    .background(sessionSurfaceBackground)
                }
                .frame(maxWidth: .infinity, maxHeight: .infinity)
                .overlay(alignment: .topLeading) {
                    if switcherPresented {
                        SessionSwitcherPalette(root: root, sessions: scoped, query: $switcherQuery,
                                               selected: selected, activate: { session in
                                                   focusStreamOnCanvas = false
                                                   activate(session)
                                               },
                                               close: closeSwitcher)
                            .frame(width: 430, height: 380)
                            .padding(.leading, Metrics.gutter).padding(.top, 48)
                            .transition(.opacity.combined(with: .move(edge: .top)))
                    }
                }
            } else if frozen {

                frozenFixture
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
                if let selected { focusSessionInput(selected) }
                if launcher.providers.isEmpty { launcher.refresh(workspace) }
            }
            .onChange(of: creating) { _, value in
                if !value, let selected { focusSessionInput(selected) }
            }
            .background(alignment: .topLeading) {
                SessionShortcutDelivery(open: openSwitcher, cycle: cycleSession, escape: handleEscape,
                                        focusComposer: focusComposer, newAgent: { composerAct(.commandShift) },
                                        toggleInputMode: toggleComposerInputMode,
                                        openHistory: { history = true },
                                        canCycle: { scoped.count > 1 },
                                        canEscape: {
                                            switcherPresented
                                                || pickingProvider
                                                || composerInputMode == .agent
                                                || selected?.kind != "terminal"
                                        },
                                        canFocusComposer: canUseComposerShortcut, canNewAgent: canUseComposerShortcut,
                                        canToggleInputMode: canUseComposerShortcut,
                                        canOpenHistory: { !frozen })
                    .frame(width: 1, height: 1)
                    .allowsHitTesting(false)
            }
            .background(alignment: .topTrailing) {
                // Hidden hooks: HistoryEntryChecks + BERTH_CONSOLE_TEST. Not chrome.
                HStack(spacing: 0) {
                    ConsoleToolbarAction(title: "历史", identifier: "terminal.history") { history = true }
                        .frame(width: 1, height: 1).opacity(0.01)
                    if ProcessInfo.processInfo.environment["BERTH_CONSOLE_TEST"] == "1" {
                        ConsoleToolbarAction(title: "新建 Shell", identifier: "console.newShell", perform: { newShell() })
                            .frame(width: 1, height: 1).opacity(0.01)
                        ConsoleToolbarAction(title: "聚焦输入", identifier: "console.focusComposer", perform: focusComposer)
                            .frame(width: 1, height: 1).opacity(0.01)
                    }
                    // Session overflow kept off the focus chrome; available when rail shows via ShellSessionBar.
                    if !frozen {
                        Menu {
                            Button("新建 Shell") { newShell() }
                            Toggle("新 Shell 启用命令块", isOn: $settings.shellIntegration)
                                .disabled(settings.configuration.preferences.shellIntegration != nil || !ShellIntegrationLease.supports(executable: TerminalSession.shell))
                            Toggle("Agent 后台通知与 Dock 角标", isOn: $settings.consoleAgentNotifications)
                                .disabled(settings.configuration.preferences.consoleAgentNotifications != nil)
                            if let selected {
                                Divider()
                                if selected.kind != "terminal" { Button("返回 Shell") { returnToShell() } }
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
                        .menuStyle(.borderlessButton)
                        .frame(width: 1, height: 1).opacity(0.01)
                        .accessibilityHidden(true)
                    }
                }
                .allowsHitTesting(true)
            }
            .onDisappear { launcher.cancel() }
            .onReceive(NotificationCenter.default.publisher(for: .agentStreamEndedExternally)) { note in
                guard let id = note.userInfo?["id"] as? UUID else { return }
                streamController.dropSession(id)
                if workspace.resumeStreamID == id { workspace.resumeStreamID = nil }
                if previewAgentID == id { previewAgentID = nil }
            }
    }
    /// Frozen snapshot chrome only — live console has no 「历史」 row.
    private var toolbar: some View {
        HStack(spacing: Metrics.inline) {
            Text("会话").foregroundStyle(Ink.inkFaint)
            Spacer()
            Text("历史")
            Image(systemName: "ellipsis")
        }
        .font(Face.sans(Metrics.chrome))
        .padding(.horizontal, Metrics.gutter)
        .padding(.vertical, Metrics.tabPadV)
        .background(Ink.canvas)
    }
    /// Thin rail only for multiple Shells. Never list Agent/Pi as peer tabs —
    /// Agent sessions are peer blocks in the scroll (agent-session cards).
    private var showSessionRail: Bool {
        shellSessions.count > 1
    }

    private var shellSessions: [TerminalSession] {
        scoped.filter { $0.kind == "terminal" }
            .sorted { $0.title.localizedStandardCompare($1.title) == .orderedAscending }
    }

    private var orderedSessions: [TerminalSession] {
        scoped.sorted { lhs, rhs in
            let lc = lhs.kind == "terminal" ? 0 : 1
            let rc = rhs.kind == "terminal" ? 0 : 1
            return lc == rc ? lhs.title.localizedStandardCompare(rhs.title) == .orderedAscending : lc < rc
        }
    }

    private func openSwitcher() {
        switcherQuery = ""
        switcherPresented = true
    }

    private func handleEscape() {
        if switcherPresented {
            closeSwitcher()
            return
        }
        composerEscape()
    }

    private func closeSwitcher() {
        switcherPresented = false
        switcherQuery = ""
    }

    /// Show the shell again. The agent PTY stays in the registry, still running.
    private func returnToShell() {
        switcherPresented = false
        switcherQuery = ""
        focusStreamOnCanvas = false
        composerInputMode = .shell
        let shell = terminalReturnID.flatMap { id in scoped.first { $0.id == id && $0.kind == "terminal" } }
            ?? scoped.first { $0.kind == "terminal" }
        if let shell {
            activate(shell)
            focusIntent = nil
            DispatchQueue.main.async { focusComposer() }
        } else { newShell() }
    }

    private func activate(_ session: TerminalSession) {
        guard !frozen, scoped.contains(where: { $0.id == session.id }) else { return }
        if session.kind != "terminal", let current = selected, current.kind == "terminal" {
            terminalReturnID = current.id
        }
        // Selection only. Stopping an agent is the explicit end-session action.
        sessions.select(session); creating = false; switcherPresented = false
        focusSessionInput(session, agentIntent: session.kind != "terminal")
        if TerminalWindows.shared.windows[session.id] != nil { TerminalWindows.shared.detach(session) }
    }

    /// Shell typing defaults to the bottom composer. Only alternate-screen TUI may own the PTY.
    private func focusSessionInput(_ session: TerminalSession, agentIntent: Bool = false) {
        if session.kind == "terminal" {
            if session.activity.alternateScreen {
                focusIntent = TerminalFocusIntent(session.id)
            } else {
                focusIntent = nil
                focusComposer()
            }
        } else {
            focusIntent = agentIntent ? TerminalFocusIntent(session.id) : nil
            guard agentIntent else { return }
            let sessionID = session.id
            let terminal = session.terminal
            func focusPTY() {
                guard workspace.activeSelection == sessionID else { return }
                if terminal.window?.makeFirstResponder(terminal) == true,
                   focusIntent?.sessionID == sessionID {
                    focusIntent?.markFulfilled()
                }
            }
            DispatchQueue.main.async {
                focusPTY()
                DispatchQueue.main.async { focusPTY() }
            }
        }
    }

    /// Dim PTY caret + bridge typing into composer unless a TUI owns the keyboard.
    private func shellPromptComposerMode(_ session: TerminalSession) -> Bool {
        session.kind == "terminal" && !session.activity.alternateScreen
    }

    private func routeTerminalPromptKey(_ event: NSEvent) {
        focusComposer()
        switch event.keyCode {
        case 51:
            if !workspace.draft.isEmpty { workspace.draft.removeLast() }
        case 36, 76:
            workspace.draft += "\n"
        default:
            if let chars = event.characters, !chars.isEmpty { workspace.draft += chars }
        }
    }

    /// Full-width focus: structured stream detail, or the selected Shell / native Agent PTY.
    @ViewBuilder private func singleCanvas(for session: TerminalSession) -> some View {
        if focusStreamOnCanvas,
           let id = previewAgentID ?? workspace.resumeStreamID,
           let stream = streamController.session(id) {
            AgentStreamDetail(session: stream, queuedCount: streamController.queuedCount(for: stream.id),
                              queuedPreview: streamController.queuedMessages(for: stream.id).first,
                              onWithdrawQueued: {
                                  if let text = streamController.withdrawQueued(id: stream.id) {
                                      workspace.draft = text
                                      composerNotice = "已从排队撤回"
                                  }
                              },
                              onInterrupt: { interruptStream(stream.id) },
                              onClose: { confirmEndStream(stream.id) },
                              onRespond: { requestID, decision in
                                  respondStream(stream.id, requestID: requestID, decision: decision)
                              },
                              onAdoptCommand: adoptAgentCommand,
                              onOpenGitPath: openGitPath,
                              onContinueInNative: openStreamInNativeTerminal)
                .frame(maxWidth: .infinity, maxHeight: .infinity)
                .background(Ink.canvas)
                .onAppear {
                    streamController.markDetailVisible(id)
                    composerInputMode = .agent
                }
        } else {
            sessionCanvas(for: session)
        }
    }

    @ViewBuilder private func sessionCanvas(for session: TerminalSession) -> some View {
        let promptComposer = shellPromptComposerMode(session)
        PaneWorkspaceView(primary: session, workspace: workspace, agent: workspace.agentMode,
                          focusIntent: focusIntent,
                          dimTerminalCaret: promptComposer,
                          bridgePromptKeys: promptComposer,
                          onPromptKey: routeTerminalPromptKey,
                          onSendCommandToAgent: attachShellCommandBlock,
                          agentRows: structuredAgentRows,
                          onEnterAgent: { selectStreamTarget($0) },
                          compactSinglePane: true)
            .frame(maxWidth: .infinity, maxHeight: .infinity)
            .onChange(of: session.activity.alternateScreen) { _, alt in
                guard selected?.id == session.id, session.kind == "terminal" else { return }
                if alt {
                    focusIntent = TerminalFocusIntent(session.id)
                } else {
                    focusIntent = nil
                    focusComposer()
                }
            }
            .onChange(of: session.shellForegroundBusy) { _, busy in
                guard selected?.id == session.id, session.kind == "terminal" else { return }
                // Non-TUI busy must not steal focus; returning to idle reasserts composer.
                if !busy, !session.activity.alternateScreen {
                    focusIntent = nil
                    focusComposer()
                }
            }
            .onChange(of: session.commandBlocks.count) { _, _ in
                guard selected?.id == session.id, session.kind == "terminal" else { return }
                focusSessionInput(session)
            }
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
    /// v15: frozen scenes no longer paint a permanent Shell-1 tab strip.
    private var frozenStrip: some View { EmptyView() }

    @ViewBuilder private var frozenFixture: some View {
        switch frozenScene {
        case .streamMixed:
            VStack(spacing: 0) {
                AgentStreamMixedFrozen()
                SessionComposerFrozen(slash: false, noProvider: false, inputMode: .agent)
            }
            .frame(maxWidth: .infinity, maxHeight: .infinity)
            .background(Ink.accentSoft.opacity(0.22))
        case .streamDetail:
            VStack(spacing: 0) {
                AgentStreamStatusFrozen()
                SessionComposerFrozen(slash: false, noProvider: false, inputMode: .agent)
            }
            .frame(maxWidth: .infinity, maxHeight: .infinity)
            .background(Ink.accentSoft.opacity(0.22))
        case .nativeAgent:
            VStack(spacing: 0) {
                Text("Claude Code · 修复登录测试\n╭──────────────────────────────╮\n│ Allow running npm test?        │\n│  [Allow]  [Deny]             │\n╰──────────────────────────────╯")
                    .font(Face.mono(Metrics.row)).foregroundStyle(Ink.ink).padding(Metrics.panelPad)
                    .frame(maxWidth: .infinity, maxHeight: .infinity, alignment: .topLeading)
                HStack {
                    Text("键盘在 Claude 1 的终端里 · ⌘↩ 回到输入条")
                        .font(Face.sans(Metrics.caption)).foregroundStyle(Ink.inkMuted)
                    Spacer()
                    Text("返回 Shell").font(Face.sans(Metrics.caption)).foregroundStyle(Ink.accent)
                }
                .padding(.horizontal, Metrics.gutter).padding(.vertical, 8)
                .background(Ink.canvas)
            }
            .frame(maxWidth: .infinity, maxHeight: .infinity)
            .background(Ink.canvas)
        default:
            VStack(spacing: 0) {
                // Block-stream freeze: completed cards + active card (PTY lives inside).
                VStack(alignment: .leading, spacing: 8) {
                    frozenBlock(command: "git status",
                                output: "On branch reunify-core\nnothing to commit, working tree clean",
                                selected: false)
                    frozenBlock(command: "ls", output: "README.md  src/", selected: false)
                    Spacer(minLength: 0)
                }
                .padding(.horizontal, Metrics.gutter)
                .padding(.top, 10)
                .frame(maxWidth: .infinity, maxHeight: .infinity, alignment: .topLeading)
                SessionComposerFrozen(slash: frozenScene == .slashMenu && creating,
                                      noProvider: frozenScene == .noProvider,
                                      inputMode: .shell)
            }
            .frame(maxWidth: .infinity, maxHeight: .infinity)
            .background(Ink.canvas)
        }
    }


    private func frozenBlock(command: String, output: String, selected: Bool) -> some View {
        VStack(alignment: .leading, spacing: 4) {
            HStack(spacing: 6) {
                Image(systemName: "checkmark.circle")
                    .font(.system(size: 10, weight: .semibold))
                    .foregroundStyle(Ink.inkFaint)
                Text("$ \(command)").font(Face.mono(Metrics.meta)).foregroundStyle(Ink.ink)
                Spacer(minLength: 0)
                Text("11:42").font(Face.mono(Metrics.caption)).foregroundStyle(Ink.inkFaint)
                Text("exit 0").font(Face.mono(Metrics.caption)).foregroundStyle(Ink.inkFaint)
            }
            Text(output).font(Face.mono(Metrics.caption)).foregroundStyle(Ink.inkMuted)
        }
        .padding(.horizontal, 10).padding(.vertical, 8)
        .frame(maxWidth: .infinity, alignment: .leading)
        .background(selected ? Ink.accentSoft : Ink.sunken.opacity(0.55))
        .overlay(RoundedRectangle(cornerRadius: 8, style: .continuous)
            .stroke(selected ? Ink.accent.opacity(0.55) : Ink.line, lineWidth: 1))
        .clipShape(RoundedRectangle(cornerRadius: 8, style: .continuous))
    }


    private var agentSessions: [TerminalSession] {
        scoped.filter { $0.kind.hasPrefix("agent:") }
            .sorted { $0.title.localizedStandardCompare($1.title) == .orderedAscending }
    }
    @ViewBuilder private var agentColumn: some View {
        VStack(spacing: 0) {
            AgentStatusColumn(sessions: agentSessions, streamRows: structuredAgentRows,
                              resumeID: workspace.resumeAgentID, streamResumeID: workspace.resumeStreamID,
                              previewID: previewAgentID, providerLabel: providerLabel,
                              providerChosen: workspace.providerChosen,
                              providerChoices: SessionProviderChoices.merge(launcher.providers),
                              defaultProviderID: workspace.providerID,
                              canOpenNativeTerminal: launcher.available(workspace),
                              canResumeFromFile: launcher.provider(workspace)?.supportsResume == true,
                              onPickProvider: {
                                  pickingProvider = true
                                  if launcher.providers.isEmpty { launcher.refresh(workspace) }
                              },
                              onNewAgent: { composerAct(.commandShift) },
                              onNewAgentProvider: { beginNewAgent(providerID: $0) },
                              onOpenNativeTerminal: { openNativeTerminalFresh() },
                              onResumeFromFile: { pickResumeFileAndLaunch() },
                              onSelectTerminal: enterAgent,
                              onStreamTarget: { selectStreamTarget($0) },
                              streamInlineRequest: streamInlineRequest,
                              onStreamRespond: { requestID, decision in
                                  if let id = previewAgentID { respondStream(id, requestID: requestID, decision: decision) }
                              })
            if let previewAgentID, let session = streamController.session(previewAgentID) {
                Hairline()
                AgentStreamDetail(session: session, queuedCount: streamController.queuedCount(for: session.id),
                                  queuedPreview: streamController.queuedMessages(for: session.id).first,
                                  onWithdrawQueued: {
                                      if let text = streamController.withdrawQueued(id: session.id) {
                                          workspace.draft = text
                                          composerNotice = "已从排队撤回"
                                      }
                                  },
                                  onInterrupt: { interruptStream(session.id) },
                                  onClose: { confirmEndStream(session.id) },
                                  onRespond: { requestID, decision in
                                      respondStream(session.id, requestID: requestID, decision: decision)
                                  },
                                  onAdoptCommand: adoptAgentCommand,
                                  onOpenGitPath: openGitPath,
                                  onContinueInNative: openStreamInNativeTerminal)
            }
        }
        .onChange(of: previewAgentID) { _, id in streamController.markDetailVisible(id) }
        .onChange(of: streamController.unsupportedNotice?.sessionID) { _, _ in
            guard let notice = streamController.unsupportedNotice else { return }
            composerNotice = "\(notice.message) · 可用「/native」或重新 ⌘↩ 走原生终端"
            workspace.resumeStreamID = notice.sessionID
            previewAgentID = notice.sessionID
        }
    }

    private var streamInlineRequest: AgentRequest? {
        guard let previewAgentID, let session = streamController.session(previewAgentID) else { return nil }
        return session.snapshot.requests.first
    }

    private var structuredAgentRows: [AgentRow] {
        let now = Date()
        return streamController.sessions.map { session in
            AgentRowProjector.streamRow(id: session.id, provider: session.provider, title: session.title,
                                        snapshot: session.snapshot, unread: streamController.isUnread(session.id),
                                        isTarget: session.id == workspace.resumeStreamID,
                                        queuedCount: streamController.queuedCount(for: session.id), now: now)
        }
    }
    private var providerLabel: String {
        launcher.providers.first { $0.id == workspace.providerID }?.name ?? SessionComposerModel.providerName(workspace.providerID)
    }
    private var composerSlashContext: SlashContext {
        ComposerConsoleBridge.slashContext(agentCount: agentSessions.count + streamController.sessions.count,
                                           shellCount: shellSessions.count, target: resumeTarget(),
                                           streamSessions: streamController.sessions,
                                           streamResumeID: workspace.resumeStreamID,
                                           provider: launcher.provider(workspace))
    }

    private var composerContext: ComposerContext {
        ComposerConsoleBridge.context(draft: workspace.draft, shell: shellTarget(), target: resumeTarget(),
                                      streamSessions: streamController.sessions,
                                      streamResumeID: workspace.resumeStreamID,
                                      providerChosen: workspace.providerChosen, providerID: workspace.providerID,
                                      slashContext: composerSlashContext)
    }

    private var slashCommands: [SlashCommand] {
        pickingProvider ? [] : SlashParser.matches(workspace.draft, context: composerSlashContext)
    }

    /// Quiet status for the route row — never a second title bar.
    private var composerStatusHint: String {
        if shellTarget()?.shellIntegrationEnabled == false, shellTarget() != nil {
            return "未启用 Shell 集成 · 在菜单中开启"
        }
        return ""
    }

    private var sessionSurfaceBackground: Color {
        // Soft Paper wash when Agent conversation owns the canvas (Warp alt-background feel).
        if focusStreamOnCanvas || composerInputMode == .agent {
            return Ink.accentSoft.opacity(0.22)
        }
        return Ink.canvas
    }

    private var worktreeShortName: String {
        let name = URL(fileURLWithPath: root).lastPathComponent
        return name.isEmpty ? root : name
    }

    private var composerBar: some View {
        return SessionComposerBar(draft: $workspace.draft, notice: composerNotice,
                           shellAttachment: shellAttachment,
                           onClearAttachment: { shellAttachment = nil },
                           statusHint: composerStatusHint,
                           pathPrefix: worktreeShortName,
                           inputMode: composerInputMode,
                           providerLabel: providerLabel,
                           providerChosen: workspace.providerChosen,
                           slashCommands: slashCommands,
                           slashHighlight: slashHighlight,
                           providers: SessionProviderChoices.merge(launcher.providers),
                           pickingProvider: pickingProvider, enabled: !launcher.planning,
                           focusToken: composerFocus,
                           onShell: { composerAct(.plain) }, onResume: { composerAct(.command) },
                           onNew: { composerAct(.commandShift) }, onEscape: composerEscape,
                           onToggleMode: toggleComposerInputMode,
                           onMenuStep: stepSlashMenu, onMenuActivate: activateSlashMenu,
                           onHistoryBack: recallHistory,
                           onCommandHeld: { commandHeld = $0 },
                           onSendControl: forwardControlToShell,
                           shellTabEnabled: shellTabCompletionEnabled && composerInputMode == .shell,
                           onShellTab: forwardTabToShell,
                           onSlash: runSlash, onProvider: chooseProvider, onPickProvider: {
                               pickingProvider = true
                               if launcher.providers.isEmpty { launcher.refresh(workspace) }
                           })
            .onChange(of: workspace.draft) { _, _ in slashHighlight = 0; historyCursor = 0 }
    }

    private func toggleComposerInputMode() {
        guard !frozen else { return }
        if composerInputMode == .shell {
            composerInputMode = .agent
            focusComposer()
            if !workspace.providerChosen {
                pickingProvider = true
                if launcher.providers.isEmpty { launcher.refresh(workspace) }
            }
            // Mode chip is the signal — no notice spam.
            composerNotice = ""
        } else {
            composerInputMode = .shell
            pickingProvider = false
            focusComposer()
            composerNotice = ""
        }
    }

    private func nativeAgentInputStrip(for session: TerminalSession) -> some View {
        HStack {
            Text("键盘在 \(session.title) 的终端里 · ⌘↩ 回到输入条")
                .font(Face.sans(Metrics.caption)).foregroundStyle(Ink.inkMuted)
            Spacer()
            ConsoleToolbarAction(title: "返回 Shell", identifier: "console.returnToShell", perform: returnToShell)
                .font(Face.sans(Metrics.caption))
        }
        .padding(.horizontal, Metrics.gutter).padding(.vertical, 8)
        .background(Ink.canvas)
    }

    private func forwardControlToShell(_ byte: UInt8) {
        guard let shell = shellTarget(), shell.isActive else { return }
        shell.terminal.send(source: shell.terminal, data: [byte][...])
    }

    private var shellTabCompletionEnabled: Bool {
        guard selected?.kind == "terminal", !frozen else { return false }
        guard let shell = shellTarget(), shell.isActive else { return false }
        return shell.shellIntegrationEnabled && !shell.shellForegroundBusy
    }

    private func forwardTabToShell() {
        guard shellTabCompletionEnabled, let shell = shellTarget() else { return }
        let text = workspace.draft
        guard deliverTabCompletion(text, to: shell) else { return }
        composerNotice = ""
        scheduleShellTabReadBack(seed: text, session: shell)
    }

    private func scheduleShellTabReadBack(seed: String, session: TerminalSession) {
        shellTabReadGeneration += 1
        let generation = shellTabReadGeneration
        let delays: [TimeInterval] = [0.05, 0.12, 0.22]
        for delay in delays {
            DispatchQueue.main.asyncAfter(deadline: .now() + delay) {
                guard generation == shellTabReadGeneration else { return }
                guard shellTabCompletionEnabled, shellTarget()?.id == session.id else { return }
                let terminal = session.terminal.getTerminal()
                guard workspace.draft == seed else { return }
                guard let line = ShellPromptLineCapture.commandLine(atPrompt: terminal, seed: seed) else { return }
                guard line != seed else { return }
                workspace.draft = line
                composerFocus += 1
            }
        }
    }

    private func shellOwnsKeyboard() -> Bool {
        guard let shell = selected, shell.kind == "terminal" else { return false }
        return shell.activity.alternateScreen
    }

    private func stepSlashMenu(_ delta: Int) {
        let count = pickingProvider ? SessionProviderChoices.merge(launcher.providers).count : slashCommands.count
        guard count > 0 else { return }
        slashHighlight = (slashHighlight + delta + count * 8) % count
    }

    private func activateSlashMenu() {
        if pickingProvider {
            let rows = SessionProviderChoices.merge(launcher.providers)
            guard slashHighlight < rows.count else { return }
            chooseProvider(rows[slashHighlight].id)
            return
        }
        if case .command(let command, let argument) = SlashParser.parse(workspace.draft, context: composerSlashContext) {
            runSlashCommand(command, argument: argument, chord: .plain)
            if workspace.draft.hasPrefix("/") { workspace.draft = "" }
            return
        }
        let commands = slashCommands
        guard slashHighlight < commands.count else { return }
        runSlash(commands[slashHighlight])
    }

    private func recallHistory() {
        let entries = sessions.commandHistory(in: root).compactMap(\.block.command)
        guard !entries.isEmpty else { return }
        let index = min(historyCursor, entries.count - 1)
        workspace.draft = entries[index]
        historyCursor = min(historyCursor + 1, entries.count - 1)
    }
    private func focusComposer() {
        guard !frozen, selected != nil else { return }
        if selected?.kind != "terminal" {
            returnToShell()
        }
        creating = false
        switcherPresented = false
        switcherQuery = ""
        composerFocus += 1
        if launcher.providers.isEmpty { launcher.refresh(workspace) }
    }
    private func canUseComposerShortcut() -> Bool {
        guard !frozen, !switcherPresented, !creating, selected != nil else { return false }
        if selected?.kind != "terminal" { return true }
        if shellOwnsKeyboard() { return false }
        if selected?.terminal.window?.firstResponder is NSTextView { return false }
        return true
    }
    private func composerEscape() {
        if pickingProvider { pickingProvider = false; focusComposer(); return }
        if workspace.draft.trimmingCharacters(in: .whitespacesAndNewlines).hasPrefix("/"),
           !slashCommands.isEmpty {
            workspace.draft = ""
            focusComposer()
            return
        }
        if composerInputMode == .agent {
            composerInputMode = .shell
            composerNotice = ""
            focusComposer()
            return
        }
        // Stay on bottom input — Esc must not steal into the active-block PTY.
        focusComposer()
    }
    private func composerAct(_ chord: SessionComposerModel.Chord) {
        guard !frozen else { return }
        if launcher.planning { return }
        if launcher.loading {
            launcher.whenReady(workspace) { composerAct(chord) }
            return
        }
        if selected?.kind != "terminal" {
            if chord == .command { returnToShell(); focusComposer() }
            return
        }
        let mapped = ComposerConsoleBridge.chord(chord)
        let action = ComposerRouter.route(mapped, composerContext)
        if case .pickProvider = action { pendingChord = chord }
        applyComposer(action, chord: chord)
    }

    private func continuePendingChord() {
        guard let chord = pendingChord else { return }
        pendingChord = nil
        composerAct(chord)
    }
    private func attachShellCommandBlock(_ block: CommandBlock) {
        guard let session = scoped.first(where: { $0.commandBlocks.contains { $0.id == block.id } }) ?? shellTarget() else { return }
        guard let command = block.command else { return }
        shellAttachment = ComposerShellAttachment(
            shellTitle: session.title,
            command: command,
            exitCode: block.exitCode,
            interrupted: block.interrupted,
            output: block.capturedOutput ?? "")
        composerInputMode = .agent
        composerNotice = "已附上 Shell 输出，↩ 发给 Agent"
        focusComposer()
    }

    private func adoptAgentCommand(_ command: String) {
        workspace.draft = command
        composerNotice = "已放进输入条"
        focusComposer()
    }

    private func openGitPath(_ path: String) {
        let trimmed = path.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !trimmed.isEmpty else { return }
        NotificationCenter.default.post(name: .berthOpenGitPath,
                                        object: BerthGitPathRequest(worktree: root, path: trimmed))
        composerNotice = "正在 Git 中打开 \(trimmed)"
    }

    private func takeAgentTurnText() -> String {
        let payload = ComposerAttachmentFormat.agentMessage(text: workspace.draft, attachment: shellAttachment)
        workspace.draft = ""
        shellAttachment = nil
        return payload
    }

    private func applyComposer(_ action: ComposerAction, chord: SessionComposerModel.Chord = .plain) {
        switch action {
        case .none:
            break
        case .shell(let id, let text):
            guard let shell = scoped.first(where: { $0.id == id }) else {
                composerNotice = "没有正在运行的 Shell"
                return
            }
            guard deliver(text, to: shell) else { return }
            workspace.draft = ""
            pickingProvider = false
            composerNotice = ""
        case .shellBusy(let reason):
            composerNotice = reason
        case .openShellThenSend(let text):
            newShell()
            if let shell = shellTarget() { deliver(text, to: shell) }
            workspace.draft = ""
        case .agentSend(let id, _):
            let text = takeAgentTurnText()
            if let session = streamController.session(id) {
                do {
                    try streamController.sendTurn(id: id, text: text)
                    workspace.resumeStreamID = id
                    previewAgentID = id
                    workspace.resumeAgentID = nil
                    // Stay on unified block stream — Agent peer card shows status + prompt + result; 进入 opens detail.
                    focusStreamOnCanvas = false
                    pickingProvider = false
                    composerNotice = "已发送 · 块流里可见 \(session.title)"
                } catch {
                    composerNotice = error.localizedDescription
                }
                return
            }
        case .agentResumeThenSend(let id, _):
            let text = takeAgentTurnText()
            if streamController.session(id) != nil {
                resumeStreamSend(id: id, text: text)
                return
            }
            // Native Agent PTY ids are not ⌘↩ targets — stay on Shell stream and open a structured session.
            guard scoped.contains(where: { $0.id == id && $0.kind.hasPrefix("agent:") }) else { return }
            beginNewAgent(prompt: text)
        case .agentQueue(let id, _):
            let text = takeAgentTurnText()
            if streamController.session(id) != nil {
                streamController.queue(id: id, text: text)
                workspace.resumeStreamID = id
                composerNotice = "已记下，将在 \(streamController.session(id)?.title ?? "Agent") 当前轮结束后发送"
                return
            }
            workspace.resumeAgentID = id
            composerNotice = "已记下，将在 \(resumeTarget()?.title ?? "Agent") 当前轮结束后发送"
            workspace.draft = text
        case .agentPaste(_, let text):
            // Never switch to Pi/Agent PTY or dump into its terminal. Structured block only.
            beginNewAgent(prompt: text)
        case .agentNew(_, let firstTurn):
            pickingProvider = false
            beginNewAgent(prompt: firstTurn)
        case .pickProvider:
            pickingProvider = true
            composerNotice = "还没有默认 Agent。先选择，这段文字会保留。"
            if launcher.providers.isEmpty { launcher.refresh(workspace) }
        case .openTarget(let id):
            if streamController.session(id) != nil {
                // 「进入」 detail only — empty ⌘↩ must not leave the Shell block stream.
                selectStreamTarget(id)
                focusStreamOnCanvas = false
                return
            }
            // Ignore native Agent PTY ids as canvas targets (no Shell|Pi jump).
            if scoped.contains(where: { $0.id == id && $0.kind.hasPrefix("agent:") }) {
                composerNotice = "Agent 在块流里 · 输入后 ⌘↩ 发送"
                return
            }
        case .refuse(let message):
            composerNotice = message
        case .runSlash(let command, let argument):
            runSlashCommand(command, argument: argument, chord: chord)
        }
    }

    private func pickResumeAgent(_ query: String) {
        let needle = query.trimmingCharacters(in: .whitespacesAndNewlines).lowercased()
        guard !needle.isEmpty else { return }
        if let stream = streamController.sessions.first(where: {
            $0.title.localizedCaseInsensitiveContains(needle) || $0.provider.localizedCaseInsensitiveContains(needle)
        }) {
            selectStreamTarget(stream.id)
            workspace.draft = ""
            return
        }
        let matches = agentSessions.filter {
            $0.title.localizedCaseInsensitiveContains(needle)
                || ($0.kind.split(separator: ":").dropFirst().first.map(String.init) ?? "").localizedCaseInsensitiveContains(needle)
        }
        guard let match = matches.first else {
            composerNotice = "没有匹配的 Agent 会话"
            return
        }
        workspace.resumeAgentID = match.id
        previewAgentID = match.id
        workspace.draft = ""
        composerNotice = "⌘↩ 将续接到 \(match.title)"
    }
    private func runSlash(_ command: SlashCommand) {
        let argument = commandArgument(command.rawValue)
        runSlashCommand(command, argument: argument, chord: .plain)
        if workspace.draft.hasPrefix("/") { workspace.draft = "" }
    }

    private func runSlashCommand(_ command: SlashCommand, argument: String, chord: SessionComposerModel.Chord) {
        switch command {
        case .modeShell:
            // Mode only — draft body is the slash token; clearing it is expected. Non-slash draft never reaches here.
            composerInputMode = .shell
            pickingProvider = false
            composerNotice = ""
            focusComposer()
        case .modeAgent:
            composerInputMode = .agent
            composerNotice = ""
            if !workspace.providerChosen {
                pickingProvider = true
                if launcher.providers.isEmpty { launcher.refresh(workspace) }
            }
            focusComposer()
        case .agentSelect:
            if argument.isEmpty {
                pickingProvider = true
                composerNotice = "选择默认 Agent"
                if launcher.providers.isEmpty { launcher.refresh(workspace) }
            } else {
                chooseProvider(argument)
            }
        case .agentNew:
            if workspace.providerChosen { beginNewAgent(prompt: argument.isEmpty ? nil : argument) }
            else { pendingChord = .commandShift; pickingProvider = true }
        case .agent:
            if !argument.isEmpty { pickResumeAgent(argument) }
        case .shellNew:
            workspace.draft = ""
            newShell()
        case .rename:
            guard !argument.isEmpty, let selected else { return }
            do {
                try selected.rename(argument)
                workspace.draft = ""
                composerNotice = "已重命名为 \(selected.title)"
            } catch { composerNotice = error.localizedDescription }
        case .close:
            if let id = workspace.resumeStreamID ?? previewAgentID, streamController.session(id) != nil {
                confirmEndStream(id)
                return
            }
            guard let target = resumeTarget() ?? selected else { return }
            workspace.draft = ""
            if target.isActive { confirmSessionStop(target) }
            else { sessions.remove(target) }
        case .interrupt:
            if let id = workspace.resumeStreamID ?? previewAgentID, streamController.session(id) != nil {
                interruptStream(id)
            } else if resumeTarget() != nil {
                composerNotice = "终端 Agent 请在 TUI 内中断 / Interrupt in the terminal TUI"
            } else {
                composerNotice = "没有可中断的目标"
            }
        case .focus:
            guard let shell = shellTarget() else { return }
            workspace.draft = ""
            focusIntent = TerminalFocusIntent(shell.id)
            composerNotice = "键盘已交给 \(shell.title)"
        case .native:
            openStreamInNativeTerminal()
        case .resume:
            pickResumeFileAndLaunch()
        case .shell:
            guard !argument.isEmpty else {
                composerNotice = "用法：/shell 名称"
                return
            }
            if let match = shellSessions.first(where: {
                $0.title.localizedCaseInsensitiveContains(argument)
            }) {
                activate(match)
                workspace.draft = ""
                composerNotice = "已切换到 \(match.title)"
            } else {
                composerNotice = "找不到 Shell：\(argument)"
            }
        case .history:
            history = true
            workspace.draft = ""
        case .obServer:
            workspace.draft = ""
            openServicesPanel()
            composerNotice = "服务面板 · Esc 返回会话"
        case .git:
            workspace.draft = ""
            openGitPanel()
            composerNotice = "Git 面板 · Esc 返回会话"
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
        continuePendingChord()
    }
    private func beginNewAgent(prompt: String? = nil, providerID: String? = nil) {
        let targetID = providerID ?? workspace.providerID
        if providerID == nil {
            guard workspace.providerChosen else {
                pickingProvider = true
                composerNotice = "还没有默认 Agent。先选择，这段文字会保留。"
                if launcher.providers.isEmpty { launcher.refresh(workspace) }
                return
            }
        }
        let body = prompt ?? workspace.draft
        let payload = ComposerAttachmentFormat.agentMessage(text: body, attachment: shellAttachment)
        workspace.draft = ""
        shellAttachment = nil
        launcher.whenReady(workspace) {
            guard let provider = launcher.providers.first(where: { $0.id == targetID }),
                  provider.installed == true else {
                composerNotice = launcher.problem ?? "所选 Agent 未安装或还不能启动"
                return
            }
            let trimmed = payload.trimmingCharacters(in: .whitespacesAndNewlines)
            guard let binary = DaemonLaunch.binaryPath() else { return }
            if provider.supportsStream {
                guard streamController.slotAvailable(agentPTYCount: agentSessions.count) else {
                    composerNotice = "会话已达上限（\(AgentStreams.maxSessions)）"
                    return
                }
                let title = "\(provider.name) \(streamController.sessions.count + 1) · 流"
                do {
                    let session = try streamController.launch(
                        binary: binary, provider: provider, root: root, title: title,
                        environment: TerminalSession.environment(),
                        firstTurn: trimmed.isEmpty ? nil : payload)
                    workspace.resumeStreamID = session.id
                    previewAgentID = session.id
                    workspace.resumeAgentID = nil
                    focusStreamOnCanvas = false
                    if !trimmed.isEmpty { workspace.draft = "" }
                    composerNotice = "已新建 \(session.title) · 块流里可见"
                } catch {
                    composerNotice = error.localizedDescription
                }
                return
            }
            if !trimmed.isEmpty { workspace.draft = payload }
            launcher.launch(root: root, workspace: workspace, prompt: !trimmed.isEmpty) { session in
                workspace.resumeAgentID = session.id
                previewAgentID = session.id
                composerNotice = "已新建 \(session.title)。⌘↩ 续接这一会话"
            }
        }
    }
    private func selectStreamTarget(_ id: UUID) {
        workspace.resumeStreamID = id
        previewAgentID = id
        focusStreamOnCanvas = true
        streamController.markDetailVisible(id)
        composerNotice = "⌘↩ 将续接到 \(streamController.session(id)?.title ?? "Agent")"
    }

    private func interruptStream(_ id: UUID) {
        do {
            try streamController.interrupt(id: id)
            composerNotice = "已请求中断当前轮次"
        } catch {
            composerNotice = error.localizedDescription
        }
    }

    private func confirmEndStream(_ id: UUID) {
        if ProcessInfo.processInfo.environment["BERTH_CONSOLE_TEST"] == "1" {
            endStream(id)
            return
        }
        let title = streamController.session(id)?.title ?? "Agent"
        let alert = ClientAlert.make()
        alert.messageText = "结束 \(title)？"
        alert.informativeText = "结构化桥会请求关闭 provider 会话；排队中的文字会放回输入条。"
        alert.addButton(withTitle: "取消")
        alert.addButton(withTitle: "结束")
        if alert.runModal() == .alertSecondButtonReturn { endStream(id) }
    }

    private func endStream(_ id: UUID) {
        let queued = streamController.drainQueuedText(id: id)
        streamController.end(id)
        if workspace.resumeStreamID == id { workspace.resumeStreamID = nil }
        if previewAgentID == id { previewAgentID = nil }
        if let queued, !queued.isEmpty {
            workspace.draft = workspace.draft.isEmpty ? queued : workspace.draft + "\n\n" + queued
            composerNotice = "结构化会话已结束，排队内容已放回输入条"
        } else {
            composerNotice = "结构化会话已结束"
        }
    }

    private func respondStream(_ id: UUID, requestID: String, decision: AgentDecision) {
        do {
            try streamController.respond(id: id, requestID: requestID, decision: decision)
        } catch {
            composerNotice = error.localizedDescription
        }
    }

    private func launchNativeAgent(prompt: String?, resumeFile: String?) {
        if let resumeFile, let message = streamController.sessionFileConflictMessage(for: resumeFile) {
            composerNotice = message
            return
        }
        let trimmed = prompt?.trimmingCharacters(in: .whitespacesAndNewlines) ?? ""
        if !trimmed.isEmpty { workspace.draft = trimmed }
        launcher.launch(root: root, workspace: workspace, prompt: !trimmed.isEmpty, resumeFile: resumeFile) { session in
            workspace.resumeAgentID = session.id
            previewAgentID = session.id
            workspace.resumeStreamID = nil
            composerNotice = resumeFile == nil
                ? "已在原生终端打开 \(session.title)"
                : "已从会话文件在原生终端续接 \(session.title)"
            activate(session)
        }
    }

    private func openNativeTerminalFresh() {
        guard workspace.providerChosen else {
            pickingProvider = true
            composerNotice = "先选择默认 Agent"
            if launcher.providers.isEmpty { launcher.refresh(workspace) }
            return
        }
        launcher.whenReady(workspace) {
            guard launcher.available(workspace) else {
                composerNotice = launcher.problem ?? "所选 Agent 未安装或还不能启动"
                return
            }
            launchNativeAgent(prompt: nil, resumeFile: nil)
        }
    }

    private func pickResumeFileAndLaunch() {
        guard workspace.providerChosen else {
            pickingProvider = true
            composerNotice = "先选择默认 Agent"
            return
        }
        guard launcher.provider(workspace)?.supportsResume == true else {
            composerNotice = "当前默认 Agent 不支持从会话文件续接"
            return
        }
        launcher.whenReady(workspace) {
            guard launcher.available(workspace) else {
                composerNotice = launcher.problem ?? "所选 Agent 未安装或还不能启动"
                return
            }
            if ProcessInfo.processInfo.environment["BERTH_CONSOLE_TEST"] == "1",
               let testPath = ProcessInfo.processInfo.environment["BERTH_TEST_RESUME_FILE"] {
                launchNativeAgent(prompt: nil, resumeFile: testPath)
                return
            }
            let panel = NSOpenPanel()
            panel.canChooseFiles = true
            panel.canChooseDirectories = false
            panel.allowsMultipleSelection = false
            panel.prompt = "校验并续接"
            panel.message = "只续接当前 worktree 的原始会话文件。"
            guard panel.runModal() == .OK, let file = panel.url else { return }
            launchNativeAgent(prompt: nil, resumeFile: file.path)
        }
    }

    private func openStreamInNativeTerminal() {
        guard let id = workspace.resumeStreamID ?? previewAgentID,
              let session = streamController.session(id) else {
            composerNotice = "先选择一个结构化 Agent 会话"
            return
        }
        guard let resumeFile = session.snapshot.reference?.sessionFile else {
            composerNotice = "此会话没有 provider 会话文件，无法原生续接"
            return
        }
        if case .ready = session.snapshot.phase,
           session.snapshot.turns.last?.outcome == .running {
            composerNotice = "请先中断当前轮次，再在原生终端中继续"
            return
        }
        endStream(id)
        launchNativeAgent(prompt: nil, resumeFile: resumeFile)
    }

    private func resumeStreamSend(id: UUID, text: String) {
        guard let provider = launcher.provider(workspace), let binary = DaemonLaunch.binaryPath() else { return }
        let session = streamController.session(id)
        if case .ended = session?.snapshot.phase {
            do {
                let reopened = try streamController.resumeEndedSession(
                    binary: binary, provider: provider, root: root, id: id,
                    environment: TerminalSession.environment(), firstTurn: text)
                workspace.resumeStreamID = reopened.id
                workspace.draft = ""
                pickingProvider = false
                composerNotice = "已续接并发送到 \(reopened.title)"
            } catch {
                composerNotice = error.localizedDescription
            }
            return
        }
        do {
            try streamController.sendTurn(id: id, text: text)
            workspace.resumeStreamID = id
            previewAgentID = id
            workspace.resumeAgentID = nil
            focusStreamOnCanvas = false
            workspace.draft = ""
            pickingProvider = false
            composerNotice = "已发送 · 块流里可见 \(session?.title ?? "Agent")"
        } catch {
            composerNotice = error.localizedDescription
        }
    }

    private func enterAgent(_ session: TerminalSession) {
        workspace.resumeAgentID = session.id
        previewAgentID = session.id
        focusStreamOnCanvas = false
        session.clearAttention()
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
    private func deliverTabCompletion(_ text: String, to session: TerminalSession) -> Bool {
        let encoded = SessionPTYInput.encodeShellTabCompletion(text)
        guard let bytes = encoded.bytes else {
            composerNotice = encoded.error ?? "这段文字不能送入会话"
            return false
        }
        guard session.isActive else {
            composerNotice = "会话未在运行"
            return false
        }
        session.terminal.send(source: session.terminal, data: bytes[...])
        return true
    }

    private func deliver(_ text: String, to session: TerminalSession) -> Bool {
        let bracketed = session.terminal.getTerminal().bracketedPasteMode
        let encoded = SessionPTYInput.encodeShellLine(text, bracketedPaste: bracketed)
        guard let bytes = encoded.bytes else {
            composerNotice = encoded.error ?? "这段文字不能送入会话"
            return false
        }
        guard session.isActive else {
            composerNotice = "会话未在运行"
            return false
        }
        session.terminal.send(source: session.terminal, data: bytes[...])
        return true
    }

    @discardableResult
    private func pasteAgent(_ text: String, to session: TerminalSession) -> Bool {
        let bracketed = session.terminal.getTerminal().bracketedPasteMode
        let encoded = SessionPTYInput.encodeAgentPaste(text, bracketedPaste: bracketed)
        guard let bytes = encoded.bytes else {
            composerNotice = encoded.error ?? "这段文字不能送入会话"
            return false
        }
        guard session.isActive else {
            composerNotice = "会话未在运行"
            return false
        }
        session.terminal.send(source: session.terminal, data: bytes[...])
        return true
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
                arguments: ["-i"], shellIntegration: settings.shellIntegration && ShellIntegrationLease.supports(executable: TerminalSession.shell),
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
        // Selected tab reads stronger than neighbors; continuous corners match toolbar chips.
        button.wantsLayer = true
        button.layer?.cornerRadius = 6
        button.layer?.cornerCurve = .continuous
        button.layer?.backgroundColor = selected ? NSColor(Ink.accentSoft).cgColor : NSColor.clear.cgColor
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
