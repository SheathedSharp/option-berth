import Foundation

// 「服务」这一层的数据。服务是**说好了要跑的东西**（`oberth.yaml` 里声明的），
// 监听是它的运行实况。两者会重叠，但谁也不能替谁 —— 一个服务可以不起端口
// （只吐日志的构建、worker），一个监听也可以不属于任何服务。

/// `groups.list` 里的一行：一个项目（daemon 管它叫 group）。
struct BerthGroup: Decodable, Identifiable, Sendable {
    let name: String
    let repo: String
    let worktree: String
    let branch: String
    let source: String
    let rootDir: String?
    let configPath: String?
    let specHash: String?
    let status: String
    let members: [Int]
    let services: [BerthService]
    /// 清单顶层 `machine:` 那一节：这个项目**依赖**的、机器上的服务。
    ///
    /// 是引用不是成员 —— `up` / `down` 都不动它们（0010）。清单说「我依赖它」，
    /// 而每一条带着实况那一半（`listening`）。
    let machine: [BerthMachineRef]

    var id: String { name }

    /// 这个项目写过清单没有。**这就是左栏 `projects` 段的判据。**
    ///
    /// 不是「有没有服务」：清单是人写的，`services: []` 是它的常态产物 ——
    /// 「写成项目」放下的就是一份空骨架，服务之后才填。拿「有服务」当判据，
    /// 刚建好的项目在左栏里根本不出现，而那一刻恰恰最需要看得见它。
    var hasConfig: Bool {
        guard let configPath else { return false }
        return !configPath.isEmpty
    }

    /// 页头那一行事实里用的路径。
    var displayRoot: String? {
        guard let rootDir, !rootDir.isEmpty else { return nil }
        return rootDir
    }

    /// 项目名。`<repo>@<worktree>` 那种组名太长，页头放不下，这里用 repo。
    var displayName: String { repo.isEmpty ? name : repo }

    private enum CodingKeys: String, CodingKey {
        case name, repo, worktree, branch, source, status, members, services, machine
        case rootDir = "root_dir"
        case configPath = "config_path"
        case specHash = "spec_hash"
    }

    init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        name = (try? c.decode(String.self, forKey: .name)) ?? ""
        repo = (try? c.decode(String.self, forKey: .repo)) ?? ""
        worktree = (try? c.decode(String.self, forKey: .worktree)) ?? ""
        branch = (try? c.decode(String.self, forKey: .branch)) ?? ""
        source = (try? c.decode(String.self, forKey: .source)) ?? ""
        status = (try? c.decode(String.self, forKey: .status)) ?? ""
        rootDir = try? c.decodeIfPresent(String.self, forKey: .rootDir)
        configPath = try? c.decodeIfPresent(String.self, forKey: .configPath)
        specHash = try? c.decodeIfPresent(String.self, forKey: .specHash)
        members = (try? c.decode([Int].self, forKey: .members)) ?? []
        // 一条解不出来的服务不该让整个项目消失 —— daemon 加一个我们还不认识的
        // 字段是常事，服务板整块空掉才是真的事故。
        services = (try? c.decode([BerthService].self, forKey: .services)) ?? []
        machine = (try? c.decode([BerthMachineRef].self, forKey: .machine)) ?? []
    }
}

/// `oberth.yaml` 里声明的一个服务，daemon 报出来的样子。
struct BerthService: Decodable, Identifiable, Sendable {
    let name: String
    let prepare: String
    let cmd: String
    let cwd: String
    let port: Int?
    let portAuto: Bool
    let health: String?
    let healthStatus: ServiceHealth?
    let description: String?
    let dependsOn: [String]
    /// daemon 的归因结果：**它有没有看到这个服务在监听端口**。
    ///
    /// 不能拿它当「服务在不在跑」—— 不声明端口的服务（worker、只吐日志的构建）
    /// 永远是 false，而它可能跑得好好的。真正的判据是运行注册表（见 RunsSnapshot）。
    let running: Bool
    let portActual: Int?
    let lastExit: ServiceExit?
    let runtimeCmd: String?
    let runtimeCwd: String?
    let manifestHash: String?
    let runtimeSpecHash: String?
    let manifestRuntimeMismatch: Bool
    /// 引擎把这个服务的日志写在哪（`spawn.LogPath` 的形状）—— **daemon 报的才是真值**，
    /// 客户端不再自己拼那份布局：两处约定迟早会漂。老 daemon 不发这个字段，那时退回
    /// `ServicesStore.expectedLogPath` 那份副本。
    let logPath: String?

