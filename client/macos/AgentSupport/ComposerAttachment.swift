import Foundation

/// Shell command output attached to the next Agent turn (in-memory only).
public struct ComposerShellAttachment: Equatable, Sendable {
    public var shellTitle: String
    public var command: String
    public var exitCode: Int?
    public var interrupted: Bool
    public var output: String

    public init(shellTitle: String, command: String, exitCode: Int?, interrupted: Bool, output: String) {
        self.shellTitle = shellTitle
        self.command = command
        self.exitCode = exitCode
        self.interrupted = interrupted
        self.output = output
    }
}

public enum ComposerAttachmentFormat {
    public static let maxOutputLines = 200

    public static func agentMessage(text: String, attachment: ComposerShellAttachment?) -> String {
        guard let attachment else { return text }
        let trimmed = text.trimmingCharacters(in: .whitespacesAndNewlines)
        let block = shellBlock(attachment)
        if trimmed.isEmpty { return block }
        return trimmed + "\n\n" + block
    }

    public static func shellBlock(_ attachment: ComposerShellAttachment) -> String {
        var header = "$ " + attachment.command
        if attachment.interrupted {
            header += "\n(interrupted)"
        } else if let code = attachment.exitCode {
            header += "\n(exit \(code))"
        }
        let output = attachment.output.trimmingCharacters(in: .whitespacesAndNewlines)
        if output.isEmpty {
            return "```shell\n" + header + "\n```"
        }
        return "```shell\n" + header + "\n\n" + output + "\n```"
    }
}
