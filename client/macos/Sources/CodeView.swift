import SwiftUI

/// The selected worktree's read-only code changes, backed by oberth JSON.
struct CodeView: View {
    @ObservedObject var git: GitStore
    let project: BerthGroup
    var scrolls = true

    @ClientDetailWidth(.git) private var detailWidth: Double
    private static let defaultDetailWidth = ClientDetailPane.defaultWidth
    private static let detailRange = ClientDetailPane.range
    private static let minimumMainWidth: CGFloat = 340

    var body: some View {
        VStack(spacing: 0) {
            if let overview = git.overview {
                gitOverview(overview)
                Hairline()
            }
            if let problem = git.problem, git.overview == nil {
                VStack(alignment: .leading, spacing: 8) {
                    Text("读不到 Git 状态")
                        .font(Face.sans(12, .medium))
                        .foregroundStyle(Ink.ink)
                    Text(problem)
                        .font(Face.mono(10.5))
                        .foregroundStyle(Ink.inkFaint)
                        .fixedSize(horizontal: false, vertical: true)
                }
                .padding(Metrics.gutter)
                .frame(maxWidth: .infinity, alignment: .leading)
                Spacer(minLength: 0)
            } else if git.loading && git.tree == nil {
                Text("正在读取变更…")
                    .font(Face.sans(11.5))
                    .foregroundStyle(Ink.inkFaint)
                    .frame(maxWidth: .infinity, maxHeight: .infinity, alignment: .topLeading)
                    .padding(Metrics.gutter)
            } else if let tree = git.tree {
                codeRows(tree.files, project: project)
            } else {
                Text("打开代码页后读取当前变更")
                    .font(Face.sans(11.5))
                    .foregroundStyle(Ink.inkFaint)
                    .frame(maxWidth: .infinity, maxHeight: .infinity, alignment: .topLeading)
                    .padding(Metrics.gutter)
            }
        }
        .onAppear(perform: normalizeDetailWidth)
    }

    private func gitOverview(_ overview: GitOverview) -> some View {
        VStack(alignment: .leading, spacing: 7) {
            HStack(spacing: 8) {
                Text(overview.branchName)
                    .font(Face.mono(12, .semibold))
                    .foregroundStyle(Ink.ink)
                if !overview.shortHead.isEmpty {
                    Text(overview.shortHead)
                        .font(Face.mono(10))
                        .foregroundStyle(Ink.inkFaint)
                }
                Spacer()
                Text(overview.clean ? "clean" : "有未提交变更")
                    .font(Face.mono(10, .medium))
                    .foregroundStyle(overview.clean ? Ink.live : Change.changed)
            }
            HStack(spacing: 12) {
                gitCount("暂存", overview.staged)
                gitCount("未暂存", overview.unstaged)
                gitCount("未跟踪", overview.untracked)
                if overview.conflicts > 0 { gitCount("冲突", overview.conflicts) }
                Spacer()
                if overview.ahead > 0 { Text("↑\(overview.ahead)").foregroundStyle(Ink.accent) }
                if overview.behind > 0 { Text("↓\(overview.behind)").foregroundStyle(Change.changed) }
            }
            .font(Face.mono(10))
            if let commit = overview.lastCommit {
                Text("最近提交  \(commit.subject)")
                    .font(Face.sans(10.5))
                    .foregroundStyle(Ink.inkMuted)
                    .lineLimit(1)
            }
            if overview.worktrees.count > 1 {
                Text("同仓库 worktree  \(overview.worktrees.count) 个")
                    .font(Face.mono(9.5))
                    .foregroundStyle(Ink.inkFaint)
            }
        }
        .padding(.horizontal, Metrics.gutter)
        .padding(.vertical, 10)
        .background(Ink.surface)
    }

