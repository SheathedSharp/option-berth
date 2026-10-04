import Foundation
import Darwin

/// The same oberth binary/JSON contract as the terminal. Only finite reads and
/// draft generation use this runner; daemon/service ownership stays in the engine.
enum CLI {
    struct Failure: Error { let message: String }

    /// A request owns its token. Cancellation is sticky, safe before spawn, and
    /// does not claim completion until the synchronous runner has reclaimed it.
    final class Cancellation: @unchecked Sendable {
        private let lock = NSLock()
        private var cancelled = false
        func cancel() { lock.lock(); cancelled = true; lock.unlock() }
        var isCancelled: Bool { lock.lock(); defer { lock.unlock() }; return cancelled }
    }

    struct Limits {
        var timeout: TimeInterval = 30
        var terminationGrace: TimeInterval = 0.25
        var pipeGrace: TimeInterval = 0.25
        static let read = Limits()
        static let draft = Limits(timeout: 300)
        fileprivate var valid: Bool {
            [timeout, terminationGrace, pipeGrace].allSatisfy { $0.isFinite && $0 > 0 && $0 <= 86_400 }
        }
    }

    nonisolated static func run(_ arguments: [String], workingDirectory: String? = nil,
                               cancellation: Cancellation? = nil, limits: Limits = .read) -> Result<Data, Failure> {
        guard let binary = DaemonLaunch.binaryPath() else { return .failure(Failure(message: missingBinary)) }
        return run(binary: binary, arguments: arguments, workingDirectory: workingDirectory,
                   cancellation: cancellation, limits: limits)
    }

    nonisolated static func run(binary: String, arguments: [String], workingDirectory: String? = nil,
                               cancellation: Cancellation? = nil, limits: Limits = .read) -> Result<Data, Failure> {
        execute(binary: binary, arguments: arguments, workingDirectory: workingDirectory,
                cancellation: cancellation, limits: limits, onLine: nil)
    }

    /// The callback runs synchronously on the caller's worker, not the main actor.
    /// It must return promptly; this API cannot preempt arbitrary callback code.
    nonisolated static func stream(_ arguments: [String], workingDirectory: String? = nil,
                                  cancellation: Cancellation? = nil, limits: Limits = .draft,
                                  onLine: @escaping (String) -> Void) -> Result<Void, Failure> {
        guard let binary = DaemonLaunch.binaryPath() else { return .failure(Failure(message: missingBinary)) }
        return stream(binary: binary, arguments: arguments, workingDirectory: workingDirectory,
                      cancellation: cancellation, limits: limits, onLine: onLine)
    }

    nonisolated static func stream(binary: String, arguments: [String], workingDirectory: String? = nil,
                                  cancellation: Cancellation? = nil, limits: Limits = .draft,
                                  onLine: @escaping (String) -> Void) -> Result<Void, Failure> {
        execute(binary: binary, arguments: arguments, workingDirectory: workingDirectory,
                cancellation: cancellation, limits: limits, onLine: onLine).map { _ in () }
    }

    private static func execute(binary: String, arguments: [String], workingDirectory: String?,
                                cancellation: Cancellation?, limits: Limits,
                                onLine: ((String) -> Void)?) -> Result<Data, Failure> {
        guard limits.valid, !binary.isEmpty,
              !([binary] + arguments + [workingDirectory ?? ""]).contains(where: { $0.utf8.contains(0) }) else {
            return .failure(Failure(message: "无效的命令参数或时限 / Invalid command arguments or limits"))
        }
        if cancellation?.isCancelled == true { return .failure(cancelled) }
        do {
            let child = try spawn(binary, arguments: arguments, directory: workingDirectory)
            return collect(child, cancellation: cancellation, limits: limits, onLine: onLine)
        } catch let failure as Failure { return .failure(failure) }
        catch { return .failure(Failure(message: "无法启动命令 / Could not start command")) }
    }

    private struct Child { let pid: pid_t; let stdout: Int32; let stderr: Int32 }

