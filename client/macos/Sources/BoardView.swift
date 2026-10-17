import AppKit
import SwiftUI
import BerthTerminal

/// The project views: declared services, their runtime facts, and read-only Git context.
/// There is no machine-wide port scope. A port only appears inside the
/// worktree the daemon attributed it to.
enum Scope: Hashable {
    case services(String)
    case code(String)
    case terminal(String)
    case console(String)

    var title: String {
        switch self {
        case .console: return "会话 / Sessions"
        case .services(let name), .code(let name), .terminal(let name):
            return name.isEmpty ? "项目" : name
        }
    }

    var projectName: String? {
        switch self {
        case .console: return nil
        case .services(let name), .code(let name), .terminal(let name):
            return name.isEmpty ? nil : name
        }
    }

    init(argument: String) {
        let parts = argument.split(separator: ":", maxSplits: 1).map(String.init)
        guard parts.count == 2 else {
            self = .services("")
            return
        }
        switch parts[0] {
        case "terminal": self = .terminal(parts[1])
        case "code", "git": self = .code(parts[1])
        case "ports", "project": self = .services(parts[1])
        case "services": self = .services(parts[1])
        default: self = .services("")
        }
    }
}

struct BoardView: View {
    @ObservedObject var store: BoardStore
    @ObservedObject var services: ServicesStore
    @ObservedObject private var settings: UISettings
    @StateObject private var git: GitStore
    @ObservedObject private var views: ViewState

    private var railWidth: Binding<Double> {
        Binding(get: { settings.sidebarWidth }, set: { settings.sidebarWidth = $0 })
    }
    static let railRange: ClosedRange<Double> = 140...320
    var scrolls: Bool = true
    private let automaticGuide: Bool
    private let guideDefaults: UserDefaults
    private let initialConsoleAgent: Bool
    private let performAction: (WorkspaceAction) -> Void

    init(
        store: BoardStore,
        services: ServicesStore,
        git: GitStore? = nil,
        scrolls: Bool = true,
        initialScope: Scope = .services(""),
        initialConsoleAgent: Bool = false,
        automaticGuide: Bool = false,
        guideDefaults: UserDefaults = .standard,
        views: ViewState? = nil,
        settings: UISettings = .shared,
        performAction: @escaping (WorkspaceAction) -> Void = { _ in }
    ) {
        self.automaticGuide = automaticGuide
        self.guideDefaults = guideDefaults
        self.performAction = performAction
        self.store = store
        self.services = services
        _settings = ObservedObject(wrappedValue: settings)
        _git = StateObject(wrappedValue: git ?? GitStore())
        self.scrolls = scrolls
        self.initialConsoleAgent = initialConsoleAgent
        _views = ObservedObject(wrappedValue: views ?? ViewState(scope: initialScope))
    }

    @Environment(\.accessibilityReduceMotion) private var reduce
    @State private var pendingAction: WorkspaceAction?
    @StateObject private var commandDelivery = WorkspaceCommandDelivery()
    @State private var proposal: GroupInitResult?
    @State private var proposalProblem: String?
    @State private var editingConfig: PendingConfig?
    @State private var configProblem: String?
    @State private var removing: BerthGroup?
    @State private var problem: String?
    @ObservedObject private var shortcuts = WorkspaceShortcuts.shared
    @ObservedObject private var terminalSessions = TerminalSessions.shared
    @ObservedObject private var recovery = WorkspaceRecovery.shared

    private var scope: Scope { views.scope }
    private var projects: [BerthGroup] { views.orderedProjects(services.projects, filtered: false) }
    private var visibleProjects: [BerthGroup] { views.orderedProjects(services.projects, filtered: scrolls) }
    private var projectDialogPresented: Bool { proposal != nil || editingConfig != nil || removing != nil || problem != nil }
    private var selected: BerthGroup? {
        guard let name = scope.projectName else { return nil }
        return projects.first { $0.name == name }
    }

    /// HIG toolbars: window title is the current document / project, never the app name.
    private var windowTitle: String {
        if let selected { return selected.displayName }
        if case .console(let root) = scope {
            let short = root.replacingOccurrences(
                of: FileManager.default.homeDirectoryForCurrentUser.path, with: "~")
            return short.isEmpty ? "独立会话" : short
        }
        return "option-berth"
    }

