import Combine
import Foundation

/// 一个服务在界面上的身份：(项目, 服务名)。两个项目可以有同名的服务
/// （每个像样的仓库里都有一个叫 `api` 的），所以名字本身不够。
struct ServiceRef: Hashable, Sendable {
    let group: String
    let service: String

    var key: String { "\(group)/\(service)" }
}

/// 服务模块的数据源：项目声明的服务、它们有没有在跑、以及正在看的那份日志。
///
/// 和 `BoardStore` 分开是有意的 —— 它们回答的是两个问题。端口那一层
/// （「现在谁在监听」）是只读的观察；服务这一层（「说好要跑的东西起没起」）
/// 是能**动**的：这里的手会去起进程、杀进程。
@MainActor
final class ServicesStore: ObservableObject {
    /// 全部项目。只有写过清单的会被左栏列出来（判据在 `projects`）。
    @Published private(set) var groups: [BerthGroup] = []
    /// 运行注册表：哪一次运行还活着。
    @Published private(set) var runs: RunsSnapshot = .empty
    @Published private(set) var problem: String?
    @Published private(set) var updatedAt: Date?

    /// 正在启动的项目名。启动是流式的、可能等依赖等上几十秒，界面要能说「在起」。
    @Published private(set) var starting: Set<String> = []
    /// 每个服务最近一次操作的反馈（pid、跳过的原因、失败的原因）。
    /// 操作完就消失，不是常驻状态 —— 常驻的是运行注册表。
    @Published private(set) var notice: [ServiceRef: String] = [:]
    /// 正在看日志的那个服务。
    @Published private(set) var focused: ServiceRef?
    /// 那份日志的内容（保留尾部若干行）。**存的是解析过的记录**，不是原始字符串：
    /// 时间、级别、续行只认一次（入队时），后面每一拍刷新都不重算。
    @Published private(set) var logRecords: [LogRecord] = []
    /// 日志文件的位置，给详情和「在 Finder 里显示」用。
    @Published private(set) var logPath: String?
    /// 一次读完丢掉过多少字节（文件太大，只读了尾部）。
    @Published private(set) var logSkippedBytes: Int64 = 0
    /// 这次看的是不是「本次运行」的输出 —— 从别处起过的服务只能看文件尾部。
    @Published private(set) var logIsFromRunStart = false

    /// 查找栏开着没有（⌘F 那条路，或页头那颗「查找」）。
    @Published private(set) var logFindOpen = false
    /// 查找词。
    ///
    /// 它和「开着没有」都放在 store 而不是视图的 `@State`：`--render-states` 那份
    /// 冻结数据得能把它冻住，而视图的 `@State` 冻不了 —— 这一屏（查找栏 + 高亮 + n/m）
    /// 正是平时看不到、做坏了也没人发现的那类。
    @Published private(set) var logQuery = ""
    /// 「把查找栏露出来并聚焦」的信号。递增而不是布尔：开着的时候再按一次 ⌘F
    /// 也该把光标送回框里，而布尔变不出第二次变化。
    @Published private(set) var logFindRequest = 0

    /// 顶部保留多少行。够回溯一次编译的完整输出，又不会让 ScrollView 变重。
    static let keepLines = 3000

    private let client: DaemonClient
    private let queue = DispatchQueue(label: "option-berth.services", qos: .utility)
    private let logQueue = DispatchQueue(label: "option-berth.services.log", qos: .utility)
    private let stream: DaemonStateStream?
    private var streamObserver: UUID?
    private var logTimer: Timer?
    private var inFlight = false
    private var runsInFlight = false
    private var pendingRunsRefresh = false
    /// 这一轮显式刷新还没跑完时又有人要刷新 —— 跑完立刻补一轮。
    /// 长连接负责状态推送，写清单、起停服务之后仍允许显式刷新完整 RPC 结果。
    private var pendingRefresh = false
    private var logInFlight = false

    private var tail: LogTail?
    /// 本次会话里「我们亲手起的」服务，它的日志从哪个字节开始。
    /// 引擎的日志文件是**跨多次运行追加**的，这一条是分辨「这一次」的唯一凭据。
    private var runOffsets: [ServiceRef: Int64] = [:]
    /// 这一会话里已经认领进名册的项目名。认领是写文件，不能每次增量都来一次。
    private var claimed: Set<String> = []


    init(socketPath: String = DaemonClient.defaultSocketPath()) {
        let client = DaemonClient(socketPath: socketPath)
        self.client = client
        self.stream = DaemonStateStream.shared(socketPath: socketPath)
    }

