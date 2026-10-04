import AppKit
import BerthAgent
import BerthTerminal
import Foundation

struct FailedCheck: Error { let detail: String }

@main
@MainActor
enum AgentChecks {
    static func check(_ condition: @autoclosure () -> Bool, _ detail: String) throws {
        if !condition() { throw FailedCheck(detail: detail) }
    }
    static func waitFor(_ condition: () -> Bool) async -> Bool {
        for _ in 0..<250 {
            if condition() { return true }
            try? await Task.sleep(nanoseconds: 20_000_000)
        }
        return condition()
    }
    static func main() async {
        if ProcessInfo.processInfo.environment["BERTH_AGENT_PLANNER_FIXTURE"] == "1" {
            fixturePlanner()
            return
        }
        _ = NSApplication.shared
        let explicitBinary = ProcessInfo.processInfo.environment["BERTH_AGENT_TEST_BINARY"]
        let binary = explicitBinary ?? URL(fileURLWithPath: CommandLine.arguments[0]).standardizedFileURL.path
        let fm = FileManager.default
        let root = URL(fileURLWithPath: NSTemporaryDirectory()).appendingPathComponent("berth-agent-check-\(UUID().uuidString)")
        var sessions: [TerminalSession] = []
        do {
            try composerChecks()
            try fm.createDirectory(at: root, withIntermediateDirectories: false, attributes: [.posixPermissions: 0o700])
            defer { try? fm.removeItem(at: root) }
            let tools = root.appendingPathComponent("tools")
            try fm.createDirectory(at: tools, withIntermediateDirectories: false)
            let executable = tools.appendingPathComponent("codex")
            let started = root.appendingPathComponent("started")
            let script = "#!/bin/sh\nprintf 'started' > \"$HOME/started\"\nprintf 'INITIAL:%s\\n' \"$2\"\nread -r line\nprintf 'NATIVE:%s\\n' \"$line\"\nexit 9\n"
            try script.write(to: executable, atomically: true, encoding: .utf8)
            try fm.setAttributes([.posixPermissions: 0o700], ofItemAtPath: executable.path)
            var env = ["HOME": root.path, "PATH": tools.path + ":/usr/bin:/bin", "BERTH_HOME": root.appendingPathComponent("state").path]
            if explicitBinary == nil { env["BERTH_AGENT_PLANNER_FIXTURE"] = "1" }
            let providers = try await AgentBridge.providers(binary: binary, environment: env)
            try check(providers.count == 5 && providers.first(where: { $0.id == "codex" })?.installed == true, "provider discovery failed")
            let prompt = "--not-an-option $(touch nope); 中文 'literal'"
            let plan = try await AgentBridge.plan(binary: binary, provider: "codex", root: root.path, mode: "native", prompt: prompt, environment: env)
            try check(plan.arguments == ["--", prompt], "initial message lost literal argv boundary")
            try check(!fm.fileExists(atPath: started.path), "planning executed the agent")
            if explicitBinary != nil {
                // All providers are local fixtures. Planning must never execute them.
                for command in ["opencode", "claude", "dsh", "pi"] {
                    let tool = tools.appendingPathComponent(command)
                    try script.write(to: tool, atomically: true, encoding: .utf8)
                    try fm.setAttributes([.posixPermissions: 0o700], ofItemAtPath: tool.path)
                }
                let cases: [(String, String, String?, [String])] = [
                    ("opencode", "native", prompt, ["--prompt", prompt]),
                    ("codex", "native", prompt, ["--", prompt]),
                    ("claude", "native", prompt, ["--", prompt]),
                    ("pi", "native", prompt, ["--", prompt]),
                    ("deepseek", "native", nil, ["--profile", "tui"]),
                    ("opencode", "task", prompt, ["run", "--", prompt]),
                    ("codex", "task", prompt, ["exec", "--", prompt]),
                    ("claude", "task", prompt, ["--print", "--", prompt]),
                    ("pi", "task", prompt, ["--print", "--", prompt]),
                    ("deepseek", "task", prompt, ["--profile", "headless", "--", prompt])
                ]
                for (provider, mode, message, expected) in cases {
                    let actual = try await AgentBridge.plan(binary: binary, provider: provider, root: root.path,
                                                            mode: mode, prompt: message, environment: env)
                    try check(actual.arguments == expected, "provider argv contract mismatch: \(provider)/\(mode)")
                }
                try check(!fm.fileExists(atPath: started.path), "provider plan matrix executed an agent")
                print("PASS: real CLI five-provider native/task plan matrix (fixture executables, no model calls)")
            }
            let session = TerminalSession(worktree: plan.worktree, title: "Fixture coding agent", kind: "agent:codex:native")
            sessions.append(session)
            var output = ""
            session.terminal.onBytes = { output += String(decoding: $0, as: UTF8.self) }
            try session.start(executable: plan.executable, arguments: plan.arguments, environment: env)
            let received = await waitFor { output.contains("INITIAL:" + prompt) }
            try check(received, "agent did not receive initial prompt literally")
            let pid = session.terminal.process!.shellPid
            let nativeWindow = NSWindow(contentRect: NSRect(x: 120, y: 120, width: 680, height: 300),
                                        styleMask: [.titled, .closable], backing: .buffered, defer: false)
            nativeWindow.isReleasedWhenClosed = false
            nativeWindow.title = "option-berth · isolated agent check"
            nativeWindow.contentView = session.terminal
            nativeWindow.makeKeyAndOrderFront(nil)
            defer { nativeWindow.close() }
            try check(nativeWindow.makeFirstResponder(session.terminal), "native agent could not take focus")
            try check(session.isActive && session.terminal.process!.shellPid == pid,
                      "entering native UI replaced the original agent process")
            for character in "continue in native mode\r" {
                let value = String(character)
                let event = NSEvent.keyEvent(with: .keyDown, location: .zero, modifierFlags: [],
                    timestamp: ProcessInfo.processInfo.systemUptime, windowNumber: nativeWindow.windowNumber,
                    context: nil, characters: value, charactersIgnoringModifiers: value,
                    isARepeat: false, keyCode: character == "\r" ? 36 : 0)!
                nativeWindow.sendEvent(event)
            }
            let ended = await waitFor { !session.isActive && output.contains("NATIVE:continue in native mode") }
            try check(ended && session.state.contains("9"), "native continuation or exit status failed")
            try check(!fm.fileExists(atPath: root.appendingPathComponent("nope").path), "natural language was interpreted as shell")
            do {
                try session.start(executable: "/bin/sh", arguments: [], environment: env)
                throw FailedCheck(detail: "exited agent fell back to shell")
            } catch is TerminalFailure {}

            let fakePlanner = tools.appendingPathComponent("slow-planner")
            try "#!/bin/sh\nexec /bin/sleep 20\n".write(to: fakePlanner, atomically: true, encoding: .utf8)
            try fm.setAttributes([.posixPermissions: 0o700], ofItemAtPath: fakePlanner.path)
            let task = Task { try await AgentBridge.providers(binary: fakePlanner.path, environment: env) }
            try await Task.sleep(nanoseconds: 60_000_000)
            task.cancel()
            do { _ = try await task.value; throw FailedCheck(detail: "cancelled planner reported success") }
            catch is CancellationError {}

            let bad = AgentLaunchPlanFixture(provider: "codex", executable: "/bin/sh", arguments: [], worktree: "/wrong", mode: "native")
            let badPlan = try JSONDecoder().decode(AgentLaunchPlan.self, from: JSONEncoder().encode(bad))
            do { try badPlan.validate(provider: "codex", root: root.path, mode: "native"); throw FailedCheck(detail: "wrong worktree accepted") }
            catch is AgentBridgeFailure {}
            for item in [
                AgentLaunchPlanFixture(provider: "claude", executable: executable.path, arguments: [], worktree: root.path, mode: "native"),
                AgentLaunchPlanFixture(provider: "codex", executable: executable.path, arguments: [], worktree: root.path, mode: "task"),
                AgentLaunchPlanFixture(provider: "codex", executable: "codex", arguments: [], worktree: root.path, mode: "native"),
                AgentLaunchPlanFixture(provider: "codex", executable: executable.path, arguments: ["bad\0argument"], worktree: root.path, mode: "native")
            ] {
                let value = try JSONDecoder().decode(AgentLaunchPlan.self, from: JSONEncoder().encode(item))
                do { try value.validate(provider: "codex", root: root.path, mode: "native"); throw FailedCheck(detail: "mismatched/unsafe launch plan accepted") }
                catch is AgentBridgeFailure {}
            }
            for message in ["bad\0prompt", String(repeating: "x", count: 64 * 1024 + 1)] {
                do {
                    _ = try await AgentBridge.plan(binary: binary, provider: "codex", root: root.path,
                                                    mode: "native", prompt: message, environment: env)
                    throw FailedCheck(detail: "invalid prompt accepted")
                } catch is AgentBridgeFailure {}
            }
            let errorPlanner = tools.appendingPathComponent("error-planner")
            try "#!/bin/sh\nprintf 'private-message-canary' >&2\nexit 7\n".write(to: errorPlanner, atomically: true, encoding: .utf8)
            try fm.setAttributes([.posixPermissions: 0o700], ofItemAtPath: errorPlanner.path)
            do { _ = try await AgentBridge.providers(binary: errorPlanner.path, environment: env); throw FailedCheck(detail: "failed planner accepted") }
            catch let error as AgentBridgeFailure { try check(!error.localizedDescription.contains("private-message-canary"), "raw planner stderr leaked") }
            print("PASS: \(explicitBinary == nil ? "synthetic planner contract" : "real oberth planner"), five-provider discovery, literal message, same native PTY continuation, no shell fallback, cancellation, wrong-root rejection, private error output")
        } catch {
            for session in sessions { session.stop(force: true) }
            _ = await waitFor { sessions.allSatisfy { !$0.isActive } }
            try? fm.removeItem(at: root)
            fputs("agent checks failed: \(error)\n", stderr)
            exit(1)
        }
    }
    private static func fixturePlanner() {
        let args = CommandLine.arguments
        let value: [String: Any]
        if args.contains("list") {
            value = ["providers": ["codex", "opencode", "claude", "pi", "deepseek"].map {
                ["id": $0, "name": $0, "command": $0, "installed": $0 == "codex", "native_prompt": $0 != "deepseek"] as [String: Any]
            }]
        } else {
            guard let position = args.firstIndex(of: "--worktree"), position + 1 < args.count,
                  let path = ProcessInfo.processInfo.environment["PATH"]?.split(separator: ":").first else { exit(2) }
            let prompt = String(decoding: FileHandle.standardInput.readDataToEndOfFile(), as: UTF8.self)
            value = ["provider": "codex", "mode": "native", "worktree": args[position + 1],
                     "executable": String(path) + "/codex", "arguments": ["--", prompt]]
        }
        do { FileHandle.standardOutput.write(try JSONSerialization.data(withJSONObject: value)) }
        catch { exit(1) }
    }

    private struct AgentLaunchPlanFixture: Encodable {
        let provider: String
        let executable: String
        let arguments: [String]
        let worktree: String
        let mode: String
    }
}
