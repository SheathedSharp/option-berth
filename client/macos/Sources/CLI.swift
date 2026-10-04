import Foundation

/// 跑一条 option-berth 命令，把 stdout 带回来。
///
/// **客户端为什么会有起进程这条路。** `DaemonClient` 走的是 daemon（服务、端口、
/// run、项目这些**状态**）；而 `git` 没有 daemon 状态 —— 它读的是这台机器的
/// 文件系统，命令行那边就是 CLI 自己干的。所以客户端走同一条路：**同一个二进制、
/// 同一份 `--json`**，而不是在 Swift 里把 git 再实现一遍。
///
/// CLI 出错时 stderr 上是 `{"error":{code,message,hint}}`
/// （docs/cli.md）—— 解出来就是一句能直接给用户看的话，解不出来才退回退出码。
enum CLI {
    struct Failure: Error {
        let message: String
    }

    /// 跑一条命令，二进制用 `DaemonLaunch.binaryPath()` 找；需要以某个项目为
    /// 当前目录时传 workingDirectory（例如 `init draft`）。
    nonisolated static func run(_ arguments: [String], workingDirectory: String? = nil) -> Result<Data, Failure> {
        guard let binary = DaemonLaunch.binaryPath() else {
            return .failure(Failure(message: missingBinary))
        }
        return run(binary: binary, arguments: arguments, workingDirectory: workingDirectory)
    }

    /// 同上，二进制由调用方给。
    nonisolated static func run(binary: String, arguments: [String], workingDirectory: String? = nil) -> Result<Data, Failure> {
        let process = Process()
        process.executableURL = URL(fileURLWithPath: binary)
        process.arguments = arguments
        if let workingDirectory, !workingDirectory.isEmpty {
            process.currentDirectoryURL = URL(fileURLWithPath: workingDirectory, isDirectory: true)
        }
        let out = Pipe()
        let err = Pipe()
        process.standardOutput = out
        process.standardError = err
        process.standardInput = FileHandle.nullDevice

        do {
            try process.run()
        } catch {
            return .failure(Failure(message: "跑不起来 \(binary)：\(error.localizedDescription)"))
        }
        let captured = drain(process: process, stdout: out, stderr: err)
        if let failure = captured.failure { return .failure(failure) }
        let outData = captured.stdout
        let errData = captured.stderr
        if process.terminationReason == .uncaughtSignal {
            return .failure(Failure(message: "option-berth 被信号终止 / Signal \(process.terminationStatus)"))
        }

        guard process.terminationStatus == 0 else {
            if let doc = try? JSONDecoder().decode(ErrorDocument.self, from: errData) {
                if let hint = doc.error.hint, !hint.isEmpty {
                    return .failure(Failure(message: "\(doc.error.message)（\(hint)）"))
                }
                return .failure(Failure(message: doc.error.message))
            }
            return .failure(Failure(message: "option-berth 退出码 / Exit \(process.terminationStatus)"))
        }
        return .success(outData)
    }

    /// Run a command whose stdout is newline-delimited progress. The callback
    /// is invoked on a background reader queue as each complete line arrives;
    /// callers that touch SwiftUI state must hop to the main actor themselves.
    nonisolated static func stream(
        _ arguments: [String],
        workingDirectory: String? = nil,
        onLine: @escaping (String) -> Void
    ) -> Result<Void, Failure> {
        guard let binary = DaemonLaunch.binaryPath() else {
            return .failure(Failure(message: missingBinary))
        }
        return stream(binary: binary, arguments: arguments,
                      workingDirectory: workingDirectory, onLine: onLine)
    }

    /// Same as `stream`, with an explicitly selected binary.
    nonisolated static func stream(
        binary: String,
        arguments: [String],
        workingDirectory: String? = nil,
        onLine: @escaping (String) -> Void
    ) -> Result<Void, Failure> {
        let process = Process()
        process.executableURL = URL(fileURLWithPath: binary)
        process.arguments = arguments
        if let workingDirectory, !workingDirectory.isEmpty {
            process.currentDirectoryURL = URL(fileURLWithPath: workingDirectory, isDirectory: true)
        }
        let out = Pipe()
        let err = Pipe()
        process.standardOutput = out
        process.standardError = err
        process.standardInput = FileHandle.nullDevice

        do {
            try process.run()
        } catch {
            return .failure(Failure(message: "跑不起来 \(binary)：\(error.localizedDescription)"))
        }

        let captured = drain(process: process, stdout: out, stderr: err, onLine: onLine)
        if let failure = captured.failure { return .failure(failure) }
        let errData = captured.stderr
        if process.terminationReason == .uncaughtSignal {
            return .failure(Failure(message: "option-berth 被信号终止 / Signal \(process.terminationStatus)"))
        }

        guard process.terminationStatus == 0 else {
            if let doc = try? JSONDecoder().decode(ErrorDocument.self, from: errData) {
                if let hint = doc.error.hint, !hint.isEmpty {
                    return .failure(Failure(message: "\(doc.error.message)（\(hint)）"))
                }
                return .failure(Failure(message: doc.error.message))
            }
            return .failure(Failure(message: "option-berth 退出码 / Exit \(process.terminationStatus)"))
        }
        return .success(())
    }