    /// 只给离屏渲染用的入口：喂一批固定数据，不连 daemon、不起定时器。
    init(
        fixture groups: [BerthGroup],
        runs: RunsSnapshot = .empty,
        focused: ServiceRef? = nil,
        logs: [String] = [],
        logPath: String? = nil,
        fromRunStart: Bool = true,
        query: String = "",
        findOpen: Bool = false,
        problem: String? = nil,
        notice: [ServiceRef: String] = [:],
        starting: Set<String> = [],
        socketPath: String = DaemonClient.defaultSocketPath()
    ) {
        client = DaemonClient(socketPath: socketPath)
        stream = nil
        self.groups = groups
        self.runs = runs
        self.focused = focused
        // 和真跑那条路走**同一个解析器**：冻在图上的是它的输出，不是另写一套。
        self.logRecords = logs.map(LogParse.record)
        self.logPath = logPath
        self.logIsFromRunStart = fromRunStart
        self.logQuery = query
        self.logFindOpen = findOpen
        self.problem = problem
        self.notice = notice
        self.starting = starting
        self.updatedAt = Date()
    }

    var socketPath: String { client.socketPath }

    /// 左栏和空态看的是这一份：**写过清单的项目**。
    ///
    /// 判据是 `hasConfig` 不是 `hasServices` —— 「写成项目」给出的就是一份空清单
    /// （服务只有人知道，机器不猜），所以「刚建好、还没填」必须算是项目。
    /// 拿「有服务」当门槛的那一版里，这类项目既不进左栏、也被归到「探到的目录」那一档，
    /// 项目页上还没有入口补服务 —— 加项目这件事在界面上就不成立了。
    var projects: [BerthGroup] { ServicesStore.declared(in: groups) }

    /// 上面那一条判据本身。探针（没有 store、也不在主线程上那条路）也用它 —— 同一件事
    /// 只写一处，两处判据不一样的时候，探针就会替界面撒谎，而它存在的理由正是
    /// 「界面上为什么没有它」。纯函数，所以 `nonisolated`。
    nonisolated static func declared(in groups: [BerthGroup]) -> [BerthGroup] {
        groups.filter(\.hasConfig)
    }

    // MARK: - 状态订阅

    func start(interval: TimeInterval = 2) {
        _ = interval // kept for callers compiled against the old polling API
        guard let stream else {
            refresh()
            return
        }
        if streamObserver == nil {
            streamObserver = stream.observe(
                snapshot: { [weak self] snapshot in
                    self?.absorb(snapshot)
                },
                delta: { [weak self] delta in
                    self?.absorb(delta)
                },
                error: { [weak self] error in
                    self?.stateFailed(error)
                }
            )
        }
        stream.start()
        refreshRuns()
    }

    func stop() {
        if let streamObserver {
            stream?.removeObserver(streamObserver)
            self.streamObserver = nil
        }
        logTimer?.invalidate()
        logTimer = nil
    }

    private func absorb(_ snapshot: DaemonStateSnapshot) {
        groups = snapshot.groups
        problem = nil
        updatedAt = Date()
        claim(snapshot.groups)
    }

    private func absorb(_ delta: DaemonStateDelta) {
        groups = applyDaemonChange(delta.groups, to: groups, key: \.name)
        problem = nil
        updatedAt = Date()
        claim(groups)
        // Runs are a separate registry because workers do not have to own a
        // listening port. Refresh that small list only when a state delta says
        // something observable changed, instead of polling both collections.
        refreshRuns()
    }

    private func stateFailed(_ error: Error) {
        problem = error.localizedDescription
    }

    private func refreshRuns() {
        guard !runsInFlight else {
            pendingRunsRefresh = true
            return
        }
        runsInFlight = true
        queue.async { [client] in
            let outcome = Result { try client.listRuns() }
            Task { @MainActor [weak self] in
                guard let self else { return }
                switch outcome {
                case .success(let snapshot):
                    self.runs = snapshot
                    self.problem = nil
                    self.updatedAt = Date()
                case .failure(let error):
                    self.problem = error.localizedDescription
                }
                self.runsInFlight = false
                if self.pendingRunsRefresh {
                    self.pendingRunsRefresh = false
                    self.refreshRuns()
                }
            }
        }
    }