    var body: some View {
        shell
        .background {
            if scrolls {
                WorkspaceCommandDeliveryAnchor(delivery: commandDelivery).frame(width: 0, height: 0)
                WorkspaceMenuRefresh(bindings: shortcuts.bindings, worktrees: visibleProjects.map(\.name), allowed: views.allowsCommands)
                    .frame(width: 0, height: 0).allowsHitTesting(false)
            }
        }
        .preferredColorScheme(settings.colorScheme)
        .clientWindowChrome(vibrancyBehindWindow: scrolls)
        .background {
            if scrolls {
                WindowTitleBinder(title: windowTitle)
                    .frame(width: 0, height: 0)
                    .allowsHitTesting(false)
            }
        }
        .disabled(views.showingGuide)
        .accessibilityHidden(views.showingGuide)
        .overlayPreferenceValue(TourAnchors.self) { anchors in
            GeometryReader { geometry in
                if views.showingGuide {
                    WorkspaceTour(navigation: views, frames: anchors.mapValues { geometry[$0] },
                                  size: geometry.size, frozen: !scrolls)
                }
            }
        }
        .onReceive(NotificationCenter.default.publisher(for: .berthOpenGitPath)) { notification in
            guard scrolls, let request = notification.object as? BerthGitPathRequest else { return }
            let target = URL(fileURLWithPath: request.worktree).standardizedFileURL.resolvingSymlinksInPath().path
            guard let project = services.projects.first(where: {
                guard let root = $0.rootDir else { return false }
                return URL(fileURLWithPath: root).standardizedFileURL.resolvingSymlinksInPath().path == target
            }) else { return }
            views.scope = .code(project.name)
            git.openPath(request.path, project: project)
        }
        .sheet(isPresented: $views.showingUpdates) { ReleaseUpdateSheet().clientWindowChrome() }
        .sheet(isPresented: $views.showingRecovery) {
            WorkspaceRecoverySheet(recovery: .shared) { root in views.scope = .console(root) }
                .clientWindowChrome()
        }
        .sheet(isPresented: $views.showingActions, onDismiss: {
            guard let action = pendingAction else { return }
            pendingAction = nil
            commandDelivery.submit(action, perform: performAction)
        }) {
            WorkspaceActionPanel(perform: { pendingAction = $0 }, shortcuts: .shared)
                .clientWindowChrome()
        }
        .sheet(isPresented: $views.showingSessions) {
            SessionManager { session in
                TerminalSessions.shared.select(session)
                if let project = projects.first(where: { project in
                    guard let root = project.rootDir else { return false }
                    return TerminalSessions.shared.inWorktree(root).contains(where: { $0.id == session.id })
                }) { views.scope = .terminal(project.name) }
                else { views.scope = .console(session.worktree) }
            }.clientWindowChrome()
        }
        .sheet(item: $proposal) { result in
            AddProjectSheet(result: result, problem: proposalProblem,
                            scrolls: scrolls,
                            onWrite: { writeProposal(result, yaml: $0) },
                            onCancel: { proposal = nil; proposalProblem = nil })
                .clientWindowChrome()
        }
        .sheet(item: $editingConfig) { pending in
            AddProjectSheet(result: GroupInitResult(path: pending.path, yaml: pending.yaml,
                                                     proposal: pending.project),
                            problem: configProblem, mode: .edit,
                            scrolls: scrolls,
                            onWrite: { saveConfig(pending, yaml: $0) },
                            onCancel: { editingConfig = nil; configProblem = nil })
                .clientWindowChrome()
        }
        .sheet(isPresented: $views.showingSettings) {
            SettingsSheet(settings: settings, onClose: { views.showingSettings = false })
                .clientWindowChrome()
        }
        .alert("移除项目", isPresented: Binding(
            get: { removing != nil }, set: { if !$0 { removing = nil } })) {
            Button("取消", role: .cancel) { removing = nil }
            Button("删除清单", role: .destructive) { removeProject() }
        } message: {
            Text("这会删除项目里的 oberth.yaml 和本机登记。")
        }
        .alert("发生了问题", isPresented: Binding(
            get: { problem != nil }, set: { if !$0 { problem = nil } })) {
            Button("知道了") { problem = nil }
        } message: {
            Text(problem ?? "")
        }
        .onAppear {
            if scrolls {
                if automaticGuide && TourFirstUse.claim(defaults: guideDefaults) { views.showingGuide = true }
                refreshGit(force: true)
            }
        }
        .onChange(of: views.connectionRequest) { _, _ in if scrolls { addProject() } }
        .onChange(of: projectDialogPresented) { _, shown in views.projectDialogPresented = shown }
        .onChange(of: views.showingGuide) { _, shown in
            if shown { views.guideTarget = .connect }
        }
        .onDisappear { commandDelivery.cancel(); if scrolls { views.showingGuide = false } }
        .onChange(of: services.updatedAt) { _, _ in
            refreshGit()
        }
        .onChange(of: scope) { _, newScope in
            refreshGit(force: true)
        }
    }

