import Foundation

/// Encodes composer text for PTY delivery. Shell lines use CR (real Return). Native agent TUI input
/// uses paste without CR; optional bracketed paste when the emulator reports that mode.
public enum SessionPTYInput {
    public struct Result: Equatable {
        public let bytes: [UInt8]?
        public let error: String?
        public init(bytes: [UInt8]?, error: String?) {
            self.bytes = bytes
            self.error = error
        }
    }

    private static let maxBytes = 64 * 1024

    public static func encodeShellLine(_ text: String, bracketedPaste: Bool = false) -> Result {
        guard !text.utf8.contains(0), text.utf8.count <= maxBytes else {
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
            return Result(bytes: nil, error: "多行内容需要 Shell 开启 bracketed paste")
        }
        var bytes = Array(sanitized.utf8)
        bytes.append(13)
        return Result(bytes: bytes, error: nil)
    }

    /// Sends draft text to the shell PTY followed by Tab for completion (no CR). The console reads the prompt row back into the composer draft.
    public static func encodeShellTabCompletion(_ text: String) -> Result {
        guard !text.utf8.contains(0), text.utf8.count <= maxBytes else {
            return Result(bytes: nil, error: "这段文字不能送入会话")
        }
        if text.contains("\n") || text.contains("\r") {
            return Result(bytes: nil, error: "Shell Tab 补全仅支持单行")
        }
        let sanitized = String(text.unicodeScalars.filter { $0 != "\u{1B}" })
        var bytes = Array(sanitized.utf8)
        bytes.append(9)
        return Result(bytes: bytes, error: nil)
    }

    /// Paste into a native agent TUI without submitting (no trailing CR).
    public static func encodeAgentPaste(_ text: String, bracketedPaste: Bool) -> Result {
        guard !text.utf8.contains(0), text.utf8.count <= maxBytes else {
            return Result(bytes: nil, error: "这段文字不能送入会话")
        }
        let sanitized = String(text.unicodeScalars.filter { $0 != "\u{1B}" })
        if bracketedPaste {
            var bytes = Array("\u{1B}[200~".utf8)
            bytes += Array(sanitized.utf8)
            bytes += Array("\u{1B}[201~".utf8)
            return Result(bytes: bytes, error: nil)
        }
        if sanitized.contains("\n") || sanitized.contains("\r") {
            return Result(bytes: nil, error: "多行内容需要 Agent 终端开启 bracketed paste")
        }
        return Result(bytes: Array(sanitized.utf8), error: nil)
    }

}
