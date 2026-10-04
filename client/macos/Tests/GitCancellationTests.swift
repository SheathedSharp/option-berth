import Foundation
import Darwin

// Isolate the service-model boundary to the only field GitStore consumes. The
// real GitStore, queue, Git decoders and CLI runner are compiled in this test.
struct BerthGroup { let rootDir: String }

@main
enum GitCancellationTests {
    static let binary = URL(fileURLWithPath: CommandLine.arguments[0]).standardizedFileURL.path
    static func require(_ condition: @autoclosure () -> Bool, _ message: String) {
        if !condition() { fatalError(message) }
    }
    @MainActor static func eventually(_ message: String, _ condition: () -> Bool) async {
        let end = ProcessInfo.processInfo.systemUptime + 3
        while !condition(), ProcessInfo.processInfo.systemUptime < end { try? await Task.sleep(nanoseconds: 10_000_000) }
        require(condition(), message)
    }
    @MainActor static func main() async {
        if CommandLine.arguments.dropFirst().first == "git" { fixture(); return }
        let root = FileManager.default.temporaryDirectory.appendingPathComponent("git-cancel-" + UUID().uuidString)
        defer { try? FileManager.default.removeItem(at: root) }
        let slow = root.appendingPathComponent("slow-root"), fast = root.appendingPathComponent("fast-root")
        for url in [slow, fast] { try! FileManager.default.createDirectory(at: url, withIntermediateDirectories: true) }
        let previous = ProcessInfo.processInfo.environment["BERTH_BIN"]
        setenv("BERTH_BIN", binary, 1)
        defer { if let previous { setenv("BERTH_BIN", previous, 1) } else { unsetenv("BERTH_BIN") } }
        let store = GitStore()
        store.refresh(project: BerthGroup(rootDir: slow.path), force: true)
        await eventually("slow overview never started") { pid(at: slow.appendingPathComponent("overview.pid")) != nil }
        let overviewPID = pid(at: slow.appendingPathComponent("overview.pid"))!
        store.refresh(project: BerthGroup(rootDir: fast.path), force: true)
        await eventually("new worktree blocked behind obsolete read") { store.overview?.root == fast.path }
        require(reaped(overviewPID), "old overview child not reaped before next read")
        require(store.problem == nil, "cancelled old generation polluted current project")
        print("PASS: worktree switch cancels/reaps actual CLI and publishes only current generation")

        let slowFile = try! JSONDecoder().decode(GitFile.self, from: Data(#"{"path":"slow","status":" M"}"#.utf8))
        let fastFile = try! JSONDecoder().decode(GitFile.self, from: Data(#"{"path":"fast","status":" M"}"#.utf8))
        store.select(slowFile, project: BerthGroup(rootDir: fast.path))
        await eventually("slow patch never started") { pid(at: fast.appendingPathComponent("patch-slow.pid")) != nil }
        let patchPID = pid(at: fast.appendingPathComponent("patch-slow.pid"))!
        store.select(fastFile, project: BerthGroup(rootDir: fast.path))
        await eventually("new file blocked behind obsolete patch") { store.patch?.files.first?.path == "fast" }
        require(reaped(patchPID), "old patch child not reaped before next read")
        require(store.patchProblem == nil && !store.patchLoading, "stale patch result damaged current state")
        print("PASS: file switch cancels/reaps actual CLI without stale patch errors")

        try! FileManager.default.removeItem(at: slow.appendingPathComponent("overview.pid"))
        store.refresh(project: BerthGroup(rootDir: slow.path), force: true)
        await eventually("second slow read never started") { pid(at: slow.appendingPathComponent("overview.pid")) != nil }
        let removedPID = pid(at: slow.appendingPathComponent("overview.pid"))!
        store.loadTree(project: BerthGroup(rootDir: slow.path), force: true)
        store.refresh(project: nil)
        await eventually("removing selection did not reclaim CLI") { reaped(removedPID) }
        require(!FileManager.default.fileExists(atPath: slow.appendingPathComponent("tree.pid").path), "obsolete queued tree was started")
        require(store.overview == nil && store.tree == nil && !store.loading, "removed project retained stale state")
        print("PASS: removed selection discards pending work and reclaims in-flight read")

        try! FileManager.default.removeItem(at: slow.appendingPathComponent("overview.pid"))
        var transient: GitStore? = GitStore()
        transient!.refresh(project: BerthGroup(rootDir: slow.path), force: true)
        await eventually("deinit fixture never started") { pid(at: slow.appendingPathComponent("overview.pid")) != nil }
        let finalPID = pid(at: slow.appendingPathComponent("overview.pid"))!
        transient = nil
        await eventually("store deinit did not cancel/reap owned read") { reaped(finalPID) }
        print("PASS: store destruction cancels its owned command")
    }
    static func pid(at url: URL) -> pid_t? { (try? String(contentsOf: url, encoding: .utf8)).flatMap { pid_t($0) } }
    static func reaped(_ pid: pid_t) -> Bool {
        var info = siginfo_t()
        return waitid(P_PID, id_t(pid), &info, WEXITED | WNOHANG | WNOWAIT) == -1 && errno == ECHILD
    }
    static func fixture() {
        let args = Array(CommandLine.arguments.dropFirst())
        let patch = args.count > 1 && args[1] == "diff"
        let tree = args.count > 1 && args[1] == "files"
        let directory = args[patch || tree ? 2 : 1]
        let file = patch ? args.last! : ""
        let name = patch ? "patch-\(file)" : (tree ? "tree" : "overview")
        let url = URL(fileURLWithPath: directory).appendingPathComponent(name + ".pid")
        signal(SIGTERM, SIG_IGN); signal(SIGALRM, SIG_DFL); alarm(5)
        try! String(getpid()).write(to: url, atomically: true, encoding: .utf8)
        if directory.hasSuffix("slow-root") || file == "slow" { while true { pause() } }
        let value: [String: Any] = patch
            ? ["root": directory, "files": [["path": file, "lines": []]]]
            : ["root": directory, "branch": "fixture", "files": []]
        FileHandle.standardOutput.write(try! JSONSerialization.data(withJSONObject: value))
    }
}
