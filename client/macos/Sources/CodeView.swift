import AppKit
import SwiftUI

enum GitReviewMode: String, CaseIterable, Identifiable {
    case changes, history
    var id: String { rawValue }
    var title: String { self == .changes ? "Changes" : "History" }
}

/// One read-only review surface for the selected worktree. All facts and patches
/// come from GitStore/oberth, never a second client-side Git process.
///
/// The mode panels are `extension CodeView` members below this type. A stray
/// brace in one panel closes that extension instead of dropping the other
/// panels out of `CodeView`.
struct CodeView: View {
    @ObservedObject var git: GitStore
    let project: BerthGroup
    var scrolls: Bool
    @ClientDetailWidth(.git) private var detailWidth
    @State private var query: String
    @State private var filter: GitReviewFilter
    @State private var mode: GitReviewMode
    @State private var showingWorktrees: Bool
    @FocusState private var searching: Bool
    @FocusState private var graphFocused: Bool
    private var files: [GitFile] { git.isFor(project) ? (git.tree?.files ?? []) : [] }
    private var visible: [GitFile] { filter.files(files, query: query) }

    init(git: GitStore, project: BerthGroup, scrolls: Bool = true, initialMode: GitReviewMode = .changes) {
        self.git = git
        self.project = project
        self.scrolls = scrolls
        _detailWidth = ClientDetailWidth(.git)
        _query = State(initialValue: "")
        _filter = State(initialValue: .all)
        _mode = State(initialValue: initialMode)
        _showingWorktrees = State(initialValue: false)
        _searching = FocusState()
        _graphFocused = FocusState()
    }

    var body: some View {
        Group {
            if git.isFor(project) { reviewContent }
            else { message("正在读取当前 worktree…") }
        }
        .background(Ink.canvas)
        .onChange(of: query) { _, _ in reconcileSelection() }
        .onChange(of: filter) { _, _ in reconcileSelection() }
        .onChange(of: mode) { _, value in
            if value == .history { git.loadGraph(project: project) }
            else { git.clearSelection() }
        }
        .onChange(of: project.rootDir) { _, _ in query = ""; filter = .all; mode = .changes; showingWorktrees = false }
        .onReceive(NotificationCenter.default.publisher(for: .init("option-berth.git.find"))) { event in
            guard scrolls, git.isFor(project), event.object as? String == project.name, !hasMarkedText else { return }
            searching = true
        }
    }
}

// MARK: - Shared chrome

extension CodeView {
    private var reviewContent: some View {
        VStack(spacing: 0) {
            if let overview = git.overview { overviewHeader(overview); Hairline() }
            if let problem = git.problem { problemBanner(problem) }
            if git.tree != nil || mode == .history {
                reviewToolbar
                Hairline()
                if mode == .history { graphPanel } else { changesPanel }
            } else {
                Text(git.loading ? "正在读取变更…" : "Git 状态暂不可用")
                    .font(Face.sans(Metrics.row)).foregroundStyle(Ink.inkMuted)
                    .frame(maxWidth: .infinity, maxHeight: .infinity, alignment: .topLeading).padding(Metrics.gutter)
            }
        }
    }

    private func problemBanner(_ problem: String) -> some View {
        HStack(alignment: .top) {
            Image(systemName: "exclamationmark.triangle")
            Text((git.tree == nil ? "读取失败：" : "刷新失败，当前为上次结果：") + problem).textSelection(.enabled)
            Spacer(minLength: 0)
            Button("重试") { git.loadTree(project: project, force: true) }.disabled(git.loading || !scrolls)
        }
        .font(Face.sans(Metrics.chrome)).foregroundStyle(Change.changed)
        .padding(.horizontal, Metrics.gutter).padding(.vertical, Metrics.tabPadV).background(Ink.surface)
    }

