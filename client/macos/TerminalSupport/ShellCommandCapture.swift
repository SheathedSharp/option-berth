import Foundation
import SwiftTerm

public enum ShellCommandCapture {
    public static let maxLines = 200

    /// Absolute cursor row in scroll-invariant coordinates (survives scrollback trim).
    public static func absoluteCursorRow(terminal: Terminal) -> Int {
        // When the viewport is pinned to the bottom, yDisp == yBase.
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
                lines.append(text)
            }
        }
        // Drop leading/trailing empties here; keep interior blanks for layout.
        while lines.first?.isEmpty == true { lines.removeFirst() }
        while lines.last?.isEmpty == true { lines.removeLast() }
        return finalizeAccumulated(lines.joined(separator: "\n"), maxLines: maxLines)
    }

    /// Strip OSC (ESC ]), CSI (ESC [), and other ESC sequences so block text is plain.
    /// Also drops orphan CSI crumbs like `[1m[7m%` when ESC was lost in transit.
    public static func stripTerminalControls(_ text: String) -> String {
        var out = String()
        out.reserveCapacity(text.count)
        let chars = Array(text)
        var i = 0
        while i < chars.count {
            let ch = chars[i]
            // ESC …
            if ch == "\u{1B}" {
                guard i + 1 < chars.count else { break }
                let next = chars[i + 1]
                if next == "]" {
                    // OSC: ESC ] … BEL | ESC \
                    i += 2
                    while i < chars.count {
                        if chars[i] == "\u{07}" { i += 1; break }
                        if chars[i] == "\u{1B}", i + 1 < chars.count, chars[i + 1] == "\\" {
                            i += 2; break
                        }
                        i += 1
                    }
                    continue
                }
                if next == "[" {
                    // CSI: ESC [ params/intermediates final (0x40…0x7E)
                    i += 2
                    while i < chars.count {
                        let c = chars[i]
                        i += 1
                        if let ascii = c.unicodeScalars.first?.value, (0x40...0x7E).contains(ascii) {
                            break
                        }
                    }
                    continue
                }
                // Other short ESC sequences (charset, etc.): skip ESC + one byte.
                i += 2
                continue
            }
            // C1 CSI (0x9B) …
            if ch == "\u{9B}" {
                i += 1
                while i < chars.count {
                    let c = chars[i]
                    i += 1
                    if let ascii = c.unicodeScalars.first?.value, (0x40...0x7E).contains(ascii) {
                        break
                    }
                }
                continue
            }
            // Orphan CSI crumb: `[` + digits/semicolons + final letter (no ESC).
            // Matches litter like `[1m[7m%[27m[1m[0m` and `[3J[H[2J`.
            if ch == "[", i + 1 < chars.count {
                var j = i + 1
                while j < chars.count {
                    let c = chars[j]
                    if c.isNumber || c == ";" || c == "?" || c == ":" {
                        j += 1
                        continue
                    }
                    break
                }
                if j < chars.count, let ascii = chars[j].unicodeScalars.first?.value,
                   (0x40...0x7E).contains(ascii) {
                    i = j + 1
                    continue
                }
            }
            // Drop other C0 controls except tab / newline / CR.
            if let ascii = ch.unicodeScalars.first?.value, ascii < 0x20 {
                if ch == "\t" || ch == "\n" || ch == "\r" {
                    out.append(ch)
                }
                i += 1
                continue
            }
            out.append(ch)
            i += 1
        }
        return out
    }

    /// Backward-compatible alias.
    public static func stripOSC(_ text: String) -> String { stripTerminalControls(text) }

    /// Drop trailing shell PS1 / zsh reverse-video `%` crumbs.
    public static func stripTrailingPrompt(_ text: String) -> String {
        var lines = text.split(separator: "\n", omittingEmptySubsequences: false).map(String.init)
        while let last = lines.last {
            let trimmed = last.trimmingCharacters(in: .whitespaces)
            if trimmed.isEmpty {
                lines.removeLast()
                continue
            }
            if looksLikePrompt(trimmed) || looksLikePercentCrumb(trimmed) {
                lines.removeLast()
                continue
            }
            break
        }
        return lines.joined(separator: "\n")
    }

    /// zsh "partial line" marker after SGR stripped: lone `%` or `%` with spaces.
    public static func looksLikePercentCrumb(_ line: String) -> Bool {
        let trimmed = line.trimmingCharacters(in: .whitespaces)
        return trimmed == "%" || trimmed == "#%" || trimmed == "%#"
    }

    /// Heuristic for zsh/bash/fish primary prompts (user@host … %/$/#).
    public static func looksLikePrompt(_ line: String) -> Bool {
        let trimmed = line.trimmingCharacters(in: .whitespaces)
        guard !trimmed.isEmpty else { return false }
        if trimmed == "%" || trimmed == "$" || trimmed == "#" { return true }
        let last = trimmed.last!
        guard last == "%" || last == "$" || last == "#" else { return false }
        if trimmed.contains("@") { return true }
        if trimmed.contains("/") || trimmed.contains("~") { return true }
        if trimmed.count <= 48 { return true }
        return false
    }


    /// True when a line is nothing but orphan CSI crumbs (`[1m[7m%[27m…` / `[3J[H[2J`).
    public static func isOrphanCSILitter(_ line: String) -> Bool {
        let trimmed = line.trimmingCharacters(in: .whitespaces)
        guard trimmed.contains("["), trimmed.count <= 64 else { return false }
        var i = trimmed.startIndex
        var saw = false
        while i < trimmed.endIndex {
            if trimmed[i] == "%" {
                i = trimmed.index(after: i)
                continue
            }
            guard trimmed[i] == "[" else { return false }
            i = trimmed.index(after: i)
            while i < trimmed.endIndex {
                let c = trimmed[i]
                if c.isNumber || c == ";" || c == "?" || c == ":" {
                    i = trimmed.index(after: i)
                    continue
                }
                break
            }
            guard i < trimmed.endIndex else { return false }
            guard let ascii = trimmed[i].unicodeScalars.first?.value,
                  (0x40...0x7E).contains(ascii) else { return false }
            i = trimmed.index(after: i)
            saw = true
        }
        return saw
    }

    public static func finalizeAccumulated(_ raw: String, maxLines: Int = maxLines) -> String {
        var cleaned = stripTrailingPrompt(
            stripTerminalControls(raw)
                .replacingOccurrences(of: "\r\n", with: "\n")
                .replacingOccurrences(of: "\r", with: "\n")
                .trimmingCharacters(in: .whitespacesAndNewlines)
        )
        // Collapse runs of blank lines left by stripped clear/CSI.
        while cleaned.contains("\n\n\n") {
            cleaned = cleaned.replacingOccurrences(of: "\n\n\n", with: "\n\n")
        }
        cleaned = cleaned.trimmingCharacters(in: .whitespacesAndNewlines)
        if cleaned.isEmpty { return "" }
        // Lone leftover quotes / empty markers.
        if cleaned == "\"\"" || cleaned == "''" { return "" }
        var lines = cleaned.split(separator: "\n", omittingEmptySubsequences: false).map(String.init)
        // Drop lines that are only orphan CSI crumbs or percent markers.
        lines = lines.filter { line in
            let t = line.trimmingCharacters(in: .whitespaces)
            if t.isEmpty { return true }
            if looksLikePercentCrumb(t) { return false }
            if isOrphanCSILitter(t) { return false }
            return true
        }
        while lines.last?.trimmingCharacters(in: .whitespaces).isEmpty == true {
            lines.removeLast()
        }
        if lines.isEmpty { return "" }
        if lines.count > maxLines {
            let dropped = lines.count - maxLines
            lines = Array(lines.suffix(maxLines))
            lines.insert("…（已截断前 \(dropped) 行）", at: 0)
        }
        return lines.joined(separator: "\n")
    }
}
