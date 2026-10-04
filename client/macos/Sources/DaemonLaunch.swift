import Foundation

/// 把 daemon 起起来。
///
/// 客户端本来只连不拉，结果是「第一次打开应用看到一句『连不上』」—— 这不是使用者的错，
/// 是应用少做了一步。daemon 是这套东西的常驻进程，而 `oberth serve --detach`
/// **会等到它开始接受连接才返回**，所以这里同步等它一下就够了，不用自己写轮询。
enum DaemonLaunch {
    enum Failure: LocalizedError {
        case binaryNotFound
        case launchFailed(String)
        case didNotComeUp(String)

        var errorDescription: String? {
            switch self {
            case .binaryNotFound:
                return "找不到 option-berth 二进制。先跑一次 mage install（装到 ~/.local/bin），或者用 BERTH_BIN 指一个"
            case .launchFailed(let detail):
                return "起不来后台：\(detail)"
            case .didNotComeUp(let path):
                return "后台起来了但 socket 没出现：\(path)"
            }
        }
    }

    /// 找 option-berth 二进制。顺序按「使用者最可能把它放哪儿」排：
    /// 环境变量 → `mage install` 的家目录 → 包管理器目录 → 开发时仓库里的构建产物 → PATH。
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
        // Distributed app archives contain the matching engine beside the app
        // executable. An explicit BERTH_BIN still has the highest precedence.
        if let executable = executableURL?.resolvingSymlinksInPath(),
           executable.deletingLastPathComponent().lastPathComponent == "MacOS",
           executable.deletingLastPathComponent().deletingLastPathComponent().lastPathComponent == "Contents" {
            candidates.append(executable.deletingLastPathComponent().appendingPathComponent("oberth").path)
        }
        candidates += [
            "\(home)/.local/bin/oberth",
            "/usr/local/bin/oberth",
            "/opt/homebrew/bin/oberth",
        ]
        // 开发时的构建产物：这个可执行文件在 <repo>/client/macos/build/OptionBerth.app/Contents/MacOS/，
        // 往上是 Contents → .app → build → macos → client → <repo>，再进 bin/。
        if let executable = executableURL?.resolvingSymlinksInPath() {
            var directory = executable.deletingLastPathComponent()
            for _ in 0..<6 {
                directory = directory.deletingLastPathComponent()
            }
            candidates.append(directory.appendingPathComponent("bin/oberth").path)
        }
        for entry in (environment["PATH"] ?? "").split(separator: ":") {
            candidates.append("\(entry)/oberth")
        }

        return candidates.first(where: isExecutable)
    }

    /// 确认 daemon 在跑；没在跑就用 `serve --detach` 起一个。
    /// 返回它用的 socket 路径，失败时抛错并且错误里写清下一步。
    static func ensureRunning(socketPath: String) throws {
        let fileManager = FileManager.default
        if fileManager.fileExists(atPath: socketPath) {
            return
        }

        // 和引擎自己的约定一致：测试和 CI 里设了这个变量就不许自动拉进程。
        if ProcessInfo.processInfo.environment["BERTH_NO_AUTOSTART"] == "1" {
            throw Failure.didNotComeUp(socketPath)
        }

        guard let binary = binaryPath() else { throw Failure.binaryNotFound }

        let process = Process()
        process.executableURL = URL(fileURLWithPath: binary)
        process.arguments = ["serve", "--detach"]
        // 环境原样带过去：socket 路径、数据库位置都是 daemon 从环境里读的。
        process.standardOutput = FileHandle.nullDevice
        process.standardError = FileHandle.nullDevice
        process.standardInput = FileHandle.nullDevice

        do {
            try process.run()
        } catch {
            throw Failure.launchFailed(error.localizedDescription)
        }
        process.waitUntilExit()

        // `--detach` 返回时理论上已经能连了，但 socket 文件落盘偶尔慢半拍 ——
        // 等它出现，而不是立刻报错。
        let deadline = Date().addingTimeInterval(5)
        while Date() < deadline {
            if fileManager.fileExists(atPath: socketPath) { return }
            Thread.sleep(forTimeInterval: 0.1)
        }
        throw Failure.didNotComeUp(socketPath)
    }
}
