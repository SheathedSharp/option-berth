import Foundation

/// 跟着一个日志文件读，每次只取新增的那一段。
///
/// 为什么要自己读文件，而不是再走一次 daemon：**日志本来就是文件**
/// （`~/.option-berth/logs/<项目>/<服务>.log`，引擎的 `spawn.LogPath` 定的），
/// 而引擎是**把 fd 交给了子进程**的 —— 它自己不留一份输出。所以要看，
/// 就得读那个文件；而「读一个文件」不需要经过任何协议，也就没有任何一条
/// 会出错的路。顺带，详情页以后要「读这个文件」，这里就是它。
///
/// 三个必须处理的情况，都是实测会遇到的：
///
/// - **文件还不存在**：服务刚被 spawn、fd 建好到第一次 flush 之间有窗口。
/// - **文件被轮转**：引擎在 10 MiB 上轮转（`spawn.Rotate`），
///   如果偏移量比现在的文件还大，说明已经是新文件了，得从头读。
/// - **一次积了太多**：App 刚打开时偏移量是 0 而文件可能已经很大 ——
///   全读进内存会把界面卡死，所以只读尾部，并如实报出跳过了多少。
struct LogTail {
    /// 一次最多读多少字节。10 MiB 的日志全读进来要几百毫秒，
    /// 而 256 KiB 是上百行 —— 够看清「刚才发生了什么」。
    static let maxBytesPerPoll: Int64 = 256 * 1024

    /// 一行没换行就攒到这么长时丢掉：多半是二进制输出（进度条刷屏、
    /// 某个工具把整个文件打到一行），留着只会越攒越大。
    static let maxCarryBytes = 64 * 1024

    let path: String
    private(set) var offset: Int64
    private var carry = Data()

    init(path: String, from offset: Int64 = 0) {
        self.path = path
        self.offset = offset
    }

    /// 换个位置重新开始（换服务、或者重新跟一次运行）。
    mutating func reset(to newOffset: Int64) {
        offset = newOffset
        carry.removeAll(keepingCapacity: false)
    }

    /// 文件现在多大。不存在就是 0。
    var fileSize: Int64 {
        let attributes = try? FileManager.default.attributesOfItem(atPath: path)
        return (attributes?[.size] as? NSNumber)?.int64Value ?? 0
    }

    /// 读自上次以来新增的完整行。
    mutating func poll() -> LogBatch {
        let size = fileSize

        // 比我们记的还小 —— 文件被轮转或者重建了。从新的第 0 字节开始。
        if size < offset {
            reset(to: 0)
        }

        var start = offset
        var skipped: Int64 = 0
        if size - start > Self.maxBytesPerPoll {
            skipped = size - start - Self.maxBytesPerPoll
            start = size - Self.maxBytesPerPoll
            // 从半路开始读，上一次那条没成行的尾巴就不接得上了。
            carry.removeAll(keepingCapacity: false)
        }
        guard size > start else { return LogBatch(lines: [], skippedBytes: skipped) }

        guard let handle = FileHandle(forReadingAtPath: path) else {
            return LogBatch(lines: [], skippedBytes: skipped)
        }
        defer { try? handle.close() }
        // 读文件失败（权限、刚好被删）不是错误状态：下一次轮询还会再试，
        // 而界面上「还没有输出」和「读不到」看起来本来就该一样。
        guard (try? handle.seek(toOffset: UInt64(start))) != nil,
              let fresh = try? handle.read(upToCount: Int(size - start)),
              !fresh.isEmpty
        else {
            return LogBatch(lines: [], skippedBytes: skipped)
        }
        offset = start + Int64(fresh.count)

        carry.append(fresh)
        var lines: [String] = []
        while let newline = carry.firstIndex(of: 0x0A) {
            let line = carry[carry.startIndex..<newline]
            carry.removeSubrange(carry.startIndex...newline)
            lines.append(Self.decode(line))
        }
        if carry.count > Self.maxCarryBytes {
            lines.append(Self.decode(carry))
            carry.removeAll(keepingCapacity: true)
        }
        return LogBatch(lines: lines, skippedBytes: skipped)
    }

    /// 字节 → 一行字符串。
    ///
    /// `String(decoding:as:)` 遇到坏的 UTF-8 会替换成 U+FFFD 而不是崩 —— 服务打出来的
    /// 东西不保证是 UTF-8（进度条、某个库的二进制输出），而一行乱码不该让日志停下来。
    private static func decode(_ bytes: Data) -> String {
        // 行尾的 \r 是 CRLF 输出留下的，读了只会在等宽字里多一个看不见的方块位置。
        var text = String(decoding: bytes, as: UTF8.self)
        if text.hasSuffix("\r") { text.removeLast() }
        return text
    }
}

/// LogTail 一次读到的结果。
struct LogBatch {
    let lines: [String]
    /// 为了不把界面卡死而丢掉的字节数（只发生在一口气积了太多的时候）。
    let skippedBytes: Int64
}
