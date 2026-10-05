import Foundation
import Darwin

/// The engine owns its socket, lock and detached daemon. The client only probes
/// the endpoint and asks the matching CLI to start it; it never unlinks or kills.
enum DaemonLaunch {
    enum Failure: LocalizedError {
        case binaryNotFound
        case launchFailed(String)
        case didNotComeUp(String)
        case probeFailed(Int32)
        case invalidSocketPath
        case autostartDisabled

        var errorDescription: String? {
            switch self {
            case .binaryNotFound:
                return "找不到可执行的 oberth。请重新安装完整客户端或检查 BERTH_BIN。 / Reinstall the complete app or check BERTH_BIN."
            case .launchFailed(let detail):
                return "后台启动失败 / Daemon launch failed: \(detail)"
            case .didNotComeUp(let path):
                return "后台尚未接受连接 / Daemon is not accepting connections: \(path)"
            case .probeFailed(let code):
                return "无法确认后台连接，不自动替换现有后台 / Cannot verify daemon; leaving it unchanged (errno \(code))."
            case .invalidSocketPath:
                return "无效的后台 socket 路径 / Invalid daemon socket path."
            case .autostartDisabled:
                return "后台未运行，BERTH_NO_AUTOSTART=1 已禁止自动启动。 / Daemon is unavailable; automatic start is disabled."
            }
        }
    }

    /// Explicit override, matching bundled engine, user installation, package
    /// manager, development build, then PATH. No shell or login-script execution.
    static func binaryPath(
        environment: [String: String] = ProcessInfo.processInfo.environment,
        home: String = FileManager.default.homeDirectoryForCurrentUser.path,
        executableURL: URL? = Bundle.main.executableURL,
        isExecutable: (String) -> Bool = { FileManager.default.isExecutableFile(atPath: $0) }
    ) -> String? {
        var candidates: [String] = []
        if let override = environment["BERTH_BIN"], !override.isEmpty {
            candidates.append(override)
        }
        if let executable = executableURL?.resolvingSymlinksInPath(),
           executable.deletingLastPathComponent().lastPathComponent == "MacOS",
           executable.deletingLastPathComponent().deletingLastPathComponent().lastPathComponent == "Contents" {
            candidates.append(executable.deletingLastPathComponent().appendingPathComponent("oberth").path)
        }
        candidates += ["\(home)/.local/bin/oberth", "/usr/local/bin/oberth", "/opt/homebrew/bin/oberth"]
        if let executable = executableURL?.resolvingSymlinksInPath() {
            var directory = executable.deletingLastPathComponent()
            for _ in 0..<6 { directory = directory.deletingLastPathComponent() }
            candidates.append(directory.appendingPathComponent("bin/oberth").path)
        }
        for entry in (environment["PATH"] ?? "").split(separator: ":") {
            candidates.append("\(entry)/oberth")
        }
        return candidates.first(where: isExecutable)
    }

    // Multiple windows can report the same unavailable endpoint at once. A
    // finite launch owns this lock; the next caller probes again before spawning.
    private static let launchLock = NSLock()

    static func ensureRunning(socketPath: String) throws {
        launchLock.lock()
        defer { launchLock.unlock() }
        try ensureRunning(socketPath: socketPath,
                          autostart: ProcessInfo.processInfo.environment["BERTH_NO_AUTOSTART"] != "1",
                          resolveBinary: { binaryPath() },
                          probe: { try canConnect(socketPath) }, launch: launchDetached)
    }

    /// Dependency-injected policy for failure/race tests. A protocol, permission,
    /// timeout or unknown probe failure is not permission to replace a daemon.
    static func ensureRunning(socketPath: String, autostart: Bool,
                              resolveBinary: () -> String?, probe: () throws -> Bool,
                              launch: (String, String) throws -> Void) throws {
        if try probe() { return }
        guard autostart else { throw Failure.autostartDisabled }
        guard let binary = resolveBinary() else { throw Failure.binaryNotFound }
        let launched = Result { try launch(binary, socketPath) }
        // Another application may win the engine's single-instance lock. The
        // transport observation, not a zero exit or an existing file, decides.
        if try probe() { return }
        if case .failure(let error) = launched { throw error }
        throw Failure.didNotComeUp(socketPath)
    }