    func refresh() {
        stream?.refresh()
        // 这一轮跑着的时候又来一次（写完清单、认领回来都走这里）：记下来，跑完补一轮。
        // 直接 `return` 会把那次刷新丢掉，而它多半是「刚做完一件事，要看结果」。
        guard !inFlight else {
            pendingRefresh = true
            return
        }
        inFlight = true
        queue.async { [client] in
            // 两个调用合起来才是完整的一帧：项目和服务定义来自 groups.list，
            // 「谁真的在跑」来自 runs.list。分开拿是有意的 —— 一次拿不到
            // 不该把另一次的结果也抹掉。
            let groups = Result { try client.listGroups() }
            let runs = Result { try client.listRuns() }
            Task { @MainActor [weak self] in
                guard let self else { return }
                switch (groups, runs) {
                case (.success(let list), .success(let snapshot)):
                    self.groups = list
                    self.runs = snapshot
                    self.problem = nil
                    self.updatedAt = Date()
                    self.claim(list)
                case (.failure(let error), _), (_, .failure(let error)):
                    // 拉不到就留着上一次的数据，只把问题说清楚 —— 界面清空
                    // 会让人以为「这个项目没有服务」，那是另一回事。
                    self.problem = error.localizedDescription
                }
                self.inFlight = false
                if self.pendingRefresh {
                    self.pendingRefresh = false
                    self.refresh()
                }
            }
        }
    }

    // MARK: - 服务在不在跑

    /// 某个 (项目, 服务) 现在活着的那次运行，没有就是 nil。
    ///
    /// **这是判据本身，不是 service.running 的补充。** `groups.list` 里的
    /// `running` 是 daemon 从端口反推的：它只看得见在监听的服务，
    /// 一个不声明端口的 worker 跑得好好的也报 false（实测过）。
    /// 运行注册表记的是 option-berth 自己起过的进程，跟端口无关。
    func live(_ service: BerthService, in group: BerthGroup) -> RunRecord? {
        runs.live(group: group.name, service: service.name)
    }

    func liveCount(in group: BerthGroup) -> Int {
        group.services.filter { live($0, in: group) != nil }.count
    }

    /// 这个项目里**这一页停得掉**的运行。「全部停止」按它决定露不露面：
    /// 一个都没在跑的时候，这个按钮没有可停的东西 —— 露着就是个骗人的按钮。
    ///
    /// 清单顶层 `machine:` 里那些引用不在这个口径里：它们不是这个项目的成员，
    /// 也就不在运行注册表里（[0010]），不需要谁把它们排除掉。
    func stoppableCount(in group: BerthGroup) -> Int { stoppableRuns(in: group).count }

    /// 这一页会去停的那些运行：这个项目里活着的。`stop` 和它的判据共用这一条，
    /// 两处口径不会漂。
    private func stoppableRuns(in group: BerthGroup) -> [RunRecord] {
        runs.runs.filter { $0.group == group.name }
    }

    func isWorking(_ group: BerthGroup) -> Bool { starting.contains(group.name) }

    // MARK: - 起停

    /// 起一个项目的服务。`only` 为空就是全部（仍然按依赖顺序）。
    ///
    /// 它在后台队列上跑完整场：引擎每处理完一个服务推一条，这里就更新一条 ——
    /// 所以界面上是**一个个亮起来**的，不是一个转十秒的圈。
    func start(_ group: BerthGroup, only: [String] = []) {
        guard !starting.contains(group.name) else { return }
        starting.insert(group.name)
        for service in group.services where only.isEmpty || only.contains(service.name) {
            notice[ServiceRef(group: group.name, service: service.name)] = nil
        }
        let name = group.name
        queue.async { [client] in
            do {
                try client.startGroup(name, only: only) { event in
                    Task { @MainActor [weak self] in self?.absorb(event, group: name) }
                }
                Task { @MainActor [weak self] in
                    self?.starting.remove(name)
                    self?.refresh()
                }
            } catch {
                Task { @MainActor [weak self] in
                    self?.starting.remove(name)
                    self?.problem = error.localizedDescription
                }
            }
        }
    }

    /// Restart one or more services through the daemon's restart lifecycle.
    /// Keeping the stop and start in one client operation preserves the same
    /// auto-port and dependency behavior as `oberth restart --only`.
    func restart(_ group: BerthGroup, only: [String]) {
        guard !starting.contains(group.name), !only.isEmpty else { return }
        starting.insert(group.name)
        let name = group.name
        queue.async { [client] in
            do {
                try client.restartGroup(name, only: only) { event in
                    Task { @MainActor [weak self] in self?.absorb(event, group: name) }
                }
                Task { @MainActor [weak self] in
                    self?.starting.remove(name)
                    self?.refresh()
                }
            } catch {
                Task { @MainActor [weak self] in
                    self?.starting.remove(name)
                    self?.problem = error.localizedDescription
                    self?.refresh()
                }
            }
        }
    }

