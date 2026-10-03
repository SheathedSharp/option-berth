import Foundation

/// 「我纳管了哪些项目」落在文件上：一个项目一个目录，和日志目录同形。
///
/// **为什么要有它。** 项目这个状态从前是纯派生的 —— `oberth.yaml` 写在项目自己的
/// 根目录（要进 git，所以不能搬走），daemon 从端口和磁盘现算出一份名单，而「用户纳管了
/// 哪些」这件事只活在一张数据库表里（`known_roots` 的若干行路径）。看得见、摸不着，
/// 也删不掉：想让一个项目从界面上消失，只能去改数据库。
///
/// 而 `oberth.yaml` 有一个特殊之处：**它写在别人的项目里**。用户哪天不想用了，
/// 得有个地方一次撤干净。所以他需要一个实体：
///
///     ~/.option-berth/projects/<项目>/project.json
///
/// 里面记这个项目叫什么、根目录在哪、清单文件在哪、什么时候纳进来的。删除一个项目就是
/// 删这个目录 **加** 删项目里那份清单 —— 两件事一起做，不留半个状态。
///
/// 它和 `~/.option-berth/logs/<项目>/` 是邻居，形状也一样：那边是输出，这边是名册。
enum ProjectRegistry {
    /// 一份登记。
    struct Entry: Codable, Equatable, Sendable {
        var name: String
        var root: String
        var config: String
        var added: String
    }

    /// 撤销纳管的结果，给界面报账用。
    struct Removal: Sendable {
        var name: String
        /// 清单原来的位置（可能本来就不在）。
        var config: String
        var configRemoved: Bool
        var registryRemoved: Bool

        /// 一句话说清刚才动了什么，出错时也说得清 —— 界面直接念它。
        var summary: String {
            var parts: [String] = []
            parts.append(configRemoved ? "清单已删除" : "清单本来就不在")
            if !registryRemoved { parts.append("登记没能删掉") }
            return "\(name)：\(parts.joined(separator: "，"))"
        }
    }

    /// 登记册的根：`~/.option-berth/projects`。
    ///
    /// 引擎的 `internal/paths` 是**引擎**布局的唯一来源，而这一份是客户端自己管的，
    /// 引擎不知道它 —— 所以这里照着同一套约定取路径（含 `BERTH_HOME` 覆盖），
    /// 不假装它是引擎的目录。
    static var rootDirectory: String {
        if let override = ProcessInfo.processInfo.environment["BERTH_HOME"], !override.isEmpty {
            return "\(override)/projects"
        }
        let home = FileManager.default.homeDirectoryForCurrentUser.path
        return "\(home)/.option-berth/projects"
    }

    /// 一个项目的登记目录：`~/.option-berth/projects/<项目>`。
    static func directory(for name: String) -> String {
        (rootDirectory as NSString).appendingPathComponent(name)
    }

    /// 登记文件的位置。
    static func entryPath(for name: String) -> String {
        (directory(for: name) as NSString).appendingPathComponent("project.json")
    }

    /// 读一份登记，没有就是 nil。
    static func entry(for name: String) -> Entry? {
        guard let data = FileManager.default.contents(atPath: entryPath(for: name)) else {
            return nil
        }
        return try? JSONDecoder().decode(Entry.self, from: data)
    }

    /// 这个项目登记过没有。
    static func contains(_ name: String) -> Bool { entry(for: name) != nil }

    /// 登记一个项目。**已经登记过的就不动** —— 幂等，而且原始时间戳该留着。
    ///
    /// 两条路都走它：用户在 App 里主动纳管（写清单 / 接入已有清单），和 App 认领一个
    /// 手写着清单、但没走过 App 的项目。
    ///
    /// - Returns: 落盘后的登记；失败是 nil（调用方不该因此中断主流程 —— 清单才是要紧的
    ///   那个，名册记不上只是少了个入口）。
    @discardableResult
    static func adopt(name: String, root: String, config: String) -> Entry? {
        if let existing = entry(for: name) { return existing }
        let entry = Entry(name: name, root: root, config: config, added: timestamp())
        return write(entry) ? entry : nil
    }