    /// SETPGROUP is applied by spawn, not raced against exec with setpgid later.
    /// CLOEXEC_DEFAULT prevents unrelated application descriptors leaking in.
    private static func spawn(_ binary: String, arguments: [String], directory: String?) throws -> Child {
        var owned: [Int32] = []
        defer { for fd in owned { Darwin.close(fd) } }
        func pipePair() throws -> [Int32] {
            var fds: [Int32] = [-1, -1]
            guard Darwin.pipe(&fds) == 0 else { throw systemFailure(errno) }
            owned.append(contentsOf: fds)
            // Keep all action sources above stdin/stdout/stderr, even when the
            // calling process was started with a standard descriptor closed.
            for i in fds.indices where fds[i] < 3 {
                let fd = fcntl(fds[i], F_DUPFD_CLOEXEC, 3)
                guard fd >= 0 else { throw systemFailure(errno) }
                owned.append(fd)
                fds[i] = fd
            }
            for fd in fds { guard fcntl(fd, F_SETFD, FD_CLOEXEC) == 0 else { throw systemFailure(errno) } }
            guard fcntl(fds[0], F_SETFL, O_NONBLOCK) == 0 else { throw systemFailure(errno) }
            return fds
        }
        let out = try pipePair(), err = try pipePair()
        var actions: posix_spawn_file_actions_t?
        var attributes: posix_spawnattr_t?
        try check(posix_spawn_file_actions_init(&actions))
        defer { posix_spawn_file_actions_destroy(&actions) }
        try check(posix_spawnattr_init(&attributes))
        defer { posix_spawnattr_destroy(&attributes) }
        try check(posix_spawn_file_actions_addopen(&actions, STDIN_FILENO, "/dev/null", O_RDONLY, 0))
        try check(posix_spawn_file_actions_adddup2(&actions, out[1], STDOUT_FILENO))
        try check(posix_spawn_file_actions_adddup2(&actions, err[1], STDERR_FILENO))
        for fd in owned where fd > 2 { try check(posix_spawn_file_actions_addclose(&actions, fd)) }
        if let directory, !directory.isEmpty {
            try check(posix_spawn_file_actions_addchdir_np(&actions, directory))
        }
        var mask = sigset_t(), defaults = sigset_t()
        sigemptyset(&mask); sigfillset(&defaults)
        try check(posix_spawnattr_setsigmask(&attributes, &mask))
        try check(posix_spawnattr_setsigdefault(&attributes, &defaults))
        try check(posix_spawnattr_setpgroup(&attributes, 0))
        try check(posix_spawnattr_setflags(&attributes, Int16(POSIX_SPAWN_SETPGROUP | POSIX_SPAWN_CLOEXEC_DEFAULT |
                                                             POSIX_SPAWN_SETSIGMASK | POSIX_SPAWN_SETSIGDEF)))
        let argv = ([binary] + arguments).map { strdup($0) } + [nil]
        let envp = ProcessInfo.processInfo.environment.map { strdup("\($0.key)=\($0.value)") } + [nil]
        defer { for p in argv + envp { free(p) } }
        guard argv.dropLast().allSatisfy({ $0 != nil }), envp.dropLast().allSatisfy({ $0 != nil }) else {
            throw systemFailure(ENOMEM)
        }
        var pid: pid_t = 0
        try check(posix_spawn(&pid, binary, &actions, &attributes, argv, envp))
        owned.removeAll { $0 == out[0] || $0 == err[0] }
        return Child(pid: pid, stdout: out[0], stderr: err[0])
    }