    private struct Captured {
        let stdout: Data
        let stderr: Data
        let failure: Failure?
    }

    /// Drain stdout and stderr independently, including after an output limit.
    /// The process must really exit before we return; no orphan timeout worker.
    /// Capture keeps at most 8 MiB stdout / 256 KiB stderr. Progress keeps only
    /// one incomplete frame (1 MiB maximum), not the whole conversation.
    private static func drain(process: Process, stdout: Pipe, stderr: Pipe,
                              onLine: ((String) -> Void)? = nil) -> Captured {
        let group = DispatchGroup()
        let output = Reader(limit: onLine == nil ? 8 * 1024 * 1024 : 1024 * 1024, onLine: onLine)
        let errors = Reader(limit: 256 * 1024)
        for (reader, handle) in [(output, stdout.fileHandleForReading), (errors, stderr.fileHandleForReading)] {
            group.enter()
            DispatchQueue.global(qos: .utility).async {
                defer { group.leave() }
                reader.read(handle)
            }
        }
        process.waitUntilExit()
        group.wait()
        // Readers have completed; no asynchronous mutation escapes this boundary.
        let failure: Failure?
        if output.overflow || errors.overflow {
            failure = Failure(message: "命令输出或进度行过大 / Command output limit exceeded")
        } else if output.failed || errors.failed {
            failure = Failure(message: "读取命令输出失败 / Failed to read command output")
        } else { failure = nil }
        return Captured(stdout: output.data, stderr: errors.data, failure: failure)
    }

    /// Exactly one worker owns each Reader; the caller observes it only after
    /// DispatchGroup.wait. File bytes are never included in overflow diagnostics.
    private final class Reader: @unchecked Sendable {
        let limit: Int
        let onLine: ((String) -> Void)?
        private(set) var data = Data()
        private(set) var overflow = false
        private(set) var failed = false
        init(limit: Int, onLine: ((String) -> Void)? = nil) { self.limit = limit; self.onLine = onLine }

        func read(_ handle: FileHandle) {
            defer { try? handle.close() }
            do {
                while let chunk = try handle.read(upToCount: 16 * 1024), !chunk.isEmpty {
                    guard !overflow else { continue }
                    if let onLine {
                        var start = chunk.startIndex
                        for newline in chunk.indices where chunk[newline] == 10 {
                            guard append(chunk[start..<newline]) else { break }
                            onLine(String(decoding: data, as: UTF8.self))
                            data.removeAll(keepingCapacity: true)
                            start = chunk.index(after: newline)
                        }
                        if !overflow { _ = append(chunk[start..<chunk.endIndex]) }
                    } else { _ = append(chunk) }
                }
                if let onLine, !overflow, !data.isEmpty {
                    onLine(String(decoding: data, as: UTF8.self))
                    data.removeAll(keepingCapacity: false)
                }
            } catch { failed = true }
        }
        @discardableResult private func append(_ bytes: Data) -> Bool {
            guard bytes.count <= limit - data.count else {
                overflow = true
                data.removeAll(keepingCapacity: false)
                return false
            }
            data.append(bytes)
            return true
        }
    }

    /// 跑一条命令并解码成 `T`。
    ///
    /// **解码失败也算失败**：`--json` 的 stdout 是一个 JSON 值（docs/cli.md），
    /// 解不出来说明二进制与客户端对不上（比如 daemon 或 CLI 是旧的那一份），
    /// 那种情况下把原因说出来比画一张空表好。
    nonisolated static func decode<T: Decodable>(_ type: T.Type, arguments: [String]) -> Result<T, Failure> {
        switch run(arguments) {
        case .failure(let why):
            return .failure(why)
        case .success(let data):
            do {
                return .success(try JSONDecoder().decode(T.self, from: data))
            } catch {
                return .failure(Failure(message: "读不懂 option-berth 的输出（\(error.localizedDescription)）—— 是不是装了一份旧二进制？"))
            }
        }
    }

    /// 二进制找不到时那句话。`BERTH_BIN` 是 dev 时的出口。
    static let missingBinary = "找不到 option-berth 二进制 —— 跑一次 mage install，或者用 BERTH_BIN 指一个"

    /// CLI 的错误信封：`{"error": {code, message, hint}}`。
    private struct ErrorDocument: Decodable {
        struct Body: Decodable {
            let code: String
            let message: String
            let hint: String?
        }
        let error: Body
    }
}