    private var reviewToolbar: some View {
        HStack(spacing: Metrics.unit) {
            ForEach(GitReviewMode.allCases) { item in
                Button { mode = item } label: {
                    HStack(spacing: Metrics.unit) {
                        Image(systemName: item == .changes ? "square.stack.3d.up" : "point.3.filled.connected.trianglepath.dotted")
                        Text(item.title)
                        if item == .history, let graph = git.graph { Text("\(graph.commits.count)").foregroundStyle(Ink.inkFaint) }
                    }
                    .font(Face.sans(Metrics.meta, mode == item ? .semibold : .regular))
                    .padding(.horizontal, Metrics.tabPadH).padding(.vertical, Metrics.tabPadV)
                    .frame(minHeight: Metrics.hit)
                    .foregroundStyle(mode == item ? Ink.accent : Ink.inkMuted)
                    .background(mode == item ? Ink.accentSoft : Color.clear)
                    .clipShape(RoundedRectangle(cornerRadius: 5))
                }.buttonStyle(.plain).disabled(!scrolls)
                    .accessibilityIdentifier("git.review.mode." + item.rawValue)
            }
            if mode == .changes, git.tree != nil {
                Hairline(axis: .vertical).frame(height: 14).padding(.horizontal, 2)
                ForEach(GitReviewFilter.allCases) { item in
                    let count = files.filter(item.matches).count
                    Button { filter = item } label: {
                        Text(item.title + " \(count)").font(Face.sans(Metrics.meta, filter == item ? .semibold : .regular))
                            .padding(.horizontal, Metrics.controlPadH).padding(.vertical, Metrics.tabPadV)
                            .frame(minHeight: Metrics.hit)
                            .foregroundStyle(filter == item ? Ink.accent : Ink.inkMuted)
                            .background(filter == item ? Ink.accentSoft : Color.clear)
                            .clipShape(RoundedRectangle(cornerRadius: 5))
                    }.buttonStyle(.plain).disabled(!scrolls)
                        .accessibilityIdentifier("git.review.filter." + item.rawValue)
                }
            }
            Spacer(minLength: 0)
            if mode == .history, git.graphLoading { ProgressView().controlSize(.small) }
        }.padding(.horizontal, Metrics.gutter).padding(.vertical, Metrics.stack).background(Ink.surface)
    }

    private func overviewHeader(_ overview: GitOverview) -> some View {
        VStack(alignment: .leading, spacing: Metrics.stack) {
            HStack(spacing: Metrics.inline) {
                Label(overview.branchName, systemImage: "arrow.triangle.branch")
                    .font(Face.mono(Metrics.row, .semibold)).lineLimit(1)
                Text(overview.shortHead.isEmpty ? "尚无提交" : overview.shortHead)
                    .font(Face.mono(Metrics.meta)).foregroundStyle(Ink.inkMuted).textSelection(.enabled)
                Spacer(minLength: 0)
                if git.loading && scrolls { ProgressView().controlSize(.small) }
                Text(!overview.countsComplete ? "计数未知" : (overview.clean ? "干净" : "有变更"))
                    .font(Face.sans(Metrics.meta)).foregroundStyle(overview.clean ? Ink.live : Change.changed)
                if scrolls { Button { git.loadTree(project: project, force: true) } label: { Image(systemName: "arrow.clockwise") }
                    .buttonStyle(.plain).disabled(git.loading).help("刷新当前 worktree，不自动 fetch") }
            }
            HStack(spacing: Metrics.inline) {
                if let upstream = overview.upstream, !upstream.isEmpty {
                    Text("上游 " + upstream).lineLimit(1).truncationMode(.middle)
                    Text(verbatim: "↑\(overview.ahead)  ↓\(overview.behind)")
                        .help("相对本地上游引用；不表示已联网同步")
                } else { Text("未设置上游") }
                Spacer(minLength: 0)
                let totals = GitReviewLineTotals(files)
                if git.tree != nil {
                    Text(verbatim: "+\(totals.additions)  −\(totals.deletions)").foregroundStyle(Ink.ink)
                    if totals.uncounted > 0 {
                        Text(verbatim: "另 \(totals.uncounted) 项未计行")
                            .help("二进制、冲突或缺失统计；没有按零计算")
                    }
                }
            }.font(Face.mono(Metrics.meta)).foregroundStyle(Ink.inkMuted)
            HStack(spacing: Metrics.inline) {
                if let commit = overview.lastCommit {
                    Text(commit.subject).font(Face.sans(Metrics.chrome)).lineLimit(1)
                        .help(commit.hash + (commit.when.map { " · " + $0 } ?? ""))
                }
                Spacer(minLength: 0)
                if !overview.worktrees.isEmpty {
                    Button { showingWorktrees.toggle() } label: {
                        Label("Worktrees · \(overview.worktrees.count)", systemImage: showingWorktrees ? "chevron.up" : "chevron.down")
                    }.buttonStyle(.plain).font(Face.sans(Metrics.meta)).disabled(!scrolls)
                        .accessibilityIdentifier("git.review.worktrees")
                }
            }.foregroundStyle(Ink.inkMuted)
            if showingWorktrees {
                ScrollView {
                    VStack(alignment: .leading, spacing: Metrics.stack) {
                        ForEach(overview.worktrees) { worktree in
                            HStack(spacing: Metrics.inline) {
                                Image(systemName: worktree.current ? "circle.inset.filled" : "circle")
                                Text(worktree.bare ? "bare" : worktree.displayBranch).frame(minWidth: 70, alignment: .leading)
                                Text(worktree.path).lineLimit(1).truncationMode(.middle)
                                Spacer(minLength: 0)
                                Text(String((worktree.head ?? "").prefix(8)))
                            }.font(Face.mono(Metrics.meta)).textSelection(.enabled)
                        }
                    }
                }.frame(maxHeight: 64)
            }
        }.padding(.horizontal, Metrics.gutter).padding(.vertical, Metrics.tabPadV).foregroundStyle(Ink.ink).background(Ink.surface)
    }