    var id: String { name }

    /// 这个服务声明了端口没有。`port: auto` 的不算 —— 它起之前没有号。
    var hasPort: Bool { port != nil || portAuto }

    /// 列表里展示的端口：跑起来了就用真实端口，没跑就用声明的。
    var shownPort: Int? { portActual ?? port }

    private enum CodingKeys: String, CodingKey {
        case name, prepare, cmd, cwd, port, description, running, health
        case portAuto = "port_auto"
        case healthStatus = "health_status"
        case dependsOn = "depends_on"
        case portActual = "port_actual"
        case lastExit = "last_exit"
        case runtimeCmd = "runtime_cmd"
        case runtimeCwd = "runtime_cwd"
        case manifestHash = "manifest_hash"
        case runtimeSpecHash = "runtime_spec_hash"
        case manifestRuntimeMismatch = "manifest_runtime_mismatch"
        case logPath = "log_path"
    }

    init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        name = (try? c.decode(String.self, forKey: .name)) ?? ""
        prepare = (try? c.decode(String.self, forKey: .prepare)) ?? ""
        cmd = (try? c.decode(String.self, forKey: .cmd)) ?? ""
        cwd = (try? c.decode(String.self, forKey: .cwd)) ?? ""
        port = try? c.decodeIfPresent(Int.self, forKey: .port)
        portAuto = (try? c.decode(Bool.self, forKey: .portAuto)) ?? false
        health = try? c.decodeIfPresent(String.self, forKey: .health)
        healthStatus = try? c.decodeIfPresent(ServiceHealth.self, forKey: .healthStatus)
        description = try? c.decodeIfPresent(String.self, forKey: .description)
        dependsOn = (try? c.decode([String].self, forKey: .dependsOn)) ?? []
        running = (try? c.decode(Bool.self, forKey: .running)) ?? false
        portActual = try? c.decodeIfPresent(Int.self, forKey: .portActual)
        lastExit = try? c.decodeIfPresent(ServiceExit.self, forKey: .lastExit)
        runtimeCmd = try? c.decodeIfPresent(String.self, forKey: .runtimeCmd)
        runtimeCwd = try? c.decodeIfPresent(String.self, forKey: .runtimeCwd)
        manifestHash = try? c.decodeIfPresent(String.self, forKey: .manifestHash)
        runtimeSpecHash = try? c.decodeIfPresent(String.self, forKey: .runtimeSpecHash)
        manifestRuntimeMismatch = (try? c.decode(Bool.self, forKey: .manifestRuntimeMismatch)) ?? false
        logPath = try? c.decodeIfPresent(String.self, forKey: .logPath)
    }
}

struct ServiceHealth: Decodable, Sendable {
    let status: String
    let code: Int
    let reason: String
    let observedAt: String?

    private enum CodingKeys: String, CodingKey {
        case status, code, reason
        case observedAt = "observed_at"
    }

    init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        status = (try? c.decode(String.self, forKey: .status)) ?? "unknown"
        code = (try? c.decode(Int.self, forKey: .code)) ?? 0
        reason = (try? c.decode(String.self, forKey: .reason)) ?? ""
        observedAt = try? c.decodeIfPresent(String.self, forKey: .observedAt)
    }
}

/// 清单顶层 `machine:` 里的一行：这台机器上、这个项目依赖的服务。
///
/// **是引用，不是这个项目的成员**（[0010]）：`up` 不起它，`down` 也不停它 ——
/// 要停它得点名，那在监听实况详情里。所以这一行没有动作按钮，
/// 它只把实况那一半给出来：那个端口上有没有人在听。
struct BerthMachineRef: Decodable, Identifiable, Sendable {
    let name: String
    let port: Int
    /// 那个端口上有人在听没有。声明说「我依赖它」，这里说「它在不在」——
    /// 不在就是依赖断了，而不是这个项目出了问题。
    let listening: Bool
    /// 认到的系统服务管理器单元（认到才有）。它是「这些端口是一家人的」那个事实 ——
    /// 一条引用靠它覆盖 mysql 的 3306 与 33060。
    let unit: String?

    var id: String { "\(name):\(port)" }

    private enum CodingKeys: String, CodingKey {
        case name, port, listening, unit
    }

    init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        name = (try? c.decode(String.self, forKey: .name)) ?? ""
        port = (try? c.decode(Int.self, forKey: .port)) ?? 0
        listening = (try? c.decode(Bool.self, forKey: .listening)) ?? false
        unit = try? c.decodeIfPresent(String.self, forKey: .unit)
    }
}