    private static func launchDetached(_ binary: String, _ socketPath: String) throws {
        // env is executed directly, never through a shell. Pin the requested
        // socket without mutating the app's process-wide environment. The bounded
        // runner reclaims only its launcher group; the engine detaches its daemon.
        let result = CLI.run(binary: "/usr/bin/env",
                             arguments: ["BERTH_SOCKET=\(socketPath)", binary, "serve", "--detach"],
                             limits: CLI.Limits(timeout: 15))
        if case .failure(let error) = result {
            throw Failure.launchFailed("\(binary): \(error.message)")
        }
    }

    /// A bounded nonblocking transport probe. Only an absent or refused socket
    /// permits autostart; an existing non-socket/symlink is never removed here.
    static func canConnect(_ path: String) throws -> Bool {
        var address = sockaddr_un()
        let capacity = MemoryLayout.size(ofValue: address.sun_path)
        guard !path.isEmpty, !path.utf8.contains(0), path.utf8.count < capacity else {
            throw Failure.invalidSocketPath
        }
        let fd = socket(AF_UNIX, SOCK_STREAM, 0)
        guard fd >= 0 else { throw Failure.probeFailed(errno) }
        defer { close(fd) }
        guard fcntl(fd, F_SETFD, FD_CLOEXEC) == 0,
              fcntl(fd, F_SETFL, O_NONBLOCK) == 0 else { throw Failure.probeFailed(errno) }
        address.sun_family = sa_family_t(AF_UNIX)
        address.sun_len = UInt8(MemoryLayout<sockaddr_un>.size)
        withUnsafeMutablePointer(to: &address.sun_path) { slot in
            slot.withMemoryRebound(to: CChar.self, capacity: capacity) { destination in
                _ = path.withCString { strcpy(destination, $0) }
            }
        }
        let connected = withUnsafePointer(to: &address) { pointer in
            pointer.withMemoryRebound(to: sockaddr.self, capacity: 1) {
                Darwin.connect(fd, $0, socklen_t(MemoryLayout<sockaddr_un>.size))
            }
        }
        if connected == 0 { return true }
        var code = errno
        if code == EINPROGRESS || code == EALREADY || code == EINTR {
            let deadline = ProcessInfo.processInfo.systemUptime + 0.5
            while true {
                let remaining = deadline - ProcessInfo.processInfo.systemUptime
                guard remaining > 0 else { throw Failure.probeFailed(ETIMEDOUT) }
                var descriptor = pollfd(fd: fd, events: Int16(POLLOUT), revents: 0)
                let ready = poll(&descriptor, 1, Int32(max(1, ceil(remaining * 1000))))
                if ready < 0 && errno == EINTR { continue }
                guard ready > 0 else { throw Failure.probeFailed(ready == 0 ? ETIMEDOUT : errno) }
                var length = socklen_t(MemoryLayout<Int32>.size)
                guard getsockopt(fd, SOL_SOCKET, SO_ERROR, &code, &length) == 0 else {
                    throw Failure.probeFailed(errno)
                }
                break
            }
            if code == 0 { return true }
        }
        guard code == ENOENT || code == ECONNREFUSED else { throw Failure.probeFailed(code) }
        var info = stat()
        if lstat(path, &info) == 0 {
            guard (info.st_mode & mode_t(S_IFMT)) == mode_t(S_IFSOCK) else {
                throw Failure.probeFailed(ENOTSOCK)
            }
        } else if errno != ENOENT { throw Failure.probeFailed(errno) }
        return false
    }
}