    @ViewBuilder
    func reviewColumns<Leading: View, Trailing: View>(in geometry: GeometryProxy,
                                                      narrowMin: CGFloat, narrowIdeal: CGFloat,
                                                      trailingMin: CGFloat, frozenFloor: CGFloat, frozenFraction: CGFloat,
                                                      @ViewBuilder leading: () -> Leading,
                                                      @ViewBuilder trailing: () -> Trailing) -> some View {
        if geometry.size.width >= 740 {
            if scrolls {
                let maximum = min(500.0, Double(max(0, geometry.size.width - 340 - SplitHandle.hitWidth)))
                let range = min(340.0, maximum)...maximum
                let width = min(max(detailWidth, range.lowerBound), range.upperBound)
                HStack(spacing: 0) {
                    leading().frame(width: max(0, geometry.size.width - width - SplitHandle.hitWidth))
                    SplitHandle(width: $detailWidth, range: range, controlsTrailingPane: true)
                    trailing().frame(width: width)
                }
            } else {
                HStack(spacing: 0) {
                    leading().frame(width: geometry.size.width * 0.40)
                    Hairline(axis: .vertical)
                    trailing()
                }
            }
        } else if scrolls {
            VSplitView {
                leading().frame(minHeight: narrowMin, idealHeight: narrowIdeal)
                trailing().frame(minHeight: trailingMin, maxHeight: .infinity)
            }
        } else {
            VStack(spacing: 0) {
                leading().frame(height: max(frozenFloor, geometry.size.height * frozenFraction))
                Hairline()
                trailing()
            }
        }
    }

    func reportDetailWidth<V: View>(_ view: V) -> some View {
        view.background {
            GeometryReader { geometry in
                Color.clear.preference(key: GitReviewDetailWidthKey.self, value: geometry.size.width)
            }
        }
    }

    func patchFileHeader(_ file: GitFileDiff) -> some View {
        Text(file.oldPath.map { "\($0) → \(file.path)" } ?? file.path)
            .font(Face.mono(Metrics.chrome, .medium)).lineLimit(2).textSelection(.enabled)
            .padding(.horizontal, Metrics.gutter).padding(.vertical, Metrics.tabPadV)
            .frame(maxWidth: .infinity, alignment: .leading).background(Ink.surface)
    }

    @ViewBuilder func patchFile(_ file: GitFileDiff, embedded: Bool) -> some View {
        patchFileHeader(file)
        Hairline()
        patchBody(file, embedded: embedded)
    }

    @ViewBuilder func patchBody(_ file: GitFileDiff, embedded: Bool) -> some View {
        if file.binary {
            message("二进制文件，没有可显示的文本差异", fills: !embedded).frame(minHeight: embedded ? 48 : nil)
        } else if file.lines.isEmpty {
            message("当前没有可显示的文本差异", fills: !embedded).frame(minHeight: embedded ? 48 : nil)
        } else {
            GitPatchView(lines: file.lines, scrolls: embedded ? false : scrolls, fills: !embedded && !scrolls)
        }
    }

