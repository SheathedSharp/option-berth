import Foundation
import Darwin

public struct ExternalAgent: Decodable, Identifiable {
    public let id: String
    public let name: String
    public let command: String
    public let installed: Bool
    public let nativePrompt: Bool
    public let note: String?
    public let resumeFile: Bool?
    public var supportsResume: Bool { resumeFile == true }
    enum CodingKeys: String, CodingKey {
        case id, name, command, installed, note
        case nativePrompt = "native_prompt"
        case resumeFile = "resume_file"
    }
    public init(id: String, name: String, command: String, installed: Bool, nativePrompt: Bool, note: String? = nil, resumeFile: Bool = false) {
        self.id = id; self.name = name; self.command = command; self.installed = installed
        self.nativePrompt = nativePrompt; self.note = note; self.resumeFile = resumeFile
    }
}

public struct AgentResumeReference: Decodable {
    public let sessionID: String
    public let sourceVersion: String
    public let worktree: String
    public let source: String
    public let approvalOwner: String
    enum CodingKeys: String, CodingKey {
        case sessionID = "session_id", sourceVersion = "source_version", approvalOwner = "approval_owner"
        case worktree, source
    }
}

public struct AgentLaunchPlan: Decodable {
    public let provider: String
    public let executable: String
    public let arguments: [String]
    public let worktree: String
    public let mode: String
    public let resume: AgentResumeReference?

    private static func canonical(_ path: String) -> String? {
        guard path.hasPrefix("/"), !path.utf8.contains(0), let resolved = realpath(path, nil) else { return nil }
        defer { free(resolved) }
        return String(cString: resolved)
    }

    public func validate(provider expected: String, root: String, mode expectedMode: String, resumeFile: String? = nil) throws {
        guard root.hasPrefix("/"), provider == expected, mode == expectedMode,
              Self.canonical(root) != nil, Self.canonical(root) == Self.canonical(worktree),
              executable.hasPrefix("/"), !executable.utf8.contains(0),
              FileManager.default.isExecutableFile(atPath: executable),
              ["native", "task"].contains(mode),
              arguments.allSatisfy({ !$0.utf8.contains(0) }) else {
            throw AgentBridgeFailure("启动计划与所选 worktree 或 agent 不一致 / Launch plan does not match the selected worktree or agent")
        }
        if let resumeFile {
            guard let resume, mode == "native", resume.approvalOwner == "provider_native",
                  !resume.sessionID.isEmpty, !resume.sourceVersion.isEmpty,
                  Self.canonical(resume.source) == Self.canonical(resumeFile),
                  Self.canonical(resume.source) != nil,
                  Self.canonical(resume.worktree) == Self.canonical(root) else {
                throw AgentBridgeFailure("续接计划与所选会话文件不一致 / Resume identity does not match the selected file")
            }
        } else if resume != nil {
            throw AgentBridgeFailure("未授权的会话续接 / Unexpected session continuation")
        }
    }
}

public struct AgentBridgeFailure: LocalizedError {
    public let message: String
    public init(_ message: String) { self.message = message }
    public var errorDescription: String? { message }
}

/// Executes only the product's small read-only CLI planner, never an agent.
/// Prompt/output staging is private and removed after the child has actually exited.
public enum AgentBridge {
    public static func providers(binary: String, environment: [String: String]) async throws -> [ExternalAgent] {
        struct Catalogue: Decodable { let providers: [ExternalAgent] }
        let data = try await invoke(binary: binary, arguments: ["agent", "list", "--json"], input: nil, environment: environment)
        return try JSONDecoder().decode(Catalogue.self, from: data).providers
    }

    public static func plan(binary: String, provider: String, root: String, mode: String,
                            prompt: String?, environment: [String: String], resumeFile: String? = nil) async throws -> AgentLaunchPlan {
        guard root.hasPrefix("/"), !root.utf8.contains(0), prompt?.utf8.contains(0) != true, (prompt?.utf8.count ?? 0) <= 64 * 1024 else {
            throw AgentBridgeFailure("工作目录或消息无效 / Invalid worktree or message")
        }
        var args = ["agent", "plan", provider, "--worktree", root, "--mode", mode, "--json"]
        if let resumeFile { args += ["--resume-file", resumeFile] }
        if prompt != nil { args.append("--prompt-stdin") }
        let data = try await invoke(binary: binary, arguments: args, input: prompt.map { Data($0.utf8) }, environment: environment)
        let result = try JSONDecoder().decode(AgentLaunchPlan.self, from: data)
        try result.validate(provider: provider, root: root, mode: mode, resumeFile: resumeFile)
        return result
    }

