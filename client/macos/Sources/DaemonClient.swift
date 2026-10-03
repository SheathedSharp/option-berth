import Darwin
import Foundation

/// 直连 daemon 的 unix socket。普通调用仍是一条请求、一条响应；状态观察则复用
/// 下面的 `DaemonStateStream` 长连接，首帧拿快照，之后只接收增量。
struct DaemonClient: Sendable {
    let socketPath: String

    /// daemon 的 socket 位置：先看环境变量，再退回默认的配置目录。
    ///
    /// 默认值必须和引擎的 `internal/paths` 一致 —— 那是布局的唯一来源，
    /// 这里只是一个跟着走的副本。`oberth daemon path` 会打印真值。
    static func defaultSocketPath() -> String {
        if let override = ProcessInfo.processInfo.environment["BERTH_SOCKET"], !override.isEmpty {
            return override
        }
        if let directory = ProcessInfo.processInfo.environment["BERTH_HOME"], !directory.isEmpty {
            return "\(directory)/daemon.sock"
        }
        let home = FileManager.default.homeDirectoryForCurrentUser.path
        return "\(home)/.option-berth/daemon.sock"
    }

    enum Failure: LocalizedError {
        case socketUnavailable(Int32)
        case pathTooLong(String)
        case cannotConnect(String, Int32)
        case cannotWrite(Int32)
        case cannotRead(Int32)
        case noAnswer
        case malformed(String)
        /// daemon 正正经经地拒绝了一次调用。它给的话比我们能编的准，
        /// `detail` 才是给人看的那一句（`message` 是协议层的描述）。
        case refused(code: Int, message: String, detail: String?)

        var errorDescription: String? {
            switch self {
            case .socketUnavailable(let code):
                return "开不了 unix socket（errno \(code)）"
            case .pathTooLong(let path):
                return "socket 路径太长：\(path)"
            case .cannotConnect(let path, let code):
                return "连不上后台（\(path)）：errno \(code)。先跑一次 oberth status，它会自己把后台起来"
            case .cannotWrite(let code):
                return "写请求失败：errno \(code)"
            case .cannotRead(let code):
                return "读后台的回复失败：errno \(code)"
            case .noAnswer:
                return "后台没回话"
            case .malformed(let detail):
                return "后台的回复读不懂：\(detail)"
            case .refused(_, let message, let detail):
                return detail?.isEmpty == false ? detail! : message
            }
        }
    }

    private struct Envelope<T: Decodable>: Decodable {
        let result: T?
    }

    private struct SnapshotResult: Decodable {
        let ports: [Port]
    }

    private struct GroupsResult: Decodable {
        let groups: [BerthGroup]
    }

    // MARK: - 服务

    /// 全部有清单的项目。`groups.list` 是服务模块的数据源：左栏那一节、服务板的每一行
    /// 都从这里来。
    func listGroups() throws -> [BerthGroup] {
        let data = try call("groups.list", params: [:])
        do {
            let envelope = try JSONDecoder().decode(Envelope<GroupsResult>.self, from: data)
            return envelope.result?.groups ?? []
        } catch {
            throw Failure.malformed(error.localizedDescription)
        }
    }

    /// 运行注册表：option-berth 自己起过的东西，还活着的和已经结束的。
    ///
    /// 这是「服务在不在跑」的准星 —— 服务行里的 `running` 是从端口反推的，
    /// 不监听端口的服务（worker、只吐日志的构建）它永远看不见。
    func listRuns() throws -> RunsSnapshot {
        let data = try call("runs.list", params: [:])
        do {
            let envelope = try JSONDecoder().decode(Envelope<RunsSnapshot>.self, from: data)
            return envelope.result ?? .empty
        } catch {
            throw Failure.malformed(error.localizedDescription)
        }
    }