    /// Split column shell: sidebar List + detail, system `.toolbar` on live
    /// windows. ImageRenderer freezes mirror the same columns (no AppKit List /
    /// segmented control — those paint blank offscreen).
    /// `railVisible` shows/hides the sidebar column (⌘B).
    @ViewBuilder private var shell: some View {
        HStack(spacing: 0) {
            if views.railVisible {
                sidebar
                    .frame(width: scrolls ? settings.sidebarWidth : Double(Metrics.railWidth),
                           alignment: .leading)
                    .clipped()
                if scrolls {
                    SplitHandle(width: railWidth, range: Self.railRange)
                } else {
                    Hairline(axis: .vertical)
                }
            }
            detailStack
        }
        .background(Ink.canvas)
        .toolbar { if scrolls { workspaceToolbarItems } }
    }

    private var projectSelection: Binding<String?> {
        Binding(
            get: { scope.projectName },
            set: { name in
                guard let name else { return }
                withAnimation(Motion.selection(reduced: reduce)) { views.selectProject(name) }
            }
        )
    }

    private var detailStack: some View {
        content
            .frame(maxWidth: .infinity, maxHeight: .infinity, alignment: .topLeading)
            .background(Ink.canvas)
    }

    @ToolbarContentBuilder private var workspaceToolbarItems: some ToolbarContent {
        // HIG: leading edge — show/hide sidebar (familiar, always available).
        ToolbarItem(placement: .navigation) {
            Button {
                withAnimation(Motion.selection(reduced: reduce)) {
                    views.railVisible.toggle()
                }
            } label: {
                Label(views.railVisible ? "隐藏侧栏" : "显示侧栏",
                      systemImage: "sidebar.leading")
            }
            .help("显示或隐藏侧栏 / Toggle sidebar")
            .accessibilityIdentifier("workspace.toolbar.sidebar")
        }

        // HIG: trailing — commands / sessions / recovery / more, icons + Label a11y.
        ToolbarItemGroup(placement: .primaryAction) {
            Button { views.showingActions = true } label: {
                Label("命令", systemImage: "magnifyingglass")
            }
            .help("搜索工作区动作 / Command panel")
            .accessibilityIdentifier("workspace.toolbar.commands")
            .tourAnchor(.commands)

            Button { views.showingSessions = true } label: {
                Label("会话 \(scrolls ? terminalSessions.sessions.count : 0)",
                      systemImage: "rectangle.on.rectangle")
            }
            .help("会话管理 / Sessions")
            .accessibilityIdentifier("workspace.toolbar.sessions")
            .tourAnchor(.sessions)

            Button { views.showingRecovery = true } label: {
                Label(scrolls && recovery.reviewPending ? "恢复 · 待确认" : "恢复",
                      systemImage: "clock.arrow.circlepath")
            }
            .help("恢复建议 / Recovery")
            .accessibilityIdentifier("workspace.toolbar.recovery")
            .tourAnchor(.recovery)

            Menu {
                Button("检查更新… / Check for updates…") { views.showingUpdates = true }
                Button("设置… / Settings…") { views.showingSettings = true }
                Button("使用指引… / Getting started…") { views.showingGuide = true }
            } label: {
                Label("更多", systemImage: "ellipsis.circle")
            }
            .accessibilityLabel("更多工作区操作")
            .accessibilityIdentifier("workspace.toolbar.more")
            .tourAnchor(.more)
        }
    }

    private func refreshGit(force: Bool = false) {
        guard scrolls else { return }
        if case .code = scope {
            git.loadTree(project: selected, force: force)
        } else {
            git.refresh(project: selected, force: force)
        }
    }

