import Foundation

public enum AgentStreamReducer {
    public static let maxTurns = 50
    public static let maxItemsPerTurn = 500

    public static func apply(_ line: String, to state: AgentStreamSnapshot) -> AgentStreamSnapshot {
        let trimmed = line.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !trimmed.isEmpty,
              let data = trimmed.data(using: .utf8),
              let object = try? JSONSerialization.jsonObject(with: data) as? [String: Any],
              let type = object["type"] as? String else {
            return state
        }
        var next = state
        switch type {
        case "session.started":
            next.phase = .ready
            if let sessionID = object["provider_session_id"] as? String {
                let file = object["session_file"] as? String
                next.reference = ProviderReference(sessionID: sessionID, sessionFile: file)
            }
        case "turn.started":
            guard let turnID = object["turn"] as? String else { break }
            let input = (object["input"] as? String) ?? ""
            let turn = AgentTurn(id: turnID, input: input, sentAt: Date(), outcome: .running, items: [])
            next.turns.append(turn)
            trimTurns(&next)
        case "item.started":
            guard let turnID = object["turn"] as? String,
                  let itemObject = object["item"] as? [String: Any],
                  let item = parseItem(itemObject) else { break }
            mutateTurn(&next, id: turnID) { turn in
                turn.items.append(item)
                trimItems(&turn)
            }
        case "item.updated":
            guard let turnID = object["turn"] as? String,
                  let itemID = object["item"] as? String,
                  let kind = object["kind"] as? String else { break }
            if kind == "message", let delta = object["text_delta"] as? String {
                mutateTurn(&next, id: turnID) { turn in
                    if let index = turn.items.firstIndex(where: { $0.id == itemID }) {
                        if case .message(let id, let text) = turn.items[index] {
                            turn.items[index] = .message(id: id, text: text + delta)
                        }
                    } else {
                        turn.items.append(.message(id: itemID, text: delta))
                        trimItems(&turn)
                    }
                }
            }
        case "item.completed":
            guard let turnID = object["turn"] as? String,
                  let itemObject = object["item"] as? [String: Any],
                  let item = parseItem(itemObject) else { break }
            mutateTurn(&next, id: turnID) { turn in
                if let index = turn.items.firstIndex(where: { $0.id == item.id }) {
                    turn.items[index] = item
                } else {
                    turn.items.append(item)
                    trimItems(&turn)
                }
            }
        case "request.opened":
            guard let turnID = object["turn"] as? String,
                  let requestObject = object["request"] as? [String: Any],
                  let request = parseRequest(requestObject, turn: turnID) else { break }
            next.requests.append(request)
        case "request.closed":
            guard let requestID = object["request"] as? String else { break }
            next.requests.removeAll { $0.id == requestID }
        case "turn.diff":
            guard let turnID = object["turn"] as? String else { break }
            let files = (object["files"] as? [[String: Any]])?.compactMap(parseFileStat) ?? []
            let patch = (object["patch"] as? String) ?? ""
            let truncated = (object["truncated"] as? Bool) ?? false
            mutateTurn(&next, id: turnID) { turn in
                turn.diff = TurnDiff(files: files, patch: patch, truncated: truncated)
            }
        case "turn.completed":
            guard let turnID = object["turn"] as? String else { break }
            let outcome = (object["outcome"] as? String) ?? "completed"
            mutateTurn(&next, id: turnID) { turn in
                turn.endedAt = Date()
                switch outcome {
                case "completed": turn.outcome = .completed
                case "failed":
                    let code = (object["code"] as? String) ?? "failed"
                    let message = (object["message"] as? String) ?? ""
                    turn.outcome = .failed(code: code, message: message)
                case "interrupted": turn.outcome = .interrupted
                default: turn.outcome = .completed
                }
            }
        case "session.ended":
            let reason = (object["reason"] as? String) ?? "closed"
            let code = object["exit_code"] as? Int
            switch reason {
            case "closed": next.phase = .ended(.closed)
            case "lost": next.phase = .ended(.lost)
            default:
                next.phase = .ended(.providerExit(code: code ?? 1))
            }
            mutateRunningTurns(&next) { turn in
                if turn.outcome == .running { turn.outcome = .lost; turn.endedAt = Date() }
            }
        case "notice":
            guard let turnID = object["turn"] as? String,
                  let level = object["level"] as? String,
                  let code = object["code"] as? String,
                  let message = object["message"] as? String else { break }
            let notice = AgentItem.notice(id: UUID().uuidString, level: noticeLevel(level),
                                          code: code, text: message)
            mutateTurn(&next, id: turnID) { turn in
                turn.items.append(notice)
                trimItems(&turn)
            }
        case "error":
            let code = object["code"] as? String
            let message = (object["message"] as? String) ?? ""
            next.bridgeErrorCode = code
            next.bridgeErrorMessage = message
            if code == "unsupported" {
                next.phase = .ended(.providerExit(code: 1))
            }
        default:
            break
        }
        return next
    }

