import Darwin
import Foundation

/// 无头自检：不打开窗口，只走一遍「连 daemon → 拉列表 → 打印」。
///
/// 加这个是为了能分清一件很容易混起来的事：窗口是空的，到底是
/// **读不到数据**（socket、daemon、解码），还是**画不出来**（界面）。
/// 界面坏了要截图看，数据坏了要让命令行告诉你 —— 所以这个探针只做数据那一段。
enum Probe {
    static func run() -> Int32 {
        let client = DaemonClient(socketPath: DaemonClient.defaultSocketPath())
        FileHandle.standardError.write(Data("socket: \(client.socketPath)\n".utf8))

        do {
            // 探针和窗口走同一条路：daemon 没起就把它拉起来，否则探针会在
            // 「二进制找不到 / daemon 起不来」这两件事上给出和窗口不一样的结论。
            try DaemonLaunch.ensureRunning(socketPath: client.socketPath)
            let ports = try client.listPorts()
            print("拿到 \(ports.count) 条 worktree 监听实况\n")

            let width = ports.map { String($0.port).count }.max() ?? 4
            for port in ports.sorted(by: { $0.port < $1.port }) {
                let number = String(port.port).padding(toLength: width, withPad: " ", startingAt: 0)
                let project = port.projectName.map { "  \($0)" } ?? ""
                let url = port.url.map { "  \($0)" } ?? ""
                print("\(number)  \(port.displayName)\(project)\(url)")
            }

            var byProject: [String: Int] = [:]
            for port in ports where port.projectName != nil {
                byProject[port.projectName!, default: 0] += 1
            }
            if !byProject.isEmpty {
                print("\n按项目：")
                for (name, count) in byProject.sorted(by: { $0.key < $1.key }) {
                    print("  \(name)  \(count) 个端口")
                }
            }

            try probeServices(client)

            // 起一次服务。要显式打开：探针的本分是只读的观察，
            // 起进程得有人说一声。而这条路值得单独验 —— 它是**流式**的，
            // 帧解析错一点就会静默地少读几个服务，而界面上只会显示「没起来」。
            if let group = ProcessInfo.processInfo.environment["BERTH_PROBE_START"], !group.isEmpty {
                try probeStart(client, group: group)
            }

            // 走一遍「把一个已经写好清单的项目接进来」。这条路的变量只有一个：
            // `groups.config.get` 回来的那一份 JSON 解得对不对 —— 解错了，
            // 界面上就是「接进来一个没有服务的项目」，看起来和「这个项目真的没有服务」一样。
            if let root = ProcessInfo.processInfo.environment["BERTH_PROBE_ADOPT"], !root.isEmpty {
                try probeAdopt(client, rootDir: root)
            }

            return 0
        } catch {
            FileHandle.standardError.write(Data("探针失败：\(error.localizedDescription)\n".utf8))
            return 1
        }
    }

    /// 读一个项目根的清单并把内容打出来。
    private static func probeAdopt(_ client: DaemonClient, rootDir: String) throws {
        let path = rootDir.hasSuffix(".yaml")
            ? rootDir
            : rootDir + "/" + ConfigName
        print("\n接入 \(short(path))：")
        do {
            let read = try client.groupConfig(path: path)
            print("  项目名 \(read.config.name.isEmpty ? "(清单里没写，后台按目录名给)" : read.config.name)")
            for service in read.config.services {
                let port = service.port.map { String($0) } ?? "—"
                print("    \(pad(service.name, 14))\(pad(port, 8))\(service.cmd)")
            }
        } catch {
            print("  读不了：\(error.localizedDescription)")
        }
    }

    /// 清单的文件名。引擎那边是 `groups.ConfigName`，这里是跟着走的副本 ——
    /// 真值是 daemon 在 `groups.init` 的返回里给的 `path`，所以这个常量只用在
    /// 探针这种「我心里已经有一个目录」的地方。
    static let ConfigName = "oberth.yaml"

    /// 走一遍客户端真正走的启动路径，把每一条事件原样打出来。
    private static func probeStart(_ client: DaemonClient, group: String) throws {
        print("\n启动 \(group)（和界面上按「启动全部」走同一条路）：")
        try client.startGroup(group) { event in
            switch event {
            case .started(let start):
                let port = start.port.map { " 端口 \($0)" } ?? " 没有端口"
                print("  ✔ \(start.service)  pid \(start.pid)\(port)"
                    + "  日志 \(short(start.logPath ?? "?")) 从第 \(start.logOffset) 字节起")
            case .skipped(let service, let reason):
                print("  – \(service)  跳过：\(reason)")
            case .failed(let service, let detail):
                print("  ✘ \(service)  失败：\(detail)")
            case .finished(let summary):
                print("  结束：起来 \(summary.started.count) 个，跳过 \(summary.skipped.count) 个，"
                    + "失败 \(summary.errors.count) 个")
            }
        }
    }

    /// 服务那一段：项目声明了什么、谁在跑、日志写在哪。
    ///
    /// 特意把「运行注册表说的」和「groups.list 里的 running 说的」并排打出来 ——
    /// 这两件事在真实数据里会不一致（不声明端口的服务，daemon 认不出来），
    /// 而界面上的状态点信的是前者。不一致的时候这里要看得出来。
    ///
    /// 名单判据和左栏 projects 段是**同一条**（`hasConfig`）：有清单就算项目，
    /// 哪怕里面一条服务都还没写。探针正是用来看「界面上为什么没有它」的 ——
    /// 两处判据不一样，它就会替界面撒谎。
    private static func probeServices(_ client: DaemonClient) throws {
        let groups = try client.listGroups()
        let runs = try client.listRuns()
        let declared = ServicesStore.declared(in: groups).sorted { $0.name < $1.name }

        print("\n服务：\(declared.count) 个项目有清单"
            + "（共 \(declared.reduce(0) { $0 + $1.services.count }) 个服务）")
        for group in declared {
            print("  \(group.name)  \(short(group.rootDir ?? ""))")
            if group.services.isEmpty {
                print("    （清单里还没有服务）")
            }
            for service in group.services {
                let run = runs.live(group: group.name, service: service.name)
                let state: String
                if let run {
                    state = "在跑 pid \(run.pid)"
                } else if service.running {
                    state = "在跑（只按端口判断）"
                } else if let exit = service.lastExit {
                    state = "上次 \(exit.reason)（退出码 \(exit.code)）"
                } else {
                    state = "没跑"
                }
                let port = service.shownPort.map { String($0) } ?? "—"
                let log = run?.logPath ?? service.logPath ?? ServicesStore.expectedLogPath(
                    group: group.name, service: service.name)
                print("    \(pad(service.name, 16))\(pad(port, 8))\(pad(state, 24))\(short(log))")
            }
        }
        if declared.isEmpty {
            print("  （还没有项目写 oberth.yaml —— 左栏「添加项目」可以加一个）")
        }
    }

    private static func pad(_ text: String, _ width: Int) -> String {
        text.count >= width ? text + " " : text.padding(toLength: width, withPad: " ", startingAt: 0)
    }

    private static func short(_ path: String) -> String {
        path.replacingOccurrences(of: FileManager.default.homeDirectoryForCurrentUser.path, with: "~")
    }
}
