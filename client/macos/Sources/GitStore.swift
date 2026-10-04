import Combine
import Foundation

/// Only the selected worktree is cached. Each document is read through the CLI,
/// with one in-flight request per kind and a generation guard on project changes.
@MainActor
final class GitStore: ObservableObject {
    @Published private(set) var overview: GitOverview?
    @Published private(set) var tree: GitTree?
    @Published private(set) var patch: GitPatch?
    @Published private(set) var selectedPath: String?
    @Published private(set) var problem: String?
    @Published private(set) var patchProblem: String?
    @Published private(set) var loading = false
    @Published private(set) var patchLoading = false

    private let queue = GitReadQueue()
    private var rootCancellation = CLI.Cancellation()
    private var patchCancellation: CLI.Cancellation?
    private var root = ""
    private var generation = 0
    private var patchRequest = 0
    private var overviewInFlight = false
    private var treeInFlight = false
    private var lastOverviewAt = Date.distantPast
    private var lastTreeAt = Date.distantPast
    private let isFixture: Bool

    init() { isFixture = false }

    init(overview: GitOverview?, tree: GitTree? = nil, patch: GitPatch? = nil,
         selectedPath: String? = nil, problem: String? = nil, loading: Bool = false,
         patchProblem: String? = nil) {
        self.overview = overview
        self.tree = tree
        self.patch = patch
        self.selectedPath = selectedPath
        self.problem = problem
        self.patchProblem = patchProblem
        self.loading = loading
        self.root = overview?.root ?? tree?.overview.root ?? ""
        self.isFixture = true
    }

    deinit {
        rootCancellation.cancel()
        patchCancellation?.cancel()
        queue.discardPending()
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

    func clearSelection() {
        patchCancellation?.cancel()
        patchCancellation = nil
        patchRequest += 1
        selectedPath = nil
        patch = nil
        patchProblem = nil
        patchLoading = false
    }

    private func readPatch(_ path: String, root: String) {
        // The current selection's request is already pending. Do not pile up
        // another patch read on each tree poll while Git is slow.
        guard !patchLoading else { return }
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

    private func prepare(_ project: BerthGroup?) -> String? {
        let next = project?.rootDir ?? ""
        if next != root {
            rootCancellation.cancel()
            rootCancellation = CLI.Cancellation()
            generation += 1
            queue.discardPending()
            root = next
            overview = nil
            tree = nil
            problem = nil
            loading = false
            overviewInFlight = false
            treeInFlight = false
            lastOverviewAt = .distantPast
            lastTreeAt = .distantPast
            clearSelection()
        }
        return next.isEmpty ? nil : next
    }
}
