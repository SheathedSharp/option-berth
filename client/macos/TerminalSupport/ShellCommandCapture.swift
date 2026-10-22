import Foundation
import SwiftTerm

public enum ShellCommandCapture {
    public static let maxLines = 200

    /// Absolute cursor row in scroll-invariant coordinates (survives scrollback trim).
    public static func absoluteCursorRow(terminal: Terminal) -> Int {
        // When the viewport is pinned to the bottom, yDisp == yBase. During
        // capture we also prefer this over viewport-relative `buffer.y` alone,
        // which breaks as soon as idle keepalive resizes the PTY (1×1 → card).
        terminal.buffer.totalLinesTrimmed + terminal.buffer.yDisp + terminal.buffer.y
    }

    /// Reads terminal buffer rows from absolute `startRow` through the current cursor row.
    public static func capture(terminal: Terminal, startRow: Int, maxLines: Int = maxLines) -> String {
        let endRow = absoluteCursorRow(terminal: terminal)
        let lo = min(startRow, endRow)
        let hi = max(startRow, endRow)
        var lines: [String] = []
        if lo <= hi {
            for row in lo...hi {
                let text = terminal.getScrollInvariantLine(row: row)?.translateToString(trimRight: true) ?? ""
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

    /// Strip OSC 133 / 633 (and other OSC) so byte-accumulated captures stay clean.
    public static func stripOSC(_ text: String) -> String {
        var out = ""
        out.reserveCapacity(text.count)
        var i = text.startIndex
        while i < text.endIndex {
            if text[i] == "\u{1B}", text.index(after: i) < text.endIndex, text[text.index(after: i)] == "]" {
                // ESC ] ... BEL  or  ESC ] ... ESC \
                var j = text.index(i, offsetBy: 2)
                var ended = false
                while j < text.endIndex {
                    let ch = text[j]
                    if ch == "\u{07}" {
                        j = text.index(after: j)
                        ended = true
                        break
                    }
                    if ch == "\u{1B}" {
                        let n = text.index(after: j)
                        if n < text.endIndex, text[n] == "\\" {
                            j = text.index(after: n)
                            ended = true
                            break
                        }
                    }
                    j = text.index(after: j)
                }
                i = ended ? j : text.index(after: i)
                continue
            }
            out.append(text[i])
            i = text.index(after: i)
        }
        return out
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

    public static func finalizeAccumulated(_ raw: String) -> String {
        let cleaned = stripTrailingPrompt(stripOSC(raw).trimmingCharacters(in: .whitespacesAndNewlines))
        if cleaned.isEmpty { return "" }
        var lines = cleaned.split(separator: "\n", omittingEmptySubsequences: false).map(String.init)
        if lines.count > maxLines {
            let dropped = lines.count - maxLines
            lines = Array(lines.suffix(maxLines))
            lines.insert("…（已截断前 \(dropped) 行）", at: 0)
        }
        return lines.joined(separator: "\n")
    }
}