    /// 起一个项目声明的服务。
    ///
    /// 流式：daemon 每处理完一个服务就推一条，所以界面上是**一个个亮起来**的，
    /// 而不是转十秒圈然后一起出现。依赖顺序、`port: auto` 的分配、日志路径
    /// 全由引擎算，客户端只负责把每一条念出来。
    ///
    /// 返回时整批已经结束（`stream.end` 到了，或者连接断了）。
    func startGroup(
        _ group: String,
        only: [String] = [],
        onEvent: @escaping (StartEvent) -> Void
    ) throws {
        var params: [String: Any] = ["name": group]
        if !only.isEmpty { params["only"] = only }

        // 超时给得比普通调用长：一个服务等它 depends_on 起监听，
        // 引擎那边最多等 30 秒（DependencyTimeout），5 秒的读数会把正常的等待读成断线。
        let fd = try openSocket(receiveTimeout: 120)
        defer { close(fd) }
        try send(fd, "groups.start", params)

        let reader = LineReader(fd: fd)
        while let line = try reader.readLine() {
            guard let object = try? JSONSerialization.jsonObject(with: line) as? [String: Any] else {
                continue
            }
            if object["error"] != nil { throw rpcError(object["error"]) }
            guard let method = object["method"] as? String else { continue }
            let payload = (object["params"] as? [String: Any])?["data"]
            switch method {
            case "stream.chunk":
                guard let chunk = try? decode(StartChunk.self, from: payload) else { continue }
                if !chunk.error.isEmpty {
                    onEvent(.failed(service: chunk.service, detail: chunk.error))
                } else if chunk.skipped {
                    onEvent(.skipped(service: chunk.service,
                                     reason: chunk.reason.isEmpty ? "已经在跑了" : chunk.reason))
                } else {
                    onEvent(.started(ServiceStart(
                        service: chunk.service,
                        pid: chunk.pid,
                        port: chunk.port,
                        logPath: chunk.logPath,
                        logOffset: chunk.logOffset,
                        runID: chunk.runID
                    )))
                }
            case "stream.end":
                let summary = (try? decode(StartSummary.self, from: payload))
                    ?? StartSummary(started: [], skipped: [], errors: [])
                onEvent(.finished(summary))
                return
            default:
                continue
            }
        }
    }

    /// Restart the selected services using the same groups.kill → groups.start
    /// lifecycle as `oberth restart --only`. Scoped stops keep `port: auto`
    /// claims attached to this worktree.
    func restartGroup(
        _ group: String,
        only: [String] = [],
        onEvent: @escaping (StartEvent) -> Void
    ) throws {
        var stop: [String: Any] = ["name": group, "release": true]
        if !only.isEmpty { stop["only"] = only }
        let data = try call("groups.kill", params: stop, receiveTimeout: Self.killReceiveTimeout)
        guard let outcome = (try? JSONDecoder().decode(Envelope<KillOutcome>.self, from: data))?.result else {
            throw Failure.malformed("重启前的停止结果读不懂，不继续启动，避免旧进程和新进程叠在一起。")
        }
        if !outcome.ok {
            let detail = outcome.results.compactMap(\.error).first { !$0.isEmpty }
                ?? "没有停掉，引擎没说是为什么。"
            throw Failure.malformed(detail)
        }
        try startGroup(group, only: only, onEvent: onEvent)
    }

    /// 停掉一次运行。传 pid，不传 run id。
    ///
    /// 三条路都实测过（demo 项目里那个只打日志、不占端口的服务）：
    ///
    /// | 传什么 | 结果 |
    /// |---|---|
    /// | `{"run_id": "…"}` | 被拒：`no listening port belongs to run …` |
    /// | `{"pid": 16155}`   | 停掉了，运行注册表也清干净了 |
    /// | `groups.kill {name}` | 被拒：`no listening port belongs to group berth-demo` |
    ///
    /// 前两条和最后一条都是**按端口找人**的 —— run id 只是它用来映射端口的一个入口。
    /// 一个不占端口的服务就没有端口可映射，于是「全部停止」按下去它还活着。
    /// pid 这条路不经过端口，而运行注册表本来就给了 pid。
    func killRun(pid: Int) throws {
        _ = try call("ports.kill", params: ["targets": [["pid": pid]]],
                     receiveTimeout: Self.killReceiveTimeout)
    }

