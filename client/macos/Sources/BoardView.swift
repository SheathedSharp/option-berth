import AppKit
import SwiftUI

/// The project views: declared services, their runtime facts, and read-only Git context.
/// There is no machine-wide port scope. A port only appears inside the
/// worktree the daemon attributed it to.
enum Scope: Hashable {
    case services(String)
    case code(String)
    case terminal(String)
    case project(String)

    var title: String {
        switch self {
        case .services(let name), .code(let name), .terminal(let name), .project(let name):
            return name.isEmpty ? "项目" : name
        }
    }

    var projectName: String? {
        switch self {
        case .services(let name), .code(let name), .terminal(let name), .project(let name):
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
        case "ports", "project": self = .project(parts[1])
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

    @AppStorage("railWidth") private var railWidth: Double = Double(Metrics.railWidth)
    static let railRange: ClosedRange<Double> = 140...320
    var scrolls: Bool = true
    private let initialConsoleAgent: Bool

    init(
        store: BoardStore,
        services: ServicesStore,
        git: GitStore? = nil,
        scrolls: Bool = true,
        initialScope: Scope = .services(""),
        initialConsoleAgent: Bool = false,
        views: ViewState? = nil,
        settings: UISettings = .shared
    ) {
        self.store = store
        self.services = services
        _settings = ObservedObject(wrappedValue: settings)
        _git = StateObject(wrappedValue: git ?? GitStore())
        self.scrolls = scrolls
        self.initialConsoleAgent = initialConsoleAgent
        _views = ObservedObject(wrappedValue: views ?? ViewState(scope: initialScope))
    }

    @Environment(\.accessibilityReduceMotion) private var reduce
    @State private var proposal: GroupInitResult?
    @State private var proposalProblem: String?
    @State private var editingConfig: PendingConfig?
    @State private var configProblem: String?
    @State private var removing: BerthGroup?
    @State private var problem: String?

    private var scope: Scope { views.scope }
    private var projects: [BerthGroup] { services.projects.sorted { $0.name < $1.name } }
    private var selected: BerthGroup? {
        guard let name = scope.projectName else { return nil }
        return projects.first { $0.name == name }
    }

    var body: some View {
        HStack(spacing: 0) {
            if views.railVisible {
                rail
                    .frame(width: railWidth, alignment: .leading)
                    .clipped()
                SplitHandle(width: $railWidth, range: Self.railRange)
            }
            content
                .padding(.top, views.railVisible ? 0 : Metrics.trafficLightInset)
        }
        .ignoresSafeArea(.container, edges: .top)
        .background(Ink.canvas)
        .sheet(item: $proposal) { result in
            AddProjectSheet(result: result, problem: proposalProblem,
                            scrolls: scrolls,
                            onWrite: { writeProposal(result, yaml: $0) },
                            onCancel: { proposal = nil; proposalProblem = nil })
        }
        .sheet(item: $editingConfig) { pending in
            AddProjectSheet(result: GroupInitResult(path: pending.path, yaml: pending.yaml,
                                                     proposal: pending.project),
                            problem: configProblem, mode: .edit,
                            scrolls: scrolls,
                            onWrite: { saveConfig(pending, yaml: $0) },
                            onCancel: { editingConfig = nil; configProblem = nil })
        }
        .sheet(isPresented: $views.showingSettings) {
            SettingsSheet(settings: settings, onClose: { views.showingSettings = false })
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
        .onAppear { refreshGit(force: true) }
        .onChange(of: services.updatedAt) { _, _ in
            refreshGit()
        }
        .onChange(of: scope) { _, newScope in
            refreshGit(force: true)
        }
    }

    private func refreshGit(force: Bool = false) {
        if case .code = scope {
            git.loadTree(project: selected, force: force)
        } else {
            git.refresh(project: selected, force: force)
        }
    }

    private var rail: some View {
        VStack(alignment: .leading, spacing: 0) {
            HStack {
                BerthMark(size: 16, occupied: !projects.isEmpty)
                Text("项目")
                    .font(Face.sans(13, .semibold))
                    .foregroundStyle(Ink.ink)
                Spacer()
                Button("+") { addProject() }
                    .buttonStyle(.plain)
                    .font(Face.mono(15, .medium))
                    .foregroundStyle(Ink.accent)
                    .help("接入一个 worktree")
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
                ForEach(projects) { project in
                    projectRow(project)
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

    private func projectRow(_ project: BerthGroup) -> some View {
        let isSelected = scope.projectName == project.name
        let live = services.liveCount(in: project)
        return Button {
            withAnimation(Motion.selection(reduced: reduce)) { views.scope = .services(project.name) }
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
                Text(verbatim: "\(live)/\(project.services.count)")
                    .font(Face.mono(10))
                    .foregroundStyle(Ink.inkMuted)
            }
            .padding(.horizontal, Metrics.gutter)
            .frame(height: 42)
            .background(isSelected ? Ink.accentSoft : Ink.surface)
            .contentShape(Rectangle())
        }
        .buttonStyle(.plain)
    }

    @ViewBuilder private var content: some View {
        if let selected {
            projectContent(selected)
        } else {
            emptyContent
        }
    }

    private var emptyContent: some View {
        VStack(alignment: .leading, spacing: 12) {
            BerthMark(size: 28, occupied: false)
            Text("选择一个 worktree")
                .font(Face.display(20, .medium))
                .foregroundStyle(Ink.ink)
            Text("状态只从清单、运行记录和运行实况组成。左栏只列有 oberth.yaml 的项目。")
                .font(Face.sans(11.5))
                .foregroundStyle(Ink.inkFaint)
                .fixedSize(horizontal: false, vertical: true)
            SheetButton(title: "接入项目", primary: true, action: addProject)
        }
        .frame(maxWidth: 460, alignment: .leading)
        .padding(Metrics.gutter + 8)
        .frame(maxWidth: .infinity, maxHeight: .infinity, alignment: .topLeading)
    }

    private func projectContent(_ project: BerthGroup) -> some View {
        VStack(spacing: 0) {
            projectHeader(project)
            tabBar(project)
            Hairline()
            switch scope {
            case .services:
                ServicesView(store: services, group: project,
                             ports: ports(for: project), scrolls: scrolls)
            case .code:
                CodeView(git: git, project: project, scrolls: scrolls)
            case .terminal:
                WorkspaceConsole(root: project.rootDir ?? "", frozen: !scrolls, initialAgent: initialConsoleAgent).id(project.rootDir ?? project.name)
            case .project:
                // Keep the old command-line scope as a compatibility alias. Runtime
                // facts now live on the service page instead of a separate port tab.
                ServicesView(store: services, group: project,
                             ports: ports(for: project), scrolls: scrolls)
            }
        }
        .frame(maxWidth: .infinity, maxHeight: .infinity, alignment: .topLeading)
    }

    private func projectHeader(_ project: BerthGroup) -> some View {
        HStack(alignment: .firstTextBaseline, spacing: 10) {
            Text(project.displayName)
                .font(Face.display(19, .semibold))
                .foregroundStyle(Ink.ink)
                .lineLimit(1)
            Text(projectFact(project))
                .font(Face.sans(11))
                .foregroundStyle(Ink.inkFaint)
                .lineLimit(1)
                .truncationMode(.middle)
            Rectangle().fill(Ink.line).frame(height: 1)
            if services.isWorking(project) {
                Text("处理中…").font(Face.sans(10.5)).foregroundStyle(Ink.inkFaint)
            } else {
                if !project.services.isEmpty {
                    RowAction(title: "启动全部", tone: Ink.accent) { services.start(project) }
                    if services.stoppableCount(in: project) > 0 {
                        RowAction(title: "全部停止") { services.stop(project) }
                    }
                }
                RowAction(title: "清单") { openConfig(project) }
                RowAction(title: "移除", tone: Ink.inkFaint) { removing = project }
            }
        }
        .padding(.horizontal, Metrics.gutter)
        .padding(.top, 12)
        .padding(.bottom, 8)
    }

    private func tabBar(_ project: BerthGroup) -> some View {
        HStack(spacing: 4) {
            tab("服务", selected: { if case .services = scope { return true }; return false }) {
                views.scope = .services(project.name)
            }
            let changed = git.tree?.files.count ?? 0
            tab("代码\(changed > 0 ? " \(changed)" : "")",
                selected: { if case .code = scope { return true }; return false }) {
                views.scope = .code(project.name)
            }
            tab("终端 / Terminal", selected: { if case .terminal = scope { return true }; return false }) {
                views.scope = .terminal(project.name)
            }
            Spacer(minLength: 0)
        }
        .padding(.horizontal, Metrics.gutter)
        .padding(.bottom, 7)
    }

    private func tab(_ title: String, selected: () -> Bool, action: @escaping () -> Void) -> some View {
        Button(action: action) {
            Text(title)
                .font(Face.sans(11, selected() ? .medium : .regular))
                .foregroundStyle(selected() ? Ink.accent : Ink.inkMuted)
                .padding(.horizontal, 8)
                .padding(.vertical, 4)
                .background(selected() ? Ink.accentSoft : Color.clear)
                .clipShape(RoundedRectangle(cornerRadius: 4, style: .continuous))
        }
        .buttonStyle(.plain)
    }

    private func ports(for project: BerthGroup) -> [Port] {
        store.ports.filter { port in
            if port.group == project.name { return true }
            return port.group == nil && port.projectRoot == project.rootDir
        }.sorted { a, b in
            a.port == b.port ? a.bindAddress < b.bindAddress : a.port < b.port
        }
    }

    private func projectFact(_ project: BerthGroup) -> String {
        var parts: [String] = []
        if let root = project.rootDir, !root.isEmpty { parts.append(shortPath(root)) }
        parts.append("\(services.liveCount(in: project))/\(project.services.count) 服务在跑")
        return parts.joined(separator: " · ")
    }

    private func addProject() {
        let panel = NSOpenPanel()
        panel.canChooseDirectories = true
        panel.canChooseFiles = false
        panel.allowsMultipleSelection = false
        panel.canCreateDirectories = false
        panel.prompt = "选它"
        guard panel.runModal() == .OK, let url = panel.url else { return }
        proposeOrAdopt(rootDir: url.path)
    }

    private func proposeOrAdopt(rootDir: String) {
        services.propose(rootDir: rootDir) { outcome in
            switch outcome {
            case .success(let result):
                if FileManager.default.fileExists(atPath: result.path) {
                    adopt(configPath: result.path)
                } else {
                    proposal = result
                }
            case .failure(let error): problem = error.localizedDescription
            }
        }
    }

    private func adopt(configPath: String) {
        services.adopt(configPath: configPath) { outcome in
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
