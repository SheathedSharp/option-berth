import Foundation

/// 日志级别。**就这四档，认不出来就不进这张表** —— 认错比认不出更坏：
/// 一行字被涂成红的，读的人会先信它。
enum LogLevel {
    case error
    case warn
    case info
    case debug

    /// 级别列里那几个字。等宽大写、五位以内 —— 列宽才立得住。
    var label: String {
        switch self {
        case .error: return "ERROR"
        case .warn: return "WARN"
        case .info: return "INFO"
        case .debug: return "DEBUG"
        }
    }

    /// 一张写死的词表，不做前缀匹配、不做模糊匹配。
    ///
    /// 引擎那边拿 `cannot` 当错误关键词，把采集器自己的哀鸣
    /// （`log: Cannot run while sandboxed`）判成了应用错误 —— 同一类错不该犯第二次。
    /// 这里比那儿还严一档：词要**正好**等于表里那一个，后面还得跟着分隔符。
    static let words: [String: LogLevel] = [
        "error": .error, "err": .error, "fatal": .error, "panic": .error,
        "critical": .error, "crit": .error, "severe": .error,
        "warn": .warn, "warning": .warn,
        "info": .info, "notice": .info,
        "debug": .debug, "trace": .debug,
    ]
}

/// 一条日志记录：从一行原文里**认出来的**那部分。
///
/// 认不出来的时候**不编**：`time` / `level` 留空，`message` 就是整行。
/// 服务打出来的东西不保证有结构（进度条、二进制、某个库自创的格式），
/// 界面不许替它编一个 —— 编出来的排版比不排版更难查。
///
/// 存的是**认出来的结果**，不存原始那一行：界面上显示的就是这几格，
/// 查找也是按这几格找的（于是「命中的一定看得见高亮」是个恒等式）。
/// 认出来之后被丢掉的东西只有三样，都是有意的：日期、时区、终端控制码。
///
/// 这一层是**客户端**的事（不是引擎的）：日志本来就是文件，排版只是读法。
struct LogRecord {
    var time: String?
    var level: LogLevel?
    var message: String
    /// 这一行像是上一条的尾巴（堆栈、被打断的多行消息）。
    var continuation: Bool = false
    /// 访问日志里那个状态码。只有 4xx / 5xx 会用它上色。
    var http: Int?
}

/// 一行原文 → 一条记录。纯函数、没有状态、没有配置 —— 同一行永远得到同一个结果。
///
/// 认的这几种形状（都是行首）：
///
/// | 形状 | 例 |
/// |---|---|
/// | ISO 戳 | `2026-09-24T09:41:07.312+08:00 INFO  ...` |
/// | 空格分开的日期 | `2026-09-24 09:41:07,312 WARN ...` |
/// | 只有钟点 | `09:41:07.312 ERROR ...` |
/// | 方/圆括号里的戳 | `[09:41:07] [INFO] ...` |
/// | 只有级别 | `INFO:  Started server process` |
/// | 级别写在字段里 | `level=error msg=...` / `{"level":"error",...}` |
///
/// 不认的：`22/Sep/2026 00:52:19`（Apache 那种月份缩写）、行中间的戳、
/// 每行一条的 JSON 里的字段（认它是为了上色，内容一个字不重排）。
/// 不认就不认 —— 原文照排，不许吞掉任何一个字节。
enum LogParse {
    static func record(_ line: String) -> LogRecord {
        // 先把终端控制码剥掉，再认前缀。**这一格是拿真日志测出来的**：
        // 这台机器上的 Java 服务（Spring 那一套）每一行都带着 `\e[1;33m`，
        // 码就压在时间戳前面 —— 不剥的话时间认不出来，而且那几个控制字符
        // 会在等宽列里占着位子，一屏看过去像乱码。级别那一路我们自己上色，
        // 终端那一套在这儿本来也没有意义。
        let text = clean(line)
        var record = LogRecord(time: nil, level: nil, message: text, http: nil)
        var rest = text[...]
        var trimmed = false

        // 行首的空白不算数：同一份日志里有些行会多一个前导空格（Spring 那边实测），
        // 而它们是同一种记录，不是上一条的尾巴。
        rest = text.drop { $0 == " " || $0 == "\t" }

        if let found = leadingTime(rest) {
            record.time = found.stamp
            rest = found.rest
        }

        if let found = leadingLevel(rest) {
            record.level = found.level
            rest = found.rest
            trimmed = true
        } else if record.time == nil {
            // 「级别写在字段里」那种。认它是为了上色，内容一个字不重排。
            record.level = inlineLevel(rest)
        }

        if record.time == nil, record.level == nil {
            // 认不出来：整行就是消息（缩进和堆栈里的空格是它自己的排版，不动）。
            record.continuation = looksLikeTail(text)
        } else if trimmed {
            // 前缀后面那一串分隔符已经被 `leadingLevel` 吃掉了。
            record.message = String(rest)
        } else {
            // 只有时间认出来：把它后面那串分隔符吃掉，别让消息从空格起头。
            record.message = String(text[skipSeparators(text[...], from: rest.startIndex)...])
        }

        record.http = httpStatus(text[...])
        return record
    }