    /// One owner polls both nonblocking pipes and observes the child without
    /// reaping. WNOWAIT reserves its PID/PGID until every group signal is done.
    /// No Process termination handler, detached reader or delayed kill survives.
    private static func collect(_ child: Child, cancellation: Cancellation?, limits: Limits,
                                onLine: ((String) -> Void)?) -> Result<Data, Failure> {
        var outFD = child.stdout, errFD = child.stderr
        defer { if outFD >= 0 { Darwin.close(outFD) }; if errFD >= 0 { Darwin.close(errFD) } }
        let output = Reader(limit: onLine == nil ? 8 * 1024 * 1024 : 1024 * 1024, onLine: onLine)
        let errors = Reader(limit: 256 * 1024)
        let started = ProcessInfo.processInfo.systemUptime
        var endedAt: TimeInterval?
        var stopAt: TimeInterval?
        var killed = false
        var status = siginfo_t()
        var failure: Failure?
        while true {
            let now = ProcessInfo.processInfo.systemUptime
            if endedAt == nil {
                var observed = siginfo_t()
                if waitid(P_PID, id_t(child.pid), &observed, WEXITED | WNOHANG | WNOWAIT) == 0 {
                    if observed.si_pid == child.pid { status = observed; endedAt = now }
                } else if errno != EINTR {
                    // Another reaper or an observation error means identity is
                    // no longer established. Never signal a possibly reused PID.
                    return .failure(Failure(message: "无法确认命令进程身份 / Could not verify child ownership"))
                }
            }
            if failure == nil {
                if cancellation?.isCancelled == true { failure = cancelled }
                else if now - started >= limits.timeout { failure = Failure(message: "命令超时 / Command timed out") }
                else if let endedAt, (outFD >= 0 || errFD >= 0), now - endedAt >= limits.pipeGrace {
                    failure = Failure(message: "命令退出后管道仍被占用 / Command output pipe remained open")
                }
            }
            if endedAt != nil, outFD < 0, errFD < 0, failure == nil {
                switch liveChildren(in: child.pid) {
                case .some(false): break
                case .some(true): failure = Failure(message: "命令遗留子进程 / Command left running children")
                case .none: failure = Failure(message: "无法验证子进程回收 / Could not verify child cleanup")
                }
                if failure == nil { break }
            }
            if failure != nil, endedAt != nil, liveChildren(in: child.pid) == false {
                // Darwin may report EPERM when signalling an already empty
                // zombie-led group. Verified absence needs no further signal.
                break
            }
            if failure != nil, stopAt == nil {
                // The unreaped leader pins this group even if it already exited.
                _ = kill(-child.pid, SIGTERM)
                if endedAt == nil { _ = kill(child.pid, SIGTERM) }
                // A signal receipt (or a race with exit) is not proof of death;
                // only waitid plus group observation completes reclamation.
                stopAt = now
            }
            if let stopAt, !killed, now - stopAt >= limits.terminationGrace {
                _ = kill(-child.pid, SIGKILL)
                if endedAt == nil { _ = kill(child.pid, SIGKILL) }
                killed = true
            }
            if killed, endedAt != nil {
                let live = liveChildren(in: child.pid)
                if live != true {
                    if live == nil { failure = Failure(message: "无法验证子进程回收 / Could not verify child cleanup") }
                    // An escaped, re-sessioned descendant may retain a pipe.
                    // Close our ends; do not chase unrelated PIDs or wait on EOF.
                    break
                }
            }
            for (reader, fd) in [(output, outFD), (errors, errFD)] where fd >= 0 {
                let result = reader.readAvailable(fd, deliver: failure == nil)
                if result != .open {
                    Darwin.close(fd)
                    if fd == outFD { outFD = -1 } else { errFD = -1 }
                    if result == .failed && failure == nil {
                        failure = Failure(message: "读取命令输出失败 / Failed to read command output")
                    }
                }
            }
            if failure == nil && (output.overflow || errors.overflow) {
                failure = Failure(message: "命令输出或进度行过大 / Command output limit exceeded")
            }
            var fds = [pollfd(fd: outFD, events: Int16(POLLIN), revents: 0),
                       pollfd(fd: errFD, events: Int16(POLLIN), revents: 0)]
            if poll(&fds, nfds_t(fds.count), 20) < 0 && errno != EINTR && failure == nil {
                failure = Failure(message: "等待命令输出失败 / Failed to poll command output")
            }
        }
        // Reap on this worker, after the final group signal and observation.
        var reapedStatus: Int32 = 0
        var reaped: pid_t
        repeat { reaped = waitpid(child.pid, &reapedStatus, 0) } while reaped < 0 && errno == EINTR
        guard reaped == child.pid else { return .failure(Failure(message: "命令回收失败 / Failed to reap command")) }
        if let failure { return .failure(failure) }
        if cancellation?.isCancelled == true { return .failure(cancelled) }
        guard status.si_code == CLD_EXITED else {
            return .failure(Failure(message: "option-berth 被信号终止 / Signal \(status.si_status)"))
        }
        guard status.si_status == 0 else {
            if let doc = try? JSONDecoder().decode(ErrorDocument.self, from: errors.data) {
                let hint = doc.error.hint.flatMap { $0.isEmpty ? nil : $0 }
                return .failure(Failure(message: doc.error.message + (hint.map { "（\($0)）" } ?? "")))
            }
            return .failure(Failure(message: "option-berth 退出码 / Exit \(status.si_status)"))
        }
        return .success(output.data)
    }