    private func gitCount(_ label: String, _ count: Int) -> some View {
        HStack(spacing: 4) {
            Text(verbatim: "\(count)").foregroundStyle(count > 0 ? Ink.ink : Ink.inkFaint)
            Text(label).foregroundStyle(Ink.inkFaint)
        }
    }

    private func codeRows(_ files: [GitFile], project: BerthGroup) -> some View {
        GeometryReader { space in
            let range = detailRange(for: space.size.width)
            let width = resolvedDetailWidth(for: range)
            HStack(spacing: 0) {
                fileList(files, project: project)
                    .frame(width: max(0, space.size.width - width - SplitHandle.hitWidth),
                           alignment: .topLeading)
                SplitHandle(width: $detailWidth, range: range, controlsTrailingPane: true)
                diffPanel
                    .frame(width: width, alignment: .topLeading)
                    .frame(maxHeight: .infinity, alignment: .topLeading)
            }
        }
    }

    private func detailRange(for availableWidth: CGFloat) -> ClosedRange<Double> {
        let maximum = min(Self.detailRange.upperBound,
                          Double(max(0, availableWidth - Self.minimumMainWidth - SplitHandle.hitWidth)))
        let minimum = min(Self.detailRange.lowerBound, maximum)
        return minimum...maximum
    }

    private func resolvedDetailWidth(for range: ClosedRange<Double>) -> CGFloat {
        if detailWidth < Self.detailRange.lowerBound {
            return CGFloat(min(Self.defaultDetailWidth, range.upperBound))
        }
        return CGFloat(min(max(detailWidth, range.lowerBound), range.upperBound))
    }

    private func normalizeDetailWidth() {
        if detailWidth < Self.detailRange.lowerBound {
            detailWidth = Self.defaultDetailWidth
        } else if detailWidth > Self.detailRange.upperBound {
            detailWidth = Self.detailRange.upperBound
        }
    }

    private func fileList(_ files: [GitFile], project: BerthGroup) -> some View {
        VStack(spacing: 0) {
            HStack {
                ColumnHead(files.isEmpty ? "工作树干净" : "变更文件")
                Spacer()
                if let problem = git.problem {
                    Text(problem).font(Face.sans(10)).foregroundStyle(Change.changed).lineLimit(1)
                }
            }
            .padding(.horizontal, Metrics.gutter)
            .padding(.vertical, 6)
            .background(Ink.surface)
            Hairline()
            if files.isEmpty {
                Text("没有需要查看的文件")
                    .font(Face.sans(11.5))
                    .foregroundStyle(Ink.inkFaint)
                    .frame(maxWidth: .infinity, maxHeight: .infinity, alignment: .topLeading)
                    .padding(Metrics.gutter)
            } else if scrolls {
                ScrollView { fileRows(files, project: project) }
            } else {
                GeometryReader { space in
                    fileRows(files, project: project)
                        .frame(width: space.size.width, height: space.size.height,
                               alignment: .topLeading)
                        .clipped()
                }
            }
        }
        .frame(maxWidth: .infinity, maxHeight: .infinity,
               alignment: .topLeading)
        .background(Ink.canvas)
    }

    private var diffPanel: some View {
        VStack(spacing: 0) {
            HStack {
                ColumnHead("diff")
                Spacer()
                if git.patch != nil || git.patchLoading || git.patchProblem != nil {
                    Button("关闭") { git.clearSelection() }
                        .buttonStyle(.plain)
                        .font(Face.sans(10))
                        .foregroundStyle(Ink.inkMuted)
                }
            }
            .padding(.horizontal, Metrics.gutter)
            .padding(.vertical, 6)
            .background(Ink.surface)
            Hairline()
            if let patch = git.patch, let file = patch.files.first {
                patchView(file)
            } else if git.patchLoading {
                Text("正在读取 diff…")
                    .font(Face.mono(10.5))
                    .foregroundStyle(Ink.inkFaint)
                    .frame(maxWidth: .infinity, maxHeight: .infinity,
                           alignment: .topLeading)
                    .padding(Metrics.gutter)
            } else if let problem = git.patchProblem {
                Text(problem)
                    .font(Face.mono(10.5))
                    .foregroundStyle(Change.changed)
                    .frame(maxWidth: .infinity, maxHeight: .infinity,
                           alignment: .topLeading)
                    .padding(Metrics.gutter)
            } else {
                Text("选择一个文件查看 diff")
                    .font(Face.sans(11.5))
                    .foregroundStyle(Ink.inkFaint)
                    .frame(maxWidth: .infinity, maxHeight: .infinity,
                           alignment: .topLeading)
                    .padding(Metrics.gutter)
            }
        }
        .frame(maxWidth: .infinity, maxHeight: .infinity,
               alignment: .topLeading)
        .background(Ink.canvas)
    }

