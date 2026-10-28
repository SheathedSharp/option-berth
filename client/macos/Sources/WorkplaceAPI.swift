import CryptoKit
import Foundation

/// The engine's workplace documents (`oberth workplace … --json`).
///
/// The engine owns this record: `oberth start` and `oberth agent stream` write
/// it without being asked, and a front end that owns its own terminal reports
/// through `workplace record`. The client keeps no second copy — it projects
/// what these two commands return.
struct EngineWorkplace: Decodable, Equatable, Identifiable, Sendable {
    let id: String
    let name: String
    let worktree: String
    let active: Bool
    let blocks: Int
    let createdAt: String
    let updatedAt: String

    enum CodingKeys: String, CodingKey {
        case id, name, worktree, active, blocks
        case createdAt = "created_at"
        case updatedAt = "updated_at"
    }
}

struct EngineBlock: Decodable, Equatable, Identifiable, Sendable {
    let id: String
    let workplace: String
    let seq: Int
    let kind: String
    let source: String
    let input: String?
    let status: String
    let output: String?
    let runID: String?
    let pid: Int?
    let logPath: String?
    let exitCode: Int?
    let provider: String?
    let startedAt: String?
    let endedAt: String?

    enum CodingKeys: String, CodingKey {
        case id, workplace, seq, kind, source, input, status, output, pid, provider
        case runID = "run_id"
        case logPath = "log_path"
        case exitCode = "exit_code"
        case startedAt = "started_at"
        case endedAt = "ended_at"
    }
}

struct EngineWorkplaceList: Decodable, Sendable {
    let workplaces: [EngineWorkplace]
}

struct EngineWorkplaceShow: Decodable, Sendable {
    let workplace: EngineWorkplace?
    let blocks: [EngineBlock]
    let truncated: Bool
}

/// One block the front end is reporting. `id` is the reporter's own stable
/// identifier, which is what makes a second report an update rather than a
/// second block.
struct WorkplaceReport: Equatable, Sendable {
    var id: UUID
    var kind: String
    var input: String?
    var output: String?
    var status: String
    var startedAt: Date?
    var endedAt: Date?
    var exitCode: Int?
    var provider: String?

    var arguments: [String] {
        var args = ["workplace", "record", "--json",
                    "--id", id.uuidString.lowercased(),
                    "--kind", kind,
                    "--status", status]
        if let input, !input.isEmpty { args += ["--input", input] }
        if let exitCode { args += ["--exit-code", String(exitCode)] }
        if let provider, !provider.isEmpty { args += ["--provider", provider] }
        if let startedAt { args += ["--started-at", Self.timestamp(startedAt)] }
        if let endedAt { args += ["--ended-at", Self.timestamp(endedAt)] }
        return args
    }

    static func timestamp(_ date: Date) -> String {
        let formatter = ISO8601DateFormatter()
        formatter.timeZone = TimeZone(secondsFromGMT: 0)
        formatter.formatOptions = [.withInternetDateTime]
        return formatter.string(from: date)
    }
}

/// Reads and writes the engine's workplace record.
///
/// Every call is a blocking `oberth` invocation and must run off the main actor.
/// `binary` is injectable so checks can point at a synthetic CLI instead of a
/// real daemon.
enum WorkplaceAPI {
    static func list(worktree: String, binary: String? = nil) throws -> [EngineWorkplace] {
        try decode(EngineWorkplaceList.self,
                   arguments: ["workplace", "list", "--json", "--worktree", worktree], binary: binary)
            .workplaces
    }

    static func show(worktree: String, binary: String? = nil) throws -> EngineWorkplaceShow {
        try decode(EngineWorkplaceShow.self,
                   arguments: ["workplace", "show", "--json", "--worktree", worktree], binary: binary)
    }

    static func create(worktree: String, name: String, binary: String? = nil) throws -> EngineWorkplace {
        let doc = try decode(WorkplaceDocument.self,
                             arguments: ["workplace", "new", name, "--json", "--worktree", worktree],
                             binary: binary)
        guard let workplace = doc.workplace else { throw WorkplaceAPIError.missingWorkplace }
        return workplace
    }