    /// Same-user group observation; zombies are already dead, with reaping owned
    /// by their parent/launchd. The leader remains waitable throughout this query.
    private static func liveChildren(in group: pid_t) -> Bool? {
        let bytes = proc_listpids(UInt32(PROC_PGRP_ONLY), UInt32(group), nil, 0)
        guard bytes > 0 else { return nil }
        var pids = [pid_t](repeating: 0, count: Int(bytes) / MemoryLayout<pid_t>.stride + 64)
        let capacity = Int32(pids.count * MemoryLayout<pid_t>.stride)
        let used = proc_listpids(UInt32(PROC_PGRP_ONLY), UInt32(group), &pids, capacity)
        guard used >= 0 else { return nil }
        if used == 0 { return false }
        if used >= capacity { return true } // Retry after stopping a growing group.
        for pid in pids.prefix(Int(used) / MemoryLayout<pid_t>.stride) where pid > 0 && pid != group {
            var info = proc_bsdinfo()
            let size = Int32(MemoryLayout<proc_bsdinfo>.stride)
            let got = proc_pidinfo(pid, PROC_PIDTBSDINFO, 0, &info, size)
            if got != size {
                if errno == ESRCH { continue }
                return nil
            }
            if info.pbi_pgid == UInt32(group) && info.pbi_status != UInt32(SZOMB) { return true }
        }
        return false
    }

    private final class Reader {
        enum Read { case open, closed, failed }
        let limit: Int
        let onLine: ((String) -> Void)?
        var data = Data()
        var overflow = false
        private var bytes = [UInt8](repeating: 0, count: 16 * 1024)
        init(limit: Int, onLine: ((String) -> Void)? = nil) { self.limit = limit; self.onLine = onLine }
        func readAvailable(_ fd: Int32, deliver: Bool) -> Read {
            let count = Darwin.read(fd, &bytes, bytes.count)
            if count < 0 { return errno == EINTR || errno == EAGAIN ? .open : .failed }
            if count == 0 {
                if let onLine, deliver, !overflow, !data.isEmpty { onLine(String(decoding: data, as: UTF8.self)); data.removeAll() }
                return .closed
            }
            guard deliver, !overflow else { return .open }
            let chunk = Data(bytes.prefix(count))
            if let onLine {
                var start = chunk.startIndex
                for newline in chunk.indices where chunk[newline] == 10 {
                    guard append(chunk[start..<newline]) else { return .open }
                    onLine(String(decoding: data, as: UTF8.self)); data.removeAll(keepingCapacity: true)
                    start = chunk.index(after: newline)
                }
                _ = append(chunk[start..<chunk.endIndex])
            } else { _ = append(chunk) }
            return .open
        }
        private func append(_ bytes: Data) -> Bool {
            guard bytes.count <= limit - data.count else { overflow = true; data.removeAll(); return false }
            data.append(bytes); return true
        }
    }

    nonisolated static func decode<T: Decodable>(_ type: T.Type, arguments: [String],
                                                 cancellation: Cancellation? = nil, limits: Limits = .read) -> Result<T, Failure> {
        run(arguments, cancellation: cancellation, limits: limits).flatMap { data in
            do { return .success(try JSONDecoder().decode(T.self, from: data)) }
            catch { return .failure(Failure(message: "读不懂 option-berth 的输出（\(error.localizedDescription)）—— 是不是装了一份旧二进制？")) }
        }
    }
    private static let cancelled = Failure(message: "命令已取消 / Command cancelled")
    private static func check(_ code: Int32) throws { if code != 0 { throw systemFailure(code) } }
    private static func systemFailure(_ code: Int32) -> Failure { Failure(message: "无法启动命令 / Command setup failed (\(code))") }
    static let missingBinary = "找不到 option-berth 二进制 —— 跑一次 mage install，或者用 BERTH_BIN 指一个"
    private struct ErrorDocument: Decodable {
        struct Body: Decodable { let code: String; let message: String; let hint: String? }
        let error: Body
    }
}
