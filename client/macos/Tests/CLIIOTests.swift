import Foundation
import Darwin

// Compile with CLI.swift and DaemonLaunch.swift. All subprocesses are this fixture;
// no daemon, provider credentials, project state or user shell is accessed.
@main
enum CLIIOTests {
    static var binary: String { URL(fileURLWithPath: CommandLine.arguments[0]).standardizedFileURL.path }
    static func require(_ condition: @autoclosure () -> Bool, _ message: String) {
        if !condition() { fputs("FAIL: \(message)\n", stderr); exit(1) }
    }
    static func main() {
        if let i = CommandLine.arguments.firstIndex(of: "--fixture"), i + 1 < CommandLine.arguments.count {
            fixture(CommandLine.arguments[i + 1]); return
        }
        let result = CLI.run(binary: binary, arguments: ["--fixture", "stderr-first"])
        guard case .success(let data) = result else { require(false, "concurrent stderr failed"); return }
        require(String(decoding: data, as: UTF8.self) == "ok\n", "stdout changed")
        print("PASS: stderr larger than pipe capacity cannot deadlock stdout")
        if CommandLine.arguments.contains("--stderr-only") { return }
        guard case .failure(let large) = CLI.run(binary: binary, arguments: ["--fixture", "large-output"]) else {
            require(false, "unbounded stdout accepted"); return
        }
        require(large.message.contains("limit"), "output limit has no actionable diagnostic: \(large.message)")
        guard case .failure(let secret) = CLI.run(binary: binary, arguments: ["--fixture", "failed", "private-argv-canary"]) else {
            require(false, "nonzero exit accepted"); return
        }
        require(!secret.message.contains("private-argv-canary") && !secret.message.contains("private-stderr-canary"), "private arguments/stderr leaked")
        var lines: [String] = []
        let streamed = CLI.stream(binary: binary, arguments: ["--fixture", "stream"], onLine: { lines.append($0) })
        guard case .success = streamed else { require(false, "stream fixture failed"); return }
        require(lines == ["first", "中文", "last"], "framing or final line changed")
        var oversizedLines = 0
        guard case .failure = CLI.stream(binary: binary, arguments: ["--fixture", "large-line"], onLine: { _ in oversizedLines += 1 }) else {
            require(false, "unbounded stream frame accepted"); return
        }
        require(oversizedLines == 0, "oversized partial frame forwarded")
        guard case .failure(let signal) = CLI.run(binary: binary, arguments: ["--fixture", "signal"]) else {
            require(false, "signal exit accepted"); return
        }
        require(signal.message.contains("Signal"), "signal was represented as a normal exit")
        print("PASS: bounded capture/frame, literal Unicode framing, private diagnostics, signal exit")
        lifecycleChecks()
    }
    static func lifecycleChecks() {
        let short = CLI.Limits(timeout: 0.2, terminationGrace: 0.06, pipeGrace: 0.06)
        let preCancelled = CLI.Cancellation(); preCancelled.cancel()
        guard case .failure(let before) = CLI.run(binary: "/nonexistent-fixture", arguments: [], cancellation: preCancelled) else {
            require(false, "pre-cancel started work"); return
        }
        require(before.message.contains("cancelled"), "pre-cancel must win over spawn")
        guard case .failure = CLI.run(binary: binary, arguments: ["bad\0argument"]) else {
            require(false, "NUL arguments silently truncated"); return
        }
        guard case .failure = CLI.run(binary: binary, arguments: [], limits: .init(timeout: .nan)) else {
            require(false, "invalid deadline accepted"); return
        }
        let initialFDs = openFDCount()
        for mode in ["hang", "closed-pipes", "moved-group", "orphan-pipes", "orphan-closed", "escaped-pipes"] {
            var pids: [pid_t] = []
            let started = ProcessInfo.processInfo.systemUptime
            let outcome = CLI.stream(binary: binary, arguments: ["--fixture", mode], limits: short) { line in
                pids += line.split(separator: ":").compactMap { pid_t($0) }
            }
            guard case .failure(let error) = outcome else { require(false, "\(mode) returned success"); return }
            require(!pids.isEmpty, "\(mode) fixture did not start")
            require(ProcessInfo.processInfo.systemUptime - started < 4, "\(mode) failed to return after cleanup")
            require(error.message.contains(mode.hasPrefix("orphan") || mode == "escaped-pipes" ? "pipe" : "timed out") ||
                    (mode == "orphan-closed" && error.message.contains("children")), "\(mode): wrong failure: \(error.message)")
            assertReaped(pids[0])
            for pid in pids.dropFirst() {
                let deadline = ProcessInfo.processInfo.systemUptime + 2
                while isLive(pid), ProcessInfo.processInfo.systemUptime < deadline { usleep(10_000) }
                require(!isLive(pid), "\(mode) left live descendant \(pid)")
            }
            print("PASS: \(mode), real child reap and descendant termination")
        }
        for _ in 0..<12 {
            let token = CLI.Cancellation()
            var pid: pid_t = 0
            let result = CLI.stream(binary: binary, arguments: ["--fixture", "hang"], cancellation: token, limits: short) { line in
                pid = pid_t(line) ?? 0
                // Exercise the token across threads without leaving a canceller.
                let group = DispatchGroup(); group.enter()
                DispatchQueue.global().async { token.cancel(); group.leave() }; group.wait()
            }
            guard case .failure(let error) = result else { require(false, "cancelled command succeeded"); return }
            require(error.message.contains("cancelled"), "cancellation became a normal/signal exit")
            require(pid > 0, "cancellation fixture did not report pid")
            assertReaped(pid)
        }
        require(openFDCount() <= initialFDs + 1, "repeated cancellation leaked file descriptors")
        let temp = FileManager.default.temporaryDirectory.appendingPathComponent("cli-cwd-空 格-" + UUID().uuidString)
        try! FileManager.default.createDirectory(at: temp, withIntermediateDirectories: true)
        defer { try? FileManager.default.removeItem(at: temp) }
        guard case .success(let cwd) = CLI.run(binary: binary, arguments: ["--fixture", "cwd"], workingDirectory: temp.path) else {
            require(false, "spawn cwd failed"); return
        }
        require(URL(fileURLWithPath: String(decoding: cwd, as: UTF8.self).trimmingCharacters(in: .whitespacesAndNewlines)).resolvingSymlinksInPath().standardizedFileURL == temp.resolvingSymlinksInPath().standardizedFileURL,
                "cwd with spaces/Unicode changed")
        print("PASS: pre-spawn/cross-thread cancellation, invalid inputs, 12 reaps, FD stability, isolated cwd")
    }
    static func openFDCount() -> Int { (0..<1024).filter { fcntl(Int32($0), F_GETFD) >= 0 }.count }
    static func assertReaped(_ pid: pid_t) {
        var status: Int32 = 0
        require(waitpid(pid, &status, WNOHANG) == -1 && errno == ECHILD, "direct child was not reaped")
        require(!isLive(pid), "direct child still alive")
    }
    static func isLive(_ pid: pid_t) -> Bool {
        var info = proc_bsdinfo()
        return proc_pidinfo(pid, PROC_PIDTBSDINFO, 0, &info, Int32(MemoryLayout<proc_bsdinfo>.stride)) > 0 && info.pbi_status != UInt32(SZOMB)
    }
    static func fixture(_ mode: String) {
        switch mode {
        case "stderr-first":
            FileHandle.standardError.write(Data(repeating: 120, count: 192 * 1024))
            FileHandle.standardOutput.write(Data("ok\n".utf8))
        case "large-output": FileHandle.standardOutput.write(Data(repeating: 120, count: 9 * 1024 * 1024))
        case "failed":
            FileHandle.standardError.write(Data("private-stderr-canary".utf8)); exit(7)
        case "stream":
            FileHandle.standardError.write(Data(repeating: 120, count: 192 * 1024))
            for byte in "first\n中文\nlast".utf8 { FileHandle.standardOutput.write(Data([byte])) }
        case "large-line": FileHandle.standardOutput.write(Data(repeating: 120, count: 2 * 1024 * 1024))
        case "signal": raise(SIGTERM)
        case "cwd": print(FileManager.default.currentDirectoryPath)
        case "hang", "closed-pipes", "moved-group":
            if mode == "moved-group" { require(setpgid(0, getpgid(getppid())) == 0, "fixture change group") }
            signal(SIGTERM, SIG_IGN)
            FileHandle.standardOutput.write(Data("\(getpid())\n".utf8))
            if mode == "closed-pipes" { Darwin.close(1); Darwin.close(2) }
            while true { pause() }
        case "orphan-pipes", "orphan-closed", "escaped-pipes":
            var ready: [Int32] = [-1, -1]; require(pipe(&ready) == 0, "fixture pipe")
            let args: [String] = [binary, "--fixture", "descendant-" + mode, String(ready[1])]
            var argv: [UnsafeMutablePointer<CChar>?] = args.map { strdup($0) }
            argv.append(nil)
            var child: pid_t = 0
            let spawned = posix_spawn(&child, binary, nil, nil, argv, [nil])
            for arg in argv { free(arg) }
            require(spawned == 0, "fixture spawn")
            Darwin.close(ready[1]); var byte: UInt8 = 0; _ = Darwin.read(ready[0], &byte, 1); Darwin.close(ready[0])
            FileHandle.standardOutput.write(Data("\(getpid()):\(child)\n".utf8)); _exit(0)
        case "descendant-orphan-pipes", "descendant-orphan-closed", "descendant-escaped-pipes":
            signal(SIGTERM, SIG_IGN)
            if mode == "descendant-escaped-pipes" { require(setsid() >= 0, "fixture setsid") }
            if mode == "descendant-orphan-closed" { Darwin.close(1); Darwin.close(2) }
            let fd = Int32(CommandLine.arguments.last!)!
            var byte: UInt8 = 1; _ = Darwin.write(fd, &byte, 1); Darwin.close(fd)
            if mode == "descendant-escaped-pipes" { sleep(1); _exit(0) }
            while true { pause() }
        default: exit(2)
        }
    }
}