    /// 停的调用要等多久（秒）。
    ///
    /// **它必须是这条路上最长的那个等待。** 引擎收到停的请求之后不是马上就回话：
    /// 先 SIGTERM，然后**等** —— 默认 grace 5 秒，端口还不空才补 SIGKILL，之后还要
    /// 重扫一遍才把结果写回来。实测一个不理会 SIGTERM 的进程，这一趟走满 5.5 秒。
    /// 而 `openSocket` 默认那 5 秒是给「读一次列表」定的，比它短 —— 结果是
    /// **停掉的那个被读成 `daemon 没回话`**：用户点完确认看到一句失败，而进程其实
    /// 在他看那句话的时候已经死了。给到 20 秒：实测值的四倍，够一次重扫和调度抖动，
    /// 又不会在 daemon 真挂掉时让人干等。
    ///
    /// 只放宽这一条路：别的调用没有理由占着一个 socket 那么久。
    static let killReceiveTimeout = 20

    /// 停掉监听在某个端口上的那个进程。服务页的未声明监听详情走这里。
    ///
    /// **按端口停，不按 pid**。引擎要拿这个端口回它自己那份扫描里认这一行，
    /// 才知道该不该走 `docker stop` —— 一个容器发布的端口，按 pid 停只会去杀
    /// 容器外面那个代理进程，容器本身还在跑。`bind_address` 一起给：同一个端口
    /// 可以同时绑多个地址，不指明它分不清你指的是哪一个。
    ///
    /// 停不掉**不抛错**。引擎把「这一行没停成」放在结果里（`ok: false` + 那一行的
    /// `error`），那句话是要说给用户听的，不是让调用方重试的异常 —— 返回它就够了。
    /// 返回值：成功（或本来就没这条）是 nil，否则是一句可以直接给用户看的话。
    func killPort(port: Int, bindAddress: String) throws -> String? {
        let data = try call("ports.kill", params: [
            "targets": [["port": port, "bind_address": bindAddress]],
        ], receiveTimeout: Self.killReceiveTimeout)
        guard let outcome = (try? JSONDecoder().decode(Envelope<KillOutcome>.self, from: data))?.result
        else {
            // 解不出来就说不知道。这里**不能**当成功：那一行还会留在表上，
            // 而用户刚刚被告知它没了 —— 一句「读不懂」比替引擎撒这个谎好。
            return "停的请求发出去了，但后台回的帧读不懂，不知道停掉没有。"
        }
        if outcome.ok { return nil }
        return outcome.results.compactMap(\.error).first { !$0.isEmpty }
            ?? "没有停掉，引擎没说是为什么。"
    }

    /// `ports.kill` 那一帧。成败都走这一个壳，靠 `ok` 分辨 —— 失败的**那一行**
    /// 才带着原因，而不是把整次调用变成一个错误。
    private struct KillOutcome: Decodable {
        let ok: Bool
        let results: [Row]

        struct Row: Decodable {
            let error: String

            private enum CodingKeys: String, CodingKey { case error }

            init(from decoder: Decoder) throws {
                let c = try decoder.container(keyedBy: CodingKeys.self)
                error = (try? c.decodeIfPresent(String.self, forKey: .error)) ?? ""
            }
        }
    }

    /// 读一个项目根的清单（`oberth.yaml`）。
    ///
    /// 要的是它的**副作用**：daemon 读过的文件会进它自己的索引，从那一刻起这个
    /// 项目就出现在 `groups.list` 里了 —— 哪怕它一个端口都没起、一条服务都没跑。
    /// 「把一个已经写好清单的项目接进来」走的就是这一条：不写文件、不改任何东西。
    ///
    /// 踩过的坑：在这之前「加项目」只有 `groups.init write:true` 一条路，
    /// 而它对已经存在 `oberth.yaml` 的目录会**拒绝**（`already exists`），
    /// 于是「我项目里早写好了清单，App 里怎么让它出来」没有答案。
    func groupConfig(path: String) throws -> GroupConfigResult {
        let data = try call("groups.config.get", params: ["path": path])
        do {
            let envelope = try JSONDecoder().decode(Envelope<GroupConfigResult>.self, from: data)
            guard let result = envelope.result else { throw Failure.noAnswer }
            return result
        } catch let failure as Failure {
            throw failure
        } catch {
            throw Failure.malformed(error.localizedDescription)
        }
    }