    /// 撤销一份登记，返回它原来记着什么。
    ///
    /// **整个目录删掉**，不只是那个文件：一个项目以后可能在这里放别的东西（偏好、
    /// 备注），撤销就该是连窝端。
    @discardableResult
    static func forget(_ name: String) -> Entry? {
        let existing = entry(for: name)
        try? FileManager.default.removeItem(atPath: directory(for: name))
        return existing
    }

    /// 名册上都有谁，排序。给「项目是从哪来的」这类问题用，界面暂时不读它 ——
    /// 左栏那一列还是 daemon 说了算（它才知道哪个项目现在起没起来）。
    static func names() -> [String] {
        let fm = FileManager.default
        guard let items = try? fm.contentsOfDirectory(atPath: rootDirectory) else { return [] }
        return items
            .filter { !$0.hasPrefix(".") }
            .filter { fm.fileExists(atPath: entryPath(for: $0)) }
            .sorted()
    }

    /// 撤销一个项目：**先删项目里那份清单，再删登记。**
    ///
    /// 顺序是有意的 —— 先动最关键的那一个。清单删掉而登记还在，下一轮认领会把这条
    /// 孤儿登记收掉；反过来留下的是「界面上还在、但登记已经没了」的项目，更难收场。
    ///
    /// 删的是用户项目里的文件，而且那文件很可能已经提交进 git 了 —— **调用方必须先
    /// 让他确认**，说清要删哪两样。
    ///
    /// 日志（`~/.option-berth/logs/<项目>/`）不动：那是历史，跟「还要不要管这个项目」
    /// 是两回事，留着也不碍事。
    static func remove(name: String, config: String) -> Removal {
        let fm = FileManager.default
        var configRemoved = false
        if !config.isEmpty, fm.fileExists(atPath: config) {
            configRemoved = (try? fm.removeItem(atPath: config)) != nil
        }
        forget(name)
        return Removal(name: name,
                       config: config,
                       configRemoved: configRemoved,
                       registryRemoved: !fm.fileExists(atPath: directory(for: name)))
    }

    private static func write(_ entry: Entry) -> Bool {
        let fm = FileManager.default
        do {
            try fm.createDirectory(atPath: directory(for: entry.name),
                                   withIntermediateDirectories: true)
            let encoder = JSONEncoder()
            encoder.outputFormatting = [.prettyPrinted, .sortedKeys]
            let data = try encoder.encode(entry)
            try data.write(to: URL(fileURLWithPath: entryPath(for: entry.name)))
            return true
        } catch {
            return false
        }
    }

    /// 命令行入口（`--remove-project <项目>`）：把撤销纳管真跑一遍。
    ///
    /// **为什么要有它。** 移除是个删用户文件的动作，而它只挂在窗口里一个按钮后面 ——
    /// 离屏渲染点不到那个按钮，这条路上不亲手跑一次，就只能靠读代码相信它删对了。
    /// 和 `--snapshot` / `--probe` 是同一类东西：给验证台用的入口，不是给用户的功能。
    ///
    /// 项目名要**先在名册上**：清单路径从登记里读，不猜（猜错就是删错文件）。
    static func runRemoval(name: String) -> Int32 {
        guard let entry = entry(for: name) else {
            FileHandle.standardError.write(Data("名册上没有 \(name)\n".utf8))
            return 1
        }
        let outcome = remove(name: name, config: entry.config)
        print(outcome.summary)
        print("  登记  \(directory(for: name))  \(outcome.registryRemoved ? "已删除" : "还在")")
        print("  清单  \(entry.config)  \(outcome.configRemoved ? "已删除" : "本来就不在")")
        let configGone = !FileManager.default.fileExists(atPath: entry.config)
        return outcome.registryRemoved && configGone ? 0 : 1
    }

    /// ISO8601，和引擎写进数据库的时间戳同一个形状。
    private static func timestamp() -> String {
        let formatter = ISO8601DateFormatter()
        formatter.formatOptions = [.withInternetDateTime]
        return formatter.string(from: Date())
    }
}