    private func absorb(_ event: StartEvent, group: String) {
        switch event {
        case .started(let started):
            let ref = ServiceRef(group: group, service: started.service)
            runOffsets[ref] = started.logOffset
            var text = started.pid > 0 ? "pid \(started.pid)" : "已启动"
            if let port = started.port, port > 0 { text += " · 端口 \(port)" }
            notice[ref] = text
            // 正在看的就是刚起来的那个？那就立刻把日志接到这次运行的位置上。
            if focused == ref { attachTail(for: ref, fromRunStart: true) }
        case .skipped(let service, let reason):
            notice[ServiceRef(group: group, service: service)] = reason
        case .failed(let service, let detail):
            notice[ServiceRef(group: group, service: service)] = detail
        case .finished:
            // 每个服务的结果都已经逐条落到 notice 里了，这里不再重复一遍 ——
            // 「3 个起来了、1 个跳过了」这种汇总数字在服务板上是看出来的，不是读出来的。
            break
        }
    }

    /// 停一个项目里 option-berth 起过的**全部**服务。
    ///
    /// 逐条按 pid 停，不用引擎那两个按组/按端口的停法 —— 它们**看不见不占端口的
    /// 服务**（实测：`groups.kill` 直接报 `no listening port belongs to group`，
    /// 而那个只打日志的服务还活着）。运行注册表是唯一知道全部进程的地方，
    /// 所以这里从它出发。顺带也能停掉用 `runs.spawn` 起、不在 yaml 里的运行。
    ///
    /// 声明了 `stop: leave` 的服务不在里面：它和 `oberth down` 做的是同一件事，
    /// 口径就得同一条 —— 机器级的 mysql 不属于这个项目的开关管。要停它，用它自己
    /// 那一行的「停止」（那是点名要停）。
    func stop(_ group: BerthGroup) {
        guard !starting.contains(group.name) else { return }
        let pids = stoppableRuns(in: group).map(\.pid)
        guard !pids.isEmpty else { return }
        starting.insert(group.name)
        let name = group.name
        queue.async { [client] in
            var failure: Error?
            for pid in pids {
                do {
                    try client.killRun(pid: pid)
                } catch {
                    failure = error
                }
            }
            Task { @MainActor [weak self] in
                guard let self else { return }
                self.starting.remove(name)
                if let failure {
                    self.problem = failure.localizedDescription
                } else {
                    self.notice = self.notice.filter { $0.key.group != name }
                }
                self.refresh()
            }
        }
    }

    /// 停一个服务。按运行注册表里的 pid 停 —— 见 `DaemonClient.killRun` 那张表，
    /// 按端口和 run id 都停不到不占端口的服务。
    func stop(_ service: BerthService, in group: BerthGroup) {
        guard let run = live(service, in: group) else { return }
        let ref = ServiceRef(group: group.name, service: service.name)
        notice[ref] = "正在停…"
        queue.async { [client] in
            let outcome = Result { try client.killRun(pid: run.pid) }
            Task { @MainActor [weak self] in
                guard let self else { return }
                switch outcome {
                case .success:
                    self.notice[ref] = nil
                    // 正在看的这个被停掉了：日志留着（那是它说过的话），
                    // 只是不再跟着读了。
                    self.refresh()
                case .failure(let error):
                    self.notice[ref] = error.localizedDescription
                }
            }
        }
    }

    // MARK: - 未声明监听上的那一个动作

    /// 停掉监听在某个端口上的进程 —— 服务页的未声明监听详情走这里。
    ///
    /// 和 `stop(_ service:in:)` 是两件事，别合并：那条停的是**这个软件自己起过的**
    /// 服务，凭据是运行注册表里的 pid；这条停的是这台机器上任何一个端口的主人 ——
    /// brew 起的 mysql、手装的 nacos、你在终端里拉起来的进程都算，它们的共同点恰恰是
    /// 运行注册表里没有它们。所以它走端口，不走 pid（见 `DaemonClient.killPort`），
    /// 也不动运行注册表：停掉了没有任何记录要清，等下一轮刷新那一行自己就不见了。
    ///
    /// `done` 两条路都会回：成功回 nil，出错回一句话说给用户听。成功那条路
    /// 不该打扰 —— 那一行下一秒就从表上消失，再弹一个「已停止」是替用户说他刚看见的事。
    func kill(_ port: Port, done: @escaping (String?) -> Void) {
        queue.async { [client] in
            let outcome = Result { try client.killPort(port: port.port, bindAddress: port.bindAddress) }
            Task { @MainActor [weak self] in
                guard let self else { return }
                switch outcome {
                case .success(let reason):
                    done(reason)
                    // 引擎在杀完那次调用里已经重新扫过了，所以这一次刷新拿到的是
                    // 已经没有了那一行的快照 —— 不用等 2 秒那一轮。
                    self.refresh()
                case .failure(let error):
                    done(error.localizedDescription)
                }
            }
        }
    }

