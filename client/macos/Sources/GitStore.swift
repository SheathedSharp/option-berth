import Combine
import Foundation

/// Only the selected worktree is cached. Each document is read through the CLI,
/// with one in-flight request per kind and a generation guard on project changes.
@MainActor
final class GitStore: ObservableObject {
    @Published private(set) var overview: GitOverview?
    @Published private(set) var tree: GitTree?
    @Published private(set) var graph: GitGraph?
    private(set) var graphLayout = GitGraphLayout(rows: [])
    @Published private(set) var patch: GitPatch?
    @Published private(set) var selectedPath: String?
    @Published private(set) var selectedCommit: String?
    @Published private(set) var problem: String?
    @Published private(set) var graphProblem: String?
    @Published private(set) var patchProblem: String?
    @Published private(set) var loading = false
    @Published private(set) var graphLoading = false
    @Published private(set) var patchLoading = false

    private let queue = GitReadQueue()
    private var rootCancellation = CLI.Cancellation()
    private var graphCancellation: CLI.Cancellation?
    private var patchCancellation: CLI.Cancellation?
    private var root = ""
    private var generation = 0
    private var patchRequest = 0
    private var overviewInFlight = false
    private var treeInFlight = false
    private var graphInFlight = false
    private var lastOverviewAt = Date.distantPast
    private var lastTreeAt = Date.distantPast
    private var lastGraphAt = Date.distantPast
    private let isFixture: Bool

    init() { isFixture = false }

    init(overview: GitOverview?, tree: GitTree? = nil, graph: GitGraph? = nil, patch: GitPatch? = nil,
         selectedPath: String? = nil, problem: String? = nil, loading: Bool = false,
         patchProblem: String? = nil, selectedCommit: String? = nil,
         graphProblem: String? = nil, graphLoading: Bool = false) {
        self.overview = overview
        self.tree = tree
        self.graph = graph
        self.graphLayout = Self.layout(of: graph)
        self.patch = patch
        self.selectedPath = selectedPath
        self.selectedCommit = selectedCommit
        self.problem = problem
        self.graphProblem = graphProblem
        self.patchProblem = patchProblem
        self.loading = loading
        self.graphLoading = graphLoading
        self.root = overview?.root ?? tree?.overview.root ?? graph?.root ?? ""
        self.isFixture = true
    }

    deinit {
        rootCancellation.cancel()
        graphCancellation?.cancel()
        patchCancellation?.cancel()
        queue.discardPending()
    }

    /// Compare the requested root, not the CLI's canonical output path. This is
    /// a pure ownership check for the render before navigation's reload fires.
    func isFor(_ project: BerthGroup) -> Bool {
        let candidate = project.rootDir ?? ""
        return !candidate.isEmpty && candidate == root
    }

    func refresh(project: BerthGroup?, force: Bool = false) {
        guard !isFixture, let requestedRoot = prepare(project) else { return }
        guard !overviewInFlight,
              force || Date().timeIntervalSince(lastOverviewAt) >= 5 else { return }
        overviewInFlight = true
        lastOverviewAt = Date()
        let requestGeneration = generation
        queue.submit(.overview) { [weak self, cancellation = rootCancellation] in
            let result = CLI.decode(GitOverview.self, arguments: ["git", requestedRoot, "--json"], cancellation: cancellation)
            Task { @MainActor [weak self] in
                guard let self, self.generation == requestGeneration else { return }
                self.overviewInFlight = false
                switch result {
                case .success(let value): self.overview = value; self.problem = nil
                case .failure(let error): self.problem = error.message
                }
            }
        }
    }

    func loadTree(project: BerthGroup?, force: Bool = false) {
        guard !isFixture, let requestedRoot = prepare(project) else { return }
        guard !treeInFlight,
              force || Date().timeIntervalSince(lastTreeAt) >= 5 else { return }
        treeInFlight = true
        lastTreeAt = Date()
        loading = true
        let requestGeneration = generation
        queue.submit(.tree) { [weak self, cancellation = rootCancellation] in
            let result = CLI.decode(GitTree.self, arguments: ["git", "files", requestedRoot, "--json"], cancellation: cancellation)
            Task { @MainActor [weak self] in
                guard let self, self.generation == requestGeneration else { return }
                self.treeInFlight = false
                self.loading = false
                switch result {
                case .success(let value):
                    self.tree = value
                    self.overview = value.overview
                    self.problem = nil
                    self.lastOverviewAt = Date()
                    if let path = self.selectedPath {
                        if value.files.contains(where: { $0.path == path }) {
                            self.readPatch(path, root: requestedRoot)
                        } else {
                            self.clearSelection()
                        }
                    }
                case .failure(let error): self.problem = error.message
                }
            }
        }
    }

    func select(_ file: GitFile, project: BerthGroup?) {
        guard !isFixture, let projectRoot = prepare(project) else { return }
        clearSelection()
        selectedPath = file.path
        patch = nil
        readPatch(file.path, root: projectRoot)
    }

