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
        require(large.message.contains("limit"), "output limit has no actionable diagnostic")
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
        default: exit(2)
        }
    }
}