    private static func invoke(binary: String, arguments: [String], input: Data?, environment: [String: String]) async throws -> Data {
        guard binary.hasPrefix("/"), FileManager.default.isExecutableFile(atPath: binary) else {
            throw AgentBridgeFailure("找不到支持 agent plan 的 oberth / A compatible oberth planner is required")
        }
        let job = PlannerJob(binary: binary, arguments: arguments, input: input, environment: environment)
        return try await withTaskCancellationHandler {
            let data = try await Task.detached(priority: .userInitiated) { try job.run() }.value
            try Task.checkCancellation()
            return data
        } onCancel: { job.stop(reason: "cancelled") }
    }
}

private final class PlannerJob: @unchecked Sendable {
    let binary: String
    let arguments: [String]
    let input: Data?
    let environment: [String: String]
    private let lock = NSLock()
    private var process: Process?
    private var failure: String?

    init(binary: String, arguments: [String], input: Data?, environment: [String: String]) {
        self.binary = binary; self.arguments = arguments; self.input = input; self.environment = environment
    }

    func stop(reason: String) {
        lock.lock()
        defer { lock.unlock() }
        guard failure == nil else { return }
        failure = reason
        // Foundation owns this child's lifetime. Do not retain a raw PID in a
        // delayed escalation task after Foundation may have reaped/reused it.
        if let child = process, child.isRunning { child.terminate() }
        // Wait for actual exit in run(). This is a cancellation request, not a
        // promise of a hard deadline if a broken planner ignores SIGTERM.
    }

    func run() throws -> Data {
        let fm = FileManager.default
        let directory = URL(fileURLWithPath: NSTemporaryDirectory()).appendingPathComponent("oberth-agent-plan-\(UUID().uuidString)")
        try fm.createDirectory(at: directory, withIntermediateDirectories: false, attributes: [.posixPermissions: 0o700])
        defer { try? fm.removeItem(at: directory) }
        let inputURL = directory.appendingPathComponent("input")
        let outputURL = directory.appendingPathComponent("output")
        let errorURL = directory.appendingPathComponent("error")
        for url in [inputURL, outputURL, errorURL] {
            guard fm.createFile(atPath: url.path, contents: url == inputURL ? (input ?? Data()) : Data(), attributes: [.posixPermissions: 0o600]) else {
                throw AgentBridgeFailure("无法创建私有启动输入 / Cannot stage private planner input")
            }
        }
        let stdin = try FileHandle(forReadingFrom: inputURL)
        let stdout = try FileHandle(forWritingTo: outputURL)
        let stderr = try FileHandle(forWritingTo: errorURL)
        defer { try? stdin.close(); try? stdout.close(); try? stderr.close() }
        let child = Process()
        child.executableURL = URL(fileURLWithPath: binary)
        child.arguments = arguments
        child.environment = environment
        child.standardInput = stdin
        child.standardOutput = stdout
        child.standardError = stderr
        lock.lock()
        if failure != nil { lock.unlock(); throw CancellationError() }
        process = child
        do { try child.run() } catch { process = nil; lock.unlock(); throw error }
        lock.unlock()
        let began = DispatchTime.now().uptimeNanoseconds
        let timer = DispatchSource.makeTimerSource(queue: .global(qos: .utility))
        timer.schedule(deadline: .now() + 0.05, repeating: 0.05)
        timer.setEventHandler { [weak self] in
            guard let self else { return }
            if DispatchTime.now().uptimeNanoseconds - began > 5_000_000_000 { stop(reason: "planner timed out") }
            for url in [outputURL, errorURL] {
                let size = (try? fm.attributesOfItem(atPath: url.path)[.size] as? NSNumber)?.intValue ?? 0
                if size > 256 * 1024 { stop(reason: "planner output limit exceeded") }
            }
        }
        timer.resume()
        defer { timer.cancel() }
        child.waitUntilExit()
        lock.lock()
        let reason = failure
        process = nil
        lock.unlock()
        if reason == "cancelled" { throw CancellationError() }
        if let reason { throw AgentBridgeFailure(reason) }
        guard child.terminationStatus == 0 else {
            // Do not surface raw stderr: it may contain prompt or credential data.
            throw AgentBridgeFailure("启动计划失败（exit \(child.terminationStatus)）。请在本机检查 oberth agent list/plan；不会回退执行 shell。 / Planner failed; no shell fallback.")
        }
        let size = (try fm.attributesOfItem(atPath: outputURL.path)[.size] as? NSNumber)?.intValue ?? 0
        guard size <= 256 * 1024 else { throw AgentBridgeFailure("planner output limit exceeded") }
        return try Data(contentsOf: outputURL)
    }
}