    func select(_ commit: GitGraphCommit, project: BerthGroup?) {
        guard !isFixture, let projectRoot = prepare(project) else { return }
        beginCommitSelection(commit.hash)
        readCommitPatch(commit.hash, root: projectRoot)
    }

    private func beginCommitSelection(_ hash: String) {
        patchCancellation?.cancel()
        patchCancellation = nil
        patchRequest += 1
        selectedPath = nil
        selectedCommit = hash
        patch = nil
        patchProblem = nil
        patchLoading = true
    }

    func clearSelection() {
        patchCancellation?.cancel()
        patchCancellation = nil
        patchRequest += 1
        selectedPath = nil
        selectedCommit = nil
        patch = nil
        patchProblem = nil
        patchLoading = false
    }

    private func readPatch(_ path: String, root: String) {
        patchRequest += 1
        let request = patchRequest
        let requestGeneration = generation
        patchLoading = true
        patchProblem = nil
        let cancellation = CLI.Cancellation()
        patchCancellation?.cancel()
        patchCancellation = cancellation
        queue.submit(.patch) { [weak self] in
            let result = CLI.decode(GitPatch.self,
                                    arguments: ["git", "diff", root, "--json", "--file", path],
                                    cancellation: cancellation)
            Task { @MainActor [weak self] in
                guard let self, self.generation == requestGeneration,
                      self.patchRequest == request else { return }
                self.patchLoading = false
                switch result {
                case .success(let value): self.patch = value; self.patchProblem = nil
                case .failure(let error): self.patch = nil; self.patchProblem = error.message
                }
            }
        }
    }

    private func readCommitPatch(_ commit: String, root: String) {
        patchRequest += 1
        let request = patchRequest
        let requestGeneration = generation
        patchLoading = true
        patchProblem = nil
        let cancellation = CLI.Cancellation()
        patchCancellation?.cancel()
        patchCancellation = cancellation
        queue.submit(.patch) { [weak self] in
            let result = CLI.decode(GitPatch.self,
                                    arguments: ["git", "diff", root, "--json", "--commit", commit],
                                    cancellation: cancellation)
            Task { @MainActor [weak self] in
                guard let self, self.generation == requestGeneration,
                      self.patchRequest == request else { return }
                self.patchLoading = false
                switch result {
                case .success(let value): self.patch = value; self.patchProblem = nil
                case .failure(let error): self.patch = nil; self.patchProblem = error.message
                }
            }
        }
    }

    func loadGraph(project: BerthGroup?, force: Bool = false) {
        guard !isFixture, let requestedRoot = prepare(project) else { return }
        guard !graphInFlight,
              force || Date().timeIntervalSince(lastGraphAt) >= 5 else { return }
        graphInFlight = true
        lastGraphAt = Date()
        graphLoading = true
        graphProblem = nil
        let requestGeneration = generation
        let cancellation = CLI.Cancellation()
        graphCancellation?.cancel()
        graphCancellation = cancellation
        queue.submit(.graph) { [weak self] in
            let result = CLI.decode(GitGraph.self,
                                    arguments: ["git", "graph", "--json", requestedRoot],
                                    cancellation: cancellation)
            Task { @MainActor [weak self] in
                guard let self, self.generation == requestGeneration else { return }
                self.graphInFlight = false
                self.graphLoading = false
                switch result {
                case .success(let value):
                    self.adopt(value)
                case .failure(let error):
                    self.adopt(nil)
                    self.graphProblem = Self.graphFailureMessage(error)
                }
            }
        }
    }

    private func adopt(_ graph: GitGraph?) {
        graphLayout = Self.layout(of: graph)
        self.graph = graph
        if graph != nil { graphProblem = nil }
    }

    private static func layout(of graph: GitGraph?) -> GitGraphLayout {
        guard let graph else { return GitGraphLayout(rows: []) }
        return GitGraphLayout.layout(graph.commits.map { GitGraphNode(hash: $0.hash, parents: $0.parents) })
    }

    private static func graphFailureMessage(_ error: CLI.Failure) -> String {
        if error.message.contains("Exit 2") {
            return "无法读取提交图：当前 oberth 过旧或不支持 git graph（退出码 2）。请在仓库根目录运行 mage install，或将 BERTH_BIN 指向与本客户端同版本构建的 oberth。"
        }
        return error.message
    }

    private func prepare(_ project: BerthGroup?) -> String? {
        let next = project?.rootDir ?? ""
        if next != root {
            rootCancellation.cancel()
            graphCancellation?.cancel()
            rootCancellation = CLI.Cancellation()
            generation += 1
            queue.discardPending()
            root = next
            overview = nil
            tree = nil
            graphLayout = GitGraphLayout(rows: [])
            graph = nil
            problem = nil
            graphProblem = nil
            loading = false
            graphLoading = false
            overviewInFlight = false
            treeInFlight = false
            graphInFlight = false
            lastOverviewAt = .distantPast
            lastTreeAt = .distantPast
            lastGraphAt = .distantPast
            clearSelection()
        }
        return next.isEmpty ? nil : next
    }
}
