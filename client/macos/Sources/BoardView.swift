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

    var body: some View {
        HStack(spacing: 0) {
            if views.railVisible {
                rail.tourAnchor(.worktrees)
                    .frame(width: scrolls ? settings.sidebarWidth : Double(Metrics.railWidth), alignment: .leading)
                    .clipped()
                SplitHandle(width: railWidth, range: Self.railRange)
            }
            VStack(spacing: 0) {
                WorkspaceToolbar(frozen: !scrolls, sessionCount: scrolls ? terminalSessions.sessions.count : 0,
                                 recoveryPending: scrolls && recovery.reviewPending,
                                 openActions: { views.showingActions = true },
                                 openSessions: { views.showingSessions = true },
                                 openRecovery: { views.showingRecovery = true },
                                 openUpdates: { views.showingUpdates = true },
                                 openSettings: { views.showingSettings = true },
                                 openGuide: { views.showingGuide = true })
                Hairline()
                content
            }.padding(.top, views.railVisible ? 0 : Metrics.trafficLightInset)
        }
        .ignoresSafeArea(.container, edges: .top)
        .background(Ink.canvas)
        .background {
            if scrolls {
                WorkspaceCommandDeliveryAnchor(delivery: commandDelivery).frame(width: 0, height: 0)
                WorkspaceMenuRefresh(bindings: shortcuts.bindings, worktrees: visibleProjects.map(\.name), allowed: views.allowsCommands)
                    .frame(width: 0, height: 0).allowsHitTesting(false)
            }
        }
        .preferredColorScheme(settings.colorScheme)
        .clientWindowChrome()
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

    private func refreshGit(force: Bool = false) {
        guard scrolls else { return }
        if case .code = scope {
            git.loadTree(project: selected, force: force)
        } else {
            git.refresh(project: selected, force: force)
        }
    }

    private var rail: some View {
        VStack(alignment: .leading, spacing: 0) {
            HStack {
                Image(systemName: "square.stack.3d.up").font(.system(size: 12, weight: .medium)).foregroundStyle(Ink.inkMuted)
                Text("Worktrees")
                    .font(Face.sans(13, .semibold))
                    .foregroundStyle(Ink.ink)
                Spacer()
                Button("+") { views.requestConnection() }
                    .buttonStyle(.plain)
                    .font(Face.mono(15, .medium))
                    .foregroundStyle(Ink.accent)
                    .help("接入一个 worktree").tourAnchor(.connect)
            }
            .padding(.horizontal, Metrics.gutter)
            .padding(.top, 14)
            .padding(.bottom, 10)
            Hairline()
            if projects.isEmpty {
                VStack(alignment: .leading, spacing: 8) {
                    Text("还没有清单项目")
                        .font(Face.sans(11.5, .medium))
                        .foregroundStyle(Ink.ink)
                    Text("接入一个目录，或者先运行 oberth init。")
                        .font(Face.sans(10.5))
                        .foregroundStyle(Ink.inkFaint)
                        .fixedSize(horizontal: false, vertical: true)
                }
                .padding(Metrics.gutter)
            } else {
                if scrolls {
                    TextField("筛选 worktree", text: $views.projectQuery).textFieldStyle(.roundedBorder)
                        .font(Face.sans(11)).padding(.horizontal, 10).padding(.vertical, 9)
                    ScrollView {
                        LazyVStack(spacing: 3) {
                            ForEach(Array(visibleProjects.enumerated()), id: \.element.id) { index, project in
                                projectRow(project, ordinal: index + 1)
                            }
                        }.padding(.horizontal, 6)
                    }
                } else {
                    VStack(spacing: 3) { ForEach(Array(visibleProjects.enumerated()), id: \.element.id) { index, project in projectRow(project, ordinal: index + 1) } }
                        .padding(.horizontal, 6).padding(.top, 9)
                }
            }
            Spacer(minLength: 0)
            if services.updatedAt != nil {
                Text("已更新")
                    .font(Face.mono(9.5))
                    .foregroundStyle(Ink.inkFaint)
                    .padding(Metrics.gutter)
            }
        }
        // The title bar is transparent and the traffic lights sit over the left rail.
        // Keep the rail surface continuous, but move its first controls below them.
        .padding(.top, Metrics.trafficLightInset)
        .background(Ink.surface)
    }

    private func projectRow(_ project: BerthGroup, ordinal: Int) -> some View {
        let isSelected = scope.projectName == project.name
        let live = services.liveCount(in: project)
        return Button {
            withAnimation(Motion.selection(reduced: reduce)) { views.selectProject(project.name) }
        } label: {
            HStack(spacing: 8) {
                StatusDot(tone: live > 0 ? Ink.live : Ink.dormant)
                VStack(alignment: .leading, spacing: 2) {
                    Text(project.displayName)
                        .font(Face.sans(12.5, .medium))
                        .foregroundStyle(Ink.ink)
                        .lineLimit(1)
                    Text(project.branch.isEmpty ? shortPath(project.rootDir ?? "") : project.branch)
                        .font(Face.mono(9.5))
                        .foregroundStyle(Ink.inkFaint)
                        .lineLimit(1)
                        .truncationMode(.middle)
                }
                Spacer(minLength: 4)
                VStack(alignment: .trailing, spacing: 3) {
                    Text(verbatim: "\(live)/\(project.services.count)")
                        .font(Face.mono(10)).foregroundStyle(Ink.inkMuted)
                    if let action = WorkspaceAction(rawValue: "worktree.\(ordinal)") {
                        Text(scrolls ? shortcuts.shortcut(action).label : action.defaultShortcut.label)
                            .font(Face.mono(9)).foregroundStyle(Ink.inkFaint)
                    }
                }
            }
            .padding(.horizontal, Metrics.gutter)
            .frame(height: 48)
            .background(isSelected ? Ink.accentSoft : Ink.surface)
            .clipShape(RoundedRectangle(cornerRadius: 6))
            .contentShape(Rectangle())
        }
        .buttonStyle(.plain)
    }

    @ViewBuilder private var content: some View {
        if case .console(let root) = scope {
            VStack(alignment: .leading, spacing: 0) {
                HStack {
                    Text("独立会话 / Standalone sessions").font(Face.sans(12, .semibold))
                    Spacer()
                    Button("全部会话 / All sessions") { views.showingSessions = true }
                }.padding(12)
                Text(root).font(Face.mono(10)).lineLimit(1).truncationMode(.middle).padding(.horizontal, 12)
                Text("未关联已登记清单；会话不代表项目服务状态。 / No registered manifest is associated.")
                    .font(Face.sans(10)).foregroundStyle(Ink.inkMuted).padding(12)
                WorkspaceConsole(root: root, frozen: !scrolls).id(root)
            }
        } else if let selected { projectContent(selected) }
        else { emptyContent }
    }

    private var emptyContent: some View {
        VStack(alignment: .leading, spacing: 12) {
            BerthMark(size: 28, occupied: false)
            Text("选择一个 worktree")
                .font(Face.sans(20, .semibold))
                .foregroundStyle(Ink.ink)
            Text("状态只从清单、运行记录和运行实况组成。左栏只列有 oberth.yaml 的项目。")
                .font(Face.sans(11.5))
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
                Text("使用指引 / Getting started").font(Face.sans(11)).foregroundStyle(Ink.accent)
                SheetButton(title: "接入项目", primary: true, action: {}).tourAnchor(.connect)
                Text("打开会话管理 / Open session manager").font(Face.sans(11)).foregroundStyle(Ink.inkMuted)
            }
        }
        .frame(maxWidth: 460, alignment: .leading)
        .padding(Metrics.gutter + 8)
        .frame(maxWidth: .infinity, maxHeight: .infinity, alignment: .topLeading)
    }

    private func projectContent(_ project: BerthGroup) -> some View {
        VStack(spacing: 0) {
            projectHeader(project)
            tabBar(project).tourAnchor(.facts)
            Hairline()
            switch scope {
            case .services:
                ServicesView(store: services, group: project,
                             ports: ports(for: project), scrolls: scrolls)
            case .code:
                CodeView(git: git, project: project, scrolls: scrolls)
            case .terminal, .console:
                WorkspaceConsole(root: project.rootDir ?? "", frozen: !scrolls, initialAgent: initialConsoleAgent).id(project.rootDir ?? project.name)

            }
        }
        .frame(maxWidth: .infinity, maxHeight: .infinity, alignment: .topLeading)
    }

    private func projectHeader(_ project: BerthGroup) -> some View {
        HStack(alignment: .center, spacing: 12) {
            VStack(alignment: .leading, spacing: 5) {
                HStack(spacing: 8) {
                    Text(project.displayName).font(Face.sans(17, .semibold)).foregroundStyle(Ink.ink).lineLimit(1)
                    if !project.branch.isEmpty {
                        Label(project.branch, systemImage: "arrow.triangle.branch")
                            .font(Face.mono(10)).foregroundStyle(Ink.inkMuted).lineLimit(1)
                            .padding(.horizontal, 7).padding(.vertical, 3).background(Ink.surface)
                            .clipShape(Capsule()).layoutPriority(-1)
                    }
                }
                Text(shortPath(project.rootDir ?? "目录不可用"))
                    .font(Face.mono(10)).foregroundStyle(Ink.inkFaint).lineLimit(1).truncationMode(.middle)
            }.frame(maxWidth: .infinity, alignment: .leading)
            HStack(spacing: 5) {
                StatusDot(tone: services.liveCount(in: project) > 0 ? Ink.live : Ink.dormant)
                Text(verbatim: "\(services.liveCount(in: project))/\(project.services.count) 服务")
                    .font(Face.mono(10)).foregroundStyle(Ink.inkMuted)
            }.fixedSize()
            if services.isWorking(project) {
                ProgressView().controlSize(.small).help("正在处理项目服务")
            } else {
                if !scrolls {
                    Label("项目", systemImage: "slider.horizontal.3").font(Face.sans(11)).foregroundStyle(Ink.inkMuted)
                } else {
                Menu {
                    Button("编辑清单…") { openConfig(project) }
                    if !project.services.isEmpty {
                        Divider()
                        Button("启动全部服务") { services.start(project) }
                        Button("停止项目服务") { services.stop(project) }.disabled(services.stoppableCount(in: project) == 0)
                    }
                    Divider()
                    Button("移除项目…", role: .destructive) { removing = project }
                } label: { Label("项目", systemImage: "slider.horizontal.3").font(Face.sans(11)) }
                    .menuStyle(.borderlessButton).fixedSize()
                }
            }
        }.padding(.horizontal, 16).padding(.top, 14).padding(.bottom, 12)
    }

    private func tabBar(_ project: BerthGroup) -> some View {
        HStack(spacing: 4) {
            WorkspaceTab(title: "服务", symbol: "server.rack", selected: scope == .services(project.name)) {
                views.scope = .services(project.name)
            }.help(shortcuts.shortcut(.services).label)
            let changed = git.tree?.files.count ?? 0
            WorkspaceTab(title: "Git" + (changed > 0 ? " · \(changed)" : ""), symbol: "chevron.left.forwardslash.chevron.right", selected: scope == .code(project.name)) {
                views.scope = .code(project.name)
            }.help(shortcuts.shortcut(.code).label)
            WorkspaceTab(title: "会话", symbol: "terminal", selected: scope == .terminal(project.name)) {
                views.scope = .terminal(project.name)
            }.help(shortcuts.shortcut(.terminal).label)
            Spacer(minLength: 0)
        }.padding(.horizontal, 12).padding(.bottom, 9)
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
        Text(title.uppercased())
            .font(Face.sans(9, .semibold))
            .tracking(0.8)
            .foregroundStyle(Ink.inkFaint)
            .lineLimit(1)
    }
}