    private var sidebar: some View {
        VStack(spacing: 0) {
            if scrolls {
                sidebarFilter
            }
            if projects.isEmpty {
                sidebarEmpty
            } else if scrolls {
                List(selection: projectSelection) {
                    Section {
                        ForEach(Array(visibleProjects.enumerated()), id: \.element.id) { index, project in
                            projectRow(project, ordinal: index + 1)
                                .tag(project.name)
                                .listRowInsets(EdgeInsets(
                                    top: Metrics.unit,
                                    leading: Metrics.gutter,
                                    bottom: Metrics.unit,
                                    trailing: Metrics.gutter
                                ))
                        }
                    } header: {
                        HStack(spacing: Metrics.inline) {
                            Text("Worktrees")
                                .font(Face.sans(Metrics.meta, .semibold))
                                .foregroundStyle(Ink.inkMuted)
                                .textCase(nil)
                            Spacer(minLength: 0)
                            Button {
                                views.requestConnection()
                            } label: {
                                Image(systemName: "plus")
                                    .font(.system(size: 11, weight: .semibold))
                            }
                            .buttonStyle(.borderless)
                            .help("接入一个 worktree")
                            .tourAnchor(.connect)
                        }
                        .textCase(nil)
                    }
                }
                .listStyle(.sidebar)
                .scrollContentBackground(.hidden)
            } else {
                // ImageRenderer cannot paint List(.sidebar); mirror density with plain rows.
                VStack(alignment: .leading, spacing: 0) {
                    HStack {
                        Text("Worktrees")
                            .font(Face.sans(Metrics.meta, .semibold))
                            .foregroundStyle(Ink.inkMuted)
                        Spacer(minLength: 0)
                        Image(systemName: "plus")
                            .font(.system(size: 11, weight: .semibold))
                            .foregroundStyle(Ink.accent)
                            .tourAnchor(.connect)
                    }
                    .padding(.horizontal, Metrics.gutter)
                    .padding(.vertical, Metrics.tabPadV)
                    ForEach(Array(visibleProjects.enumerated()), id: \.element.id) { index, project in
                        let selected = scope.projectName == project.name
                        projectRow(project, ordinal: index + 1)
                            .padding(.horizontal, Metrics.gutter)
                            .padding(.vertical, Metrics.unit)
                            .background(selected ? Ink.accentSoft : Color.clear)
                            .clipShape(RoundedRectangle(cornerRadius: 6, style: .continuous))
                            .padding(.horizontal, Metrics.unit)
                    }
                    Spacer(minLength: 0)
                }
                .frame(maxWidth: .infinity, maxHeight: .infinity, alignment: .topLeading)
            }
            sidebarFooter
        }
        .tourAnchor(.worktrees)
        // Live: system sidebar material + ~8% Paper canvas wash so Paper world survives
        // (HIG materials: glass on chrome only; content stays opaque).
        .background {
            if scrolls {
                ZStack {
                    SidebarMaterial()
                    Ink.canvas.opacity(0.08)
                }
            } else {
                Ink.surface
            }
        }
    }

    /// Leading magnifyingglass field — not a decorative Spotlight capsule (tells / quieter).
    private var sidebarFilter: some View {
        HStack(spacing: Metrics.inline) {
            Image(systemName: "magnifyingglass")
                .font(.system(size: 11, weight: .medium))
                .foregroundStyle(Ink.inkFaint)
                .accessibilityHidden(true)
            TextField("筛选 worktree", text: $views.projectQuery)
                .textFieldStyle(.plain)
                .font(Face.sans(Metrics.chrome))
                .foregroundStyle(Ink.ink)
        }
        .padding(.horizontal, Metrics.gutter)
        .padding(.vertical, Metrics.controlPadV + 1)
        .background(
            RoundedRectangle(cornerRadius: 6, style: .continuous)
                .fill(Ink.sunken.opacity(0.55))
        )
        .padding(.horizontal, Metrics.gutter)
        .padding(.top, Metrics.gutter)
        .padding(.bottom, Metrics.unit)
    }