    func message(_ text: String, fills: Bool = true) -> some View {
        Text(text).font(Face.sans(Metrics.row)).foregroundStyle(Ink.inkMuted).textSelection(.enabled)
            .frame(maxWidth: .infinity, maxHeight: fills ? .infinity : nil, alignment: .topLeading).padding(Metrics.gutter)
    }
}

// MARK: - Changes

extension CodeView {
    private var changesPanel: some View {
        GeometryReader { geometry in
            reviewColumns(in: geometry, narrowMin: 85, narrowIdeal: 150, trailingMin: 100,
                          frozenFloor: 70, frozenFraction: 0.43) { fileList } trailing: { diffPanel }
        }
    }

    private var fileList: some View {
        VStack(spacing: 0) {
            HStack(spacing: Metrics.inline) {
                if scrolls {
                    TextField("筛选文件或原路径", text: $query).textFieldStyle(.plain).focused($searching)
                        .accessibilityIdentifier("git.review.search")
                        .onSubmit { if !hasMarkedText, let first = visible.first { git.select(first, project: project) } }
                        .onMoveCommand { direction in
                            guard !hasMarkedText else { return }
                            if direction == .down { moveSelection(1) }
                            if direction == .up { moveSelection(-1) }
                        }
                    // The application's command catalogue owns key equivalents;
                    // keeping a second hard-coded Cmd+F here breaks user overrides.
                    Button { searching = true } label: { Image(systemName: "magnifyingglass") }
                        .buttonStyle(.plain).help("查找当前 worktree 的变更文件")
                } else { Label("筛选文件或原路径", systemImage: "magnifyingglass") }
                Text(verbatim: "\(visible.count)/\(files.count)").foregroundStyle(Ink.inkFaint)
            }.font(Face.sans(Metrics.chrome)).padding(.horizontal, Metrics.gutter).padding(.vertical, 7).background(Ink.surface)
            Hairline()
            if visible.isEmpty {
                Text(files.isEmpty ? "没有变更文件" : "没有匹配的文件")
                    .font(Face.sans(Metrics.row)).foregroundStyle(Ink.inkMuted)
                    .frame(maxWidth: .infinity, maxHeight: .infinity, alignment: .topLeading).padding(Metrics.gutter)
            } else if scrolls {
                ScrollViewReader { proxy in
                    ScrollView { LazyVStack(spacing: 0) { rows } }
                        .onChange(of: git.selectedPath) { _, path in if let path { proxy.scrollTo(path, anchor: .center) } }
                }
            } else { VStack(spacing: 0) { rows }.frame(maxHeight: .infinity, alignment: .top).clipped() }
        }.frame(maxWidth: .infinity, maxHeight: .infinity).background(Ink.canvas)
    }

    @ViewBuilder private var rows: some View {
        ForEach(visible) { file in
            Button { git.select(file, project: project) } label: {
                HStack(spacing: Metrics.inline) {
                    Text(file.status).font(Face.mono(Metrics.meta, .medium)).frame(width: 24)
                        .foregroundStyle(file.isConflict ? Change.removed : Ink.accent)
                    VStack(alignment: .leading, spacing: Metrics.stack) {
                        Text(file.displayPath).font(Face.mono(11)).lineLimit(1).truncationMode(.middle)
                        Text(file.statusLabel).font(Face.sans(9)).foregroundStyle(Ink.inkMuted)
                    }
                    Spacer(minLength: 4)
                    if file.binary { Text("binary").font(Face.mono(Metrics.caption)).foregroundStyle(Ink.inkFaint) }
                    else if let plus = file.additions, let minus = file.deletions, !file.isConflict {
                        Text(verbatim: "+\(plus) −\(minus)").font(Face.mono(Metrics.caption)).foregroundStyle(Ink.inkMuted)
                    }
                }.padding(.horizontal, Metrics.gutter).padding(.vertical, Metrics.headerPad).foregroundStyle(Ink.ink)
                    .frame(maxWidth: .infinity, alignment: .leading).contentShape(Rectangle())
                    .background(git.selectedPath == file.path ? Ink.accentSoft : Ink.canvas)
            }.buttonStyle(.plain).id(file.path).disabled(!scrolls)
                .accessibilityIdentifier("git.review.file." + file.path).help(file.displayPath + " · " + file.statusLabel)
            Hairline()
        }
    }

