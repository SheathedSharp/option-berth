import AppKit
import SwiftUI

enum GitReviewMode: String, CaseIterable, Identifiable {
    case changes, history
    var id: String { rawValue }
    var title: String { self == .changes ? "Changes" : "History" }
}

/// One read-only review surface for the selected worktree. All facts and patches
/// come from GitStore/oberth, never a second client-side Git process.
struct CodeView: View {
    @ObservedObject var git: GitStore
    let project: BerthGroup
    var scrolls = true
    @ClientDetailWidth(.git) private var detailWidth
    @State private var query = ""
    @State private var filter = GitReviewFilter.all
    @State private var mode = GitReviewMode.changes
    @State private var showingWorktrees = false
    @FocusState private var searching: Bool
    private var files: [GitFile] { git.isFor(project) ? (git.tree?.files ?? []) : [] }
    private var visible: [GitFile] { filter.files(files, query: query) }

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

    private var reviewContent: some View {
        VStack(spacing: 0) {
            if let overview = git.overview { overviewHeader(overview); Hairline() }
            if let problem = git.problem {
                HStack(alignment: .top) {
                    Image(systemName: "exclamationmark.triangle")
                    Text((git.tree == nil ? "读取失败：" : "刷新失败，当前为上次结果：") + problem).textSelection(.enabled)
                    Spacer(minLength: 0)
                    Button("重试") { git.loadTree(project: project, force: true) }.disabled(git.loading || !scrolls)
                }.font(Face.sans(11)).foregroundStyle(Change.changed).padding(10).background(Ink.surface)
            }
            if git.tree != nil {
                reviewModeBar
                Hairline()
                if mode == .history {
                    graphPanel
                } else {
                    filterBar
                    Hairline()
                    changesPanel
                }
            } else if mode == .history {
                reviewModeBar
                Hairline()
                graphPanel
            } else {
                Text(git.loading ? "正在读取变更…" : "Git 状态暂不可用")
                    .font(Face.sans(12)).foregroundStyle(Ink.inkMuted)
                    .frame(maxWidth: .infinity, maxHeight: .infinity, alignment: .topLeading).padding(16)
            }
        }
    }