    private var sidebarEmpty: some View {
        VStack(alignment: .leading, spacing: Metrics.inline) {
            Image(systemName: "shippingbox")
                .font(.system(size: 22, weight: .regular))
                .foregroundStyle(Ink.inkFaint)
                .accessibilityHidden(true)
            Text("还没有清单项目")
                .font(Face.sans(Metrics.chrome, .medium))
                .foregroundStyle(Ink.ink)
            Text("接入一个目录，或者先运行 oberth init。侧栏只列有 oberth.yaml 的 worktree。")
                .font(Face.sans(Metrics.meta))
                .foregroundStyle(Ink.inkFaint)
                .fixedSize(horizontal: false, vertical: true)
            Button("接入…") { views.requestConnection() }
                .buttonStyle(.borderless)
                .foregroundStyle(Ink.accent)
                .help("接入一个 worktree")
                .tourAnchor(.connect)
        }
        .padding(Metrics.gutter)
        .frame(maxWidth: .infinity, maxHeight: .infinity, alignment: .topLeading)
    }

    /// Useful relative time when the store has refreshed — not a decorative stamp.
    private var sidebarFooter: some View {
        Group {
            if let updated = services.updatedAt {
                Text("已更新 · \(TimeText.relative(updated))")
                    .font(Face.sans(Metrics.caption))
                    .foregroundStyle(Ink.inkFaint)
                    .padding(.horizontal, Metrics.gutter)
                    .padding(.vertical, Metrics.gutter)
                    .frame(maxWidth: .infinity, alignment: .leading)
            }
        }
    }

    @ViewBuilder
    private func projectRow(_ project: BerthGroup, ordinal: Int) -> some View {
        let live = services.liveCount(in: project)
        HStack(spacing: Metrics.inline) {
            StatusDot(tone: live > 0 ? Ink.live : Ink.dormant)
            VStack(alignment: .leading, spacing: Metrics.stack) {
                Text(project.displayName)
                    .font(Face.sans(Metrics.row, .medium))
                    .foregroundStyle(Ink.ink)
                    .lineLimit(1)
                Text(project.branch.isEmpty ? shortPath(project.rootDir ?? "") : project.branch)
                    .font(Face.mono(Metrics.caption))
                    .foregroundStyle(Ink.inkFaint)
                    .lineLimit(1)
                    .truncationMode(.middle)
            }
            Spacer(minLength: Metrics.unit)
            VStack(alignment: .trailing, spacing: Metrics.stack) {
                Text(verbatim: "\(live)/\(project.services.count)")
                    .font(Face.mono(Metrics.meta))
                    .foregroundStyle(Ink.inkMuted)
                if let action = WorkspaceAction(rawValue: "worktree.\(ordinal)") {
                    Text(scrolls ? shortcuts.shortcut(action).label : action.defaultShortcut.label)
                        .font(Face.mono(Metrics.caption))
                        .foregroundStyle(Ink.inkFaint)
                }
            }
        }
        .frame(minHeight: Metrics.sidebarRow - Metrics.unit)
        .contentShape(Rectangle())
        .onTapGesture {
            withAnimation(Motion.selection(reduced: reduce)) { views.selectProject(project.name) }
        }
    }

    @ViewBuilder private var content: some View {
        if case .console(let root) = scope {
            VStack(alignment: .leading, spacing: 0) {
                HStack {
                    Text("独立会话 / Standalone sessions").font(Face.sans(Metrics.row, .semibold))
                    Spacer()
                    Button("全部会话 / All sessions") { views.showingSessions = true }
                }.padding(.horizontal, Metrics.gutter).padding(.vertical, Metrics.headerPad)
                Text(root).font(Face.mono(Metrics.meta)).lineLimit(1).truncationMode(.middle).padding(.horizontal, Metrics.gutter)
                Text("未关联已登记清单；会话不代表项目服务状态。 / No registered manifest is associated.")
                    .font(Face.sans(Metrics.meta)).foregroundStyle(Ink.inkMuted)
                    .padding(.horizontal, Metrics.gutter).padding(.vertical, Metrics.headerPad)
                WorkspaceConsole(root: root, frozen: !scrolls).id(root)
            }
        } else if let selected { projectContent(selected) }
        else { emptyContent }
    }