    private func fileRows(_ files: [GitFile], project: BerthGroup) -> some View {
        VStack(spacing: 0) {
            ForEach(files) { file in
                Button {
                    git.select(file, project: project)
                } label: {
                    HStack(spacing: 8) {
                        Text(file.status)
                            .font(Face.mono(10, .medium))
                            .foregroundStyle(file.status == "??" ? Ink.live : Change.changed)
                            .frame(width: 28, alignment: .leading)
                        Text(file.displayPath)
                            .font(Face.mono(10.5))
                            .foregroundStyle(Ink.ink)
                            .lineLimit(1)
                            .truncationMode(.middle)
                        Spacer(minLength: 4)
                        if file.binary {
                            Text("binary").font(Face.mono(9.5)).foregroundStyle(Ink.inkFaint)
                        } else {
                            if let additions = file.additions {
                                Text(verbatim: "+\(additions)").font(Face.mono(9.5)).foregroundStyle(Ink.live)
                            }
                            if let deletions = file.deletions {
                                Text(verbatim: "−\(deletions)").font(Face.mono(9.5)).foregroundStyle(Change.removed)
                            }
                        }
                    }
                    .padding(.horizontal, Metrics.gutter)
                    .frame(minHeight: Metrics.rowHeight)
                    .contentShape(Rectangle())
                }
                .buttonStyle(.plain)
                Hairline()
            }
        }
    }

    private func patchView(_ file: GitFileDiff) -> some View {
        VStack(alignment: .leading, spacing: 0) {
            HStack {
                Text(file.path).font(Face.mono(10.5, .medium)).foregroundStyle(Ink.ink)
                Spacer()
            }
            .padding(.horizontal, Metrics.gutter)
            .padding(.vertical, 5)
            patchLines(file)
        }
        .frame(maxWidth: .infinity, maxHeight: .infinity,
               alignment: .topLeading)
        .background(Ink.canvas)
    }

    @ViewBuilder
    private func patchLines(_ file: GitFileDiff) -> some View {
        let lines = VStack(alignment: .leading, spacing: 0) {
            if file.binary {
                Text("二进制文件，没有可显示的行")
                    .font(Face.mono(10.5))
                    .foregroundStyle(Ink.inkFaint)
            } else {
                ForEach(Array(file.lines.enumerated()), id: \.offset) { _, line in
                    Text(line.text)
                        .font(Face.mono(10))
                        .foregroundStyle(patchColor(line.kind))
                        .frame(maxWidth: .infinity, alignment: .leading)
                }
            }
        }
        .padding(.horizontal, Metrics.gutter)
        if scrolls {
            ScrollView { lines }
        } else {
            GeometryReader { space in
                lines
                    .frame(width: space.size.width, height: space.size.height,
                           alignment: .topLeading)
                    .clipped()
            }
        }
    }

    private func patchColor(_ kind: String) -> Color {
        switch kind {
        case "add": return Ink.live
        case "del": return Change.removed
        case "hunk": return Ink.accent
        case "meta": return Ink.inkFaint
        default: return Ink.inkMuted
        }
    }

}
