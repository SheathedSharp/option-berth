import Foundation

/// 时间戳说成人话。提交时间和「上次看是什么时候」都在这儿，两处说法保持一致。
enum TimeText {
    /// 「刚刚 / 12 分钟前 / 3 小时前 / 2 天前 / 2026-09-01」—— 人对一个提交问的是
    /// 「这是刚提交的还是上周的」，不是「第几秒」。
    static func relative(_ rfc3339: String?) -> String? {
        guard let rfc3339, let date = ISO8601DateFormatter().date(from: rfc3339) else { return nil }
        let seconds = Date().timeIntervalSince(date)
        switch seconds {
        case ..<60: return "刚刚"
        case ..<3600: return "\(Int(seconds / 60)) 分钟前"
        case ..<86400: return "\(Int(seconds / 3600)) 小时前"
        case ..<2592000: return "\(Int(seconds / 86400)) 天前"
        default: return String(rfc3339.prefix(10))
        }
    }

    /// 「14:20 / 昨天 14:20 / 9月22日 14:20」—— 带钟点的说法，给「上次看」那类
    /// 需要精确到分钟的地方。
    static func stamp(_ rfc3339: String) -> String {
        guard let date = ISO8601DateFormatter().date(from: rfc3339) else { return rfc3339 }
        let formatter = DateFormatter()
        formatter.locale = Locale(identifier: "zh_CN")
        if Calendar.current.isDateInToday(date) {
            formatter.dateFormat = "HH:mm"
        } else if Calendar.current.isDateInYesterday(date) {
            formatter.dateFormat = "'昨天' HH:mm"
        } else {
            formatter.dateFormat = "M月d日 HH:mm"
        }
        return formatter.string(from: date)
    }

    /// 「12 秒 / 1 分 23 秒」—— 跑了多久，给还在跑的那件事。
    ///
    /// 和 `relative` 分开是因为问的不是一回事：`relative` 答「多久以前」，
    /// 这里答「已经花了多久」—— 起草那一屏要人判断「还等不等得下去」，
    /// 而钟点（14:20）放在那儿是要他自己做减法的。
    static func elapsed(since rfc3339: String?) -> String? {
        guard let rfc3339, let date = ISO8601DateFormatter().date(from: rfc3339) else { return nil }
        let seconds = max(0, Int(Date().timeIntervalSince(date)))
        if seconds < 60 { return "\(seconds) 秒" }
        return "\(seconds / 60) 分 \(String(format: "%02d", seconds % 60)) 秒"
    }
}