    private var emptyContent: some View {
        VStack(alignment: .leading, spacing: Metrics.section) {
            Image(systemName: "rectangle.split.2x1")
                .font(.system(size: 28, weight: .regular))
                .foregroundStyle(Ink.inkFaint)
                .accessibilityHidden(true)
            Text("选择一个 worktree")
                .font(Face.sans(Metrics.display + 2, .semibold))
                .foregroundStyle(Ink.ink)
            Text("从左栏点一个项目开始。状态只从清单、运行记录和运行实况组成；没有 oberth.yaml 的目录不会出现在这里。")
                .font(Face.sans(Metrics.chrome))
                .foregroundStyle(Ink.inkFaint)
                .fixedSize(horizontal: false, vertical: true)
            if scrolls {
                Button("使用指引 / Getting started") { views.showingGuide = true }
                    .accessibilityIdentifier("workspace.guide")
                SheetButton(title: "接入项目", primary: true, action: { views.requestConnection() }).tourAnchor(.connect)
                Button("打开会话管理 / Open session manager") { views.showingSessions = true }
            } else {
                // Frozen captures cannot render AppKit-backed buttons. Keep
                // inert labels here; real controls are exercised in NSWindow.
                Text("使用指引 / Getting started").font(Face.sans(Metrics.chrome)).foregroundStyle(Ink.accent)
                SheetButton(title: "接入项目", primary: true, action: {}).tourAnchor(.connect)
                Text("打开会话管理 / Open session manager").font(Face.sans(Metrics.chrome)).foregroundStyle(Ink.inkMuted)
            }
        }
        .frame(maxWidth: 460, alignment: .leading)
        .padding(Metrics.panelPad + Metrics.unit)
        .frame(maxWidth: .infinity, maxHeight: .infinity, alignment: .topLeading)
    }

    private func projectContent(_ project: BerthGroup) -> some View {
        VStack(spacing: 0) {
            ProjectDocumentHeader(
                displayName: project.displayName,
                liveCount: services.liveCount(in: project),
                serviceCount: project.services.count,
                branch: project.branch,
                path: shortPath(project.rootDir ?? ""),
                isWorking: services.isWorking(project)
            ) {
                projectMenu(project)
            }

            // Stage craft v9: face strip as a calm tool band — recessed
            // segmented control on surface, strong rule into the operate stage.
            HStack(alignment: .center, spacing: Metrics.inline) {
                ProjectFacePicker(
                    projectName: project.name,
                    scope: $views.scope,
                    gitChanged: git.tree?.files.count ?? 0,
                    frozen: !scrolls,
                    servicesHelp: shortcuts.shortcut(.services).label,
                    codeHelp: shortcuts.shortcut(.code).label,
                    terminalHelp: shortcuts.shortcut(.terminal).label
                )
                Spacer(minLength: 0)
            }
            .padding(.horizontal, Metrics.gutter)
            .padding(.vertical, Metrics.unit + 2)
            .frame(maxWidth: .infinity, alignment: .leading)
            .background(Ink.surface)
            .overlay(alignment: .bottom) {
                Rectangle().fill(Ink.lineStrong).frame(height: 1)
            }

            switch scope {
            case .services:
                ServicesView(store: services, group: project,
                             ports: ports(for: project), scrolls: scrolls)
            case .code:
                CodeView(git: git, project: project, scrolls: scrolls)
            case .terminal, .console:
                WorkspaceConsole(root: project.rootDir ?? "", frozen: !scrolls, initialAgent: initialConsoleAgent)
                    .id(project.rootDir ?? project.name)
            }
        }
        .frame(maxWidth: .infinity, maxHeight: .infinity, alignment: .topLeading)
    }

    @ViewBuilder
    private func projectMenu(_ project: BerthGroup) -> some View {
        if !scrolls {
            Label("项目", systemImage: "slider.horizontal.3")
                .font(Face.sans(Metrics.chrome))
                .foregroundStyle(Ink.inkMuted)
                .labelStyle(.iconOnly)
        } else {
            Menu {
                Button("编辑清单…") { openConfig(project) }
                if !project.services.isEmpty {
                    Divider()
                    Button("启动全部服务") { services.start(project) }
                    Button("停止项目服务") { services.stop(project) }
                        .disabled(services.stoppableCount(in: project) == 0)
                }
                Divider()
                Button("移除项目…", role: .destructive) { removing = project }
            } label: {
                Image(systemName: "ellipsis.circle")
                    .font(.system(size: 13, weight: .regular))
                    .foregroundStyle(Ink.inkMuted)
                    .frame(width: Metrics.hit, height: Metrics.hit)
                    .contentShape(Rectangle())
            }
            .menuStyle(.borderlessButton)
            .help("项目操作")
            .fixedSize()
        }
    }