    /// 问 daemon「这个目录能起什么服务」，可选地真把 `oberth.yaml` 写下去。
    ///
    /// `write: false` 是预览：daemon 把提案和它真会写下去的字节一起给回来，
    /// 界面上先给用户看，确认了再写。
    ///
    /// **服务列表总是空的，这是设计。** 引擎那边 `groups.Propose` 只从端口表出发
    /// （第三个参数，Compose 工作目录索引，传的是 nil），而它看到的东西**只当注释
    /// 写进文件**，一条服务都不声明 —— 「启动命令」这件事不在扫描结果里，理由写在
    /// `engine/internal/groups/propose.go` 的 `Propose` 上。
    /// （`oberth init` 那条命令行不走这个 handler：它自己扫盘、自己读
    /// package.json 和 compose，会把**声明类**的服务写进去，所以两条路不一样。）
    ///
    /// `yaml` 是**用户在预览里改过的那一份**。给了它，daemon 就照写、不再自己渲染 ——
    /// 一份被重新渲染过的编辑就不再是那个人的编辑了。它照样要过一遍校验，写坏了会在
    /// 动文件之前被拒掉。
    /// `force` 是给「改一份已经在那里的清单」用的：daemon 默认拒绝覆盖已有文件
    /// （那是「加项目」那条路要的保护，手滑不该盖掉别人的仓库），而编辑这条路
    /// 拿的就是那份文件的内容，覆盖是它的本意。
    func initGroup(rootDir: String, write: Bool, yaml: String? = nil,
                   force: Bool = false) throws -> GroupInitResult {
        var params: [String: Any] = ["root_dir": rootDir, "write": write]
        if force {
            params["force"] = true
        }
        if let yaml, !yaml.isEmpty {
            params["yaml"] = yaml
        }
        let data = try call("groups.init", params: params)
        do {
            let envelope = try JSONDecoder().decode(Envelope<GroupInitResult>.self, from: data)
            guard let result = envelope.result else { throw Failure.noAnswer }
            return result
        } catch let failure as Failure {
            throw failure
        } catch {
            throw Failure.malformed(error.localizedDescription)
        }
    }

    /// `groups.start` 推来的原始一条。字段是所有形态的并集 ——
    /// 成功、跳过、失败三条路共用一帧，靠哪些字段有值来分。
    private struct StartChunk: Decodable {
        let service: String
        let skipped: Bool
        let reason: String
        let error: String
        let pid: Int
        let port: Int?
        let logPath: String?
        let logOffset: Int64
        let runID: String

        private enum CodingKeys: String, CodingKey {
            case service, skipped, reason, error, pid, port
            case logPath = "log_path"
            case logOffset = "log_offset"
            case runID = "run_id"
        }

        init(from decoder: Decoder) throws {
            let c = try decoder.container(keyedBy: CodingKeys.self)
            service = (try? c.decode(String.self, forKey: .service)) ?? ""
            skipped = (try? c.decode(Bool.self, forKey: .skipped)) ?? false
            reason = (try? c.decode(String.self, forKey: .reason)) ?? ""
            error = (try? c.decode(String.self, forKey: .error)) ?? ""
            pid = (try? c.decode(Int.self, forKey: .pid)) ?? 0
            port = try? c.decodeIfPresent(Int.self, forKey: .port)
            logPath = try? c.decodeIfPresent(String.self, forKey: .logPath)
            logOffset = (try? c.decode(Int64.self, forKey: .logOffset)) ?? 0
            runID = (try? c.decode(String.self, forKey: .runID)) ?? ""
        }
    }

    /// 一帧 JSON 解成结构化类型。先归一到 Data 再交给 JSONDecoder ——
    /// JSONSerialization 出来的 `Any` 没法直接喂给 Decodable。
    private func decode<T: Decodable>(_ type: T.Type, from payload: Any?) throws -> T {
        guard let payload else { throw Failure.malformed("通知里没有 data") }
        let data = try JSONSerialization.data(withJSONObject: payload)
        return try JSONDecoder().decode(T.self, from: data)
    }

    /// 取 daemon 当前快照中的监听端口。端口是项目状态的证据，
    /// 不是一份独立的机器清单。
    func listPorts() throws -> [Port] {
        let data = try call("state.snapshot", params: [:])
        do {
            let envelope = try JSONDecoder().decode(Envelope<SnapshotResult>.self, from: data)
            return envelope.result?.ports ?? []
        } catch {
            throw Failure.malformed(error.localizedDescription)
        }
    }

