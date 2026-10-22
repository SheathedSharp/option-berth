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
        return stripTrailingPrompt(lines.joined(separator: "\n"))
    }

    /// Drop trailing shell PS1 lines so completed blocks are command + output only.
    public static func stripTrailingPrompt(_ text: String) -> String {
        var lines = text.split(separator: "\n", omittingEmptySubsequences: false).map(String.init)
        while let last = lines.last {
            let trimmed = last.trimmingCharacters(in: .whitespaces)
            if trimmed.isEmpty {
                lines.removeLast()
                continue
            }
            if looksLikePrompt(trimmed) {
                lines.removeLast()
                continue
            }
            break
        }
        return lines.joined(separator: "\n")
    }

    /// Heuristic for zsh/bash/fish primary prompts (user@host … %/$/#).
    public static func looksLikePrompt(_ line: String) -> Bool {
        let trimmed = line.trimmingCharacters(in: .whitespaces)
        guard !trimmed.isEmpty else { return false }
        // Bare prompt markers.
        if trimmed == "%" || trimmed == "$" || trimmed == "#" { return true }
        // Ends with classic prompt terminator (optional trailing space already trimmed).
        let last = trimmed.last!
        guard last == "%" || last == "$" || last == "#" else { return false }
        // Prefer lines that look like user@host or path-bearing prompts.
        if trimmed.contains("@") { return true }
        if trimmed.contains("/") || trimmed.contains("~") { return true }
        // Short "dir %" style.
        if trimmed.count <= 48 { return true }
        return false
    }
}
