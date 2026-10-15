import Foundation
import SwiftTerm

/// Reads the shell command fragment on the current prompt row after Tab completion.
public enum ShellPromptLineCapture {
    private static let promptMarkers = ["% ", "$ ", "❯ ", "➜ ", "> "]

    public static func commandLine(atPrompt terminal: Terminal, seed: String) -> String? {
        let row = terminal.buffer.y
        guard let raw = terminal.getLine(row: row)?.translateToString(trimRight: true) else { return nil }
        return extractCommand(from: raw, seed: seed)
    }

    public static func extractCommand(from fullLine: String, seed: String) -> String? {
        let trimmed = fullLine.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !trimmed.isEmpty else { return nil }
        if !seed.isEmpty, let range = trimmed.range(of: seed) {
            let tail = String(trimmed[range.lowerBound...]).trimmingCharacters(in: .whitespaces)
            if !tail.isEmpty { return tail }
        }
        for marker in promptMarkers {
            if let range = trimmed.range(of: marker, options: .backwards) {
                let tail = String(trimmed[range.upperBound...]).trimmingCharacters(in: .whitespaces)
                if !tail.isEmpty { return tail }
            }
        }
        return seed.isEmpty ? trimmed : nil
    }
}