    /// 剥掉 ANSI 转义序列（CSI：`ESC [ 参数 终止字母`，颜色码都是这一种）。
    ///
    /// 认不出形状的 ESC 就地丢掉，后面的字照留 —— 一行里混进半个控制序列，
    /// 不该让那一行整个消失（「原文照排」这条对控制码也一样：去掉码，留下字）。
    static func clean(_ line: String) -> String {
        guard line.contains("\u{1B}") else { return line }
        var out = ""
        out.reserveCapacity(line.count)
        var i = line.startIndex
        while i < line.endIndex {
            guard line[i] == "\u{1B}" else {
                out.append(line[i])
                i = line.index(after: i)
                continue
            }
            var j = line.index(after: i)
            if j < line.endIndex, line[j] == "[" {
                j = line.index(after: j)
                while j < line.endIndex, isCSIParameter(line[j]) { j = line.index(after: j) }
                if j < line.endIndex, isCSIFinal(line[j]) { j = line.index(after: j) }
            }
            i = j
        }
        return out
    }

    /// CSI 的参数字节：`0x30–0x3F`，以及中间那些 `0x20–0x2F`。
    private static func isCSIParameter(_ character: Character) -> Bool {
        guard let ascii = character.asciiValue else { return false }
        return (0x30...0x3F).contains(ascii) || (0x20...0x2F).contains(ascii)
    }

    /// CSI 的终止字节：`0x40–0x7E`（颜色码是 `m`）。
    private static func isCSIFinal(_ character: Character) -> Bool {
        guard let ascii = character.asciiValue else { return false }
        return (0x40...0x7E).contains(ascii)
    }

    // MARK: - 时间戳

    /// 行首那个时间戳 → 归一化的 `HH:MM:SS[.mmm]` 和剩下的部分。
    ///
    /// 归一化只做一件事：把逗号毫秒换成点、补足三位。日期与时区**吃掉不显示** ——
    /// 面板是看尾巴的（十分钟前的和刚才的都在这一屏里），日期在等宽列里要占掉
    /// 十七个字符，而那一列宽度的代价是每一行都少读五六个字。要完整的原文，
    /// 复制出去的就是原来那一行。
    private static func leadingTime(_ line: Substring) -> (stamp: String, rest: Substring)? {
        var i = line.startIndex
        var closer: Character?
        if i < line.endIndex, line[i] == "[" || line[i] == "(" {
            closer = line[i] == "[" ? "]" : ")"
            i = line.index(after: i)
        }
        if let afterDate = skipDate(line, from: i) { i = afterDate }
        guard let clock = skipClock(line, from: i) else { return nil }

        let afterZone = skipZone(line, from: clock.end)
        if let closer {
            guard afterZone < line.endIndex, line[afterZone] == closer else { return nil }
            return (clock.stamp, line[line.index(after: afterZone)...])
        }
        // 没括号：后面要么行尾，要么一个分隔符。`09:41:07x` 不是时间。
        guard afterZone == line.endIndex || isSeparator(line[afterZone]) else { return nil }
        return (clock.stamp, line[afterZone...])
    }

    /// 日期前缀（`2026-09-24` / `2026/09/24`）后面的位置。认不出就 nil，不动 `from`。
    private static func skipDate(_ line: Substring, from index: Substring.Index) -> Substring.Index? {
        guard let year = digits(line, from: index, count: 4), year.value >= 1970,
              year.end < line.endIndex, line[year.end] == "-" || line[year.end] == "/"
        else { return nil }
        let separator = line[year.end]
        let afterYear = line.index(after: year.end)
        guard let month = digits(line, from: afterYear, count: 2),
              month.end < line.endIndex, line[month.end] == separator
        else { return nil }
        let afterMonth = line.index(after: month.end)
        guard let day = digits(line, from: afterMonth, count: 2) else { return nil }
        guard day.end < line.endIndex, line[day.end] == "T" || line[day.end] == " " else { return nil }
        var i = line.index(after: day.end)
        while i < line.endIndex, line[i] == " " { i = line.index(after: i) }
        return i
    }