    private func ports(for project: BerthGroup) -> [Port] {
        store.ports.filter { port in
            if port.group == project.name { return true }
            return port.group == nil && port.projectRoot == project.rootDir
        }.sorted { a, b in
            a.port == b.port ? a.bindAddress < b.bindAddress : a.port < b.port
        }
    }

    private func addProject() {
        guard views.allowsCommands else { return }
        views.projectOperationPending = true
        let panel = NSOpenPanel()
        panel.canChooseDirectories = true
        panel.canChooseFiles = false
        panel.allowsMultipleSelection = false
        panel.canCreateDirectories = false
        panel.prompt = "选它"
        guard panel.runModal() == .OK, let url = panel.url else { views.projectOperationPending = false; return }
        proposeOrAdopt(rootDir: url.path)
    }

    private func proposeOrAdopt(rootDir: String) {
        services.propose(rootDir: rootDir) { outcome in
            switch outcome {
            case .success(let result):
                if FileManager.default.fileExists(atPath: result.path) {
                    adopt(configPath: result.path)
                } else {
                    proposal = result; views.projectOperationPending = false
                }
            case .failure(let error): problem = error.localizedDescription; views.projectOperationPending = false
            }
        }
    }

    private func adopt(configPath: String) {
        services.adopt(configPath: configPath) { outcome in
            defer { views.projectOperationPending = false }
            switch outcome {
            case .success(let read):
                let name = read.config.name.isEmpty
                    ? URL(fileURLWithPath: configPath).deletingLastPathComponent().lastPathComponent
                    : read.config.name
                let root = URL(fileURLWithPath: configPath).deletingLastPathComponent().path
                ProjectRegistry.adopt(name: name, root: root, config: configPath)
                views.scope = .services(name)
            case .failure(let error): problem = error.localizedDescription
            }
        }
    }

    private func writeProposal(_ result: GroupInitResult, yaml: String) {
        proposalProblem = nil
        let root = URL(fileURLWithPath: result.path).deletingLastPathComponent().path
        services.writeConfig(rootDir: root, yaml: yaml) { outcome in
            switch outcome {
            case .success(let written):
                proposal = nil
                let name = written.proposal.name
                if !name.isEmpty {
                    ProjectRegistry.adopt(name: name, root: root, config: written.path)
                    views.scope = .services(name)
                }
                services.refresh()
            case .failure(let error): proposalProblem = error.localizedDescription
            }
        }
    }

    private func openConfig(_ project: BerthGroup) {
        guard let path = project.configPath, !path.isEmpty else {
            problem = "引擎没有返回清单路径。"
            return
        }
        do {
            editingConfig = PendingConfig(project: project, path: path,
                                          yaml: try String(contentsOfFile: path, encoding: .utf8))
        } catch { problem = error.localizedDescription }
    }

    private func saveConfig(_ pending: PendingConfig, yaml: String) {
        let root = URL(fileURLWithPath: pending.path).deletingLastPathComponent().path
        services.rewriteConfig(rootDir: root, yaml: yaml) { outcome in
            switch outcome {
            case .success:
                editingConfig = nil
                services.refresh()
            case .failure(let error): configProblem = error.localizedDescription
            }
        }
    }

    private func removeProject() {
        guard let project = removing,
              let config = project.configPath, !config.isEmpty else { return }
        let outcome = ProjectRegistry.remove(name: project.name, config: config)
        removing = nil
        if outcome.registryRemoved, let root = project.rootDir { TerminalSessions.shared.forgetWorkspace(root) }
        if scope.projectName == project.name { views.scope = .services("") }
        services.refresh()
        if !outcome.registryRemoved || (!outcome.configRemoved && FileManager.default.fileExists(atPath: config)) {
            problem = outcome.summary
        }
    }

    private func shortPath(_ path: String) -> String {
        path.replacingOccurrences(of: FileManager.default.homeDirectoryForCurrentUser.path, with: "~")
    }
}

struct ColumnHead: View {
    let title: String
    init(_ title: String) { self.title = title }
    var body: some View {
        Text(title)
            .font(Face.sans(Metrics.meta, .semibold))
            .foregroundStyle(Ink.inkMuted)
            .tracking(0.3)
            .lineLimit(1)
    }
}
