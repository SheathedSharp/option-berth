import Combine
import Foundation

/// 界面的数据源：订阅 daemon 的状态流，把首帧快照和后续增量摆到台面上。
///
/// socket 读循环在 utility 队列上，UI 状态仍只在主 actor 改。
@MainActor
final class BoardStore: ObservableObject {
    @Published private(set) var ports: [Port] = []
    @Published private(set) var problem: String?
    @Published private(set) var updatedAt: Date?
    /// 正在把 daemon 拉起来的这一段。界面要能说「稍等」而不是「连不上」。
    @Published private(set) var starting = false

    private let client: DaemonClient
    private let queue = DispatchQueue(label: "option-berth.board.poll", qos: .utility)
    private let stream: DaemonStateStream?
    private var streamObserver: UUID?
    private var inFlight = false
    /// 只自动拉一次。daemon 真的起不来时，长连接会退避重连；这里只负责拉起它。
    private var launchAttempted = false

    init(socketPath: String = DaemonClient.defaultSocketPath()) {
        let client = DaemonClient(socketPath: socketPath)
        self.client = client
        self.stream = DaemonStateStream.shared(socketPath: socketPath)
    }

    /// 只给离屏渲染用的入口：喂一批固定数据，不连 daemon、不起定时器。
    ///
    /// 没有它，`--snapshot` 画出来的永远是**这台机器的实况** —— 那样就没法断言，
    /// 也看不到空态、启动中、连不上这几个只在出问题时才出现的界面，
    /// 而那几屏恰恰是最容易做坏、也最少被人看到的。
    init(
        fixture ports: [Port],
        problem: String? = nil,
        starting: Bool = false,
        socketPath: String = DaemonClient.defaultSocketPath()
    ) {
        client = DaemonClient(socketPath: socketPath)
        stream = nil
        self.ports = ports
        self.problem = problem
        self.starting = starting
        self.updatedAt = Date()
    }

    var socketPath: String { client.socketPath }

    func start(interval: TimeInterval = 2) {
        _ = interval // kept for callers compiled against the old polling API
        if let stream {
            if streamObserver == nil {
                streamObserver = stream.observe(
                    snapshot: { [weak self] snapshot in
                        self?.absorb(snapshot)
                    },
                    delta: { [weak self] delta in
                        self?.absorb(delta)
                    },
                    error: { [weak self] error in
                        self?.streamFailed(error)
                    }
                )
            }
            stream.start()
        } else {
            refresh()
        }
    }

    func stop() {
        if let stream, let streamObserver {
            stream.removeObserver(streamObserver)
            self.streamObserver = nil
        }
    }

    private func absorb(_ snapshot: DaemonStateSnapshot) {
        ports = snapshot.ports
        problem = nil
        starting = false
        updatedAt = Date()
    }

    private func absorb(_ delta: DaemonStateDelta) {
        ports = applyDaemonChange(delta.ports, to: ports, key: \.id)
        problem = nil
        starting = false
        updatedAt = Date()
    }

    private func streamFailed(_ error: Error) {
        problem = error.localizedDescription
        guard !launchAttempted else { return }
        launchAttempted = true
        starting = true
        problem = nil
        queue.async { [weak self] in
            guard let self else { return }
            let launched = Result { try DaemonLaunch.ensureRunning(socketPath: self.client.socketPath) }
            Task { @MainActor [weak self] in
                guard let self else { return }
                self.starting = false
                if case .failure(let error) = launched {
                    self.problem = error.localizedDescription
                }
            }
        }
    }

    func refresh() {
        if let stream {
            // Explicit refresh is also an explicit retry after a failed launch.
            // Subscription backoff never resets this budget on its own.
            if !starting { launchAttempted = false }
            stream.refresh()
            return
        }
        // 上一轮还没回来就跳过：daemon 卡住时不要堆请求。
        guard !inFlight else { return }
        inFlight = true
        queue.async { [client] in
            let outcome = Result { try client.listPorts() }
            Task { @MainActor [weak self] in
                guard let self else { return }
                if case .failure = outcome, !self.launchAttempted {
                    self.launchAttempted = true
                    self.starting = true
                    self.problem = nil
                    self.queue.async { [weak self] in
                        // 首次连不上，多半是 daemon 根本没起 —— 顺手把它拉起来再试一次。
                        // 这一步在后台线程做：`serve --detach` 要等到能接受连接才返回。
                        let launched = Result { try DaemonLaunch.ensureRunning(socketPath: client.socketPath) }
                        Task { @MainActor [weak self] in
                            guard let self else { return }
                            self.starting = false
                            switch launched {
                            case .success:
                                self.inFlight = false
                                self.refresh()
                                return
                            case .failure(let error):
                                self.problem = error.localizedDescription
                            }
                            self.inFlight = false
                        }
                    }
                    return
                }
                switch outcome {
                case .success(let list):
                    self.ports = list
                    self.problem = nil
                    self.updatedAt = Date()
                case .failure(let error):
                    // 拉不到就把上一次的数据留在屏幕上，只把问题说清楚 ——
                    // 界面清空会让人以为是「没有端口」，那是两件不同的事。
                    self.problem = error.localizedDescription
                }
                self.inFlight = false
            }
        }
    }
}
