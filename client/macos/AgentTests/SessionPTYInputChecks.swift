import BerthAgent
import Foundation

@MainActor
extension AgentChecks {
    static func sessionPTYInputChecks() throws {
        let shellLine = SessionPTYInput.encodeShellLine("ls -la")
        try check(shellLine.bytes?.last == 13, "shell line did not end with CR")
        let pasted = SessionPTYInput.encodeShellLine("line1\nline2", bracketedPaste: true)
        try check(pasted.bytes?.starts(with: Array("\u{1B}[200~".utf8)) == true && pasted.bytes?.last == 13,
                  "shell bracketed paste")
        let blocked = SessionPTYInput.encodeShellLine("a\nb", bracketedPaste: false)
        try check(blocked.bytes == nil, "multiline shell without bracketed paste rejected")
        let agentPaste = SessionPTYInput.encodeAgentPaste("hi", bracketedPaste: false)
        try check(agentPaste.bytes == Array("hi".utf8), "agent paste does not end with CR")
        let agentBracketed = SessionPTYInput.encodeAgentPaste("line1\nline2", bracketedPaste: true)
        try check(agentBracketed.bytes?.starts(with: Array("\u{1B}[200~".utf8)) == true
                    && agentBracketed.bytes?.last != 13,
                  "agent bracketed paste does not submit")
        let tab = SessionPTYInput.encodeShellTabCompletion("git che")
        try check(tab.bytes == Array("git che".utf8) + [9], "shell tab completion ends with HT")
        let tabBlocked = SessionPTYInput.encodeShellTabCompletion("a\nb")
        try check(tabBlocked.bytes == nil, "multiline shell tab rejected")
    }
}