    private var diffPanel: some View {
        reportDetailWidth(VStack(alignment: .leading, spacing: 0) {
            HStack(spacing: Metrics.inline) {
                Text("DIFF").font(Face.mono(Metrics.meta, .semibold))
                Text(git.overview?.head == nil ? "初始变更" : "HEAD → 工作区（含暂存）")
                    .font(Face.sans(Metrics.meta)).foregroundStyle(Ink.inkMuted).lineLimit(1)
                Spacer(minLength: 0)
                if scrolls, git.selectedPath != nil {
                    Button("关闭") { git.clearSelection() }.buttonStyle(.plain).font(Face.sans(Metrics.meta))
                }
            }.padding(.horizontal, Metrics.gutter).padding(.vertical, Metrics.tabPadV).background(Ink.surface)
            Hairline()
            if git.patchLoading { message("正在读取差异…") }
            else if let problem = git.patchProblem { message(problem) }
            else if let file = git.patch?.files.first {
                patchFile(file, embedded: false)
            } else { message("选择文件查看差异") }
        }.frame(maxWidth: .infinity, maxHeight: .infinity, alignment: .topLeading)
            .foregroundStyle(Ink.ink).background(Ink.canvas))
    }

    private var hasMarkedText: Bool { (NSApp.keyWindow?.firstResponder as? NSTextInputClient)?.hasMarkedText() == true }

    private func reconcileSelection() {
        if git.isFor(project), let path = git.selectedPath, !visible.contains(where: { $0.path == path }) { git.clearSelection() }
    }

    private func moveSelection(_ offset: Int) {
        guard !visible.isEmpty else { return }
        let current = visible.firstIndex { $0.path == git.selectedPath }
        let index = current.map { max(0, min(visible.count - 1, $0 + offset)) } ?? (offset > 0 ? 0 : visible.count - 1)
        git.select(visible[index], project: project)
    }
}

// MARK: - History

extension CodeView {
    private var graphPanel: some View {
        GeometryReader { geometry in
            reviewColumns(in: geometry, narrowMin: 150, narrowIdeal: 250, trailingMin: 120,
                          frozenFloor: 100, frozenFraction: 0.52) { graphList } trailing: { historicalDiffPanel }
        }
    }

    private var graphList: some View {
        VStack(spacing: 0) {
            HStack(spacing: Metrics.inline) {
                Image(systemName: "point.3.filled.connected.trianglepath.dotted").foregroundStyle(Ink.accent)
                if let graph = git.graph {
                    Text(graph.branchName).font(Face.mono(11, .semibold)).lineLimit(1)
                    if !graph.shortHead.isEmpty { Text(graph.shortHead).font(Face.mono(Metrics.caption)).foregroundStyle(Ink.inkMuted) }
                    Spacer(minLength: 0)
                    if graph.truncated { Text("前 \(graph.limit) 条").font(Face.mono(Metrics.caption)).foregroundStyle(Change.changed) }
                    Text("\(graph.commits.count) commits").font(Face.mono(Metrics.caption)).foregroundStyle(Ink.inkMuted)
                } else {
                    Text("Git graph").font(Face.mono(11, .semibold))
                    Spacer(minLength: 0)
                }
                Button { git.loadGraph(project: project, force: true) } label: { Image(systemName: "arrow.clockwise") }
                    .buttonStyle(.plain).disabled(git.graphLoading || !scrolls).help("刷新只读 graph，不自动 fetch")
            }.padding(.horizontal, Metrics.gutter).padding(.vertical, Metrics.tabPadV).background(Ink.surface)
            Hairline()
            if let problem = git.graphProblem {
                VStack(alignment: .leading, spacing: 8) {
                    Label("Graph 读取失败", systemImage: "exclamationmark.triangle")
                        .foregroundStyle(Change.changed)
                    Text(problem).font(Face.sans(Metrics.chrome)).foregroundStyle(Ink.inkMuted).textSelection(.enabled)
                    Button("重试") { git.loadGraph(project: project, force: true) }
                        .buttonStyle(.bordered).controlSize(.small).disabled(git.graphLoading || !scrolls)
                }.frame(maxWidth: .infinity, maxHeight: .infinity, alignment: .topLeading).padding(Metrics.gutter)
            } else if git.graphLoading && git.graph == nil {
                message("正在读取有界提交 graph…")
            } else if let graph = git.graph {
                graphScroller(graph, layout: GitGraphLayout.layout(graph.commits.map { GitGraphNode(hash: $0.hash, parents: $0.parents) }))
            } else {
                message("没有可用的提交 graph")
            }
        }.background(Ink.canvas)
    }