    /// 发一条 JSON-RPC，返回它那一帧。
    ///
    /// `receiveTimeout` 只有 `killRun` / `killPort` 会改 —— 它们等的是引擎走完一轮
    /// SIGTERM → grace → SIGKILL，见 `killReceiveTimeout`。
    private func call(_ method: String, params: [String: Any],
                      receiveTimeout: Int = 5) throws -> Data {
        let fd = try openSocket(receiveTimeout: receiveTimeout)
        defer { close(fd) }
        try send(fd, method, params)

        let line = try LineReader(fd: fd).readLine()
        guard let line, !line.isEmpty else { throw Failure.noAnswer }
        if let object = try? JSONSerialization.jsonObject(with: line) as? [String: Any],
           object["error"] != nil {
            throw rpcError(object["error"])
        }
        return line
    }

    /// 连上 daemon，返回一个已经连好的 fd（调用方负责 close）。
    ///
    /// `receiveTimeout` 是读超时：daemon 挂了不该让界面一直等下去。
    /// 普通调用 5 秒够；流式启动可能等依赖等上几十秒，得单独放宽。
    ///
    /// 名字不叫 `connect`：那是 Darwin 里的系统调用，同名会把调用点全挡住。
    fileprivate func openSocket(receiveTimeout: Int? = 5) throws -> Int32 {
        let fd = socket(AF_UNIX, SOCK_STREAM, 0)
        guard fd >= 0 else { throw Failure.socketUnavailable(errno) }

        // A restart may close the peer while a request is being sent. Report
        // EPIPE through Failure instead of letting SIGPIPE terminate the app.
        var noSignal: Int32 = 1
        _ = withUnsafePointer(to: &noSignal) {
            setsockopt(fd, SOL_SOCKET, SO_NOSIGPIPE, $0, socklen_t(MemoryLayout<Int32>.size))
        }

        if let receiveTimeout {
            var timeout = timeval(tv_sec: receiveTimeout, tv_usec: 0)
            _ = withUnsafePointer(to: &timeout) {
                setsockopt(fd, SOL_SOCKET, SO_RCVTIMEO, $0, socklen_t(MemoryLayout<timeval>.size))
            }
        }

        var addr = sockaddr_un()
        addr.sun_family = sa_family_t(AF_UNIX)
        let path = socketPath
        let capacity = MemoryLayout.size(ofValue: addr.sun_path)
        guard path.utf8.count < capacity else {
            close(fd)
            throw Failure.pathTooLong(path)
        }
        withUnsafeMutablePointer(to: &addr.sun_path) { slot in
            slot.withMemoryRebound(to: CChar.self, capacity: capacity) { destination in
                _ = path.withCString { strcpy(destination, $0) }
            }
        }

        let connected = withUnsafePointer(to: &addr) { pointer in
            pointer.withMemoryRebound(to: sockaddr.self, capacity: 1) {
                connect(fd, $0, socklen_t(MemoryLayout<sockaddr_un>.size))
            }
        }
        guard connected == 0 else {
            let code = errno
            close(fd)
            throw Failure.cannotConnect(path, code)
        }
        return fd
    }

    /// 请求帧：一行 JSON。daemon 用换行分帧，和 daemon 的其它客户端一致。
    fileprivate func send(_ fd: Int32, _ method: String, _ params: [String: Any]) throws {
        var frame = try JSONSerialization.data(withJSONObject: [
            "jsonrpc": "2.0", "id": 1, "method": method, "params": params,
        ])
        frame.append(0x0A)
        try write(fd, frame)
    }

    /// 把 daemon 的错误对象翻成一条能读的错误。
    ///
    /// `data.detail` 才是给人看的那一句（"add a `services:` list, or start the
    /// command yourself"），`message` 是协议层的描述。两个都有的时候优先前者。
    fileprivate func rpcError(_ raw: Any?) -> Failure {
        guard let object = raw as? [String: Any] else { return .malformed("空错误") }
        let code = (object["code"] as? Int) ?? 0
        let message = (object["message"] as? String) ?? "后台拒绝了这个调用"
        var detail: String?
        if let data = object["data"] as? [String: Any] {
            detail = data["detail"] as? String
            if detail?.isEmpty == true, let hint = data["hint"] as? String, !hint.isEmpty {
                detail = hint
            }
        }
        return .refused(code: code, message: message, detail: detail)
    }

