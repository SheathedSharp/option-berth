import Foundation
import SwiftTerm

public enum ShellCommandCapture {
    public static let maxLines = 200

    /// Reads terminal buffer rows from `startRow` through the current cursor row.
    public static func capture(terminal: Terminal, startRow: Int, maxLines: Int = maxLines) -> String {
        let endRow = terminal.buffer.y
        let lo = min(startRow, endRow)
        let hi = max(startRow, endRow)
        var lines: [String] = []
        if lo <= hi {
            for row in lo...hi {
                let text = terminal.getLine(row: row)?.translateToString(trimRight: true) ?? ""
                if !text.isEmpty { lines.append(text) }
            }
        }
        if lines.count > maxLines {
            let dropped = lines.count - maxLines
            lines = Array(lines.suffix(maxLines))
            lines.insert("…（已截断前 \(dropped) 行）", at: 0)
        }
        return lines.joined(separator: "\n")
    }
}
