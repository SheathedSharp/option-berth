import BerthAgent
import Foundation

@MainActor
extension AgentChecks {
    static func slashParserChecks() throws {
        let base = SlashContext(hasAgents: true, shellCount: 2, targetWorking: true,
                                targetResumable: true, providerSupportsResume: true)
        try check(SlashParser.parse("/agent-select", context: base) == .command(.agentSelect, argument: ""),
                  "exact slash command")
        try check(SlashParser.parse("/usr/bin/env", context: base) == .none, "path is not a command")
        try check(SlashParser.parse("/agent codex", context: base) == .command(.agent, argument: "codex"),
                  "argument preserved")
        let narrow = SlashContext(hasAgents: false, shellCount: 1, targetWorking: false,
                                  targetResumable: false, providerSupportsResume: true)
        try check(SlashParser.parse("/agent", context: narrow) == .none, "unavailable command hidden")
        let composing = SlashParser.parse("/ag", context: base)
        try check(composing == .composing(filter: "ag"), "prefix enters composing")
        let matches = SlashParser.matches("/agent", context: base).map(\.rawValue)
        try check(matches.contains("/agent-select") && matches.contains("/agent"), "fuzzy matches")
        let nativeOnly = SlashParser.matches("/native", context: base).map(\.rawValue)
        try check(nativeOnly == ["/native"], "/native must not match /agent-select")
        let streamBusy = SlashContext(hasAgents: true, shellCount: 1, targetWorking: false, streamWorking: true,
                                    targetResumable: false, providerSupportsResume: true)
        try check(SlashParser.parse("/interrupt", context: streamBusy) == .command(.interrupt, argument: ""),
                  "structured stream working turn enables /interrupt")
        try check(SlashParser.parse("/resume", context: streamBusy) == .command(.resume, argument: ""),
                  "/resume stays available while structured turn is running")
        let streamNative = SlashContext(hasAgents: true, shellCount: 1, targetWorking: false,
                                        targetResumable: false, streamResumable: true, providerSupportsResume: false)
        try check(SlashParser.parse("/native", context: streamNative) == .command(.native, argument: ""),
                  "structured stream handoff enables /native without catalogue resume flag")
        try check(SlashParser.parse("/resume", context: base) == .command(.resume, argument: ""),
                  "/resume available when provider supports resume files")
        let noResume = SlashContext(hasAgents: true, shellCount: 1, targetWorking: false,
                                    targetResumable: false, providerSupportsResume: false)
        try check(SlashParser.parse("/resume", context: noResume) == .none, "/resume hidden without resume support")

        try check(SlashParser.parse("/ob-server", context: base) == .command(.obServer, argument: ""),
                  "/ob-server opens services panel")
        try check(SlashParser.parse("/services", context: base) == .command(.obServer, argument: ""),
                  "/services aliases /ob-server")
        try check(SlashParser.parse("/git", context: base) == .command(.git, argument: ""),
                  "/git opens git panel")
        let serviceMatches = SlashParser.matches("/serv", context: base).map(\.rawValue)
        try check(serviceMatches.contains("/ob-server"), "/serv matches services alias")

        try check(SlashParser.matches("ls", context: base).isEmpty, "ordinary text has no slash menu")
    }
}
