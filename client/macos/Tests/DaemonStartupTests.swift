import Foundation
import Darwin

private struct CheckFailure: Error, CustomStringConvertible {
    let description: String
}

@main
struct DaemonStartupTests {
    static func require(_ value: Bool, _ name: String) throws {
        if !value { throw CheckFailure(description: name) }
    }

    static func main() throws {
        try policyChecks()
        guard CommandLine.arguments.count == 2 else {
            throw CheckFailure(description: "Pass the explicitly built fixture engine")
        }
        let binary = URL(fileURLWithPath: CommandLine.arguments[1]).standardizedFileURL.path
        try require(FileManager.default.isExecutableFile(atPath: binary), "fixture binary must exist")
        // Short paths fit sockaddr_un even on CI; all state belongs to this test.
        let root = URL(fileURLWithPath: "/tmp/oberth-start-\(UUID().uuidString)")
        let home = root.appendingPathComponent("home")
        let state = root.appendingPathComponent("state")
        let selected = root.appendingPathComponent("selected.sock").path
        let inherited = root.appendingPathComponent("inherited.sock").path
        for directory in [root, home, state] {
            try FileManager.default.createDirectory(at: directory, withIntermediateDirectories: true,
                                                    attributes: [.posixPermissions: 0o700])
        }
        defer { try? FileManager.default.removeItem(at: root) }
        let overrides = ["HOME": home.path, "CFFIXED_USER_HOME": home.path,
                         "BERTH_HOME": state.path, "BERTH_SOCKET": inherited,
                         "BERTH_BIN": binary, "BERTH_NO_AUTOSTART": "0", "SHELL": "/bin/sh"]
        let previous = ProcessInfo.processInfo.environment
        for (key, value) in overrides { setenv(key, value, 1) }
        defer {
            for key in overrides.keys {
                if let value = previous[key] { setenv(key, value, 1) } else { unsetenv(key) }
            }
        }
        func run(_ args: [String]) throws -> Data {
            switch CLI.run(binary: "/usr/bin/env", arguments: ["BERTH_SOCKET=\(selected)", binary] + args,
                           limits: CLI.Limits(timeout: 20)) {
            case .success(let data): return data
            case .failure(let failure): throw CheckFailure(description: failure.message)
            }
        }
        func stop() throws {
            if try DaemonLaunch.canConnect(selected) {
                _ = try run(["daemon", "stop", "--json"])
            }
            try require(try !DaemonLaunch.canConnect(selected), "own daemon must release its endpoint")
        }
        do {
            // A healthy endpoint is independently verified with the CLI's hello
            // and daemon.status RPC, not just the transport probe under test.
            try DaemonLaunch.ensureRunning(socketPath: selected)
            let firstData = try run(["daemon", "status", "--json"])
            let first = try JSONSerialization.jsonObject(with: firstData) as? [String: Any]
            try require(first?["running"] as? Bool == true, "cold start must serve a valid RPC")
            try require(first?["socket"] as? String == selected, "explicit socket must reach the engine")
            try require(!FileManager.default.fileExists(atPath: inherited), "must not start inherited socket")
            let firstPID = first?["pid"] as? Int
            try DaemonLaunch.ensureRunning(socketPath: selected)
            let next = try JSONSerialization.jsonObject(with: run(["daemon", "status", "--json"])) as? [String: Any]
            try require(firstPID != nil && firstPID == next?["pid"] as? Int, "ready daemon must not be replaced")
            try stop()

            try staleSocket(at: selected)
            try require(FileManager.default.fileExists(atPath: selected), "stale fixture must exist")
            try require(try !DaemonLaunch.canConnect(selected), "stale fixture must have no listener")
            let failures = LockedFailures()
            DispatchQueue.concurrentPerform(iterations: 6) { _ in
                do { try DaemonLaunch.ensureRunning(socketPath: selected) }
                catch { failures.append(error.localizedDescription) }
            }
            try require(failures.isEmpty, "concurrent cold starts must share the engine-owned daemon")
            let live = try JSONSerialization.jsonObject(with: run(["daemon", "status", "--json"])) as? [String: Any]
            try require(live?["running"] as? Bool == true, "stale socket recovery must serve RPC")
            try stop()

            setenv("BERTH_NO_AUTOSTART", "1", 1)
            var disabled = false
            do { try DaemonLaunch.ensureRunning(socketPath: selected) }
            catch DaemonLaunch.Failure.autostartDisabled { disabled = true }
            try require(disabled, "disabled startup must report an explicit error")
            setenv("BERTH_NO_AUTOSTART", "0", 1)

            setenv("BERTH_BIN", "/usr/bin/false", 1)
            var launchFailed = false
            do { try DaemonLaunch.ensureRunning(socketPath: selected) }
            catch DaemonLaunch.Failure.launchFailed { launchFailed = true }
            try require(launchFailed, "nonzero launcher exit must not be success")

            let sleeper = root.appendingPathComponent("slow-launcher")
            try "#!/bin/sh\nexec /bin/sleep 60\n".write(to: sleeper, atomically: true, encoding: .utf8)
            try FileManager.default.setAttributes([.posixPermissions: 0o700], ofItemAtPath: sleeper.path)
            setenv("BERTH_BIN", sleeper.path, 1)
            let began = ProcessInfo.processInfo.systemUptime
            var timedOut = false
            do { try DaemonLaunch.ensureRunning(socketPath: selected) }
            catch DaemonLaunch.Failure.launchFailed(let detail) { timedOut = detail.contains("timed out") }
            try require(timedOut, "stuck launcher must be terminated and reclaimed by bounded runner")
            try require(ProcessInfo.processInfo.systemUptime - began < 25, "launcher timeout must be bounded")
            setenv("BERTH_BIN", binary, 1)
            try stop()
        } catch {
            // Only our isolated endpoint can be stopped, never a user daemon.
            setenv("BERTH_BIN", binary, 1)
            try? stop()
            throw error
        }
        print("Daemon startup: policy, cold/stale socket, exact path, concurrency, disabled, failure and timeout passed")
    }