    private func write(_ fd: Int32, _ bytes: Data) throws {
        var offset = 0
        try bytes.withUnsafeBytes { raw in
            while offset < raw.count {
                let written = Darwin.write(fd, raw.baseAddress!.advanced(by: offset), raw.count - offset)
                if written <= 0 {
                    if errno == EINTR { continue }
                    throw Failure.cannotWrite(errno)
                }
                offset += written
            }
        }
    }

    /// 一个连接上的行分帧器。
    ///
    /// **缓冲必须活到连接结束，不能一次调用一个。** 踩过的坑：原来是一个
    /// `readLine(fd)` 方法，一次 `read` 拿 64 KiB，找到第一个换行就 return ——
    /// 换行之后那几个字节（往往是**后面几帧的全部内容**）就跟着局部变量一起被丢了。
    /// 单发单收的调用看不出来（响应只有一帧），而 `groups.start` 的流式响应
    /// 一次 recv 就能带回「首帧 + 若干 chunk + stream.end」，于是第一帧之后
    /// 全丢，客户端在原地等一个永远不来的结束帧 —— 实测就是这样卡死的。
    fileprivate final class LineReader {
        private let fd: Int32
        private var pending = Data()

        init(fd: Int32) { self.fd = fd }

        /// 读下一行。nil 表示超时或者对端关了（两种情况调用方的处理是一样的：
        /// 这条流到此为止）。
        func readLine() throws -> Data? {
            while true {
                if let newline = pending.firstIndex(of: 0x0A) {
                    let line = pending[pending.startIndex..<newline]
                    pending.removeSubrange(pending.startIndex...newline)
                    return Data(line)
                }
                var chunk = [UInt8](repeating: 0, count: 64 * 1024)
                let got = read(fd, &chunk, chunk.count)
                if got < 0 {
                    if errno == EINTR { continue }
                    if errno == EAGAIN || errno == EWOULDBLOCK { return nil }
                    throw Failure.cannotRead(errno)
                }
                if got == 0 {
                    // 对端关了。缓冲区里剩下的（没有结尾换行的最后一帧）照样要给出去。
                    guard !pending.isEmpty else { return nil }
                    let rest = pending
                    pending.removeAll(keepingCapacity: false)
                    return rest
                }
                pending.append(contentsOf: chunk[0..<got])
            }
        }
    }
}

/// The wire shape of a state subscription. `groups` intentionally decodes to
/// the same client model as `groups.list`: the daemon publishes one canonical
/// group representation, and the client only renders it.
struct DaemonStateSnapshot: Decodable, Sendable {
    let seq: UInt64
    let at: String
    let ports: [Port]
    let groups: [BerthGroup]

    private enum CodingKeys: String, CodingKey { case seq, at, ports, groups }

    init(seq: UInt64, at: String, ports: [Port], groups: [BerthGroup]) {
        self.seq = seq
        self.at = at
        self.ports = ports
        self.groups = groups
    }

    init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        seq = try c.decode(UInt64.self, forKey: .seq)
        at = try c.decode(String.self, forKey: .at)
        ports = try c.decodeIfPresent([Port].self, forKey: .ports) ?? []
        groups = try c.decodeIfPresent([BerthGroup].self, forKey: .groups) ?? []
    }
}

struct DaemonStateChange<T: Decodable>: Decodable {
    let added: [T]
    let updated: [T]
    let removed: [String]

    private enum CodingKeys: String, CodingKey { case added, updated, removed }

    init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        added = try c.decode([T].self, forKey: .added)
        updated = try c.decode([T].self, forKey: .updated)
        removed = try c.decode([String].self, forKey: .removed)
    }
}

struct DaemonStateDelta: Decodable {
    let seq: UInt64
    let at: String
    let ports: DaemonStateChange<Port>
    let groups: DaemonStateChange<BerthGroup>

