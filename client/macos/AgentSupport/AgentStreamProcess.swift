import Foundation

public struct AgentStreamCommand: Sendable {
    public let id: String
    public let body: [String: Any]
    public init(id: String, body: [String: Any]) { self.id = id; self.body = body }
}

/// Owns one `oberth agent stream` child with newline-framed NDJSON on stdin/stdout.
public final class AgentStreamProcess: @unchecked Sendable {
    public static let maxLineBytes = 2 * 1024 * 1024
    public enum State: Equatable, Sendable { case idle, running, ended(code: Int) }

    private let lock = NSLock()
    private var process: Process?
    private var stdinHandle: FileHandle?
    private var stdoutHandle: FileHandle?
    private var readTask: Task<Void, Never>?
    private var state: State = .idle
    private var nextCommand = 0
    private var lineBuffer = Data()
    private var onLine: (@Sendable (String) -> Void)?
    private var onEnd: (@Sendable (Int) -> Void)?

    public init() {}

    public func setHandlers(onLine: @escaping @Sendable (String) -> Void,
                            onEnd: @escaping @Sendable (Int) -> Void) {
        lock.lock()
        self.onLine = onLine
        self.onEnd = onEnd
        lock.unlock()
    }

    public func start(binary: String, arguments: [String], directory: String,
                      environment: [String: String]) throws {
        lock.lock()
        guard state == .idle else { lock.unlock(); throw AgentStreamFailure("stream already started") }
        lock.unlock()
        guard binary.hasPrefix("/"), FileManager.default.isExecutableFile(atPath: binary),
              directory.hasPrefix("/"), arguments.allSatisfy({ !$0.utf8.contains(0) }) else {
            throw AgentStreamFailure("无效的流式启动参数 / Invalid stream launch parameters")
        }
        let stdinPipe = Pipe()
        let stdoutPipe = Pipe()
        let child = Process()
        child.executableURL = URL(fileURLWithPath: binary)
        child.arguments = arguments
        child.currentDirectoryURL = URL(fileURLWithPath: directory)
        child.environment = environment
        child.standardInput = stdinPipe
        child.standardOutput = stdoutPipe
        child.standardError = Pipe()
        try child.run()
        lock.lock()
        process = child
        stdinHandle = stdinPipe.fileHandleForWriting
        stdoutHandle = stdoutPipe.fileHandleForReading
        state = .running
        lock.unlock()
        readTask = Task.detached(priority: .utility) { [weak self] in
            self?.pumpStdout()
        }
    }

    public func send(_ command: AgentStreamCommand) throws {
        var body = command.body
        body["v"] = 1
        body["id"] = command.id
        guard JSONSerialization.isValidJSONObject(body),
              let data = try? JSONSerialization.data(withJSONObject: body),
              data.count <= Self.maxLineBytes else {
            throw AgentStreamFailure("命令无效或过大 / Invalid or oversized stream command")
        }
        var line = data
        line.append(0x0A)
        lock.lock()
        guard state == .running, let stdinHandle else {
            lock.unlock()
            throw AgentStreamFailure("流未在运行 / Stream is not running")
        }
        lock.unlock()
        try stdinHandle.write(contentsOf: line)
    }

    public func sendTurnStart(text: String) throws {
        nextCommand += 1
        let id = "c\(nextCommand)"
        try send(AgentStreamCommand(id: id, body: ["type": "turn.start", "text": text]))
    }

    public func sendInterrupt() throws {
        nextCommand += 1
        try send(AgentStreamCommand(id: "c\(nextCommand)", body: ["type": "turn.interrupt"]))
    }

    public func sendRequestRespond(requestID: String, decision: AgentDecision, message: String? = nil) throws {
        nextCommand += 1
        var body: [String: Any] = ["type": "request.respond", "request": requestID, "decision": decision.rawValue]
        if let message, !message.isEmpty { body["message"] = message }
        try send(AgentStreamCommand(id: "c\(nextCommand)", body: body))
    }