    static func policyChecks() throws {
        var launches = 0
        func policy(_ answers: [Bool], enabled: Bool = true, available: Bool = true,
                    launchError: Bool = false) throws {
            var remaining = answers
            try DaemonLaunch.ensureRunning(socketPath: "/fixture/selected.sock", autostart: enabled,
                resolveBinary: { available ? "/fixture/oberth" : nil },
                probe: { remaining.removeFirst() }, launch: { binary, socket in
                    try require(binary == "/fixture/oberth" && socket == "/fixture/selected.sock", "launcher arguments")
                    launches += 1
                    if launchError { throw CheckFailure(description: "launcher failure") }
                })
        }
        try policy([true], enabled: false, available: false)
        try require(launches == 0, "healthy daemon needs no binary or autostart")
        var disabled = false
        do { try policy([false], enabled: false) } catch DaemonLaunch.Failure.autostartDisabled { disabled = true }
        try require(disabled && launches == 0, "disabled means no process")
        var missing = false
        do { try policy([false], available: false) } catch DaemonLaunch.Failure.binaryNotFound { missing = true }
        try require(missing && launches == 0, "missing binary cannot launch")
        do {
            try DaemonLaunch.ensureRunning(socketPath: "/fixture/selected.sock", autostart: true,
                resolveBinary: { "/fixture/oberth" }, probe: { throw DaemonLaunch.Failure.probeFailed(EACCES) },
                launch: { _, _ in launches += 1 })
            throw CheckFailure(description: "permission error must fail closed")
        } catch DaemonLaunch.Failure.probeFailed { }
        try require(launches == 0, "unknown or permission failure cannot replace an endpoint")
        try policy([false, true], launchError: true)
        try require(launches == 1, "concurrent engine winner can satisfy readiness after another launcher fails")
        var notReady = false
        do { try policy([false, false]) } catch DaemonLaunch.Failure.didNotComeUp { notReady = true }
        try require(notReady, "zero exit alone is not readiness")
        for path in ["", String(repeating: "x", count: 200), "/tmp/invalid\0socket"] {
            var rejected = false
            do { _ = try DaemonLaunch.canConnect(path) } catch DaemonLaunch.Failure.invalidSocketPath { rejected = true }
            try require(rejected, "invalid path must not be truncated")
        }
        let regular = URL(fileURLWithPath: "/tmp/oberth-plain-\(UUID().uuidString)")
        try Data("not a socket".utf8).write(to: regular)
        defer { try? FileManager.default.removeItem(at: regular) }
        var refused = false
        do { _ = try DaemonLaunch.canConnect(regular.path) } catch { refused = true }
        try require(refused, "regular file must not request autostart")
        try require(try Data(contentsOf: regular) == Data("not a socket".utf8), "regular file stays unchanged")
    }

    static func staleSocket(at path: String) throws {
        let fd = socket(AF_UNIX, SOCK_STREAM, 0)
        try require(fd >= 0, "fixture socket creation")
        defer { close(fd) }
        var address = sockaddr_un()
        address.sun_family = sa_family_t(AF_UNIX)
        let capacity = MemoryLayout.size(ofValue: address.sun_path)
        try require(path.utf8.count < capacity, "fixture path length")
        withUnsafeMutablePointer(to: &address.sun_path) { slot in
            slot.withMemoryRebound(to: CChar.self, capacity: capacity) { destination in
                _ = path.withCString { strcpy(destination, $0) }
            }
        }
        let bound = withUnsafePointer(to: &address) { pointer in
            pointer.withMemoryRebound(to: sockaddr.self, capacity: 1) {
                Darwin.bind(fd, $0, socklen_t(MemoryLayout<sockaddr_un>.size))
            }
        }
        try require(bound == 0, "fixture socket bind")
    }
}

private final class LockedFailures: @unchecked Sendable {
    private let lock = NSLock()
    private var values: [String] = []
    func append(_ value: String) { lock.lock(); defer { lock.unlock() }; values.append(value) }
    var isEmpty: Bool { lock.lock(); defer { lock.unlock() }; return values.isEmpty }
}