    @ViewBuilder private func graphScroller(_ graph: GitGraph, layout: GitGraphLayout) -> some View {
        if scrolls {
            ScrollViewReader { proxy in
                ScrollView {
                    LazyVStack(spacing: 0) {
                        graphPreface(graph)
                        graphCommits(graph, layout: layout)
                    }
                }
                .focusable()
                .focused($graphFocused)
                .onMoveCommand { direction in
                    guard !hasMarkedText else { return }
                    if direction == .down { moveCommit(1, in: graph) }
                    if direction == .up { moveCommit(-1, in: graph) }
                }
                .onChange(of: git.selectedCommit) { _, hash in
                    if let hash { proxy.scrollTo(hash, anchor: .center) }
                }
            }
        } else {
            VStack(spacing: 0) {
                graphPreface(graph)
                graphCommits(graph, layout: layout)
            }
                .frame(maxHeight: .infinity, alignment: .top).clipped()
        }
    }

    private func graphPreface(_ graph: GitGraph) -> some View {
        VStack(spacing: 0) {
            if let upstream = graph.upstream, !upstream.isEmpty {
                HStack(spacing: Metrics.inline) {
                    Text("upstream").font(Face.mono(9, .semibold)).foregroundStyle(Ink.inkFaint)
                    Text(upstream).font(Face.mono(Metrics.caption)).foregroundStyle(Ink.inkMuted).lineLimit(1)
                    Text("↑\(graph.ahead) ↓\(graph.behind)").font(Face.mono(Metrics.caption)).foregroundStyle(Ink.inkMuted)
                    Spacer(minLength: 0)
                }.padding(.horizontal, Metrics.gutter).padding(.vertical, Metrics.headerPad)
                Hairline()
            }
        }
    }

    private func graphCommits(_ graph: GitGraph, layout: GitGraphLayout) -> some View {
        ForEach(Array(graph.commits.enumerated()), id: \.element.id) { index, commit in
            graphRow(commit, in: graph, row: layout.rows[index], columns: layout.columnCount)
                .id(commit.id)
        }
    }

    private func moveCommit(_ offset: Int, in graph: GitGraph) {
        guard scrolls, !graph.commits.isEmpty else { return }
        let current = graph.commits.firstIndex { $0.hash == git.selectedCommit }
        let index = current.map { max(0, min(graph.commits.count - 1, $0 + offset)) } ?? (offset > 0 ? 0 : graph.commits.count - 1)
        git.select(graph.commits[index], project: project)
    }

    private func graphRow(_ commit: GitGraphCommit, in graph: GitGraph, row: GitGraphLayout.Row, columns: Int) -> some View {
        let head = commit.hash == graph.observedHead
        let width = GitGraphMetrics.width(columns: columns)
        return VStack(spacing: 0) {
        Button {
            git.select(commit, project: project)
            graphFocused = true
        } label: {
            VStack(alignment: .leading, spacing: Metrics.stack) {
                HStack(spacing: Metrics.inline) {
                    Text(commit.shortHash).font(Face.mono(Metrics.meta, .semibold)).foregroundStyle(Ink.accent)
                    if head {
                        Text("HEAD").font(Face.mono(8, .semibold)).foregroundStyle(Ink.live)
                            .padding(.horizontal, 4).padding(.vertical, 1)
                            .overlay(Capsule().stroke(Ink.live, lineWidth: 1))
                    }
                    ForEach(refMarks(commit, graph: graph)) { mark in
                        Text(mark.name).font(Face.mono(8, mark.current ? .semibold : .regular))
                            .foregroundStyle(mark.current ? Ink.live : (mark.kind == "branch" ? Ink.accent : Ink.inkMuted))
                            .padding(.horizontal, 4).padding(.vertical, 1)
                            .background(mark.current ? Ink.accentSoft : (mark.kind == "branch" ? Ink.accentSoft : Ink.sunken))
                            .clipShape(Capsule())
                    }
                    Spacer(minLength: 0)
                    if let when = commit.when { Text(when).font(Face.mono(8)).foregroundStyle(Ink.inkFaint).lineLimit(1) }
                }
                Text(commit.subject).font(Face.sans(Metrics.chrome, commit.hash == git.selectedCommit ? .semibold : .regular))
                    .foregroundStyle(Ink.ink).lineLimit(2)
                HStack(spacing: Metrics.inline) {
                    if let author = commit.author { Text(author).lineLimit(1) }
                    if commit.parents.count > 1 { Text("\(commit.parents.count) parents") }
                }.font(Face.mono(Metrics.caption)).foregroundStyle(Ink.inkMuted)
            }
            .padding(.leading, width + 8).padding(.trailing, 10).padding(.vertical, Metrics.headerPad)
            .frame(maxWidth: .infinity, alignment: .leading)
            .background(alignment: .leading) {
                GitGraphColumn(row: row, head: head, merge: commit.parents.count > 1)
                    .frame(width: width).frame(maxHeight: .infinity).padding(.leading, Metrics.inline)
            }
            .background(commit.hash == git.selectedCommit ? Ink.accentSoft : Color.clear)
        }.buttonStyle(.plain).disabled(!scrolls)
            .accessibilityIdentifier("git.review.commit." + commit.hash)
        HStack(spacing: 0) {
            Color.clear.frame(width: width + 6)
            Hairline()
        }
        }
    }