    // MARK: - 日志

    /// 把日志面板切到某个服务。传 nil 就停下来。
    func focus(_ ref: ServiceRef?) {
        guard ref != focused else { return }
        focused = ref
        logRecords = []
        logSkippedBytes = 0
        tail = nil
        logPath = nil
        logTimer?.invalidate()
        logTimer = nil
        // 换人看（或者不看了）就把查找收掉：查找栏里那个词是对**上一个服务**的日志说的，
        // 留着它会让下一份日志一进来就满屏高亮，而用户没在那儿查过任何东西。
        logFindOpen = false
        logQuery = ""
        guard let ref else { return }

        // 路径的权威在 daemon：跑着的服务看运行注册表，没跑过的看清单行上那个
        // `log_path`（引擎按自己的布局报的）。两个都没有才退回本地推算 —— 那是老
        // daemon 的兼容路径，不是第二份真值。
        let declared = groups.first { $0.name == ref.group }?
            .services.first { $0.name == ref.service }
        let path = runs.live(group: ref.group, service: ref.service)?.logPath
            ?? declared?.logPath
            ?? Self.expectedLogPath(group: ref.group, service: ref.service)
        logPath = path
        let fromRunStart = runOffsets[ref] != nil
        logIsFromRunStart = fromRunStart
        attachTail(for: ref, fromRunStart: fromRunStart)
        // 立刻读一次，不等第一个定时器周期。
        //
        // 文件里本来就有东西（上一次跑剩下的、或者服务已经在跑），
        // 而 0.5 秒的空窗看起来和「这个服务什么都没输出」一模一样 ——
        // 是 `--snapshot` 那张实况图抓出来的：日志文件 15 KB，面板上写着「还没有输出」。
        pollLog()

        // 日志比列表更新得快：服务在编译、在打请求，两秒一跳会明显发顿。
        logTimer = Timer.scheduledTimer(withTimeInterval: 0.5, repeats: true) { [weak self] _ in
            Task { @MainActor in self?.pollLog() }
        }
    }

    /// 引擎把日志写在哪 —— **老 daemon 的兼容副本，不是真值**。真值随行发布：
    /// 运行注册表的 `log_path`（跑着的服务）、清单行的 `log_path`（引擎按
    /// `internal/paths` 的布局报的）。两条都缺时才落到这里。
    ///
    /// `nonisolated`：它只读文件系统，没有任何状态，命令行探针也要用。
    nonisolated static func expectedLogPath(group: String, service: String) -> String {
        let home = FileManager.default.homeDirectoryForCurrentUser.path
        return "\(home)/.option-berth/logs/\(group)/\(service).log"
    }

    /// 一个项目的日志目录：`~/.option-berth/logs/<项目>`。一个服务一个文件。
    nonisolated static func logsDirectory(group: String) -> String {
        (expectedLogPath(group: group, service: "service") as NSString).deletingLastPathComponent
    }

    private func attachTail(for ref: ServiceRef, fromRunStart: Bool) {
        guard let path = logPath else { return }
        var fresh = LogTail(path: path)
        if fromRunStart {
            fresh.reset(to: runOffsets[ref] ?? 0)
        } else {
            // 不是我们起的（在终端里跑的、上次 App 关掉前起的）：没有偏移量可依，
            // 就从文件尾部往前取一段 —— 「最近发生了什么」比「从头到尾」更有用。
            fresh.reset(to: max(0, fresh.fileSize - 16 * 1024))
        }
        tail = fresh
    }