    private var changesPanel: some View {
        GeometryReader { geometry in
                    if geometry.size.width >= 740 {
                        if scrolls {
                            let maximum = min(500.0, Double(max(0, geometry.size.width - 340 - SplitHandle.hitWidth)))
                            let range = min(340.0, maximum)...maximum
                            let width = min(max(detailWidth, range.lowerBound), range.upperBound)
                            HStack(spacing: 0) {
                                fileList.frame(width: max(0, geometry.size.width - width - SplitHandle.hitWidth))
                                SplitHandle(width: $detailWidth, range: range, controlsTrailingPane: true)
                                diffPanel.frame(width: width)
                            }
                        } else {
                            HStack(spacing: 0) {
                                fileList.frame(width: geometry.size.width * 0.40)
                                Hairline(axis: .vertical)
                                diffPanel
                            }
                        }
                    } else if scrolls {
                        // Width is a preference for side-by-side review. Small
                        // windows keep both panes readable in a native vertical split.
                        VSplitView {
                            fileList.frame(minHeight: 85, idealHeight: 150)
                            diffPanel.frame(minHeight: 100, maxHeight: .infinity)
                        }
                    } else {
                        VStack(spacing: 0) {
                            fileList.frame(height: max(70, geometry.size.height * 0.43))
                            Hairline()
                            diffPanel
                        }
                    }
                }
        }
    }

    private var reviewModeBar: some View {
        HStack(spacing: 4) {
            ForEach(GitReviewMode.allCases) { item in
                Button {
                    mode = item
                } label: {
                    HStack(spacing: 5) {
                        Image(systemName: item == .changes ? "square.stack.3d.up" : "point.3.filled.connected.trianglepath.dotted")
                        Text(item.title)
                        if item == .history, let graph = git.graph { Text("\(graph.commits.count)").foregroundStyle(Ink.inkFaint) }
                    }
                    .font(Face.sans(10, mode == item ? .semibold : .regular))
                    .padding(.horizontal, 9).padding(.vertical, 6)
                    .foregroundStyle(mode == item ? Ink.accent : Ink.inkMuted)
                    .background(mode == item ? Ink.accentSoft : Color.clear)
                    .clipShape(RoundedRectangle(cornerRadius: 5))
                }.buttonStyle(.plain).disabled(!scrolls)
                    .accessibilityIdentifier("git.review.mode." + item.rawValue)
            }
            Spacer(minLength: 0)
            if mode == .history, git.graphLoading { ProgressView().controlSize(.small) }
        }.padding(.horizontal, 8).padding(.vertical, 4).background(Ink.surface)
    }

    private var graphPanel: some View {
        GeometryReader { geometry in
            if geometry.size.width >= 740 {
                let maximum = min(500.0, Double(max(0, geometry.size.width - 340 - SplitHandle.hitWidth)))
                let range = min(340.0, maximum)...maximum
                let width = min(max(detailWidth, range.lowerBound), range.upperBound)
                HStack(spacing: 0) {
                    graphList.frame(width: max(0, geometry.size.width - width - SplitHandle.hitWidth))
                    SplitHandle(width: $detailWidth, range: range, controlsTrailingPane: true)
                    historicalDiffPanel.frame(width: width)
                }
            } else if scrolls {
                VSplitView {
                    graphList.frame(minHeight: 150, idealHeight: 250)
                    historicalDiffPanel.frame(minHeight: 120, maxHeight: .infinity)
                }
            } else {
                VStack(spacing: 0) {
                    graphList.frame(height: max(100, geometry.size.height * 0.52))
                    Hairline()
                    historicalDiffPanel
                }
            }
        }
    }

    private var graphList: some View {
        VStack(spacing: 0) {
            HStack(spacing: 8) {
                Image(systemName: "point.3.filled.connected.trianglepath.dotted").foregroundStyle(Ink.accent)
                if let graph = git.graph {
                    Text(graph.branchName).font(Face.mono(11, .semibold)).lineLimit(1)
                    if !graph.shortHead.isEmpty { Text(graph.shortHead).font(Face.mono(9)).foregroundStyle(Ink.inkMuted) }
                    Spacer(minLength: 0)
                    if graph.truncated { Text("前 \(graph.limit) 条").font(Face.mono(9)).foregroundStyle(Change.changed) }
                    Text("\(graph.commits.count) commits").font(Face.mono(9)).foregroundStyle(Ink.inkMuted)
                } else {
                    Text("Git graph").font(Face.mono(11, .semibold))
                    Spacer(minLength: 0)
                }
                Button { git.loadGraph(project: project, force: true) } label: { Image(systemName: "arrow.clockwise") }
                    .buttonStyle(.plain).disabled(git.graphLoading || !scrolls).help("刷新只读 graph，不自动 fetch")
            }.padding(.horizontal, 10).padding(.vertical, 8).background(Ink.surface)
            Hairline()
            if let problem = git.graphProblem {
                VStack(alignment: .leading, spacing: 8) {
                    Label("Graph 读取失败", systemImage: "exclamationmark.triangle")
                        .foregroundStyle(Change.changed)
                    Text(problem).font(Face.sans(11)).foregroundStyle(Ink.inkMuted).textSelection(.enabled)
                    Button("重试") { git.loadGraph(project: project, force: true) }
                        .buttonStyle(.bordered).controlSize(.small).disabled(git.graphLoading || !scrolls)
                }.frame(maxWidth: .infinity, maxHeight: .infinity, alignment: .topLeading).padding(14)
            } else if git.graphLoading && git.graph == nil {
                message("正在读取有界提交 graph…")
            } else if let graph = git.graph {
                ScrollViewReader { proxy in
                    ScrollView {
                        LazyVStack(spacing: 0) {
                            if let upstream = graph.upstream, !upstream.isEmpty {
                                HStack(spacing: 8) {
                                    Text("upstream").font(Face.mono(9, .semibold)).foregroundStyle(Ink.inkFaint)
                                    Text(upstream).font(Face.mono(9)).foregroundStyle(Ink.inkMuted).lineLimit(1)
                                    Text("↑\(graph.ahead) ↓\(graph.behind)").font(Face.mono(9)).foregroundStyle(Ink.inkMuted)
                                    Spacer(minLength: 0)
                                }.padding(.horizontal, 11).padding(.vertical, 7)
                                Hairline()
                            }
                            ForEach(Array(graph.commits.enumerated()), id: \.element.id) { index, commit in
                                graphRow(commit, index: index, total: graph.commits.count)
                                    .id(commit.id)
                            }
                        }
                    }
                    .onChange(of: git.selectedCommit) { _, hash in
                        if let hash { proxy.scrollTo(hash, anchor: .center) }
                    }
                }
            } else {
                message("没有可用的提交 graph")
            }
        }.background(Ink.canvas)
    }

    @ViewBuilder private func graphRow(_ commit: GitGraphCommit, index: Int, total: Int) -> some View {
        Button { git.select(commit, project: project) } label: {
            HStack(alignment: .top, spacing: 9) {
                VStack(spacing: 0) {
                    Circle().fill(commit.hash == git.graph?.observedHead ? Ink.accent : Ink.inkMuted)
                        .frame(width: 8, height: 8).padding(.top, 4)
                    if index + 1 < total { Rectangle().fill(Ink.line).frame(width: 1, height: 34) }
                }.frame(width: 14)
                VStack(alignment: .leading, spacing: 4) {
                    HStack(spacing: 7) {
                        Text(commit.shortHash).font(Face.mono(10, .semibold)).foregroundStyle(Ink.accent)
                        if commit.hash == git.graph?.observedHead {
                            Text("HEAD").font(Face.mono(8, .semibold)).foregroundStyle(Ink.live)
                        }
                        ForEach(commit.refs, id: \.self) { ref in
                            Text(ref).font(Face.mono(8)).foregroundStyle(Ink.accent)
                                .padding(.horizontal, 4).padding(.vertical, 2)
                                .background(Ink.accentSoft).clipShape(Capsule())
                        }
                        Spacer(minLength: 0)
                        if let when = commit.when { Text(when).font(Face.mono(8)).foregroundStyle(Ink.inkFaint).lineLimit(1) }
                    }
                    Text(commit.subject).font(Face.sans(11, commit.hash == git.selectedCommit ? .semibold : .regular))
                        .foregroundStyle(Ink.ink).lineLimit(2)
                    HStack(spacing: 8) {
                        if let author = commit.author { Text(author).lineLimit(1) }
                        if !commit.parents.isEmpty { Text(commit.parents.count == 1 ? "1 parent" : "\(commit.parents.count) parents") }
                    }.font(Face.mono(9)).foregroundStyle(Ink.inkMuted)
                }
            }.padding(.horizontal, 10).padding(.vertical, 8)
                .frame(maxWidth: .infinity, alignment: .leading)
                .background(commit.hash == git.selectedCommit ? Ink.accentSoft : Color.clear)
        }.buttonStyle(.plain).disabled(!scrolls)
            .accessibilityIdentifier("git.review.commit." + commit.hash)
        Hairline()
    }

    private var historicalDiffPanel: some View {
        VStack(alignment: .leading, spacing: 0) {
            HStack(spacing: 8) {
                Text("COMMIT DIFF").font(Face.mono(10, .semibold))
                if let commit = git.patch?.commit {
                    Text(String(commit.prefix(8)) + " → first parent")
                        .font(Face.mono(9)).foregroundStyle(Ink.inkMuted).lineLimit(1)
                } else { Text("选择 graph 节点").font(Face.sans(10)).foregroundStyle(Ink.inkMuted) }
                Spacer(minLength: 0)
                if scrolls, git.selectedCommit != nil { Button("关闭") { git.clearSelection() }.buttonStyle(.plain).font(Face.sans(10)) }
            }.padding(10).background(Ink.surface)
            Hairline()
            if git.patchLoading { message("正在读取提交差异…") }
            else if let problem = git.patchProblem { message(problem) }
            else if let patch = git.patch, patch.commit != nil {
                if patch.files.isEmpty { message("该提交没有文件差异") }
                else if scrolls {
                    ScrollView([.horizontal, .vertical]) {
                        VStack(alignment: .leading, spacing: 12) {
                            ForEach(patch.files) { file in historicalFile(file) }
                        }.padding(.bottom, 12)
                    }.textSelection(.enabled)
                } else {
                    VStack(alignment: .leading, spacing: 8) {
                        ForEach(patch.files) { file in historicalFile(file) }
                    }.frame(maxHeight: .infinity, alignment: .top).clipped()
                }
            } else { message("选择提交查看相对 first parent 的只读差异") }
        }.frame(maxWidth: .infinity, maxHeight: .infinity, alignment: .topLeading)
            .foregroundStyle(Ink.ink).background(Ink.canvas)
    }

    private func historicalFile(_ file: GitFileDiff) -> some View {
        VStack(alignment: .leading, spacing: 0) {
            Text(file.oldPath.map { "\($0) → \(file.path)" } ?? file.path)
                .font(Face.mono(11, .medium)).lineLimit(2).padding(.horizontal, 10).padding(.vertical, 7)
                .frame(maxWidth: .infinity, alignment: .leading).background(Ink.surface)
            if file.binary { message("二进制文件，没有可显示的文本差异").frame(minHeight: 60) }
            else if file.lines.isEmpty { message("当前没有可显示的文本差异").frame(minHeight: 60) }
            else { patchLines(file).padding(.bottom, 5) }
        }.overlay(RoundedRectangle(cornerRadius: 5).stroke(Ink.line, lineWidth: 1))
    }

    private func overviewHeader(_ overview: GitOverview) -> some View {
        VStack(alignment: .leading, spacing: 7) {
            HStack(spacing: 8) {
                Label(overview.branchName, systemImage: "arrow.triangle.branch")
                    .font(Face.mono(12, .semibold)).lineLimit(1)
                Text(overview.shortHead.isEmpty ? "尚无提交" : overview.shortHead)
                    .font(Face.mono(10)).foregroundStyle(Ink.inkMuted).textSelection(.enabled)
                Spacer(minLength: 0)
                if git.loading && scrolls { ProgressView().controlSize(.small) }
                Text(!overview.countsComplete ? "计数未知" : (overview.clean ? "干净" : "有变更"))
                    .font(Face.sans(10)).foregroundStyle(overview.clean ? Ink.live : Change.changed)
                if scrolls { Button { git.loadTree(project: project, force: true) } label: { Image(systemName: "arrow.clockwise") }
                    .buttonStyle(.plain).disabled(git.loading).help("刷新当前 worktree，不自动 fetch") }
            }
            HStack(spacing: 12) {
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
            }.font(Face.mono(10)).foregroundStyle(Ink.inkMuted)
            HStack(spacing: 8) {
                if let commit = overview.lastCommit {
                    Text(commit.subject).font(Face.sans(11)).lineLimit(1)
                        .help(commit.hash + (commit.when.map { " · " + $0 } ?? ""))
                }
                Spacer(minLength: 0)
                if !overview.worktrees.isEmpty {
                    Button { showingWorktrees.toggle() } label: {
                        Label("Worktrees · \(overview.worktrees.count)", systemImage: showingWorktrees ? "chevron.up" : "chevron.down")
                    }.buttonStyle(.plain).font(Face.sans(10)).disabled(!scrolls)
                        .accessibilityIdentifier("git.review.worktrees")
                }
            }.foregroundStyle(Ink.inkMuted)
            if showingWorktrees {
                ScrollView {
                    VStack(alignment: .leading, spacing: 5) {
                        ForEach(overview.worktrees) { worktree in
                            HStack(spacing: 8) {
                                Image(systemName: worktree.current ? "circle.inset.filled" : "circle")
                                Text(worktree.bare ? "bare" : worktree.displayBranch).frame(minWidth: 70, alignment: .leading)
                                Text(worktree.path).lineLimit(1).truncationMode(.middle)
                                Spacer(minLength: 0)
                                Text(String((worktree.head ?? "").prefix(8)))
                            }.font(Face.mono(10)).textSelection(.enabled)
                        }
                    }
                }.frame(maxHeight: 90)
            }
        }.padding(.horizontal, 12).padding(.vertical, 10).foregroundStyle(Ink.ink).background(Ink.surface)
    }

    private var filterBar: some View {
        HStack(spacing: 4) {
            ForEach(GitReviewFilter.allCases) { item in
                let count = files.filter(item.matches).count
                Button { filter = item } label: {
                    Text(item.title + " \(count)").font(Face.sans(10, filter == item ? .semibold : .regular))
                        .padding(.horizontal, 8).padding(.vertical, 6)
                        .foregroundStyle(filter == item ? Ink.accent : Ink.inkMuted)
                        .background(filter == item ? Ink.accentSoft : Color.clear)
                        .clipShape(RoundedRectangle(cornerRadius: 5))
                }.buttonStyle(.plain).disabled(!scrolls)
                    .accessibilityIdentifier("git.review.filter." + item.rawValue)
            }
            Spacer(minLength: 0)
        }.padding(.horizontal, 8).padding(.vertical, 4)
    }

    private var fileList: some View {
        VStack(spacing: 0) {
            HStack(spacing: 8) {
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
            }.font(Face.sans(11)).padding(10).background(Ink.surface)
            Hairline()
            if visible.isEmpty {
                Text(files.isEmpty ? "没有变更文件" : "没有匹配的文件")
                    .font(Face.sans(12)).foregroundStyle(Ink.inkMuted)
                    .frame(maxWidth: .infinity, maxHeight: .infinity, alignment: .topLeading).padding(14)
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
                HStack(spacing: 8) {
                    Text(file.status).font(Face.mono(10, .medium)).frame(width: 24)
                        .foregroundStyle(file.isConflict ? Change.removed : Ink.accent)
                    VStack(alignment: .leading, spacing: 3) {
                        Text(file.displayPath).font(Face.mono(11)).lineLimit(1).truncationMode(.middle)
                        Text(file.statusLabel).font(Face.sans(9)).foregroundStyle(Ink.inkMuted)
                    }
                    Spacer(minLength: 4)
                    if file.binary { Text("binary").font(Face.mono(9)).foregroundStyle(Ink.inkFaint) }
                    else if let plus = file.additions, let minus = file.deletions, !file.isConflict {
                        Text(verbatim: "+\(plus) −\(minus)").font(Face.mono(9)).foregroundStyle(Ink.inkMuted)
                    }
                }.padding(.horizontal, 10).padding(.vertical, 8).foregroundStyle(Ink.ink)
                    .frame(maxWidth: .infinity, alignment: .leading).contentShape(Rectangle())
                    .background(git.selectedPath == file.path ? Ink.accentSoft : Ink.canvas)
            }.buttonStyle(.plain).id(file.path).disabled(!scrolls)
                .accessibilityIdentifier("git.review.file." + file.path).help(file.displayPath + " · " + file.statusLabel)
            Hairline()
        }
    }

    private var diffPanel: some View {
        VStack(alignment: .leading, spacing: 0) {
            HStack(spacing: 8) {
                Text("DIFF").font(Face.mono(10, .semibold))
                Text(git.overview?.head == nil ? "初始变更" : "HEAD → 工作区（含暂存）")
                    .font(Face.sans(10)).foregroundStyle(Ink.inkMuted).lineLimit(1)
                Spacer(minLength: 0)
                if scrolls, git.selectedPath != nil {
                    Button("关闭") { git.clearSelection() }.buttonStyle(.plain).font(Face.sans(10))
                }
            }.padding(10).background(Ink.surface)
            Hairline()
            if git.patchLoading { message("正在读取差异…") }
            else if let problem = git.patchProblem { message(problem) }
            else if let file = git.patch?.files.first {
                Text(file.oldPath.map { "\($0) → \(file.path)" } ?? file.path)
                    .font(Face.mono(11, .medium)).lineLimit(2).textSelection(.enabled).padding(10)
                if file.binary { message("二进制文件，没有可显示的文本差异") }
                else if file.lines.isEmpty { message("当前没有可显示的文本差异") }
                else if scrolls {
                    ScrollView([.horizontal, .vertical]) { patchLines(file).textSelection(.enabled) }
                } else { patchLines(file).frame(maxHeight: .infinity, alignment: .top).clipped() }
            } else { message("选择文件查看差异") }
        }.frame(maxWidth: .infinity, maxHeight: .infinity, alignment: .topLeading)
            .foregroundStyle(Ink.ink).background(Ink.canvas)
            .background { GeometryReader { geometry in
                Color.clear.preference(key: GitReviewDetailWidthKey.self, value: geometry.size.width)
            } }
    }
    private func message(_ text: String) -> some View {
        Text(text).font(Face.sans(12)).foregroundStyle(Ink.inkMuted).textSelection(.enabled)
            .frame(maxWidth: .infinity, maxHeight: .infinity, alignment: .topLeading).padding(14)
    }
    private func patchLines(_ file: GitFileDiff) -> some View {
        VStack(alignment: .leading, spacing: 0) {
            ForEach(Array(file.lines.enumerated()), id: \.offset) { _, line in
                Text(line.text).font(Face.mono(11))
                    .foregroundStyle(line.kind == "add" ? Change.added : line.kind == "del" ? Change.removed : line.kind == "hunk" ? Ink.accent : Ink.inkMuted)
                    .fixedSize(horizontal: true, vertical: false)
                    .padding(.horizontal, 10).padding(.vertical, 1)
            }
        }
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

struct GitReviewDetailWidthKey: PreferenceKey {
    static var defaultValue: CGFloat = 0
    static func reduce(value: inout CGFloat, nextValue: () -> CGFloat) { value = max(value, nextValue()) }
}