    /// 时分秒（带可选毫秒）→ 归一化的 `HH:MM:SS[.mmm]` 和读完的位置。
    private static func skipClock(_ line: Substring, from index: Substring.Index)
        -> (stamp: String, end: Substring.Index)? {
        guard let hour = digits(line, from: index, count: 2),
              hour.end < line.endIndex, line[hour.end] == ":", hour.value <= 23
        else { return nil }
        let afterHour = line.index(after: hour.end)
        guard let minute = digits(line, from: afterHour, count: 2),
              minute.end < line.endIndex, line[minute.end] == ":", minute.value <= 59
        else { return nil }
        let afterMinute = line.index(after: minute.end)
        guard let second = digits(line, from: afterMinute, count: 2), second.value <= 60 else { return nil }

        var stamp = String(format: "%02d:%02d:%02d", hour.value, minute.value, second.value)
        var i = second.end
        if i < line.endIndex, line[i] == "." || line[i] == "," {
            var fraction = ""
            var k = line.index(after: i)
            while k < line.endIndex, isDigit(line[k]) {
                if fraction.count < 3 { fraction.append(line[k]) }
                k = line.index(after: k)
            }
            if !fraction.isEmpty {
                stamp += "." + fraction.padding(toLength: 3, withPad: "0", startingAt: 0)
                i = k
            }
        }
        return (stamp, i)
    }

    /// 时区（`Z` / `+08:00` / `-0700`）后面的位置。认它是为了把它吃掉。
    private static func skipZone(_ line: Substring, from index: Substring.Index) -> Substring.Index {
        guard index < line.endIndex else { return index }
        if line[index] == "Z" || line[index] == "z" { return line.index(after: index) }
        guard line[index] == "+" || line[index] == "-" else { return index }
        var i = line.index(after: index)
        guard let hour = digits(line, from: i, count: 2) else { return index }
        i = hour.end
        if i < line.endIndex, line[i] == ":" { i = line.index(after: i) }
        if let minute = digits(line, from: i, count: 2) { i = minute.end }
        return i
    }

    // MARK: - 级别

    /// 时间后面（或行首）那个级别词：`INFO` / `[WARN]` / `(error)` / `INFO:` / `INFO - `。
    private static func leadingLevel(_ line: Substring) -> (level: LogLevel, rest: Substring)? {
        var i = skipSeparators(line, from: line.startIndex)
        var closer: Character?
        if i < line.endIndex, line[i] == "[" || line[i] == "(" {
            closer = line[i] == "[" ? "]" : ")"
            i = line.index(after: i)
        }
        var word = ""
        var k = i
        while k < line.endIndex, isWordCharacter(line[k]) {
            word.append(line[k])
            k = line.index(after: k)
        }
        guard let level = LogLevel.words[word.lowercased()] else { return nil }

        // 词后面必须是分隔符（有括号就得先撞上闭括号）——
        // 不然 `errors: 3` 会被当成级别、`information` 会被当成 info。
        var after = k
        if let closer {
            guard after < line.endIndex, line[after] == closer else { return nil }
            after = line.index(after: after)
        }
        guard after == line.endIndex || isSeparator(line[after]) else { return nil }
        return (level, line[skipSeparators(line, from: after)...])
    }

    /// 级别写在字段里的那种：`level=error` / `"level":"error"` / `lvl: warn`。
    ///
    /// 只在**前 120 个字符**里找 —— 再往后就是消息正文，
    /// 「日志级别设为 error」这类句子里的 error 不该给整行上色。
    private static func inlineLevel(_ line: Substring) -> LogLevel? {
        let head = line.prefix(120)
        guard let key = head.range(of: "level", options: [.caseInsensitive]) else { return nil }
        var i = key.upperBound
        while i < head.endIndex, head[i] == "\"" || head[i] == " " { i = head.index(after: i) }
        guard i < head.endIndex, head[i] == "=" || head[i] == ":" else { return nil }
        i = head.index(after: i)
        while i < head.endIndex, head[i] == " " || head[i] == "\"" { i = head.index(after: i) }
        var word = ""
        var k = i
        while k < head.endIndex, isWordCharacter(head[k]) {
            word.append(head[k])
            k = head.index(after: k)
        }
        return LogLevel.words[word.lowercased()]
    }

    // MARK: - 续行与状态码