    private enum CodingKeys: String, CodingKey { case seq, at, ports, groups }

    init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        seq = try c.decode(UInt64.self, forKey: .seq)
        at = try c.decode(String.self, forKey: .at)
        ports = try c.decode(DaemonStateChange<Port>.self, forKey: .ports)
        groups = try c.decode(DaemonStateChange<BerthGroup>.self, forKey: .groups)
    }
}

extension DaemonStateChange {
    init(added: [T], updated: [T], removed: [String]) {
        self.added = added
        self.updated = updated
        self.removed = removed
    }
}

/// One subscription per socket feeds all visible stores. Delivery stays on the
/// main actor in wire order; blocking reads stay on a dedicated utility queue.
@MainActor
final class DaemonStateStream {
    private struct Observer {
        let snapshot: (DaemonStateSnapshot) -> Void
        let delta: (DaemonStateDelta) -> Void
        let error: (Error) -> Void
    }

    private static var sharedStreams: [String: DaemonStateStream] = [:]
    static func shared(socketPath: String) -> DaemonStateStream {
        if let stream = sharedStreams[socketPath] { return stream }
        let stream = DaemonStateStream(client: DaemonClient(socketPath: socketPath))
        sharedStreams[socketPath] = stream
        return stream
    }

    private let client: DaemonClient
    private let queue = DispatchQueue(label: "option-berth.daemon.state", qos: .utility)
    private var observers: [UUID: Observer] = [:]
    private var connection: DaemonStateConnection?
    private var retry: DispatchWorkItem?
    private var retryDelay = 0.25
    private var cached: DaemonStateSnapshot?
    private var lastError: Error?
    private var generation = 0
    private var active = false
    private var refreshScheduled = false

    init(client: DaemonClient) { self.client = client }

    func observe(snapshot: @escaping (DaemonStateSnapshot) -> Void,
                 delta: @escaping (DaemonStateDelta) -> Void,
                 error: @escaping (Error) -> Void) -> UUID {
        let id = UUID()
        observers[id] = Observer(snapshot: snapshot, delta: delta, error: error)
        // A second store may attach after the first snapshot has arrived.
        if let cached { snapshot(cached) }
        if let lastError { error(lastError) }
        return id
    }

    func removeObserver(_ id: UUID) {
        observers.removeValue(forKey: id)
        if observers.isEmpty { stop() }
    }

    func start() {
        guard !active else { return }
        active = true
        connect()
    }

    func stop() {
        active = false
        generation += 1
        retry?.cancel()
        retry = nil
        connection?.cancel()
        connection = nil
        cached = nil
        lastError = nil
    }

    /// An explicit refresh gets a new atomic snapshot and subscription. Two
    /// stores requesting it in the same UI action coalesce into one reconnect.
    func refresh() {
        guard active, !refreshScheduled else { return }
        refreshScheduled = true
        DispatchQueue.main.async { [weak self] in
            guard let self else { return }
            self.refreshScheduled = false
            guard self.active else { return }
            self.retry?.cancel()
            self.retry = nil
            self.connect()
        }
    }

    private func connect() {
        generation += 1
        let current = generation
        connection?.cancel()
        let connection = DaemonStateConnection(client: client, afterSeq: cached?.seq ?? 0)
        self.connection = connection
        queue.async { [weak self] in
            connection.read { message in
                // Dispatch FIFO preserves snapshot/delta ordering, including
                // several frames received in one read.
                DispatchQueue.main.async { [weak self] in
                    guard let self, self.active, self.generation == current else { return }
                    self.receive(message)
                }
            }
        }
    }