    public func closeSession() throws {
        nextCommand += 1
        try send(AgentStreamCommand(id: "c\(nextCommand)", body: ["type": "session.close"]))
        try stdinHandle?.close()
    }

    /// Ask the bridge to shut down without blocking the caller.
    ///
    /// `closeSession()` only sends the request and closes stdin — the provider
    /// decides when it actually exits, and it may be mid-turn. Waiting on that
    /// from the main actor is what froze the window when a person ended a
    /// session (`AgentStreamsController.end`, `noteUnsupportedIfNeeded`,
    /// `AgentStreamRegistry.end`), because `waitUntilExit()` there blocks for as
    /// long as the provider takes.
    ///
    /// Reclamation is still **observed, not assumed**: the child is waited on by
    /// its own handle (never by the `state` flag, which `cancel()` clears
    /// eagerly), and a bridge that ignores `session.close` and SIGTERM is
    /// escalated to SIGKILL once `graceSeconds` runs out — the same ladder the
    /// engine uses for service teardown.
    ///
    /// - Parameter completion: runs on the main queue once the child is gone.
    public func requestShutdown(graceSeconds: TimeInterval = 3,
                                completion: (@Sendable () -> Void)? = nil) {
        try? closeSession()
        lock.lock()
        let child = process
        lock.unlock()
        guard let child else {
            if let completion { DispatchQueue.main.async(execute: completion) }
            return
        }
        DispatchQueue.global(qos: .utility).async {
            let deadline = Date().addingTimeInterval(graceSeconds)
            while child.isRunning, Date() < deadline {
                Thread.sleep(forTimeInterval: 0.02)
            }
            if child.isRunning { kill(child.processIdentifier, SIGKILL) }
            // Blocks this background queue only, until the child is reaped.
            child.waitUntilExit()
            self.finish(code: Int(child.terminationStatus))
            if let completion { DispatchQueue.main.async(execute: completion) }
        }
    }

    public func cancel() {
        lock.lock()
        let child = process
        process = nil
        stdinHandle = nil
        stdoutHandle = nil
        state = .ended(code: -1)
        lock.unlock()
        readTask?.cancel()
        if let child, child.isRunning { child.terminate() }
        child?.waitUntilExit()
    }

    public var isRunning: Bool {
        lock.lock()
        defer { lock.unlock() }
        if case .running = state { return true }
        return false
    }

    public func waitUntilExit() -> Int {
        lock.lock()
        let child = process
        lock.unlock()
        child?.waitUntilExit()
        let code = Int(child?.terminationStatus ?? -1)
        finish(code: code)
        return code
    }

    private func pumpStdout() {
        while !Task.isCancelled {
            lock.lock()
            let handle = stdoutHandle
            lock.unlock()
            guard let handle else { return }
            let chunk = handle.availableData
            if chunk.isEmpty {
                lock.lock()
                let child = process
                lock.unlock()
                if child?.isRunning == false {
                    finish(code: Int(child?.terminationStatus ?? -1))
                    return
                }
                Thread.sleep(forTimeInterval: 0.02)
                continue
            }
            ingest(chunk)
        }
    }

    private func ingest(_ chunk: Data) {
        lineBuffer.append(chunk)
        while true {
            guard let newline = lineBuffer.firstIndex(of: 0x0A) else { return }
            let lineData = lineBuffer[..<newline]
            lineBuffer.removeSubrange(...newline)
            if lineData.count > Self.maxLineBytes {
                cancel()
                return
            }
            guard let line = String(data: lineData, encoding: .utf8), !line.isEmpty else { continue }
            lock.lock()
            let handler = onLine
            lock.unlock()
            handler?(line)
        }
    }

    private func finish(code: Int) {
        lock.lock()
        guard case .running = state else { lock.unlock(); return }
        state = .ended(code: code)
        let end = onEnd
        stdoutHandle = nil
        lock.unlock()
        end?(code)
    }
}

public struct AgentStreamFailure: LocalizedError {
    public let message: String
    public init(_ message: String) { self.message = message }
    public var errorDescription: String? { message }
}