    /// 这一行像是上一条的尾巴：缩进、堆栈帧、`Traceback` 这些。
    ///
    /// 这一条决定的是**它排在哪一列** —— 判成续行就从消息列开始、不占时间和级别列。
    /// 判错的代价是缩进看着有点怪；不判的话，一屏堆栈会跟正文糊成一片，
    /// 那正是这一版要修的那个「太乱」。
    private static func looksLikeTail(_ line: String) -> Bool {
        guard let first = line.first else { return false }
        if first == " " || first == "\t" { return true }
        let lowered = line.lowercased()
        return ["traceback", "caused by:", "at ", "file \"", "goroutine "].contains {
            lowered.hasPrefix($0)
        }
    }

    /// 访问日志里那个状态码：`"GET /health HTTP/1.1" 404`、`HTTP/1.1 200 OK`。
    private static func httpStatus(_ line: Substring) -> Int? {
        var i = line.startIndex
        while let protocolRange = line.range(of: "HTTP/1.", range: i..<line.endIndex) {
            var k = protocolRange.upperBound
            if k < line.endIndex, isDigit(line[k]) { k = line.index(after: k) }
            if k < line.endIndex, line[k] == "\"" { k = line.index(after: k) }
            while k < line.endIndex, line[k] == " " { k = line.index(after: k) }
            if let code = digits(line, from: k, count: 3), (100...599).contains(code.value),
               code.end == line.endIndex || !isDigit(line[code.end]) {
                return code.value
            }
            i = protocolRange.upperBound
        }
        return nil
    }

    // MARK: - 字符

    private static func digits(_ line: Substring, from index: Substring.Index, count: Int)
        -> (value: Int, end: Substring.Index)? {
        var i = index
        var value = 0
        for _ in 0..<count {
            guard i < line.endIndex, isDigit(line[i]) else { return nil }
            value = value * 10 + (line[i].wholeNumberValue ?? 0)
            i = line.index(after: i)
        }
        return (value, i)
    }

    /// 数字判定要卡 ASCII：`٣` 这种也算 `wholeNumberValue`，
    /// 而一行阿拉伯数字的日志不该被当成时间戳。
    private static func isDigit(_ character: Character) -> Bool {
        character.isASCII && character.isNumber
    }

    private static func isWordCharacter(_ character: Character) -> Bool {
        (character.isASCII && (character.isLetter || character.isNumber)) || character == "_"
    }

    private static func isSeparator(_ character: Character) -> Bool {
        " \t|,:;".contains(character)
    }

    private static func skipSeparators(_ line: Substring, from index: Substring.Index) -> Substring.Index {
        var i = index
        while i < line.endIndex, isSeparator(line[i]) || line[i] == "]" || line[i] == ")" {
            i = line.index(after: i)
        }
        return i
    }

    // MARK: - 验证台的入口

    /// `--parse-log <文件>`：把一份日志按上面这套规则解剖一遍，打印出来。
    ///
    /// 为什么要有这么一条：排版认不认得出来，只有拿**真文件**跑一遍才算数，
    /// 而窗口里那条路要人坐在这儿一行行看。和 `--remove-project` 一样，
    /// 它是给验证台的入口 —— 认不出的行必须原样出现在消息列，
    /// 那就是「不编」那条的验收。
    static func report(path: String) -> Int32 {
        guard let text = try? String(contentsOfFile: path, encoding: .utf8) else {
            FileHandle.standardError.write(Data("读不到这个文件：\(path)\n".utf8))
            return 1
        }
        let lines = text.split(separator: "\n", omittingEmptySubsequences: false).map(String.init)
        var timed = 0, leveled = 0, tails = 0, codes = 0
        print("文件 \(path)（\(lines.count) 行）")
        print("  行号  时间         级别    消息")
        for (index, line) in lines.enumerated() {
            let record = record(line)
            if record.time != nil { timed += 1 }
            if record.level != nil { leveled += 1 }
            if record.continuation { tails += 1 }
            if record.http != nil { codes += 1 }
            guard index < 200 else { continue }
            let time = (record.time ?? "").padding(toLength: 15, withPad: " ", startingAt: 0)
            let level = (record.level?.label ?? "").padding(toLength: 6, withPad: " ", startingAt: 0)
            let mark = record.continuation ? "↳ " : "  "
            print("\(String(format: "%5d", index + 1))  \(time)\(level)\(mark)\(record.message)")
        }
        if lines.count > 200 {
            print("… 余下 \(lines.count - 200) 行没有打印（只打前 200 行）")
        }
        print("—— 认到时间 \(timed) 行 / 级别 \(leveled) 行 / 续行 \(tails) 行 / 状态码 \(codes) 行，共 \(lines.count) 行")
        return 0
    }
}