    private func receive(_ message: DaemonStateConnection.Message) {
        switch message {
        case .snapshot(let snapshot):
            cached = snapshot
            lastError = nil
            retryDelay = 0.25
            for observer in Array(observers.values) { observer.snapshot(snapshot) }
        case .delta(let delta):
            // Empty and opt-in-only updates may be suppressed by the daemon:
            // seq is monotonic, not necessarily contiguous for this subscriber.
            guard let snapshot = cached, delta.seq > snapshot.seq else { return }
            cached = DaemonStateSnapshot(
                seq: delta.seq, at: delta.at,
                ports: applyDaemonChange(delta.ports, to: snapshot.ports, key: \.id),
                groups: applyDaemonChange(delta.groups, to: snapshot.groups, key: \.name))
            lastError = nil
            for observer in Array(observers.values) { observer.delta(delta) }
        case .failure(let error):
            lastError = error
            for observer in Array(observers.values) { observer.error(error) }
            let item = DispatchWorkItem { [weak self] in
                guard let self, self.active else { return }
                self.retry = nil
                self.connect()
            }
            retry = item
            DispatchQueue.main.asyncAfter(deadline: .now() + retryDelay, execute: item)
            retryDelay = min(retryDelay * 2, 5)
        }
    }
}

/// Cancellation only shuts down the fd; the read loop closes it under the same
/// lock. This prevents cancelling an old connection from touching a reused fd.
private final class DaemonStateConnection: @unchecked Sendable {
    enum Message {
        case snapshot(DaemonStateSnapshot)
        case delta(DaemonStateDelta)
        case failure(Error)
    }

    private let client: DaemonClient
    private let afterSeq: UInt64
    private let lock = NSLock()
    private var fd: Int32 = -1
    private var cancelled = false

    init(client: DaemonClient, afterSeq: UInt64 = 0) {
        self.client = client
        self.afterSeq = afterSeq
    }

    func cancel() {
        lock.lock()
        defer { lock.unlock() }
        cancelled = true
        if fd >= 0 { _ = Darwin.shutdown(fd, SHUT_RDWR) }
    }

    private func install(_ socket: Int32) -> Bool {
        lock.lock()
        defer { lock.unlock() }
        guard !cancelled else { close(socket); return false }
        fd = socket
        return true
    }

    private func finish() -> Bool {
        lock.lock()
        defer { lock.unlock() }
        if fd >= 0 { close(fd); fd = -1 }
        return cancelled
    }

    private struct Response: Decodable { let result: DaemonStateSnapshot }
    private struct Notification: Decodable {
        let method: String
        let params: DaemonStateDelta?
    }

    func read(deliver: (Message) -> Void) {
        do {
            // No read timeout after connect: an unchanged state is the normal
            // case for a subscription, not a reason to reconnect.
            let socket = try client.openSocket(receiveTimeout: nil)
            guard install(socket) else { return }
            var params: [String: Any] = ["events": false]
            if afterSeq > 0 { params["after_seq"] = afterSeq }
            try client.send(socket, "state.subscribe", params)
            let reader = DaemonClient.LineReader(fd: socket)
            guard let first = try reader.readLine() else { throw DaemonClient.Failure.noAnswer }
            if let object = try JSONSerialization.jsonObject(with: first) as? [String: Any],
               let error = object["error"] {
                throw client.rpcError(error)
            }
            let snapshot = try JSONDecoder().decode(Response.self, from: first).result
            deliver(.snapshot(snapshot))
            while let line = try reader.readLine() {
                let notification = try JSONDecoder().decode(Notification.self, from: line)
                if notification.method == "state.delta", let delta = notification.params {
                    deliver(.delta(delta))
                }
            }
            throw DaemonClient.Failure.noAnswer
        } catch {
            if !finish() { deliver(.failure(error)) }
        }
    }
}

/// Apply the daemon's keyed collection diff while retaining the published row
/// order. A restart is represented by remove + add; the new row replaces the
/// old one in place, matching the engine's state.Apply contract.
func applyDaemonChange<T>(_ change: DaemonStateChange<T>, to rows: [T],
                          key: (T) -> String) -> [T] {
    var replacements: [String: T] = [:]
    for row in change.updated { replacements[key(row)] = row }
    for row in change.added { replacements[key(row)] = row }
    let removed = Set(change.removed)
    var seen = Set<String>()
    var result: [T] = []
    result.reserveCapacity(rows.count + change.added.count)
    for row in rows {
        let rowKey = key(row)
        if let replacement = replacements[rowKey] {
            result.append(replacement)
            seen.insert(rowKey)
        } else if !removed.contains(rowKey) {
            result.append(row)
            seen.insert(rowKey)
        }
    }
    for row in change.added where !seen.contains(key(row)) {
        result.append(row)
        seen.insert(key(row))
    }
    return result
}