    private func pollLog() {
        guard focused != nil, var current = tail, !logInFlight else { return }
        logInFlight = true
        let path = current.path
        logQueue.async { [weak self] in
            let batch = current.poll()
            Task { @MainActor [weak self] in
                guard let self else { return }
                self.logInFlight = false
                // 读的过程中用户换了服务（或者换了项目里同名服务的那条日志）：
                // 这一批是上一个文件的，丢掉。
                guard self.logPath == path else { return }
                self.tail = current
                self.logSkippedBytes += batch.skippedBytes
                guard !batch.lines.isEmpty else { return }
                self.logRecords.append(contentsOf: batch.lines.map(LogParse.record))
                if self.logRecords.count > Self.keepLines {
                    self.logRecords.removeFirst(self.logRecords.count - Self.keepLines)
                }
            }
        }
    }

    /// 打开查找栏（⌘F / 页头那颗「查找」）。清词是**不**做的：
    /// 开着的时候再按一次 ⌘F 是把光标送回框里，顺手把上次那个词抹掉反而更烦。
    func beginLogFind() {
        guard focused != nil else { return }
        logFindOpen = true
        logFindRequest += 1
    }

    func setLogQuery(_ text: String) {
        guard text != logQuery else { return }
        logQuery = text
        // 词变了，但「开着」这件事跟着用户走：他清空框子不等于要关掉查找栏。
        logFindOpen = true
    }

    /// 关掉查找栏并清词（Esc / 那颗 ×）。
    func endLogFind() {
        logFindOpen = false
        logQuery = ""
    }

    // MARK: - 名册

    /// 左栏看得到的项目，`~/.option-berth/projects/` 里都得有名字。
    ///
    /// 项目是**显式纳管**的东西，而「界面上看得到、名册上却没有」是半个状态：那样的
    /// 项目点不了移除，也说不清它是被谁纳进来的。手写的清单（用户自己往项目里放了
    /// `oberth.yaml`，没走过 App）在这里被认领一次。
    ///
    /// 认领过就不再管 —— 用户要是手动删了那个目录，那是他的意思，不该每两秒给他建回来。
    private func claim(_ groups: [BerthGroup]) {
        for group in groups where group.hasConfig {
            guard !claimed.contains(group.name) else { continue }
            claimed.insert(group.name)
            guard !ProjectRegistry.contains(group.name),
                  let root = group.rootDir, !root.isEmpty,
                  let config = group.configPath, !config.isEmpty
            else { continue }
            ProjectRegistry.adopt(name: group.name, root: root, config: config)
        }
    }

    // MARK: - 添加项目

    /// 问 daemon「这个目录能起什么服务」。不写盘。
    func propose(rootDir: String, completion: @escaping (Result<GroupInitResult, Error>) -> Void) {
        queue.async { [client] in
            let outcome = Result { try client.initGroup(rootDir: rootDir, write: false) }
            Task { @MainActor in completion(outcome) }
        }
    }

    /// 把一个**已经有清单**的项目接进来。
    ///
    /// 只读一次那个文件，不写、不改：见 `DaemonClient.groupConfig` 的注释 ——
    /// daemon 读过的文件会进它自己的索引，这个项目随即出现在 `groups.list` 里。
    func adopt(configPath: String, completion: @escaping (Result<GroupConfigResult, Error>) -> Void) {
        queue.async { [client] in
            let outcome = Result { try client.groupConfig(path: configPath) }
            Task { @MainActor [weak self] in
                if case .success = outcome { self?.refresh() }
                completion(outcome)
            }
        }
    }

    /// 把一个**已经纳管**的项目的清单写回去。
    ///
    /// 和「加项目」那一次唯一的差别是 `force`：那份文件本来就在这儿，改它就是这个
    /// 动作的本意。校验、重新索引、界面刷新都和写入新清单走同一条路。
    func rewriteConfig(rootDir: String, yaml: String,
                       completion: @escaping (Result<GroupInitResult, Error>) -> Void) {
        queue.async { [client] in
            let outcome = Result { try client.initGroup(rootDir: rootDir, write: true, yaml: yaml, force: true) }
            Task { @MainActor [weak self] in
                if case .success = outcome { self?.refresh() }
                completion(outcome)
            }
        }
    }

    /// 真把 `oberth.yaml` 写下去。
    ///
    /// `yaml` 是用户在预览里改过的那份：给了就照它写（daemon 先校验），不给就写
    /// daemon 自己生成的那份。
    func writeConfig(rootDir: String, yaml: String? = nil,
                     completion: @escaping (Result<GroupInitResult, Error>) -> Void) {
        queue.async { [client] in
            let outcome = Result { try client.initGroup(rootDir: rootDir, write: true, yaml: yaml) }
            Task { @MainActor in
                completion(outcome)
            }
        }
    }
}
