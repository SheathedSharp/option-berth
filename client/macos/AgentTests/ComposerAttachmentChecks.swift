import BerthAgent
import Foundation

@MainActor
extension AgentChecks {
    static func composerAttachmentChecks() throws {
        let attachment = ComposerShellAttachment(shellTitle: "Shell 1", command: "npm test",
                                                 exitCode: 1, interrupted: false, output: "FAIL\n")
        let merged = ComposerAttachmentFormat.agentMessage(text: "请修复", attachment: attachment)
        try check(merged.contains("请修复"), "user text missing")
        try check(merged.contains("```shell"), "shell fence missing")
        try check(merged.contains("npm test"), "command missing")
        try check(merged.contains("(exit 1)"), "exit code missing")
        try check(merged.contains("FAIL"), "output missing")
        let solo = ComposerAttachmentFormat.agentMessage(text: "", attachment: attachment)
        try check(!solo.contains("请修复"), "empty text should not add prose")
        try check(solo.hasPrefix("```shell"), "attachment-only message")
        print("PASS: ComposerAttachmentFormat merges shell blocks for Agent turns")
    }
}