    private func refMarks(_ commit: GitGraphCommit, graph: GitGraph) -> [RefMark] {
        commit.refs.map { name in
            if let ref = graph.refs.first(where: { $0.name == name && $0.target == commit.hash }) {
                return RefMark(name: ref.name, kind: ref.kind, current: ref.current)
            }
            return RefMark(name: name, kind: "ref", current: false)
        }.sorted { lhs, rhs in
            if lhs.current != rhs.current { return lhs.current }
            if lhs.kind != rhs.kind { return lhs.kind == "branch" }
            return lhs.name < rhs.name
        }
    }

    private struct RefMark: Identifiable {
        var name: String
        var kind: String
        var current: Bool
        var id: String { kind + ":" + name }
    }

    private var historicalDiffPanel: some View {
        reportDetailWidth(VStack(alignment: .leading, spacing: 0) {
            HStack(spacing: Metrics.inline) {
                Text("COMMIT DIFF").font(Face.mono(Metrics.meta, .semibold))
                if let commit = git.patch?.commit {
                    Text(String(commit.prefix(8)) + " → first parent")
                        .font(Face.mono(Metrics.caption)).foregroundStyle(Ink.inkMuted).lineLimit(1)
                } else { Text("选择 graph 节点").font(Face.sans(Metrics.meta)).foregroundStyle(Ink.inkMuted) }
                Spacer(minLength: 0)
                if scrolls, git.selectedCommit != nil { Button("关闭") { git.clearSelection() }.buttonStyle(.plain).font(Face.sans(Metrics.meta)) }
            }.padding(.horizontal, Metrics.gutter).padding(.vertical, Metrics.tabPadV).background(Ink.surface)
            Hairline()
            if git.patchLoading { message("正在读取提交差异…") }
            else if let problem = git.patchProblem { message(problem) }
            else if let patch = git.patch, patch.commit != nil {
                if patch.files.isEmpty { message("该提交没有文件差异") }
                else if scrolls {
                    ScrollView([.horizontal, .vertical]) {
                        VStack(alignment: .leading, spacing: 0) {
                            ForEach(patch.files) { file in historicalFile(file) }
                        }
                    }.textSelection(.enabled)
                } else {
                    VStack(alignment: .leading, spacing: 0) {
                        ForEach(patch.files) { file in historicalFile(file) }
                    }.frame(maxHeight: .infinity, alignment: .top).clipped()
                }
            } else { message("选择提交查看相对 first parent 的只读差异") }
        }.frame(maxWidth: .infinity, maxHeight: .infinity, alignment: .topLeading)
            .foregroundStyle(Ink.ink).background(Ink.canvas))
    }

    private func historicalFile(_ file: GitFileDiff) -> some View {
        VStack(alignment: .leading, spacing: 0) {
            patchFile(file, embedded: true)
            Hairline()
        }
    }
}

struct GitReviewDetailWidthKey: PreferenceKey {
    static var defaultValue: CGFloat = 0
    static func reduce(value: inout CGFloat, nextValue: () -> CGFloat) { value = max(value, nextValue()) }
}