    static func use(worktree: String, query: String, binary: String? = nil) throws -> EngineWorkplace {
        let doc = try decode(WorkplaceDocument.self,
                             arguments: ["workplace", "use", query, "--json", "--worktree", worktree],
                             binary: binary)
        guard let workplace = doc.workplace else { throw WorkplaceAPIError.missingWorkplace }
        return workplace
    }

    static func delete(worktree: String, query: String, binary: String? = nil) throws {
        _ = try decode(WorkplaceDocument.self,
                       arguments: ["workplace", "delete", query, "--json", "--worktree", worktree],
                       binary: binary)
    }

    /// Report one block. Output travels through a temporary file: a finished
    /// command can carry megabytes, and argv is not the place for it.
    static func record(_ report: WorkplaceReport, worktree: String, binary: String? = nil) throws {
        var arguments = report.arguments
        var scratch: URL?
        defer { if let scratch { try? FileManager.default.removeItem(at: scratch) } }
        if let output = report.output, !output.isEmpty {
            let url = FileManager.default.temporaryDirectory
                .appendingPathComponent("oberth-workplace-" + UUID().uuidString + ".txt")
            try Data(output.utf8).write(to: url, options: .atomic)
            scratch = url
            arguments += ["--output-file", url.path]
        }
        arguments += ["--worktree", worktree]
        guard let binary = binary ?? DaemonLaunch.binaryPath() else {
            throw WorkplaceAPIError.missingBinary
        }
        switch CLI.run(binary: binary, arguments: arguments, workingDirectory: worktree) {
        case .success: return
        case .failure(let failure): throw failure
        }
    }

    private static func decode<T: Decodable>(_ type: T.Type, arguments: [String],
                                             binary: String?) throws -> T {
        guard let binary = binary ?? DaemonLaunch.binaryPath() else {
            throw WorkplaceAPIError.missingBinary
        }
        switch CLI.run(binary: binary, arguments: arguments) {
        case .failure(let failure):
            throw failure
        case .success(let data):
            do {
                return try JSONDecoder().decode(T.self, from: data)
            } catch {
                throw CLI.Failure(message: "读不懂 option-berth 的输出（\(error.localizedDescription)）—— 是不是装了一份旧二进制？")
            }
        }
    }
}

enum WorkplaceAPIError: LocalizedError {
    case missingBinary
    case missingWorkplace

    var errorDescription: String? {
        switch self {
        case .missingBinary: return CLI.missingBinary
        case .missingWorkplace: return "读不懂 workplace 的输出 / Unreadable workplace document"
        }
    }
}

/// One workplace document, for the commands whose JSON wraps a single record.
private struct WorkplaceDocument: Decodable {
    let workplace: EngineWorkplace?
}

/// Engine block ids are engine-chosen strings (`oberth start` uses its own run
/// ids), while the console's cards are keyed by `UUID`. The mapping is a pure
/// function of the string, so the same block keeps the same identity across
/// refreshes and restarts.
enum WorkplaceIdentity {
    static func uuid(for identifier: String) -> UUID {
        if let direct = UUID(uuidString: identifier) { return direct }
        let digest = SHA256.hash(data: Data(identifier.utf8))
        var bytes = Array(digest.prefix(16))
        // RFC 4122 version 5 shape: namespaced, deterministic, not a real v5.
        bytes[6] = (bytes[6] & 0x0F) | 0x50
        bytes[8] = (bytes[8] & 0x3F) | 0x80
        return UUID(uuid: (bytes[0], bytes[1], bytes[2], bytes[3], bytes[4], bytes[5],
                           bytes[6], bytes[7], bytes[8], bytes[9], bytes[10], bytes[11],
                           bytes[12], bytes[13], bytes[14], bytes[15]))
    }
}

enum WorkplaceTime {
    /// The engine writes RFC 3339 in UTC. Fractional seconds are not promised,
    /// so accept both shapes rather than losing a block to a strict parse.
    static func date(_ value: String?) -> Date? {
        guard let value, !value.isEmpty else { return nil }
        let withFraction = ISO8601DateFormatter()
        withFraction.formatOptions = [.withInternetDateTime, .withFractionalSeconds]
        if let date = withFraction.date(from: value) { return date }
        let plain = ISO8601DateFormatter()
        plain.formatOptions = [.withInternetDateTime]
        return plain.date(from: value)
    }
}