/// 一次运行的结局。只有 option-berth 起过的服务才有 —— 归因不上的端口没有「上一次退出」。
struct ServiceExit: Decodable, Sendable {
    let code: Int
    /// exited（正常退出）/ crashed（非零）/ stopped（有人叫它停）/
    /// ready_timeout（就绪等待超时）/ dependency_timeout（依赖等待超时）/
    /// dependency_not_ready（依赖未就绪）。
    let reason: String
    let at: String
    let runID: String

    private enum CodingKeys: String, CodingKey {
        case code, reason, at
        case runID = "run_id"
    }
}

/// `runs.list` 的结果：还在跑的，和已经结束的。
struct RunsSnapshot: Decodable, Sendable {
    let runs: [RunRecord]
    let exited: [RunRecord]

    static let empty = RunsSnapshot(runs: [], exited: [])

    init(runs: [RunRecord], exited: [RunRecord]) {
        self.runs = runs
        self.exited = exited
    }

    init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        runs = (try? c.decode([RunRecord].self, forKey: .runs)) ?? []
        exited = (try? c.decode([RunRecord].self, forKey: .exited)) ?? []
    }

    private enum CodingKeys: String, CodingKey { case runs, exited }

    /// 某个 (项目, 服务) 当前的那次运行。
    ///
    /// 这是「服务在不在跑」唯一可靠的判据：daemon 的 service.running 是**从端口反推**的，
    /// 不监听端口的服务它认不出来。而运行注册表知道每一次 option-berth 自己起的进程。
    func live(group: String, service: String) -> RunRecord? {
        runs.first { $0.group == group && $0.name == service }
    }
}

/// 运行注册表里的一条。
struct RunRecord: Decodable, Identifiable, Sendable {
    let id: String
    let pid: Int
    let group: String
    let name: String
    let cmd: String
    let cwd: String
    let startedAt: String
    let ports: [Int]
    let portHint: Int?
    let url: String?
    /// starting / running / exited
    let status: String
    let configPath: String?
    let logPath: String?
    let exitCode: Int?
    let reason: String?
    let lastLines: [String]

    private enum CodingKeys: String, CodingKey {
        case id, pid, group, name, cmd, cwd, ports, url, status, reason
        case startedAt = "started_at"
        case portHint = "port_hint"
        case configPath = "config_path"
        case logPath = "log_path"
        case exitCode = "exit_code"
        case lastLines = "last_lines"
    }

    init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        id = (try? c.decode(String.self, forKey: .id)) ?? ""
        pid = (try? c.decode(Int.self, forKey: .pid)) ?? 0
        group = (try? c.decode(String.self, forKey: .group)) ?? ""
        name = (try? c.decode(String.self, forKey: .name)) ?? ""
        cmd = (try? c.decode(String.self, forKey: .cmd)) ?? ""
        cwd = (try? c.decode(String.self, forKey: .cwd)) ?? ""
        startedAt = (try? c.decode(String.self, forKey: .startedAt)) ?? ""
        ports = (try? c.decode([Int].self, forKey: .ports)) ?? []
        portHint = try? c.decodeIfPresent(Int.self, forKey: .portHint)
        url = try? c.decodeIfPresent(String.self, forKey: .url)
        status = (try? c.decode(String.self, forKey: .status)) ?? ""
        configPath = try? c.decodeIfPresent(String.self, forKey: .configPath)
        logPath = try? c.decodeIfPresent(String.self, forKey: .logPath)
        exitCode = try? c.decodeIfPresent(Int.self, forKey: .exitCode)
        reason = try? c.decodeIfPresent(String.self, forKey: .reason)
        lastLines = (try? c.decode([String].self, forKey: .lastLines)) ?? []
    }
}

// MARK: - 流式启动

/// `groups.start` 推送过来的东西。一个服务一条，顺序就是它被处理的顺序。
enum StartEvent: Sendable {
    /// 起来了。`logOffset` 是**这次运行开始的位置** —— 日志文件是跨多次运行追加的，
    /// 详情页要从这里读才是「这一次的输出」。
    case started(ServiceStart)
    /// 已经在跑了，没动它。
    case skipped(service: String, reason: String)
    /// 这个服务没能起来（依赖超时、命令找不到、目录不在家目录下……）。
    case failed(service: String, detail: String)
    /// 整批结束。
    case finished(StartSummary)
}

struct ServiceStart: Decodable, Sendable {
    let service: String
    let pid: Int
    let port: Int?
    let logPath: String?
    /// 这一批运行开始前文件有多长。零表示从头（新文件，或者第一次跑）。
    let logOffset: Int64
    let runID: String

