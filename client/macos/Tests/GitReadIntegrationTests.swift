import Foundation
import Darwin

/// Production runner -> real engine -> real Git. Fixtures never access the
/// user's repositories, HOME, daemon, shell startup files or agent accounts.
@main enum GitReadIntegrationTests {
    static func require(_ value: @autoclosure () -> Bool, _ message: String) {
        guard value() else { fputs("FAIL: \(message)\n", stderr); exit(1) }
    }
    static func main() throws {
        guard let binary = ProcessInfo.processInfo.environment["BERTH_GIT_TEST_BINARY"], binary.hasPrefix("/") else {
            fputs("BERTH_GIT_TEST_BINARY must identify the built engine\n", stderr); exit(2)
        }
        let fm = FileManager.default
        let temp = fm.temporaryDirectory.appendingPathComponent("oberth-git-read-" + UUID().uuidString).resolvingSymlinksInPath()
        try fm.createDirectory(at: temp, withIntermediateDirectories: true, attributes: [.posixPermissions: 0o700])
        defer { try? fm.removeItem(at: temp) }
        for name in ["home", "berth", "repo"] { try fm.createDirectory(at: temp.appendingPathComponent(name), withIntermediateDirectories: true) }
        for name in ["HOME", "CFFIXED_USER_HOME"] { setenv(name, temp.appendingPathComponent("home").path, 1) }
        setenv("BERTH_HOME", temp.appendingPathComponent("berth").path, 1)
        setenv("GIT_CONFIG_GLOBAL", "/dev/null", 1); setenv("GIT_CONFIG_NOSYSTEM", "1", 1)
        for key in ProcessInfo.processInfo.environment.keys where key.hasPrefix("GIT_CONFIG_KEY_") || key.hasPrefix("GIT_CONFIG_VALUE_") { unsetenv(key) }
        for key in ["GIT_DIR", "GIT_WORK_TREE", "GIT_COMMON_DIR", "GIT_INDEX_FILE", "GIT_CONFIG_COUNT", "GIT_CONFIG_PARAMETERS"] { unsetenv(key) }
        let repo = temp.appendingPathComponent("repo"), linked = temp.appendingPathComponent("linked")
        func git(_ root: URL, _ arguments: [String]) -> Data {
            let prefix = ["git", "--no-optional-locks", "-c", "core.fsmonitor=false", "-c", "maintenance.auto=false", "-c", "gc.auto=0", "-c", "commit.gpgsign=false", "-C", root.path]
            switch CLI.run(binary: "/usr/bin/env", arguments: prefix + arguments) {
            case .success(let data): return data
            case .failure(let error): require(false, "fixture setup: " + error.message); return Data()
            }
        }
        _ = git(repo, ["init", "-q"])
        _ = git(repo, ["config", "user.name", "Fixture"])
        _ = git(repo, ["config", "user.email", "fixture@example.invalid"])
        try Data("before\n".utf8).write(to: repo.appendingPathComponent("tracked.txt"))
        _ = git(repo, ["add", "."]); _ = git(repo, ["commit", "-qm", "fixture"])
        _ = git(repo, ["worktree", "add", "-q", "-b", "linked", linked.path])
        let hook = repo.appendingPathComponent(".git/monitor-fixture")
        // The bounded child deliberately closes its pipes. Before the fix this
        // reproduces "Command left running children", not merely a slow hook.
        try Data("#!/bin/sh\nprintf invoked >> \"$0.called\"\nsleep 3 </dev/null >/dev/null 2>&1 &\nprintf 'fixture-token\\000/\\000'\n".utf8).write(to: hook)
        try fm.setAttributes([.posixPermissions: 0o700], ofItemAtPath: hook.path)
        _ = git(repo, ["config", "core.fsmonitor", hook.path])
        _ = git(repo, ["config", "core.fsmonitorHookVersion", "2"])
        _ = git(repo, ["config", "log.showSignature", "true"])
        let configURL = repo.appendingPathComponent(".git/config")
        let config = try Data(contentsOf: configURL)
        for root in [repo, linked] {
            try Data("after\n".utf8).write(to: root.appendingPathComponent("tracked.txt"))
            try Data("new\n".utf8).write(to: root.appendingPathComponent("new.txt"))
            let gitDir = String(decoding: git(root, ["rev-parse", "--absolute-git-dir"]), as: UTF8.self).trimmingCharacters(in: .whitespacesAndNewlines)
            let identityURLs = ["index", "HEAD"].map { URL(fileURLWithPath: gitDir).appendingPathComponent($0) }
            let before = try identityURLs.map { try Data(contentsOf: $0) }
            let reads = [["git", root.path, "--json"], ["git", "files", root.path, "--json"],
                         ["git", "diff", root.path, "--json", "--file", "tracked.txt"],
                         ["git", "diff", root.path, "--json", "--file", "new.txt"]]
            for _ in 0..<3 {
                for arguments in reads {
                    switch CLI.run(binary: binary, arguments: arguments) {
                    case .success(let data):
                        require((try? JSONSerialization.jsonObject(with: data)) is [String: Any], "real Git result is not JSON")
                    case .failure(let error): require(false, "real Git read: " + error.message)
                    }
                    require(!fm.fileExists(atPath: hook.path + ".called"), "read launched fsmonitor")
                }
            }
            let after = try identityURLs.map { try Data(contentsOf: $0) }
            require(before == after, "read rewrote index or HEAD")
        }
        let configAfter = try Data(contentsOf: configURL)
        require(config == configAfter, "read rewrote repository config")
        print("PASS: 24 production CLI/Git reads, linked worktree, tracked/untracked patches; no fsmonitor invocation, index/HEAD/config unchanged")
    }
}
