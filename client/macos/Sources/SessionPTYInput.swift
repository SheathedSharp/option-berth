import Foundation

/// Encodes composer text for PTY delivery. Shell lines use LF; agent TUI input uses CR
/// and optional bracketed paste when the emulator reports that mode.
enum SessionPTYInput {
    struct Result: Equatable {
        let bytes: [UInt8]?
        let error: String?
    }

    static func encodeShellLine(_ text: String) -> Result {
        guard !text.utf8.contains(0), text.utf8.count <= 64 * 1024 else {
            return Result(bytes: nil, error: "这段文字不能送入会话")
        }
        var bytes = Array(text.utf8)
        if bytes.last != 10 { bytes.append(10) }
        return Result(bytes: bytes, error: nil)
    }

    static func encodeAgentTurn(_ text: String, bracketedPaste: Bool) -> Result {
        guard !text.utf8.contains(0), text.utf8.count <= 64 * 1024 else {
            return Result(bytes: nil, error: "这段文字不能送入会话")
        }
        let sanitized = String(text.unicodeScalars.filter { $0 != "\u{1B}" })
        if bracketedPaste {
            var bytes = Array("\u{1B}[200~".utf8)
            bytes += Array(sanitized.utf8)
            bytes += Array("\u{1B}[201~".utf8)
            bytes.append(13)
            return Result(bytes: bytes, error: nil)
        }
        if sanitized.contains("\n") || sanitized.contains("\r") {
            return Result(bytes: nil, error: "多行内容需要 Agent 终端开启 bracketed paste")
        }
        var bytes = Array(sanitized.utf8)
        bytes.append(13)
        return Result(bytes: bytes, error: nil)
    }
}