    private enum CodingKeys: String, CodingKey {
        case service, pid, port
        case logPath = "log_path"
        case logOffset = "log_offset"
        case runID = "run_id"
    }

    init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        service = (try? c.decode(String.self, forKey: .service)) ?? ""
        pid = (try? c.decode(Int.self, forKey: .pid)) ?? 0
        port = try? c.decodeIfPresent(Int.self, forKey: .port)
        logPath = try? c.decodeIfPresent(String.self, forKey: .logPath)
        // 缺省是 0，不是「不知道」—— 服务端对 0 用 omitempty 省掉了这个字段。
        logOffset = (try? c.decode(Int64.self, forKey: .logOffset)) ?? 0
        runID = (try? c.decode(String.self, forKey: .runID)) ?? ""
    }

    init(service: String, pid: Int, port: Int?, logPath: String?, logOffset: Int64, runID: String) {
        self.service = service
        self.pid = pid
        self.port = port
        self.logPath = logPath
        self.logOffset = logOffset
        self.runID = runID
    }
}

struct StartSummary: Decodable, Sendable {
    let started: [String]
    let skipped: [String]
    let errors: [String]

    private enum CodingKeys: String, CodingKey { case started, skipped, errors }

    init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        started = (try? c.decode([String].self, forKey: .started)) ?? []
        skipped = (try? c.decode([String].self, forKey: .skipped)) ?? []
        errors = (try? c.decode([String].self, forKey: .errors)) ?? []
    }

    init(started: [String], skipped: [String], errors: [String]) {
        self.started = started
        self.skipped = skipped
        self.errors = errors
    }
}

/// `groups.config.get` 读回来的一个项目的清单。
///
/// 这一趟调用有个**很有用的副作用**：daemon 读过的文件会进它自己的索引，
/// 从那一刻起这个项目就出现在 `groups.list` 里了 —— 哪怕它一个端口都没起。
/// 所以「把一个已经写好清单的项目接进来」就是读它一次，不用写任何文件。
struct GroupConfigResult: Decodable, Sendable {
    let path: String
    let config: GroupConfig

    private enum CodingKeys: String, CodingKey { case path, config }

    init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        path = (try? c.decode(String.self, forKey: .path)) ?? ""
        config = (try? c.decode(GroupConfig.self, forKey: .config)) ?? GroupConfig(name: "", services: [])
    }

    init(path: String, config: GroupConfig) {
        self.path = path
        self.config = config
    }
}

struct GroupConfig: Decodable, Sendable {
    let name: String
    let services: [BerthService]

    private enum CodingKeys: String, CodingKey { case name, services }

    init(name: String, services: [BerthService]) {
        self.name = name
        self.services = services
    }

    init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        name = (try? c.decode(String.self, forKey: .name)) ?? ""
        services = (try? c.decode([BerthService].self, forKey: .services)) ?? []
    }
}

/// `groups.init` 的预览：daemon 从这个目录里读出来的东西，和它打算写的文件。
struct GroupInitResult: Decodable, Sendable {    let path: String
    let yaml: String
    let proposal: BerthGroup

    private enum CodingKeys: String, CodingKey { case path, yaml, proposal }

    init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        path = (try? c.decode(String.self, forKey: .path)) ?? ""
        yaml = (try? c.decode(String.self, forKey: .yaml)) ?? ""
        proposal = (try? c.decode(BerthGroup.self, forKey: .proposal)) ?? GroupInitResult.emptyGroup
    }

    init(path: String, yaml: String, proposal: BerthGroup) {
        self.path = path
        self.yaml = yaml
        self.proposal = proposal
    }

    /// proposal 缺失时的占位 —— 与其让整帧解不出来，不如让界面显示一个空项目。
    fileprivate static var emptyGroup: BerthGroup {
        let json = #"{"name":"","repo":"","worktree":"","branch":"","source":"","status":"","members":[],"services":[]}"#
        return (try? JSONDecoder().decode(BerthGroup.self, from: Data(json.utf8)))!
    }
}

/// 打开清单编辑器需要的全部东西：哪个项目、文件在哪、里面现在是什么。
///
/// 编辑框吃的是 `GroupInitResult`（「加项目」那条路的形状），所以这里把**从磁盘读到的**
/// 内容包成同一个东西 —— 两条路共用一个视图，差别只有标题和按钮文案。
struct PendingConfig: Identifiable {
    let project: BerthGroup
    let path: String
    let yaml: String

    var id: String { project.name }
}