    public static func replay(_ lines: [String]) -> AgentStreamSnapshot {
        lines.reduce(into: AgentStreamSnapshot()) { state, line in
            state = apply(line, to: state)
        }
    }

    private static func trimTurns(_ state: inout AgentStreamSnapshot) {
        if state.turns.count > maxTurns {
            state.turns.removeFirst(state.turns.count - maxTurns)
            state.turnCapDropped = true
        }
    }

    private static func trimItems(_ turn: inout AgentTurn) {
        if turn.items.count > maxItemsPerTurn {
            turn.items.removeFirst(turn.items.count - maxItemsPerTurn)
        }
    }

    private static func mutateTurn(_ state: inout AgentStreamSnapshot, id: String, body: (inout AgentTurn) -> Void) {
        guard let index = state.turns.firstIndex(where: { $0.id == id }) else { return }
        body(&state.turns[index])
        if state.turns[index].items.count > maxItemsPerTurn {
            state.turns[index].items.removeFirst(state.turns[index].items.count - maxItemsPerTurn)
            state.itemCapDropped = true
        }
    }

    private static func mutateRunningTurns(_ state: inout AgentStreamSnapshot, body: (inout AgentTurn) -> Void) {
        for index in state.turns.indices where state.turns[index].outcome == .running {
            body(&state.turns[index])
        }
    }

    private static func parseFileStat(_ object: [String: Any]) -> FileStat? {
        guard let path = object["path"] as? String,
              let added = object["added"] as? Int,
              let removed = object["removed"] as? Int else { return nil }
        return FileStat(path: path, added: added, removed: removed)
    }

    private static func parseRequest(_ object: [String: Any], turn: String) -> AgentRequest? {
        guard let id = object["id"] as? String,
              let kindRaw = object["kind"] as? String else { return nil }
        let kind: AgentRequestKind
        switch kindRaw {
        case "command": kind = .command
        case "file_change": kind = .fileChange
        case "permission": kind = .permission
        case "tool": kind = .tool
        default: return nil
        }
        let decisions = (object["decisions"] as? [String])?.compactMap { AgentDecision(rawValue: $0) } ?? []
        return AgentRequest(
            id: id,
            turn: turn,
            kind: kind,
            command: object["command"] as? String,
            cwd: object["cwd"] as? String,
            paths: object["paths"] as? [String] ?? [],
            reason: object["reason"] as? String,
            preview: object["preview"] as? String,
            decisions: decisions
        )
    }

    private static func parseItem(_ object: [String: Any]) -> AgentItem? {
        guard let id = object["id"] as? String,
              let kind = object["kind"] as? String else { return nil }
        let status = itemStatus(object["status"] as? String)
        switch kind {
        case "message":
            return .message(id: id, text: (object["text"] as? String) ?? "")
        case "reasoning":
            return .reasoning(id: id, summary: (object["summary"] as? String) ?? "")
        case "command":
            return .command(id: id, command: (object["command"] as? String) ?? "",
                          cwd: object["cwd"] as? String, status: status,
                          exitCode: object["exit_code"] as? Int,
                          durationMs: object["duration_ms"] as? Int,
                          outputTail: (object["output_tail"] as? String) ?? "")
        case "file_change":
            let files = (object["files"] as? [[String: Any]])?.compactMap(parseFileStat) ?? []
            return .fileChange(id: id, files: files, status: status)
        case "tool":
            return .tool(id: id, name: (object["name"] as? String) ?? "",
                         inputPreview: (object["input_preview"] as? String) ?? "", status: status)
        case "plan":
            let steps = (object["steps"] as? [[String: Any]])?.map {
                PlanStep(title: ($0["title"] as? String) ?? "", done: ($0["done"] as? Bool) ?? false)
            } ?? []
            return .plan(id: id, steps: steps)
        default:
            return nil
        }
    }

    private static func itemStatus(_ raw: String?) -> ItemStatus {
        switch raw {
        case "completed": return .completed
        case "failed": return .failed
        case "declined": return .declined
        default: return .running
        }
    }

    private static func noticeLevel(_ raw: String) -> NoticeLevel {
        switch raw {
        case "warn": return .warn
        case "error": return .error
        default: return .info
        }
    }
}
